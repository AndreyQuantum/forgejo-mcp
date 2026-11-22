package ci

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

func runJSON(id, no int, wf, status string) string {
	return fmt.Sprintf(`{"id":%d,"index_in_repo":%d,"workflow_id":%q,"status":%q,"event":"pull_request","prettyref":"#42","commit_sha":%q,"duration":41000000000}`, id, no, wf, status, sha40)
}

func jobJSON(id int, name, status string) string {
	return fmt.Sprintf(`{"id":%d,"name":%q,"status":%q,"attempt":1,"task_id":%d}`, id, name, status, id)
}

// prRoutes serves PR #42 on branch main whose head is sha40 with the given
// statuses/runs bodies.
func prRoutes(statuses, runs string) routes {
	return routes{
		"GET /repos/o/r/pulls/42":                     `{"state":"open","mergeable":true,"head":{"sha":"` + sha40 + `"},"base":{"ref":"main"}}`,
		"GET /repos/o/r/commits/" + sha40 + "/status": `{"statuses":` + statuses + `}`,
		"GET /repos/o/r/actions/runs":                 `{"workflow_runs":` + runs + `,"total_count":1}`,
		"GET /repos/o/r/branches/main":                `{"required_approvals":0}`,
		"GET /repos/o/r/pulls/42/reviews":             `[]`,
	}
}

func TestCIWaitRunGreen(t *testing.T) {
	f := newFake(routes{
		"GET /repos/o/r/actions/runs/1236":      runJSON(1236, 58, "ci.yml", "success"),
		"GET /repos/o/r/actions/runs/1236/jobs": "[" + jobJSON(1, "build", "success") + "," + jobJSON(2, "lint", "success") + "]",
	})
	got, isErr := call(t, f.env(t), "ci_wait", map[string]any{"run": 1236})
	if isErr {
		t.Fatal(got)
	}
	testutil.Golden(t, "ci_wait_run_green", got)
}

func TestCIWaitRunFailedDigest(t *testing.T) {
	r := failRoutes()
	// the run is still running while one job has failed: fail-fast reports failed
	r["GET /repos/o/r/actions/runs/1234"] = strings.Replace(failRun, `"status":"failure"`, `"status":"running"`, 1)
	f := newFake(r)
	got, isErr := call(t, f.env(t), "ci_wait", map[string]any{"run": 1234})
	if isErr {
		t.Fatal(got)
	}
	testutil.Golden(t, "ci_wait_run_failed", got)

	// with_failure=false drops the digest
	f = newFake(r)
	got, _ = call(t, f.env(t), "ci_wait", map[string]any{"run": 1234, "with_failure": false})
	if strings.Contains(got, "failed job") || !strings.HasPrefix(got, "verdict: failure") {
		t.Fatalf("with_failure=false:\n%s", got)
	}
}

func TestCIWaitRunNumberTimeoutResume(t *testing.T) {
	var polls atomic.Int32
	f := newFake(routes{
		"GET /repos/o/r/actions/runs": `{"workflow_runs":[` + runJSON(1237, 58, "ci.yml", "running") + `],"total_count":1}`,
		"GET /repos/o/r/actions/runs/1237": func(w http.ResponseWriter, r *http.Request) {
			polls.Add(1)
			_, _ = w.Write([]byte(runJSON(1237, 58, "ci.yml", "running")))
		},
		"GET /repos/o/r/actions/runs/1237/jobs": "[" + jobJSON(1, "build", "success") + "," + jobJSON(2, "test", "running") + "]",
	})
	env := f.env(t)
	got, isErr := call(t, env, "ci_wait", map[string]any{"number": 58, "timeout": 10})
	if isErr {
		t.Fatal(got)
	}
	testutil.Golden(t, "ci_wait_run_timeout", got)
	if polls.Load() < 3 {
		t.Fatalf("expected several polls, got %d", polls.Load())
	}
	if rc := env.Clock.(*recClock); len(rc.sleeps) == 0 {
		t.Fatal("polling must sleep through Env.Clock")
	}
}

func TestCIWaitPRGreen(t *testing.T) {
	f := newFake(prRoutes(
		`[{"id":1,"status":"success","context":"ci / build"},{"id":2,"status":"success","context":"ci / test"}]`,
		"["+runJSON(1236, 58, "ci.yml", "success")+"]"))
	got, isErr := call(t, f.env(t), "ci_wait", map[string]any{"pr": 42})
	if isErr {
		t.Fatal(got)
	}
	testutil.Golden(t, "ci_wait_pr_green", got)
}

func TestCIWaitPRTimeoutResumeKeepsSelector(t *testing.T) {
	f := newFake(prRoutes(
		`[{"id":1,"status":"pending","context":"ci / build"}]`,
		"["+runJSON(1236, 58, "ci.yml", "running")+"]"))
	got, isErr := call(t, f.env(t), "ci_wait", map[string]any{"pr": 42, "timeout": 5})
	if isErr {
		t.Fatal(got)
	}
	if !strings.Contains(got, "verdict: timeout") || !strings.Contains(got, "resume: ci_wait pr=42") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestCIWaitDefaultTargetAndClamp(t *testing.T) {
	r := routes{
		"GET /repos/o/r":                              `{"default_branch":"main"}`,
		"GET /repos/o/r/commits":                      `[{"sha":"` + sha40 + `"}]`,
		"GET /repos/o/r/commits/" + sha40 + "/status": `{"statuses":[{"id":1,"status":"pending","context":"ci / build"}]}`,
		"GET /repos/o/r/actions/runs":                 `{"workflow_runs":[` + runJSON(1236, 58, "ci.yml", "running") + `],"total_count":1}`,
	}
	waited := func(text string) time.Duration {
		for _, l := range strings.Split(text, "\n") {
			if rest, ok := strings.CutPrefix(l, "verdict: timeout | waited "); ok {
				d, err := time.ParseDuration(strings.Fields(rest)[0])
				if err != nil {
					t.Fatalf("parse %q: %v", rest, err)
				}
				return d
			}
		}
		t.Fatalf("no timeout line:\n%s", text)
		return 0
	}
	small := withCfg(func(c *config.Config) { c.WaitDefault, c.WaitMax = 4*time.Second, 8*time.Second })

	got, _ := call(t, newFake(r).env(t, small), "ci_wait", map[string]any{})
	if !strings.HasPrefix(got, "target: branch main (default)\nverdict: timeout") || !strings.HasSuffix(got, "resume: ci_wait ref=main") {
		t.Fatalf("got:\n%s", got)
	}
	if w := waited(got); w > 4*time.Second {
		t.Fatalf("default timeout exceeded: %s", w)
	}
	got, _ = call(t, newFake(r).env(t, small), "ci_wait", map[string]any{"timeout": 600})
	if w := waited(got); w > 8*time.Second || w < 5*time.Second {
		t.Fatalf("timeout must be clamped to FORGEJO_WAIT_MAX (8s), waited %s", w)
	}
}

func TestCIWaitShortSHAResume(t *testing.T) {
	r := prRoutes(`[{"id":1,"status":"pending","context":"ci / build"}]`, "[]")
	r["GET /repos/o/r/git/commits/8a1b2c3"] = `{"sha":"` + sha40 + `"}`
	got, isErr := call(t, newFake(r).env(t), "ci_wait", map[string]any{"sha": "8a1b2c3", "timeout": 3})
	if isErr || !strings.Contains(got, "resume: ci_wait sha=8a1b2c3") {
		t.Fatalf("err=%v\n%s", isErr, got)
	}
}

func TestCIWaitInvalid(t *testing.T) {
	f := newFake(routes{})
	env := f.env(t)
	for name, args := range map[string]map[string]any{
		"two selectors": {"run": 1, "number": 2},
		"neg timeout":   {"pr": 1, "timeout": -3},
	} {
		got, isErr := call(t, env, "ci_wait", args)
		if !isErr || !strings.HasPrefix(got, "error: invalid:") {
			t.Errorf("%s: err=%v %q", name, isErr, got)
		}
	}
}

func TestCIWaitFullSHAIsLowercased(t *testing.T) {
	upper := strings.ToUpper(sha40)
	var headSHA string
	r := prRoutes(`[{"id":1,"status":"pending","context":"ci / build"}]`, "[]")
	r["GET /repos/o/r/actions/runs"] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headSHA = r.URL.Query().Get("head_sha")
		_, _ = w.Write([]byte(`{"workflow_runs":[],"total_count":0}`))
	})
	got, isErr := call(t, newFake(r).env(t), "ci_wait", map[string]any{"sha": upper, "timeout": 3})
	if isErr {
		t.Fatal(got)
	}
	if headSHA != sha40 {
		t.Fatalf("head_sha = %q, want %q", headSHA, sha40)
	}
}

func TestCIWaitSHA256CommitID(t *testing.T) {
	long := strings.Repeat("ab", 32)
	r := routes{
		"GET /repos/o/r/commits/" + long + "/status": `{"statuses":[{"id":1,"status":"success","context":"x"}]}`,
		"GET /repos/o/r/actions/runs":                `{"workflow_runs":[],"total_count":0}`,
	}
	got, isErr := call(t, newFake(r).env(t), "ci_wait", map[string]any{"sha": long, "timeout": 3})
	if isErr || strings.Contains(got, "bad sha") {
		t.Fatalf("err=%v\n%s", isErr, got)
	}
}
