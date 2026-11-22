package integration

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The `run` tool against a live instance without a runner: runs stay
// `waiting`, so everything that needs a finished job (logs with content,
// rerun_job) is covered by the runner e2e in actions_e2e_test.go.

const runCIWorkflow = `name: CI
on:
  push:
  workflow_dispatch:
jobs:
  ok:
    runs-on: self-hosted
    steps:
      - run: echo hello
  bad:
    runs-on: self-hosted
    steps:
      - run: echo before-failure; exit 3
`

const runManualWorkflow = `name: Manual
on:
  workflow_dispatch:
    inputs:
      env:
        description: Target environment
        type: choice
        options: [dev, prod]
        required: true
        default: dev
      debug:
        type: boolean
        default: false
jobs:
  deploy:
    runs-on: self-hosted
    steps:
      - run: echo ${{ inputs.env }}
`

const runStrictWorkflow = `name: Strict
on:
  workflow_dispatch:
    inputs:
      token_name:
        description: Needed name
        required: true
jobs:
  x:
    runs-on: self-hosted
    steps:
      - run: echo ${{ inputs.token_name }}
`

var (
	reRunID   = regexp.MustCompile(`run (\d+) no\.(\d+)`)
	reRunLine = regexp.MustCompile(`(?m)^run (\d+) no\.(\d+) (\w+)`)
	reJobLine = regexp.MustCompile(`(?m)^job (\d+) (\S+) (\w+)`)
)

// runMkRepo creates a private repo with the three workflow files committed on
// main and removes it with the test. It returns "owner/name".
func runMkRepo(t *testing.T) string {
	t.Helper()
	skipUnlessIntegration(t)
	name := uniqueName("itrun")
	st, body := rawAPI(t, http.MethodPost, "/user/repos", map[string]any{
		"name": name, "private": true, "auto_init": true, "default_branch": "main",
	})
	if st != http.StatusCreated {
		t.Fatalf("create repo: %d %s", st, body)
	}
	var r struct {
		FullName string `json:"full_name"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.FullName == "" {
		t.Fatalf("repo response: %s", body)
	}
	t.Cleanup(func() { rawAPI(t, http.MethodDelete, "/repos/"+r.FullName, nil) })
	// ordered: ci.yml last, so only its commit triggers a push run
	for _, f := range []struct{ file, content string }{
		{"strict.yml", runStrictWorkflow},
		{"manual.yml", runManualWorkflow},
		{"ci.yml", runCIWorkflow},
	} {
		file, content := f.file, f.content
		st, body := rawAPI(t, http.MethodPost, "/repos/"+r.FullName+"/contents/.forgejo/workflows/"+file, map[string]any{
			"content": base64.StdEncoding.EncodeToString([]byte(content)),
			"message": "add " + file, "branch": "main",
		})
		if st != http.StatusCreated {
			t.Fatalf("put %s: %d %s", file, st, body)
		}
	}
	return r.FullName
}

// runWaitForRun polls `run list` until a run exists and returns its id and number.
func runWaitForRun(t *testing.T, repo string) (id, number int) {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for {
		text, isErr := call(t, "run", map[string]any{"action": "list", "repo": repo, "limit": 5})
		if isErr {
			t.Fatalf("run list: %s", text)
		}
		if m := reRunLine.FindStringSubmatch(text); m != nil {
			id, _ = strconv.Atoi(m[1])
			number, _ = strconv.Atoi(m[2])
			return id, number
		}
		if time.Now().After(deadline) {
			t.Skipf("no Actions run appeared after the push in 40 s (Actions disabled on this instance?): %s", text)
		}
		time.Sleep(2 * time.Second)
	}
}

func TestRunWorkflowsAndList(t *testing.T) {
	repo := runMkRepo(t)

	wf := mustOK(t, "run", map[string]any{"action": "workflows", "repo": repo})
	golden(t, "run_workflows", wf)
	for _, want := range []string{
		"ci.yml", "manual.yml", "strict.yml",
		"env(choice dev|prod, required, default dev)", "debug(boolean, default false)",
		"token_name(string, required)", "on: push,workflow_dispatch",
	} {
		if !strings.Contains(wf, want) {
			t.Errorf("workflows output misses %q:\n%s", want, wf)
		}
	}
	if v := mustOK(t, "run", map[string]any{"action": "workflows", "repo": repo, "verbose": true}); !strings.Contains(v, "jobs: ok,bad") && !strings.Contains(v, "jobs: bad,ok") {
		t.Errorf("verbose workflows must list jobs:\n%s", v)
	}

	id, number := runWaitForRun(t, repo)

	list := mustOK(t, "run", map[string]any{"action": "list", "repo": repo})
	if !strings.HasPrefix(list, "# runs ") || !strings.Contains(list, "ci.yml") {
		t.Fatalf("list: %s", list)
	}
	for name, args := range map[string]map[string]any{
		"ref":      {"ref": "main"},
		"workflow": {"workflow": "ci.yml"},
		"event":    {"event": "push"},
		"number":   {"number": number},
		"status":   {"status": []string{"waiting", "running", "success", "failure", "blocked", "unknown"}},
	} {
		args["action"], args["repo"] = "list", repo
		if out := mustOK(t, "run", args); !strings.Contains(out, "run "+strconv.Itoa(id)+" ") {
			t.Errorf("filter %s lost run %d:\n%s", name, id, out)
		}
	}
	if out := mustOK(t, "run", map[string]any{"action": "list", "repo": repo, "workflow": "manual.yml"}); out != "# runs 0/0" {
		t.Errorf("manual.yml has no runs yet, got: %s", out)
	}
	if e := mustErr(t, "run", map[string]any{"action": "list", "repo": repo, "ref": "#1"}); !strings.Contains(e, "use pr=12") {
		t.Errorf("ref=#1: %s", e)
	}

	get := mustOK(t, "run", map[string]any{"action": "get", "repo": repo, "run": id, "include": []string{"steps"}})
	if !strings.HasPrefix(get, "run "+strconv.Itoa(id)+" no."+strconv.Itoa(number)) || !strings.Contains(get, "\nurl: ") {
		t.Fatalf("get: %s", get)
	}
	jobs := reJobLine.FindAllStringSubmatch(get, -1)
	if len(jobs) != 2 {
		t.Fatalf("want jobs ok and bad, got: %s", get)
	}
	byNumber := mustOK(t, "run", map[string]any{"action": "get", "repo": repo, "number": number})
	if !strings.HasPrefix(byNumber, "run "+strconv.Itoa(id)+" ") {
		t.Errorf("get by number: %s", byNumber)
	}

	jobID, _ := strconv.Atoi(jobs[0][1])
	job := mustOK(t, "run", map[string]any{"action": "job", "repo": repo, "job": jobID})
	if !strings.HasPrefix(job, "job "+jobs[0][1]+" "+jobs[0][2]) {
		t.Errorf("job: %s", job)
	}

	// no runner: the job never started, which is a result and not an error
	logText := mustOK(t, "run", map[string]any{"action": "log", "repo": repo, "job": jobID})
	if !strings.Contains(logText, "no log:") {
		t.Errorf("log of a job that did not run: %s", logText)
	}
	byName := mustOK(t, "run", map[string]any{"action": "log", "repo": repo, "run": id, "name": jobs[0][2], "tail": 5})
	if !strings.Contains(byName, "no log:") {
		t.Errorf("log by run+name: %s", byName)
	}
	if e := mustErr(t, "run", map[string]any{"action": "log", "repo": repo, "run": id, "name": "nope"}); !strings.Contains(e, "no job named") {
		t.Errorf("unknown job name: %s", e)
	}

	art := mustOK(t, "run", map[string]any{"action": "artifacts", "repo": repo})
	if art != "# artifacts 0/0" {
		t.Errorf("artifacts: %s", art)
	}
	if byRun := mustOK(t, "run", map[string]any{"action": "artifacts", "repo": repo, "run": id}); byRun != "# artifacts 0/0" {
		t.Errorf("run artifacts: %s", byRun)
	}
	if e := mustErr(t, "run", map[string]any{"action": "artifact_download", "repo": repo, "id": 999999, "save_to": uniqueName("it") + ".zip"}); !strings.Contains(e, "404") {
		t.Errorf("unknown artifact: %s", e)
	}
	if e := mustErr(t, "run", map[string]any{"action": "get", "repo": repo, "run": id + 100000}); !strings.Contains(e, "404") {
		t.Errorf("unknown run: %s", e)
	}
}

func TestRunDispatchCancelRerunDelete(t *testing.T) {
	repo := runMkRepo(t)
	runWaitForRun(t, repo) // Actions are on

	out := mustOK(t, "run", map[string]any{
		"action": "dispatch", "repo": repo, "workflow": "manual.yml",
		"inputs": map[string]any{"env": "prod", "debug": true},
	})
	m := reRunID.FindStringSubmatch(out)
	if !strings.HasPrefix(out, "ok: dispatched manual.yml on main: run ") || m == nil || !strings.Contains(out, "jobs: deploy") {
		t.Fatalf("dispatch: %s", out)
	}
	id, _ := strconv.Atoi(m[1])

	// the dispatched run carries the event and a waiting job
	get := mustOK(t, "run", map[string]any{"action": "get", "repo": repo, "run": id})
	if !strings.Contains(get, "workflow_dispatch") || !strings.Contains(get, "job ") {
		t.Errorf("get dispatched run: %s", get)
	}

	// required input without default/value: the server's message is shown
	e := mustErr(t, "run", map[string]any{"action": "dispatch", "repo": repo, "workflow": "strict.yml"})
	if !strings.Contains(e, "400") || !strings.Contains(e, "input required") {
		t.Errorf("missing required input: %s", e)
	}
	if e := mustErr(t, "run", map[string]any{"action": "dispatch", "repo": repo, "workflow": "absent.yml"}); !strings.Contains(e, "not found") {
		t.Errorf("unknown workflow: %s", e)
	}
	if e := mustErr(t, "run", map[string]any{"action": "dispatch", "repo": repo, "workflow": "manual.yml", "ref": "no-such-branch"}); !strings.Contains(e, "400") && !strings.Contains(e, "404") {
		t.Errorf("unknown ref: %s", e)
	}

	// delete refuses an unfinished run before the server turns it into a 500
	if e := mustErr(t, "run", map[string]any{"action": "delete", "repo": repo, "run": id}); !strings.Contains(e, "only finished runs") {
		t.Errorf("delete of a waiting run: %s", e)
	}
	if e := mustErr(t, "run", map[string]any{"action": "rerun", "repo": repo, "run": id}); !strings.Contains(e, "wait for it to finish or cancel it first") {
		t.Errorf("rerun of a waiting run: %s", e)
	}

	cancel := mustOK(t, "run", map[string]any{"action": "cancel", "repo": repo, "run": id})
	if cancel != "ok: run "+strconv.Itoa(id)+" cancelled" {
		t.Fatalf("cancel: %s", cancel)
	}
	again := mustOK(t, "run", map[string]any{"action": "cancel", "repo": repo, "run": id})
	if again != "ok: run "+strconv.Itoa(id)+" already cancelled (cancel was a no-op)" {
		t.Errorf("second cancel: %s", again)
	}

	rerun := mustOK(t, "run", map[string]any{"action": "rerun", "repo": repo, "run": id})
	if !strings.HasPrefix(rerun, "ok: rerun started: run "+strconv.Itoa(id)+" ") {
		t.Fatalf("rerun: %s", rerun)
	}
	mustOK(t, "run", map[string]any{"action": "cancel", "repo": repo, "run": id})

	if d := mustOK(t, "run", map[string]any{"action": "delete", "repo": repo, "run": id}); d != "ok: deleted run "+strconv.Itoa(id) {
		t.Fatalf("delete: %s", d)
	}
	if e := mustErr(t, "run", map[string]any{"action": "get", "repo": repo, "run": id}); !strings.Contains(e, "404") {
		t.Errorf("deleted run still readable: %s", e)
	}
}

func TestRunDispatchWaitTimesOutWithoutRunner(t *testing.T) {
	repo := runMkRepo(t)
	runWaitForRun(t, repo)
	out := mustOK(t, "run", map[string]any{
		"action": "dispatch", "repo": repo, "workflow": "manual.yml",
		"wait": true, "timeout": 3,
	})
	if !strings.HasPrefix(out, "ok: dispatched manual.yml on main: run ") || !strings.Contains(out, "\nverdict: timeout") || !strings.Contains(out, "\nresume: ci_wait run=") {
		t.Fatalf("dispatch wait: %s", out)
	}
}

func TestRunReadOnlyHidesMutations(t *testing.T) {
	skipUnlessIntegration(t)
	s := newSession(t, map[string]string{"FORGEJO_READ_ONLY": "1"})
	for _, name := range s.toolNames(t) {
		if name == "run" {
			goto found
		}
	}
	t.Fatal("run must stay registered in read-only mode")
found:
	for _, action := range []string{"cancel", "rerun", "rerun_job", "delete", "dispatch", "artifact_delete"} {
		text, isErr := s.call(t, "run", map[string]any{"action": action, "repo": "o/r", "run": 1, "job": 1, "id": 1, "workflow": "ci.yml"})
		if !isErr || !strings.Contains(text, "disabled (read-only mode)") {
			t.Errorf("run %s in read-only mode: %q", action, text)
		}
	}
	if text, isErr := s.call(t, "run", map[string]any{"action": "help"}); isErr || strings.Contains(text, "\ndispatch ") {
		t.Errorf("read-only help must not list dispatch: %q", text)
	}
}
