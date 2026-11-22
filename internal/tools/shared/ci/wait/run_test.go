package wait

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/clock"
	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

// runFake serves run 1236 and its jobs; step() mutates them on each run read.
type runFake struct {
	mu     sync.Mutex
	reads  int
	status string
	jobs   []map[string]any
	step   func(f *runFake)
	runner string
}

func (f *runFake) client(t *testing.T) *forgejo.Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		enc := json.NewEncoder(w)
		switch r.URL.Path {
		case "/api/v1/settings/api":
			_, _ = w.Write([]byte(`{"default_paging_num":20,"max_response_items":50}`))
		case "/api/v1/repos/o/r/actions/runs/1236":
			f.reads++
			if f.step != nil {
				f.step(f)
			}
			_ = enc.Encode(map[string]any{"id": 1236, "index_in_repo": 58, "workflow_id": "ci.yml", "status": f.status, "event": "pull_request", "prettyref": "#42"})
		case "/api/v1/repos/o/r/actions/runs/1236/jobs":
			_ = enc.Encode(f.jobs)
		case "/api/v1/repos/o/r/actions/runners":
			_, _ = w.Write([]byte(f.runner))
		case "/api/v1/repos/o/r/actions/jobs/5678":
			_, _ = w.Write([]byte(`{"id":5678,"name":"bad","status":"failure","task_id":1,"steps":[{"number":1,"name":"Run","status":"failure"}]}`))
		case "/api/v1/repos/o/r/actions/jobs/5678/logs":
			http.ServeContent(w, r, "l", time.Time{}, strings.NewReader("2026-11-22T09:50:00Z before-failure\n2026-11-22T09:50:01Z FAIL boom\n"))
		default:
			if !strings.HasPrefix(r.URL.Path, "/api/v1/repos/o/r/actions/runs/999") {
				t.Errorf("unexpected %s", r.URL.RequestURI())
			}
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return forgejo.New(forgejo.Config{BaseURL: srv.URL, HTTPClient: srv.Client(), Clock: clock.Real()})
}

func job(id int, name, status string) map[string]any {
	return map[string]any{"id": id, "name": name, "status": status}
}

func TestWaitRunSuccess(t *testing.T) {
	f := &runFake{status: "running", jobs: []map[string]any{job(5677, "ok", "running"), job(5678, "bad", "waiting")}}
	f.step = func(f *runFake) {
		if f.reads >= 3 {
			f.status = "success"
			f.jobs = []map[string]any{job(5677, "ok", "success"), job(5678, "bad", "success")}
		}
	}
	res, err := WaitRun(t.Context(), f.client(t), RunOpts{Owner: "o", Repo: "r", Run: 1236, FailFast: true, Poll: PollOpts{Clock: newRecClock(), Timeout: time.Minute}})
	if err != nil || res.Verdict != VerdictSuccess {
		t.Fatalf("%v %v", res, err)
	}
	testutil.Golden(t, "run_success", res.Text)
}

func TestWaitRunFailFastWhileRunning(t *testing.T) {
	f := &runFake{status: "running", jobs: []map[string]any{job(5677, "ok", "success"), job(5678, "bad", "failure"), job(5679, "slow", "running")}}
	clk := newRecClock()
	res, err := WaitRun(t.Context(), f.client(t), RunOpts{Owner: "o", Repo: "r", Run: 1236, FailFast: true, WithFailure: true, Poll: PollOpts{Clock: clk, Timeout: time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictFailure || f.reads != 2 && f.reads != 1 {
		t.Fatalf("verdict %s reads %d", res.Verdict, f.reads)
	}
	if !strings.Contains(res.Text, "1 running (slow)") || !strings.Contains(res.Text, "failed job 5678") {
		t.Fatalf("text:\n%s", res.Text)
	}
}

func TestWaitRunNoFailFastWaitsForEnd(t *testing.T) {
	f := &runFake{status: "running", jobs: []map[string]any{job(5678, "bad", "failure"), job(5679, "slow", "running")}}
	f.step = func(f *runFake) {
		if f.reads >= 3 {
			f.status = "failure"
			f.jobs[1]["status"] = "success"
		}
	}
	res, err := WaitRun(t.Context(), f.client(t), RunOpts{Owner: "o", Repo: "r", Run: 1236, Poll: PollOpts{Clock: newRecClock(), Timeout: time.Minute}})
	if err != nil || res.Verdict != VerdictFailure || f.reads != 3 {
		t.Fatalf("%v %v reads=%d", res, err, f.reads)
	}
}

func TestWaitRunCancelledAndSkipped(t *testing.T) {
	for _, st := range []string{"cancelled", "skipped"} {
		f := &runFake{status: st}
		res, err := WaitRun(t.Context(), f.client(t), RunOpts{Owner: "o", Repo: "r", Run: 1236, FailFast: true, Poll: PollOpts{Clock: newRecClock(), Timeout: time.Minute}})
		if err != nil || res.Verdict != st {
			t.Fatalf("%s: %v %v", st, res, err)
		}
	}
}

func TestWaitRunTimeout(t *testing.T) {
	f := &runFake{status: "running", jobs: []map[string]any{job(5677, "build", "success"), job(5678, "test", "running")}}
	res, err := WaitRun(t.Context(), f.client(t), RunOpts{Owner: "o", Repo: "r", Run: 1236, FailFast: true, Resume: "ci_wait run=1236", Poll: PollOpts{Clock: newRecClock(), Timeout: 8 * time.Second}})
	if err != nil || res.Verdict != VerdictTimeout {
		t.Fatalf("%v %v", res, err)
	}
	testutil.Golden(t, "run_timeout", res.Text)
}

func TestWaitRunTimeoutStuckQueue(t *testing.T) {
	f := &runFake{
		status: "waiting", jobs: []map[string]any{{"id": 5677, "name": "build", "status": "waiting", "runs_on": []string{"docker"}}},
		runner: `[{"name":"a","status":"active","labels":["ubuntu-latest"]},{"name":"b","status":"idle","labels":["node:host"]}]`,
	}
	res, err := WaitRun(t.Context(), f.client(t), RunOpts{Owner: "o", Repo: "r", Run: 1236, Poll: PollOpts{Clock: newRecClock(), Timeout: 3 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	want := "hint: no online runner covers runs-on [docker]; online runners: 2 [ubuntu-latest,node]"
	if !strings.Contains(res.Text, want) || !strings.Contains(res.Text, "resume: ci_wait run=1236") {
		t.Fatalf("text:\n%s", res.Text)
	}
}

func TestWaitRunNotFound(t *testing.T) {
	f := &runFake{}
	_, err := WaitRun(t.Context(), f.client(t), RunOpts{Owner: "o", Repo: "r", Run: 999, Poll: PollOpts{Clock: newRecClock(), Timeout: time.Minute}})
	if err == nil {
		t.Fatal("want error for an unknown run")
	}
}
