package wait

import (
	"encoding/json"
	"fmt"
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

type memo map[string]bool

func (m memo) Has(k string) bool { return m[k] }

// fake is a mutable CI server for one repo o/r with PR #42.
type fake struct {
	mu sync.Mutex

	polls  int // number of combined-status reads
	onPoll func(f *fake)

	prState, headSHA, mergeSHA string
	merged, mergeable, draft   bool
	statuses                   []map[string]any
	runs                       []map[string]any
	jobs                       map[int][]map[string]any
	runners                    string
	timeline                   string
	timelinePages              map[string]string // page -> body; overrides timeline
	protection                 string            // effective_branch_protection_name
	blockRejected              bool
	required                   int
	reviews                    string
	statusCalls                []string
}

func newFake() *fake {
	return &fake{prState: "open", headSHA: "8a1b2c3d4e5f0000000000000000000000000000", mergeable: true, jobs: map[int][]map[string]any{}, runners: "[]", timeline: "[]", reviews: "[]"}
}

func st(ctx, state string) map[string]any {
	return map[string]any{"id": len(ctx), "status": state, "context": ctx, "description": "d-" + ctx}
}

func run(id, no int, wf, status string) map[string]any {
	return map[string]any{"id": id, "index_in_repo": no, "workflow_id": wf, "status": status, "event": "pull_request", "prettyref": "#42", "duration": int64(41 * time.Second)}
}

func (f *fake) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		enc := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		p := r.URL.Path
		switch {
		case p == "/api/v1/settings/api":
			_, _ = w.Write([]byte(`{"default_paging_num":20,"max_response_items":50}`))
		case p == "/api/v1/repos/o/r/pulls/42":
			enc(map[string]any{
				"state": f.prState, "merged": f.merged, "draft": f.draft, "mergeable": f.mergeable, "merge_commit_sha": f.mergeSHA,
				"head": map[string]any{"sha": f.headSHA}, "base": map[string]any{"ref": "main"},
			})
		case strings.HasSuffix(p, "/status") && strings.HasPrefix(p, "/api/v1/repos/o/r/commits/"):
			f.polls++
			f.statusCalls = append(f.statusCalls, p)
			if f.onPoll != nil {
				f.onPoll(f)
			}
			enc(map[string]any{"statuses": f.statuses})
		case p == "/api/v1/repos/o/r/commits":
			enc([]map[string]any{{"sha": f.headSHA}})
		case p == "/api/v1/repos/o/r/actions/runs":
			enc(map[string]any{"workflow_runs": f.runs, "total_count": len(f.runs)})
		case strings.HasPrefix(p, "/api/v1/repos/o/r/actions/runs/") && strings.HasSuffix(p, "/jobs"):
			var id int
			_, _ = fmt.Sscanf(strings.TrimPrefix(p, "/api/v1/repos/o/r/actions/runs/"), "%d/jobs", &id)
			enc(f.jobs[id])
		case strings.HasPrefix(p, "/api/v1/repos/o/r/actions/runs/"):
			var id int
			_, _ = fmt.Sscanf(strings.TrimPrefix(p, "/api/v1/repos/o/r/actions/runs/"), "%d", &id)
			for _, rr := range f.runs {
				if rr["id"] == id {
					enc(rr)
					return
				}
			}
			http.NotFound(w, r)
		case p == "/api/v1/repos/o/r/actions/runners":
			_, _ = w.Write([]byte(f.runners))
		case p == "/api/v1/repos/o/r/issues/42/timeline":
			if f.timelinePages != nil {
				body, ok := f.timelinePages[r.URL.Query().Get("page")]
				if !ok {
					body = "[]"
				}
				_, _ = w.Write([]byte(body))
				return
			}
			_, _ = w.Write([]byte(f.timeline))
		case p == "/api/v1/repos/o/r/branches/main":
			enc(map[string]any{"required_approvals": f.required, "effective_branch_protection_name": f.protection})
		case p == "/api/v1/repos/o/r/branch_protections/main":
			enc(map[string]any{"block_on_rejected_reviews": f.blockRejected})
		case p == "/api/v1/repos/o/r/pulls/42/reviews":
			_, _ = w.Write([]byte(f.reviews))
		default:
			t.Errorf("unexpected request %s", r.URL.RequestURI())
			http.NotFound(w, r)
		}
	})
}

func (f *fake) client(t *testing.T) *forgejo.Client {
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	return forgejo.New(forgejo.Config{BaseURL: srv.URL, HTTPClient: srv.Client(), Clock: clock.Real(), Timeout: 5 * time.Second})
}

func target(clk clock.Clock, timeout time.Duration) TargetOpts {
	return TargetOpts{
		Owner: "o", Repo: "r", PR: 42, FailFast: true, TargetLabel: "target: pr #42 (branch fix/login)",
		Poll: PollOpts{Clock: clk, Timeout: timeout},
	}
}

func TestWaitTargetGreen(t *testing.T) {
	f := newFake()
	f.statuses = []map[string]any{st("build", "pending"), st("test", "success")}
	f.runs = []map[string]any{run(1236, 58, "ci.yml", "running")}
	f.onPoll = func(f *fake) {
		if f.polls >= 3 {
			f.statuses = []map[string]any{st("build", "success"), st("test", "success")}
			f.runs = []map[string]any{run(1236, 58, "ci.yml", "success")}
		}
	}
	res, err := WaitTarget(t.Context(), f.client(t), target(newRecClock(), 50*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictGreen {
		t.Fatalf("verdict %s\n%s", res.Verdict, res.Text)
	}
	testutil.Golden(t, "target_green", res.Text)
}

func TestWaitTargetGreenBlockedBy(t *testing.T) {
	f := newFake()
	f.required = 2
	f.reviews = `[{"state":"APPROVED","official":true,"user":{"login":"a"}},{"state":"COMMENT","user":{"login":"b"}}]`
	f.statuses = []map[string]any{st("build", "success")}
	res, err := WaitTarget(t.Context(), f.client(t), target(newRecClock(), 50*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "mergeable: yes | blocked_by: approvals:1/2") {
		t.Fatalf("text:\n%s", res.Text)
	}
}

func TestWaitTargetFailedFailFast(t *testing.T) {
	f := newFake()
	f.statuses = []map[string]any{st("build", "failure"), st("test", "pending")}
	f.runs = []map[string]any{run(1236, 58, "ci.yml", "running")}
	clk := newRecClock()
	res, err := WaitTarget(t.Context(), f.client(t), target(clk, 50*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictFailed || f.polls != 1 || len(clk.sleeps) != 0 {
		t.Fatalf("verdict %s polls %d sleeps %v", res.Verdict, f.polls, clk.sleeps)
	}
	testutil.Golden(t, "target_failed_failfast", res.Text)
}

func TestWaitTargetFailedNoFailFast(t *testing.T) {
	f := newFake()
	f.statuses = []map[string]any{st("build", "failure"), st("test", "pending")}
	f.onPoll = func(f *fake) {
		if f.polls >= 3 {
			f.statuses = []map[string]any{st("build", "failure"), st("test", "success")}
		}
	}
	o := target(newRecClock(), 50*time.Second)
	o.FailFast = false
	res, err := WaitTarget(t.Context(), f.client(t), o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictFailed || f.polls != 3 {
		t.Fatalf("verdict %s polls %d\n%s", res.Verdict, f.polls, res.Text)
	}
}

func TestWaitTargetFailureFromRunWithoutStatuses(t *testing.T) {
	f := newFake()
	f.runs = []map[string]any{run(1236, 58, "ci.yml", "failure")}
	res, err := WaitTarget(t.Context(), f.client(t), target(newRecClock(), 50*time.Second))
	if err != nil || res.Verdict != VerdictFailed {
		t.Fatalf("%v %v", res, err)
	}
}

func TestWaitTargetTimeoutStuckQueue(t *testing.T) {
	f := newFake()
	f.statuses = []map[string]any{st("ci / build (push)", "pending")}
	f.runs = []map[string]any{run(1236, 58, "ci.yml", "waiting")}
	f.jobs[1236] = []map[string]any{{"id": 5678, "name": "build", "status": "waiting", "runs_on": []string{"docker"}}}
	f.runners = `[{"name":"r1","status":"idle","labels":["ubuntu-latest:docker://node:20"]},{"name":"r2","status":"offline","labels":["docker"]}]`
	o := target(newRecClock(), 10*time.Second)
	o.Resume = "ci_wait pr=42"
	res, err := WaitTarget(t.Context(), f.client(t), o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictTimeout || res.Waited != 8*time.Second {
		t.Fatalf("verdict %s waited %v", res.Verdict, res.Waited)
	}
	testutil.Golden(t, "target_timeout_stuck", res.Text)
}

func TestWaitTargetTimeoutAllRunnersOffline(t *testing.T) {
	f := newFake()
	f.statuses = []map[string]any{st("build", "pending")}
	f.runs = []map[string]any{run(1236, 58, "ci.yml", "waiting")}
	f.jobs[1236] = []map[string]any{{"id": 1, "name": "build", "status": "waiting", "runs_on": []string{"docker"}}}
	f.runners = `[{"name":"r2","status":"offline","labels":["docker"]}]`
	res, err := WaitTarget(t.Context(), f.client(t), target(newRecClock(), 3*time.Second))
	if err != nil || !strings.Contains(res.Text, "hint: all runners offline") {
		t.Fatalf("%v\n%v", err, res)
	}
	if !strings.Contains(res.Text, "resume: ci_wait pr=42") {
		t.Fatalf("default resume line missing:\n%s", res.Text)
	}
}

func TestWaitTargetNoChecksAfterGrace(t *testing.T) {
	f := newFake()
	clk := newRecClock()
	res, err := WaitTarget(t.Context(), f.client(t), target(clk, 50*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictNoChecks || res.Waited < 20*time.Second || res.Waited > 21*time.Second {
		t.Fatalf("verdict %s waited %v", res.Verdict, res.Waited)
	}
	testutil.Golden(t, "target_no_checks", res.Text)
}

func TestWaitTargetNoChecksStatusesAppear(t *testing.T) {
	f := newFake()
	f.onPoll = func(f *fake) {
		if f.polls >= 3 {
			f.statuses = []map[string]any{st("build", "success")}
		}
	}
	res, err := WaitTarget(t.Context(), f.client(t), target(newRecClock(), 50*time.Second))
	if err != nil || res.Verdict != VerdictGreen {
		t.Fatalf("%v %v", res, err)
	}
}

func TestWaitTargetMergedAndClosed(t *testing.T) {
	f := newFake()
	f.statuses = []map[string]any{st("build", "pending")}
	f.onPoll = func(f *fake) {
		if f.polls >= 2 {
			f.merged, f.prState, f.mergeSHA = true, "closed", "9f9f9f9f0000"
		}
	}
	res, err := WaitTarget(t.Context(), f.client(t), target(newRecClock(), 50*time.Second))
	if err != nil || res.Verdict != VerdictMerged {
		t.Fatalf("%v %v", res, err)
	}
	testutil.Golden(t, "target_merged", res.Text)

	g := newFake()
	g.prState = "closed"
	g.statuses = []map[string]any{st("build", "pending")}
	res, err = WaitTarget(t.Context(), g.client(t), target(newRecClock(), 50*time.Second))
	if err != nil || res.Verdict != VerdictClosed || g.polls != 0 {
		t.Fatalf("closed: %v %v polls=%d", res, err, g.polls)
	}
}

func TestWaitTargetHeadMoved(t *testing.T) {
	f := newFake()
	f.statuses = []map[string]any{st("build", "pending")}
	f.onPoll = func(f *fake) {
		if f.polls == 2 {
			f.headSHA = "9f9f9f9f1111"
		}
		if f.polls >= 3 {
			f.statuses = []map[string]any{st("build", "success")}
		}
	}
	res, err := WaitTarget(t.Context(), f.client(t), target(newRecClock(), 50*time.Second))
	if err != nil || res.Verdict != VerdictGreen {
		t.Fatalf("%v %v", res, err)
	}
	if !strings.Contains(res.Text, "head moved 8a1b2c3d->9f9f9f9f") || !strings.Contains(res.Text, "head 9f9f9f9f") {
		t.Fatalf("text:\n%s", res.Text)
	}
	if last := f.statusCalls[len(f.statusCalls)-1]; !strings.Contains(last, "9f9f9f9f1111") {
		t.Fatalf("the new head must be polled: %v", f.statusCalls)
	}
}

func TestWaitTargetGreenAutoMergeThenMerged(t *testing.T) {
	f := newFake()
	f.statuses = []map[string]any{st("build", "success")}
	f.onPoll = func(f *fake) {
		if f.polls >= 4 {
			f.merged, f.prState, f.mergeSHA = true, "closed", "abcdef012345"
		}
	}
	o := target(newRecClock(), 50*time.Second)
	o.Memo = memo{"automerge:o/r#42": true}
	res, err := WaitTarget(t.Context(), f.client(t), o)
	if err != nil || res.Verdict != VerdictMerged {
		t.Fatalf("%v %v", res, err)
	}
	if !strings.Contains(res.Text, "merged as abcdef01") || f.polls < 4 {
		t.Fatalf("polls %d\n%s", f.polls, res.Text)
	}
}

func TestWaitTargetGreenAutoMergeStillPending(t *testing.T) {
	f := newFake()
	f.statuses = []map[string]any{st("build", "success")}
	f.timeline = `[{"type":"comment"},{"type":"pull_cancel_scheduled_merge"},{"type":"pull_scheduled_merge"}]`
	clk := newRecClock()
	res, err := WaitTarget(t.Context(), f.client(t), target(clk, 50*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictGreen || !strings.Contains(res.Text, "auto-merge pending") {
		t.Fatalf("%s\n%s", res.Verdict, res.Text)
	}
	if res.Waited < 30*time.Second || res.Waited > 31*time.Second {
		t.Fatalf("waited %v, want ~30s follow-up", res.Waited)
	}
	if strings.Contains(res.Text, "mergeable:") {
		t.Fatalf("no mergeable line while auto-merge is pending:\n%s", res.Text)
	}
}

func TestWaitTargetCancelledAutoMergeIsIgnored(t *testing.T) {
	f := newFake()
	f.statuses = []map[string]any{st("build", "success")}
	f.timeline = `[{"type":"pull_scheduled_merge"},{"type":"pull_cancel_scheduled_merge"}]`
	clk := newRecClock()
	res, err := WaitTarget(t.Context(), f.client(t), target(clk, 50*time.Second))
	if err != nil || res.Verdict != VerdictGreen || len(clk.sleeps) != 0 {
		t.Fatalf("%v %v sleeps=%v", res, err, clk.sleeps)
	}
}

func TestWaitTargetRefMode(t *testing.T) {
	f := newFake()
	f.statuses = []map[string]any{st("build", "success")}
	o := TargetOpts{Owner: "o", Repo: "r", Ref: "main", FailFast: true, Poll: PollOpts{Clock: newRecClock(), Timeout: time.Minute}}
	res, err := WaitTarget(t.Context(), f.client(t), o)
	if err != nil || res.Verdict != VerdictGreen || !strings.HasPrefix(res.Text, "verdict: green | waited 0s | ref main head 8a1b2c3d") {
		t.Fatalf("%v\n%v", err, res)
	}
	if strings.Contains(res.Text, "mergeable") {
		t.Fatalf("no mergeable line outside PR mode:\n%s", res.Text)
	}
}

func TestWaitTargetRequiresSelector(t *testing.T) {
	if _, err := WaitTarget(t.Context(), newFake().client(t), TargetOpts{Owner: "o", Repo: "r"}); err == nil {
		t.Fatal("want error")
	}
}

func TestWaitTargetWithFailureDigest(t *testing.T) {
	f := newFake()
	f.statuses = []map[string]any{st("ci / build (push)", "failure")}
	f.runs = []map[string]any{run(1236, 58, "ci.yml", "failure")}
	f.jobs[1236] = []map[string]any{{"id": 5678, "name": "build", "status": "failure", "task_id": 1}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/o/r/actions/jobs/5678":
			_, _ = w.Write([]byte(`{"id":5678,"name":"build","status":"failure","task_id":1,"steps":[{"number":1,"name":"Run tests","status":"failure"}]}`))
		case "/api/v1/repos/o/r/actions/jobs/5678/logs":
			http.ServeContent(w, r, "l", time.Time{}, strings.NewReader("2026-11-22T09:50:00Z FAIL TestX\n2026-11-22T09:50:01Z exit status 1\n"))
		default:
			f.handler(t).ServeHTTP(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := forgejo.New(forgejo.Config{BaseURL: srv.URL, HTTPClient: srv.Client(), Clock: clock.Real()})
	o := target(newRecClock(), 50*time.Second)
	o.WithFailure = true
	res, err := WaitTarget(t.Context(), c, o)
	if err != nil || res.Verdict != VerdictFailed {
		t.Fatalf("%v %v", res, err)
	}
	testutil.Golden(t, "target_failed_digest", res.Text)
}

func TestWaitTargetProgress(t *testing.T) {
	f := newFake()
	f.statuses = []map[string]any{st("build", "pending")}
	f.onPoll = func(f *fake) {
		if f.polls >= 3 {
			f.statuses = []map[string]any{st("build", "success")}
		}
	}
	var msgs []string
	o := target(newRecClock(), 50*time.Second)
	o.Poll.Progress = func(m string, done, total float64) { msgs = append(msgs, m) }
	if _, err := WaitTarget(t.Context(), f.client(t), o); err != nil {
		t.Fatal(err)
	}
	// polls 1 and 2 share a snapshot, poll 3 differs
	if len(msgs) != 2 || !strings.Contains(msgs[0], "pending") || !strings.Contains(msgs[1], "success") {
		t.Fatalf("progress %q", msgs)
	}
}

func TestWaitTargetAutoMergeOnLaterTimelinePage(t *testing.T) {
	f := newFake()
	f.statuses = []map[string]any{st("build", "success")}
	// page 1 is short (the server dropped inline review comments after
	// paging); the scheduling event sits on page 2
	f.timelinePages = map[string]string{
		"1": `[{"type":"comment"}]`,
		"2": `[{"type":"pull_scheduled_merge"}]`,
	}
	res, err := WaitTarget(t.Context(), f.client(t), target(newRecClock(), 50*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictGreen || !strings.Contains(res.Text, "auto-merge pending") {
		t.Fatalf("verdict %s\n%s", res.Verdict, res.Text)
	}
}

func TestWaitTargetOnlyOfficialApprovalsCount(t *testing.T) {
	f := newFake()
	f.required = 1
	f.reviews = `[{"state":"APPROVED","official":false,"user":{"login":"a"}}]`
	f.statuses = []map[string]any{st("build", "success")}
	res, err := WaitTarget(t.Context(), f.client(t), target(newRecClock(), 50*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "blocked_by: approvals:0/1") {
		t.Fatalf("text:\n%s", res.Text)
	}
}

func TestWaitTargetChangesRequestedOnlyWhenBlocking(t *testing.T) {
	f := newFake()
	f.protection = "main"
	f.reviews = `[{"state":"REQUEST_CHANGES","official":true,"user":{"login":"a"}}]`
	f.statuses = []map[string]any{st("build", "success")}
	res, err := WaitTarget(t.Context(), f.client(t), target(newRecClock(), 50*time.Second))
	if err != nil || !strings.Contains(res.Text, "ready: yes") {
		t.Fatalf("%v\n%v", err, res)
	}
	f.blockRejected = true
	res, err = WaitTarget(t.Context(), f.client(t), target(newRecClock(), 50*time.Second))
	if err != nil || !strings.Contains(res.Text, "blocked_by: changes_requested") {
		t.Fatalf("%v\n%v", err, res)
	}
}

func TestWaitTargetRejectsShortSHA(t *testing.T) {
	f := newFake()
	o := TargetOpts{Owner: "o", Repo: "r", SHA: "8a1b2c3d", Poll: PollOpts{Clock: newRecClock(), Timeout: time.Second}}
	if _, err := WaitTarget(t.Context(), f.client(t), o); err == nil || !strings.HasPrefix(err.Error(), "invalid:") {
		t.Fatalf("err = %v", err)
	}
}

func TestWaitTargetHintForRunningRunAndPerRunnerLabels(t *testing.T) {
	f := newFake()
	f.statuses = []map[string]any{st("ci / build (push)", "pending")}
	f.runs = []map[string]any{run(1236, 58, "ci.yml", "running")}
	f.jobs[1236] = []map[string]any{
		{"id": 1, "name": "a", "status": "running", "runs_on": []string{"x"}},
		{"id": 2, "name": "b", "status": "waiting", "runs_on": []string{"x", "y"}},
	}
	// x and y are online, but on different runners
	f.runners = `[{"name":"r1","status":"idle","labels":["x"]},{"name":"r2","status":"idle","labels":["y"]}]`
	res, err := WaitTarget(t.Context(), f.client(t), target(newRecClock(), 5*time.Second))
	if err != nil || !strings.Contains(res.Text, "hint: no online runner covers runs-on [x,y]") {
		t.Fatalf("%v\n%v", err, res)
	}
}

func TestBoundIsRealTimeDeadline(t *testing.T) {
	ctx, cancel := Bound(t.Context(), PollOpts{Timeout: 10 * time.Second})
	defer cancel()
	d, ok := ctx.Deadline()
	if !ok || time.Until(d) > 10*time.Second+ctxReserve || time.Until(d) < 9*time.Second {
		t.Fatalf("deadline %v ok=%v", d, ok)
	}
}
