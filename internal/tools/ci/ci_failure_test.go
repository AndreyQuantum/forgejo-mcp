package ci

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

const (
	failRun = `{"id":1234,"index_in_repo":56,"workflow_id":"ci.yml","status":"failure","event":"push","prettyref":"main","commit_sha":"` + sha40 + `","trigger_user":{"login":"alice"},"created":"2026-11-22T09:50:00Z"}`
	failJob = `{"id":5678,"name":"build","status":"failure","attempt":1,"task_id":1,"steps":[
 {"number":0,"name":"Set up job","status":"success"},
 {"number":1,"name":"Run tests","status":"failure","started":"2026-11-22T09:50:00Z","stopped":"2026-11-22T09:50:12Z"}]}`
)

func failLog() string {
	var b strings.Builder
	for i := range 4 {
		fmt.Fprintf(&b, "2026-11-22T09:50:%02d.1Z ok step %d\n", i, i)
	}
	b.WriteString("2026-11-22T09:50:07.1Z FAIL TestSessionRefresh (0.02s)\n")
	b.WriteString("2026-11-22T09:50:08.1Z exit status 1\n")
	return b.String()
}

// failRoutes serves one failed run (1234) with one failed job.
func failRoutes() routes {
	return routes{
		"GET /repos/o/r/actions/runs/1234":      failRun,
		"GET /repos/o/r/actions/runs/1234/jobs": "[" + failJob + "]",
		"GET /repos/o/r/actions/jobs/5678":      failJob,
		"GET /repos/o/r/actions/jobs/5678/logs": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.ServeContent(w, r, "x.log", time.Time{}, strings.NewReader(failLog()))
		}),
	}
}

func TestCIFailureRun(t *testing.T) {
	f := newFake(failRoutes())
	got, isErr := call(t, f.env(t), "ci_failure", map[string]any{"run": 1234, "tail": 3})
	if isErr {
		t.Fatal(got)
	}
	testutil.Golden(t, "ci_failure_run", got)
	if strings.Contains(got, "target:") {
		t.Fatal("explicit selector must not print a target line")
	}
}

func TestCIFailureNumber(t *testing.T) {
	r := failRoutes()
	r["GET /repos/o/r/actions/runs"] = `{"workflow_runs":[` + failRun + `],"total_count":1}`
	f := newFake(r)
	got, isErr := call(t, f.env(t), "ci_failure", map[string]any{"number": 56})
	if isErr || !strings.Contains(got, "run 1234 no.56 failure") {
		t.Fatalf("err=%v\n%s", isErr, got)
	}
}

func TestCIFailurePRAndGroups(t *testing.T) {
	runs := `{"workflow_runs":[
 {"id":1250,"index_in_repo":13,"workflow_id":"docs.yml","status":"success","event":"push","prettyref":"fix/login","commit_sha":"` + sha40 + `"},
 ` + failRun + `,
 {"id":1200,"index_in_repo":50,"workflow_id":"ci.yml","status":"failure","event":"push","prettyref":"main","commit_sha":"` + sha40 + `"}],"total_count":3}`
	r := failRoutes()
	r["GET /repos/o/r/pulls/42"] = `{"head":{"sha":"` + sha40 + `"}}`
	r["GET /repos/o/r/actions/runs"] = runs
	f := newFake(r)
	got, isErr := call(t, f.env(t), "ci_failure", map[string]any{"pr": 42})
	if isErr {
		t.Fatal(got)
	}
	testutil.Golden(t, "ci_failure_pr", got)
	if strings.Contains(got, "run 1200") {
		t.Fatal("superseded run of the same workflow must be dropped")
	}
}

func TestCIFailureShortSHAExpanded(t *testing.T) {
	r := failRoutes()
	r["GET /repos/o/r/git/commits/8a1b2c3"] = `{"sha":"` + sha40 + `"}`
	r["GET /repos/o/r/actions/runs"] = `{"workflow_runs":[` + failRun + `],"total_count":1}`
	f := newFake(r)
	got, isErr := call(t, f.env(t), "ci_failure", map[string]any{"sha": "8a1b2c3"})
	if isErr || !strings.Contains(got, "failed job 5678") {
		t.Fatalf("err=%v\n%s", isErr, got)
	}
}

func TestCIFailureDefaultTargetNoRun(t *testing.T) {
	f := newFake(routes{
		"GET /repos/o/r":                              `{"default_branch":"main"}`,
		"GET /repos/o/r/commits":                      `[{"sha":"` + sha40 + `"}]`,
		"GET /repos/o/r/actions/runs":                 `{"workflow_runs":[],"total_count":0}`,
		"GET /repos/o/r/commits/" + sha40 + "/status": `{"statuses":[{"id":1,"status":"failure","context":"ext/ci","description":"Build broke","target_url":"https://ci.example.com/1"}]}`,
	})
	got, isErr := call(t, f.env(t), "ci_failure", map[string]any{})
	if isErr {
		t.Fatal(got)
	}
	testutil.Golden(t, "ci_failure_default_external", got)
}

func TestCIFailureCurrentBranchPR(t *testing.T) {
	r := failRoutes()
	r["GET /repos/o/r"] = `{"default_branch":"main"}`
	r["GET /repos/o/r/branches/feature"] = `{"name":"feature"}`
	r["GET /repos/o/r/pulls"] = `[{"number":42,"head":{"ref":"feature","repo":{"full_name":"o/r"}}}]`
	r["GET /repos/o/r/pulls/42"] = `{"head":{"sha":"` + sha40 + `"}}`
	r["GET /repos/o/r/actions/runs"] = `{"workflow_runs":[` + failRun + `],"total_count":1}`
	f := newFake(r)
	env := f.env(t)
	gitDir(t, env, "feature")
	got, isErr := call(t, env, "ci_failure", map[string]any{})
	if isErr {
		t.Fatal(got)
	}
	if !strings.HasPrefix(got, "target: pr #42 (branch feature)\n") || !strings.Contains(got, "failed job 5678") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestCIFailureInvalid(t *testing.T) {
	f := newFake(routes{})
	env := f.env(t)
	for name, args := range map[string]map[string]any{
		"two selectors": {"run": 1, "pr": 2},
		"hash ref":      {"ref": "#12"},
		"negative tail": {"run": 1, "tail": -1},
	} {
		got, isErr := call(t, env, "ci_failure", args)
		if !isErr || !strings.HasPrefix(got, "error: invalid:") {
			t.Errorf("%s: err=%v %q", name, isErr, got)
		}
	}
	if n := len(f.calls); n != 0 {
		t.Errorf("invalid input must not reach the server, got %v", f.calls)
	}
}

func TestCIFailureNotFound(t *testing.T) {
	f := newFake(routes{})
	got, isErr := call(t, f.env(t), "ci_failure", map[string]any{"run": 99})
	if !isErr || !strings.HasPrefix(got, "error:") {
		t.Fatalf("err=%v %q", isErr, got)
	}
}
