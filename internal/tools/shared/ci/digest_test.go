package ci

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

func testJob(id int, name, status string, taskID int, stepStatus string) string {
	return fmt.Sprintf(`{"id":%d,"name":%q,"status":%q,"attempt":1,"task_id":%d,"steps":[
 {"number":0,"name":"Set up job","status":"success"},
 {"number":1,"name":"Run tests","status":%q,"started":"2026-11-22T09:50:00Z","stopped":"2026-11-22T09:50:12Z"},
 {"number":2,"name":"Complete job","status":"skipped"}]}`, id, name, status, taskID, stepStatus)
}

func stepLog(failLine string) string {
	var b strings.Builder
	for i := range 6 {
		fmt.Fprintf(&b, "2026-11-22T09:50:%02d.1Z ok step %d\n", i, i)
	}
	fmt.Fprintf(&b, "2026-11-22T09:50:07.1Z %s\n", failLine)
	b.WriteString("2026-11-22T09:50:08.1Z exit status 1\n")
	return b.String()
}

// ciFake serves a PR head with several workflow runs.
func ciFake(t *testing.T) http.HandlerFunc {
	runs := `{"workflow_runs":[
 {"id":1250,"index_in_repo":13,"workflow_id":"docs.yml","status":"success","event":"push","prettyref":"fix/login","commit_sha":"77aa11bb22cc","trigger_user":{"login":"alice"}},
 {"id":1240,"index_in_repo":12,"workflow_id":"lint.yml","status":"running","event":"pull_request","prettyref":"#42","commit_sha":"77aa11bb22cc","trigger_user":{"login":"alice"},"created":"2026-11-22T09:51:00Z"},
 {"id":1234,"index_in_repo":56,"workflow_id":"ci.yml","status":"failure","event":"push","prettyref":"fix/login","commit_sha":"77aa11bb22cc","trigger_user":{"login":"alice"},"created":"2026-11-22T09:50:00Z"},
 {"id":1200,"index_in_repo":50,"workflow_id":"ci.yml","status":"failure","event":"push","prettyref":"fix/login","commit_sha":"77aa11bb22cc","trigger_user":{"login":"alice"}}],"total_count":4}`
	jobs := map[string]string{
		"1234": "[" + strings.Join([]string{
			testJob(5678, "build (linux)", "failure", 1, "failure"),
			testJob(5679, "build (mac)", "failure", 2, "failure"),
			testJob(5680, "lint", "success", 3, "success"),
			testJob(5681, "test", "skipped", 0, "skipped"),
		}, ",") + "]",
		"1240": "[" + testJob(5702, "lint", "failure", 4, "failure") + "," + testJob(5703, "vet", "running", 5, "running") + "]",
	}
	jobByID := map[string]string{
		"5678": testJob(5678, "build (linux)", "failure", 1, "failure"),
		"5679": testJob(5679, "build (mac)", "failure", 2, "failure"),
		"5702": testJob(5702, "lint", "failure", 4, "failure"),
		"5682": testJob(5682, "never", "failure", 0, "failure"),
	}
	logs := map[string]string{
		"5678": stepLog("FAIL TestSessionRefresh (0.02s)"),
		"5679": stepLog("FAIL TestSessionRefresh (0.02s)"),
		"5702": stepLog("main.go:9: unused variable x"),
	}
	return func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/api/v1/repos/o/r/pulls/42":
			_, _ = w.Write([]byte(`{"head":{"sha":"77aa11bb22cc"}}`))
		case p == "/api/v1/repos/o/r/actions/runs" && r.URL.Query().Get("head_sha") == "77aa11bb22cc":
			_, _ = w.Write([]byte(runs))
		case strings.HasPrefix(p, "/api/v1/repos/o/r/actions/runs/") && strings.HasSuffix(p, "/jobs"):
			id := strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/repos/o/r/actions/runs/"), "/jobs")
			if j, ok := jobs[id]; ok {
				_, _ = w.Write([]byte(j))
				return
			}
			_, _ = w.Write([]byte("[]"))
		case strings.HasPrefix(p, "/api/v1/repos/o/r/actions/runs/"):
			id := strings.TrimPrefix(p, "/api/v1/repos/o/r/actions/runs/")
			if id == "1234" {
				_, _ = w.Write([]byte(`{"id":1234,"index_in_repo":56,"workflow_id":"ci.yml","status":"failure","event":"push","prettyref":"fix/login","commit_sha":"77aa11bb22cc"}`))
				return
			}
			http.NotFound(w, r)
		case strings.HasSuffix(p, "/logs") && strings.Contains(p, "/actions/jobs/"):
			id := strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/repos/o/r/actions/jobs/"), "/logs")
			if r.URL.Query().Get("step") != "1" {
				t.Errorf("job %s: step %q, want 1", id, r.URL.Query().Get("step"))
			}
			http.ServeContent(w, r, "x.log", time.Time{}, bytesReader(logs[id]))
		case strings.HasPrefix(p, "/api/v1/repos/o/r/actions/jobs/"):
			id := strings.TrimPrefix(p, "/api/v1/repos/o/r/actions/jobs/")
			if j, ok := jobByID[id]; ok {
				_, _ = w.Write([]byte(j))
				return
			}
			http.NotFound(w, r)
		default:
			t.Errorf("unexpected %s", r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}
}

func TestFailureDigestMultiWorkflow(t *testing.T) {
	c, _ := newClient(t, 50, ciFake(t))
	text, failed, err := FailureDigest(t.Context(), c, DigestOpts{Owner: "o", Repo: "r", PR: 42, Tail: 4, TargetLabel: "target: pr #42 (branch fix/login)"})
	if err != nil {
		t.Fatal(err)
	}
	if !failed {
		t.Fatal("failed = false")
	}
	testutil.Golden(t, "digest_multi", text)
	if strings.Count(text, "run 1200") != 0 {
		t.Fatal("the superseded run of the same workflow must not be shown")
	}
}

func TestFailureDigestRunAndLimits(t *testing.T) {
	c, _ := newClient(t, 50, ciFake(t))
	text, failed, err := FailureDigest(t.Context(), c, DigestOpts{Owner: "o", Repo: "r", Run: 1234, Tail: 3, MaxJobs: 1})
	if err != nil || !failed {
		t.Fatalf("%v %v", failed, err)
	}
	if !strings.Contains(text, "+1 more failed jobs: build (mac) (5679)") || strings.Contains(text, "failed job 5679") {
		t.Fatalf("max_jobs not applied:\n%s", text)
	}
	testutil.Golden(t, "digest_run", text)
}

func TestFailureDigestNotStartedJob(t *testing.T) {
	c, _ := newClient(t, 50, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/actions/runs/7"):
			_, _ = w.Write([]byte(`{"id":7,"index_in_repo":1,"workflow_id":"ci.yml","status":"failure"}`))
		case strings.HasSuffix(r.URL.Path, "/runs/7/jobs"):
			_, _ = w.Write([]byte(`[{"id":5682,"name":"never","status":"failure","task_id":0}]`))
		case strings.HasSuffix(r.URL.Path, "/actions/jobs/5682"):
			_, _ = w.Write([]byte(`{"id":5682,"name":"never","status":"failure","task_id":0}`))
		default:
			t.Errorf("unexpected %s", r.URL.RequestURI())
		}
	})
	text, failed, err := FailureDigest(t.Context(), c, DigestOpts{Owner: "o", Repo: "r", Run: 7})
	if err != nil || !failed {
		t.Fatalf("%v %v", failed, err)
	}
	if !strings.Contains(text, "no log (job not started)") {
		t.Fatalf("text:\n%s", text)
	}
}

func TestFailureDigestExternalFallback(t *testing.T) {
	c, _ := newClient(t, 50, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/actions/runs"):
			_, _ = w.Write([]byte(`{"workflow_runs":[],"total_count":0}`))
		case strings.HasSuffix(r.URL.Path, "/status"):
			_, _ = w.Write([]byte(`{"state":"failure","statuses":[{"id":1,"status":"failure","context":"ci/external","description":"build broke","target_url":"https://ci.example/1"},{"id":2,"status":"success","context":"lint"}]}`))
		default:
			t.Errorf("unexpected %s", r.URL.RequestURI())
		}
	})
	text, failed, err := FailureDigest(t.Context(), c, DigestOpts{Owner: "o", Repo: "r", SHA: "abc1230000000000000000000000000000000000"})
	if err != nil || !failed {
		t.Fatalf("%v %v", failed, err)
	}
	testutil.Golden(t, "digest_external", text)
}

func TestFailureDigestAllGreen(t *testing.T) {
	c, _ := newClient(t, 50, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/actions/runs") {
			_, _ = w.Write([]byte(`{"workflow_runs":[{"id":3,"workflow_id":"ci.yml","event":"push","status":"success"}]}`))
			return
		}
		t.Errorf("unexpected %s", r.URL.RequestURI())
	})
	text, failed, err := FailureDigest(t.Context(), c, DigestOpts{Owner: "o", Repo: "r", SHA: "abc1230000000000000000000000000000000000"})
	if err != nil || failed {
		t.Fatalf("%v %v", failed, err)
	}
	if !strings.Contains(text, "no failed run") || !strings.Contains(text, "ok: ci.yml") {
		t.Fatalf("text:\n%s", text)
	}
}

func TestFailureDigestCancelledRunWithFailedJob(t *testing.T) {
	c, _ := newClient(t, 50, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/actions/runs/7"):
			_, _ = w.Write([]byte(`{"id":7,"index_in_repo":1,"workflow_id":"ci.yml","status":"cancelled"}`))
		case strings.HasSuffix(r.URL.Path, "/runs/7/jobs"):
			_, _ = w.Write([]byte(`[{"id":5682,"name":"bad","status":"failure","task_id":0},{"id":5683,"name":"slow","status":"cancelled","task_id":0}]`))
		case strings.HasSuffix(r.URL.Path, "/actions/jobs/5682"):
			_, _ = w.Write([]byte(`{"id":5682,"name":"bad","status":"failure","task_id":0}`))
		default:
			t.Errorf("unexpected %s", r.URL.RequestURI())
		}
	})
	text, failed, err := FailureDigest(t.Context(), c, DigestOpts{Owner: "o", Repo: "r", Run: 7})
	if err != nil || !failed {
		t.Fatalf("%v %v", failed, err)
	}
	if !strings.Contains(text, `failed job 5682 "bad"`) || strings.Contains(text, "5683") {
		t.Fatalf("text:\n%s", text)
	}
	// without a failed job a cancelled run stays under `cancelled:`
	c2, _ := newClient(t, 50, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/runs/7/jobs"):
			_, _ = w.Write([]byte(`[{"id":5683,"name":"slow","status":"cancelled","task_id":0}]`))
		case strings.HasSuffix(r.URL.Path, "/actions/runs/7"):
			_, _ = w.Write([]byte(`{"id":7,"index_in_repo":1,"workflow_id":"ci.yml","status":"cancelled"}`))
		}
	})
	text, failed, err = FailureDigest(t.Context(), c2, DigestOpts{Owner: "o", Repo: "r", Run: 7})
	if err != nil || failed || !strings.Contains(text, "cancelled: ci.yml") {
		t.Fatalf("%v %v\n%s", failed, err, text)
	}
}

func TestFailureDigestShortSHA(t *testing.T) {
	c, _ := newClient(t, 50, func(w http.ResponseWriter, r *http.Request) { t.Errorf("unexpected %s", r.URL.RequestURI()) })
	if _, _, err := FailureDigest(t.Context(), c, DigestOpts{Owner: "o", Repo: "r", SHA: "abc123"}); err == nil || !strings.HasPrefix(err.Error(), "invalid:") {
		t.Fatalf("err = %v", err)
	}
}
