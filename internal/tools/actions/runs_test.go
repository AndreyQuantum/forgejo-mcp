package actions

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

// runJSON builds an ActionRun as the server sends it, including the heavy
// embedded objects the projection must drop.
func runJSON(id, idx int, status, wf, title, event, ref, sha, user, created string, durNs int64) string {
	return fmt.Sprintf(`{"id":%d,"title":%q,"workflow_id":%q,"index_in_repo":%d,"status":%q,"event":%q,"prettyref":%q,`+
		`"commit_sha":%q,"created":%q,"started":%q,"stopped":%q,"duration":%d,"need_approval":false,`+
		`"html_url":"http://example/o/r/actions/runs/%d","trigger_event":%q,"event_payload":"{\"huge\":true}",`+
		`"trigger_user":{"login":%q,"email":"x@example.com","avatar_url":"http://example/a.png"},`+
		`"repository":{"id":1,"full_name":"o/r","owner":{"login":"o"},"description":"big object"}}`,
		id, title, wf, idx, status, event, ref, sha, created, created, created, durNs, idx, event, user)
}

const (
	sha1 = "8a1b2c3d00000000000000000000000000000001"
	sha2 = "77aa11bb00000000000000000000000000000002"
	sha3 = "5e5e5e5e00000000000000000000000000000003"
)

func runsBody(total int, runs ...string) string {
	return fmt.Sprintf(`{"workflow_runs":[%s],"total_count":%d}`, strings.Join(runs, ","), total)
}

func threeRuns() string {
	return runsBody(58,
		runJSON(1236, 58, "running", "ci.yml", "fix login", "pull_request", "#12", sha1, "alice", "2026-11-22T10:15:00Z", 41e9),
		runJSON(1234, 56, "failure", "ci.yml", "wip", "push", "main", sha2, "alice", "2026-11-22T09:50:00Z", 63e9),
		runJSON(1230, 55, "success", "release.yml", "release", "push", "v1.2", sha3, "bob", "2026-11-21T18:02:00Z", 250e9),
	)
}

func TestListQueryAndGolden(t *testing.T) {
	s := newFake(t)
	s.reply("GET /repos/o/r/actions/runs", 200, threeRuns())
	s.reply("GET /repos/o/r/git/commits/8a1b2c3d", 200, `{"sha":"`+sha1+`"}`)
	text := runOK(t, s, map[string]any{
		"action": "list", "limit": 3, "status": []string{"failure", "RUNNING"}, "workflow": "ci.yml",
		"ref": "main", "sha": "8a1b2c3d", "event": "push", "number": 56,
	})
	testutil.Golden(t, "list", text)
	r := s.only("GET /repos/o/r/actions/runs")
	wantQuery(t, r, map[string][]string{
		"status": {"failure", "running"}, "workflow_id": {"ci.yml"}, "ref": {"refs/heads/main"},
		"head_sha": {sha1}, "event": {"push"}, "run_number": {"56"}, "limit": {"3"}, "page": {"1"},
	})
}

func TestListDefaultsAndPR(t *testing.T) {
	s := newFake(t)
	s.reply("GET /repos/o/r/actions/runs", 200, runsBody(0))
	text := runOK(t, s, map[string]any{"action": "list", "pr": 12})
	if text != "# runs 0/0" {
		t.Fatalf("got %q", text)
	}
	r := s.only("GET /repos/o/r/actions/runs")
	wantQuery(t, r, map[string][]string{"ref": {"refs/pull/12/head"}, "limit": {"10"}})
}

func TestListRejects(t *testing.T) {
	s := newFake(t)
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"action": "list", "ref": "#12"}, "error: invalid: use pr=12"},
		{map[string]any{"action": "list", "ref": "main", "pr": 3}, "error: invalid: give only one of ref, pr"},
		{map[string]any{"action": "list", "status": []string{"in_progress"}}, `error: invalid: status "in_progress"; valid: unknown,waiting`},
	} {
		got := runErr(t, s, tc.args)
		if !strings.HasPrefix(got, tc.want) {
			t.Errorf("args %v: got %q, want prefix %q", tc.args, got, tc.want)
		}
	}
	if n := len(s.seen("GET /repos/o/r/actions/runs")); n != 0 {
		t.Fatalf("a rejected call must not reach the server, got %d requests", n)
	}
}

func TestListBareRefRetriedAsTag(t *testing.T) {
	s := newFake(t)
	s.on("GET /repos/o/r/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("ref") == "refs/tags/v1.2" {
			_, _ = w.Write([]byte(runsBody(1, runJSON(1230, 55, "success", "release.yml", "release", "push", "v1.2", sha3, "bob", "2026-11-21T18:02:00Z", 250e9))))
			return
		}
		_, _ = w.Write([]byte(runsBody(0)))
	})
	text := runOK(t, s, map[string]any{"action": "list", "ref": "v1.2"})
	if !strings.Contains(text, "run 1230 no.55 success") {
		t.Fatalf("tag runs not found: %s", text)
	}
	got := s.seen("GET /repos/o/r/actions/runs")
	if len(got) != 2 || got[0].Query.Get("ref") != "refs/heads/v1.2" || got[1].Query.Get("ref") != "refs/tags/v1.2" {
		t.Fatalf("want branch then tag lookup, got %+v", got)
	}
}

func jobsBody() string {
	return `[` +
		`{"id":5680,"run_id":1234,"name":"test","status":"skipped","attempt":1,"needs":["build"],"runs_on":["docker"],"task_id":0},` +
		`{"id":5678,"run_id":1234,"name":"build","status":"failure","attempt":1,"needs":[],"runs_on":["docker"],"task_id":91},` +
		`{"id":5679,"run_id":1234,"name":"lint","status":"success","attempt":1,"needs":[],"runs_on":["docker"],"task_id":92}]`
}

func jobDetail() string {
	return `{"id":5678,"run_id":1234,"name":"build","status":"failure","attempt":1,"needs":[],"runs_on":["docker"],"task_id":91,"steps":[` +
		`{"number":0,"name":"Set up job","status":"success","started":"2026-11-22T09:50:00Z","stopped":"2026-11-22T09:50:02Z"},` +
		`{"number":1,"name":"Run tests","status":"failure","started":"2026-11-22T09:50:02Z","stopped":"2026-11-22T09:50:14Z"},` +
		`{"number":2,"name":"Complete job","status":"skipped"}]}`
}

func TestGetWithJobsGolden(t *testing.T) {
	s := newFake(t)
	s.reply("GET /repos/o/r/actions/runs/1234", 200,
		runJSON(1234, 56, "failure", "ci.yml", "fix: login\nbody", "push", "main", sha2, "alice", "2026-11-22T09:50:00Z", 63e9))
	s.reply("GET /repos/o/r/actions/runs/1234/jobs", 200, jobsBody())
	text := runOK(t, s, map[string]any{"action": "get", "run": 1234})
	testutil.Golden(t, "get", text)
	if n := len(s.seen("GET /repos/o/r/actions/jobs/5678")); n != 0 {
		t.Fatalf("steps must not be fetched without include=steps")
	}
}

func TestGetByNumberWithStepsGolden(t *testing.T) {
	s := newFake(t)
	s.reply("GET /repos/o/r/actions/runs", 200,
		runsBody(1, runJSON(1234, 56, "failure", "ci.yml", "wip", "push", "main", sha2, "alice", "2026-11-22T09:50:00Z", 63e9)))
	s.reply("GET /repos/o/r/actions/runs/1234", 200,
		runJSON(1234, 56, "failure", "ci.yml", "wip", "push", "main", sha2, "alice", "2026-11-22T09:50:00Z", 63e9))
	s.reply("GET /repos/o/r/actions/runs/1234/jobs", 200, jobsBody())
	s.reply("GET /repos/o/r/actions/jobs/5678", 200, jobDetail())
	text := runOK(t, s, map[string]any{"action": "get", "number": 56, "include": []string{"steps"}, "verbose": true})
	testutil.Golden(t, "get_steps_verbose", text)
	wantQuery(t, s.only("GET /repos/o/r/actions/runs"), map[string][]string{"run_number": {"56"}})
	// success job and the never-started job have no steps to fetch
	for _, k := range []string{"GET /repos/o/r/actions/jobs/5679", "GET /repos/o/r/actions/jobs/5680"} {
		if n := len(s.seen(k)); n != 0 {
			t.Errorf("%s must not be requested", k)
		}
	}
}

func TestGetValidation(t *testing.T) {
	s := newFake(t)
	got := runErr(t, s, map[string]any{"action": "get"})
	if got != "error: invalid: pass run (id) or number (no.N)" {
		t.Errorf("got %q", got)
	}
	got = runErr(t, s, map[string]any{"action": "get", "run": 1, "number": 2})
	if !strings.HasPrefix(got, "error: invalid: give only one of run, number") {
		t.Errorf("got %q", got)
	}
	got = runErr(t, s, map[string]any{"action": "get", "run": 1, "include": []string{"logs"}})
	if !strings.HasPrefix(got, `error: invalid: include "logs"`) {
		t.Errorf("got %q", got)
	}
	s.reply("GET /repos/o/r/actions/runs", 200, runsBody(0))
	got = runErr(t, s, map[string]any{"action": "get", "number": 999})
	if got != "error: no run with number no.999" {
		t.Errorf("unknown number: %q", got)
	}
}

func TestJobGolden(t *testing.T) {
	s := newFake(t)
	s.reply("GET /repos/o/r/actions/jobs/5678", 200, jobDetail())
	text := runOK(t, s, map[string]any{"action": "job", "job": 5678})
	testutil.Golden(t, "job", text)
	text = runErr(t, s, map[string]any{"action": "job"})
	if !strings.Contains(text, "missing param job") {
		t.Errorf("required job: %q", text)
	}
}

func TestCancel(t *testing.T) {
	s := newFake(t)
	run := func(status string) string {
		return runJSON(1234, 56, status, "ci.yml", "wip", "push", "main", sha2, "alice", "2026-11-22T09:50:00Z", 0)
	}
	// running -> POST -> cancelled
	state := "running"
	s.on("GET /repos/o/r/actions/runs/1234", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(run(state)))
	})
	s.on("POST /repos/o/r/actions/runs/1234/cancel", func(w http.ResponseWriter, _ *http.Request) {
		state = "cancelled"
		w.WriteHeader(204)
	})
	got := runOK(t, s, map[string]any{"action": "cancel", "run": 1234})
	if got != "ok: run 1234 cancelled" {
		t.Fatalf("got %q", got)
	}
	if n := len(s.seen("POST /repos/o/r/actions/runs/1234/cancel")); n != 1 {
		t.Fatalf("want one POST, got %d", n)
	}

	// already finished: no POST, no-op wording
	s2 := newFake(t)
	s2.reply("GET /repos/o/r/actions/runs/1234", 200, run("success"))
	got = runOK(t, s2, map[string]any{"action": "cancel", "run": 1234})
	if got != "ok: run 1234 already success (cancel was a no-op)" {
		t.Fatalf("got %q", got)
	}
	if n := len(s2.seen("POST /repos/o/r/actions/runs/1234/cancel")); n != 0 {
		t.Fatalf("a finished run must not be cancelled, got %d POSTs", n)
	}

	// the run finished between the check and the POST
	s3 := newFake(t)
	calls := 0
	s3.on("GET /repos/o/r/actions/runs/1234", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		st := "running"
		if calls > 1 {
			st = "failure"
		}
		_, _ = w.Write([]byte(run(st)))
	})
	s3.reply("POST /repos/o/r/actions/runs/1234/cancel", 204, "")
	got = runOK(t, s3, map[string]any{"action": "cancel", "run": 1234})
	if got != "ok: run 1234 already failure (cancel was a no-op)" {
		t.Fatalf("got %q", got)
	}
}

func TestRerun(t *testing.T) {
	run := func(status string) string {
		return runJSON(1234, 56, status, "ci.yml", "wip", "push", "main", sha2, "alice", "2026-11-22T09:50:00Z", 0)
	}
	s := newFake(t)
	state := "failure"
	s.on("GET /repos/o/r/actions/runs/1234", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(run(state))) })
	s.on("POST /repos/o/r/actions/runs/1234/rerun", func(w http.ResponseWriter, _ *http.Request) {
		state = "running"
		w.WriteHeader(204)
	})
	got := runOK(t, s, map[string]any{"action": "rerun", "run": 1234})
	if got != "ok: rerun started: run 1234 running" {
		t.Fatalf("got %q", got)
	}

	s2 := newFake(t)
	s2.reply("GET /repos/o/r/actions/runs/1234", 200, run("running"))
	got = runErr(t, s2, map[string]any{"action": "rerun", "run": 1234})
	if !strings.HasPrefix(got, "error: run 1234 is running;") {
		t.Fatalf("got %q", got)
	}
	if n := len(s2.seen("POST /repos/o/r/actions/runs/1234/rerun")); n != 0 {
		t.Fatalf("pre-check must stop the POST")
	}
}

func TestRerunJob(t *testing.T) {
	s := newFake(t)
	s.reply("GET /repos/o/r/actions/jobs/5678", 200, jobDetail())
	s.reply("POST /repos/o/r/actions/jobs/5678/rerun", 204, "")
	got := runOK(t, s, map[string]any{"action": "rerun_job", "job": 5678})
	if got != `ok: rerun started: job 5678 "build" (and the jobs that need it)` {
		t.Fatalf("got %q", got)
	}

	s2 := newFake(t)
	s2.reply("GET /repos/o/r/actions/jobs/5678", 200, strings.Replace(jobDetail(), `"status":"failure","attempt"`, `"status":"running","attempt"`, 1))
	got = runErr(t, s2, map[string]any{"action": "rerun_job", "job": 5678})
	if !strings.HasPrefix(got, "error: job 5678 is running;") {
		t.Fatalf("got %q", got)
	}
}

func TestDelete(t *testing.T) {
	run := func(status string) string {
		return runJSON(1234, 56, status, "ci.yml", "wip", "push", "main", sha2, "alice", "2026-11-22T09:50:00Z", 0)
	}
	s := newFake(t)
	s.reply("GET /repos/o/r/actions/runs/1234", 200, run("success"))
	s.reply("DELETE /repos/o/r/actions/runs/1234", 204, "")
	got := runOK(t, s, map[string]any{"action": "delete", "run": 1234})
	if got != "ok: deleted run 1234" {
		t.Fatalf("got %q", got)
	}

	s2 := newFake(t)
	s2.reply("GET /repos/o/r/actions/runs/1234", 200, run("waiting"))
	got = runErr(t, s2, map[string]any{"action": "delete", "run": 1234})
	if !strings.Contains(got, "only finished runs can be deleted") {
		t.Fatalf("got %q", got)
	}
	if n := len(s2.seen("DELETE /repos/o/r/actions/runs/1234")); n != 0 {
		t.Fatalf("the DELETE would be a 500; pre-check must stop it")
	}

	// 403 carries the permission hint of the action
	s3 := newFake(t)
	s3.reply("GET /repos/o/r/actions/runs/1234", 200, run("success"))
	s3.reply("DELETE /repos/o/r/actions/runs/1234", 403, `{"message":"forbidden"}`)
	got = runErr(t, s3, map[string]any{"action": "delete", "run": 1234})
	if !strings.Contains(got, "403") || !strings.Contains(got, "repo admin") {
		t.Fatalf("got %q", got)
	}
}

func TestMinVersionGate(t *testing.T) {
	s := newFake(t)
	s.reply("GET /version", 200, `{"version":"15.0.1"}`)
	got := runErr(t, s, map[string]any{"action": "list"})
	if got != "error: run.list needs Forgejo >= 16 (server is 15.0.1)" {
		t.Fatalf("got %q", got)
	}
	// get, dispatch, workflows and artifacts are not gated
	s.reply("GET /repos/o/r/actions/runs/1", 200, runJSON(1, 1, "success", "ci.yml", "t", "push", "main", sha1, "a", "2026-11-22T09:50:00Z", 0))
	s.reply("GET /repos/o/r/actions/runs/1/jobs", 200, `[]`)
	runOK(t, s, map[string]any{"action": "get", "run": 1})
}

func TestReadOnlyHidesMutations(t *testing.T) {
	s := newFake(t)
	text, isErr := callRun(t, s, map[string]any{"action": "cancel", "run": 1}, testutil.WithConfig(func(c *config.Config) { c.ReadOnly = true }))
	if !isErr || !strings.Contains(text, "cancel") {
		t.Fatalf("got %q err=%v", text, isErr)
	}
	if n := len(s.seen("POST /repos/o/r/actions/runs/1/cancel")); n != 0 {
		t.Fatalf("read-only must not reach the server")
	}
}

func TestHelpText(t *testing.T) {
	s := newFake(t)
	text := runOK(t, s, map[string]any{"action": "help"})
	for _, name := range []string{"list", "get", "job", "log", "cancel", "rerun", "rerun_job", "delete", "dispatch", "workflows", "artifacts", "artifact_download", "artifact_delete"} {
		if !strings.Contains(text, name) {
			t.Errorf("help misses %s", name)
		}
	}
}

// Forgejo 17 leaves `event` empty on workflow_dispatch runs and reports the
// real value in `trigger_event`.
func TestEmptyEventFallsBackToTriggerEvent(t *testing.T) {
	body := strings.Replace(
		runJSON(9, 1, "waiting", "m.yml", "M", "workflow_dispatch", "main", sha1, "alice", "2026-11-22T10:00:00Z", 0),
		`"event":"workflow_dispatch"`, `"event":""`, 1)
	s := newFake(t)
	s.reply("GET /repos/o/r/actions/runs", 200, `{"workflow_runs":[`+body+`],"total_count":1}`)
	s.reply("GET /repos/o/r/actions/runs/9", 200, body)
	s.reply("GET /repos/o/r/actions/runs/9/jobs", 200, `[]`)
	list := runOK(t, s, map[string]any{"action": "list"})
	if !strings.Contains(list, "| workflow_dispatch main 8a1b2c3d |") {
		t.Errorf("list: %s", list)
	}
	get := runOK(t, s, map[string]any{"action": "get", "run": 9, "verbose": true})
	if !strings.Contains(get, "\nworkflow_dispatch main 8a1b2c3d by alice") || strings.Contains(get, "trigger_event:") {
		t.Errorf("get: %s", get)
	}
}
