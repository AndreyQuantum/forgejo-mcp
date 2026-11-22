package integration

import (
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/runners"
)

// Runner e2e (DESIGN.md 12.2): a real forgejo-runner container executes
// host-labelled jobs, so no Docker-in-Docker and no actions checkout is
// needed. Needs FORGEJO_TEST_RUNNER=1 and the docker CLI.
//
// Optional environment:
//
//	FORGEJO_TEST_RUNNER_IMAGE     runner image (default: pin in runner.version)
//	FORGEJO_TEST_DOCKER_NETWORK   docker network to attach the runner to
//	FORGEJO_TEST_RUNNER_ADDRESS   instance URL as seen from the container
//	                              (default: localhost/127.0.0.1 -> host.docker.internal)

const e2eManualWorkflow = `name: Manual
on:
  workflow_dispatch:
    inputs:
      env:
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
      - run: echo deploying-to-${{ inputs.env }}
`

var (
	reE2ERunNo  = regexp.MustCompile(`run (\d+) no\.(\d+)`)
	reE2EJobRow = regexp.MustCompile(`(?m)^\s*job (\d+) (\S+) (\w+)`)
	reE2ENext   = regexp.MustCompile(`next_offset[:= ]+(\d+)`)
)

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", args[0], err, out)
	}
	return strings.TrimSpace(string(out))
}

// e2eRunnerAddress maps the instance URL to one reachable from the container.
func e2eRunnerAddress(t *testing.T) string {
	if v := strings.TrimRight(os.Getenv("FORGEJO_TEST_RUNNER_ADDRESS"), "/"); v != "" {
		return v
	}
	u, err := url.Parse(instURL)
	if err != nil {
		t.Fatalf("instance url: %v", err)
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		host := "host.docker.internal"
		if p := u.Port(); p != "" {
			host += ":" + p
		}
		return u.Scheme + "://" + host
	}
	return strings.TrimRight(instURL, "/")
}

// e2eFileCommit commits files to branch through the `file` tool.
func e2eFileCommit(t *testing.T, repo, branch, newBranch, msg string, files map[string]string) {
	t.Helper()
	var ops []any
	for p, c := range files {
		ops = append(ops, map[string]any{"op": "create", "path": p, "content": c})
	}
	args := map[string]any{"action": "commit", "repo": repo, "branch": branch, "message": msg, "ops": ops}
	if newBranch != "" {
		args["new_branch"] = newBranch
	}
	mustOK(t, "file", args)
}

func e2eRunID(t *testing.T, out string) int {
	t.Helper()
	m := reE2ERunNo.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no run id in:\n%s", out)
	}
	id, _ := strconv.Atoi(m[1])
	return id
}

// e2eJobs maps job name to job id for a `run get` text.
func e2eJobs(text string) map[string]int {
	jobs := map[string]int{}
	for _, m := range reE2EJobRow.FindAllStringSubmatch(text, -1) {
		id, _ := strconv.Atoi(m[1])
		jobs[m[2]] = id
	}
	return jobs
}

func e2eFindRun(t *testing.T, repo, sha string) int {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for {
		out := mustOK(t, "run", map[string]any{"action": "list", "repo": repo, "sha": sha, "limit": 10})
		if m := reRunLine.FindStringSubmatch(out); m != nil {
			id, _ := strconv.Atoi(m[1])
			return id
		}
		if time.Now().After(deadline) {
			t.Fatalf("no run for %s:\n%s", sha, out)
		}
		time.Sleep(2 * time.Second)
	}
}

func TestActionsE2E(t *testing.T) {
	requireRunner(t)
	repo := ciMkRepo(t)
	image := runnerImage(t)

	// 1. workflows on main; the push run stays waiting without a runner.
	e2eFileCommit(t, repo, "main", "", "add manual", map[string]string{
		".forgejo/workflows/manual.yml": e2eManualWorkflow,
		"NOTES.md":                      "# e2e\n",
	})
	e2eFileCommit(t, repo, "main", "", "add ci workflow", map[string]string{
		".forgejo/workflows/ci.yml": runCIWorkflow,
	})
	head := ciHead(t, repo, "main")
	runID := e2eFindRun(t, repo, head)

	if l := mustOK(t, "run", map[string]any{"action": "list", "repo": repo}); !strings.Contains(l, "run "+strconv.Itoa(runID)) {
		t.Errorf("run list lacks run %d:\n%s", runID, l)
	}
	if q := mustOK(t, "runner", map[string]any{"action": "queue", "repo": repo}); !strings.HasPrefix(q, "waiting: ") {
		t.Errorf("runner queue:\n%s", q)
	}
	w := mustOK(t, "ci_wait", map[string]any{"repo": repo, "sha": head, "timeout": 5})
	if !strings.Contains(w, "verdict: timeout") || (!strings.Contains(w, "no online runner") && !strings.Contains(w, "runners offline")) {
		t.Errorf("ci_wait without runner:\n%s", w)
	}

	// 2. register through our own tool.
	rname := uniqueName("it-runner")
	reg := mustOK(t, "runner", map[string]any{"action": "register", "repo": repo, "name": rname, "labels": "self-hosted:host"})
	m := reRnRegistered.FindStringSubmatch(reg)
	if m == nil {
		t.Fatalf("register output has no runner id")
	}
	rid, _ := strconv.Atoi(m[1])
	t.Cleanup(func() {
		_, _ = call(t, "runner", map[string]any{"action": "delete", "repo": repo, "id": rid})
	})
	line, ok := runners.ExtractRunnerFile(reg)
	if !ok {
		t.Fatal("register output has no .runner line")
	}
	// The tool prints the URL it talks to; rewrite only the address for the container.
	if from, to := strings.TrimRight(instURL, "/"), e2eRunnerAddress(t); from != to {
		line = strings.Replace(line, `"address":"`+from+`"`, `"address":"`+to+`"`, 1)
	}
	dir := filepath.Join(t.TempDir(), "runner")
	if err := os.MkdirAll(filepath.Join(dir, "work"), 0o777); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(dir, 0o777)
	_ = os.Chmod(filepath.Join(dir, "work"), 0o777)
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".runner", line+"\n")
	write("config.yml", "log:\n  level: info\nrunner:\n  file: /data/.runner\n  capacity: 1\nhost:\n  workdir_parent: /data/work\n")

	// 3. start the container.
	cname := uniqueName("fmcp-it-runner")
	runArgs := []string{"run", "-d", "--name", cname, "--add-host", "host.docker.internal:host-gateway", "-v", dir + ":/data"}
	if n := os.Getenv("FORGEJO_TEST_DOCKER_NETWORK"); n != "" {
		runArgs = append(runArgs, "--network", n)
	}
	runArgs = append(runArgs, image, "forgejo-runner", "daemon", "--config", "/data/config.yml")
	docker(t, runArgs...)
	t.Cleanup(func() {
		if t.Failed() {
			if logs, err := exec.Command("docker", "logs", "--tail", "30", cname).CombinedOutput(); err == nil {
				t.Logf("runner container log tail:\n%s", logs)
			}
		}
		// The runner runs as root and leaves root-owned caches in the bind
		// mount; on Linux the test user cannot delete them, so open them up
		// before t.TempDir's cleanup (registered earlier, runs later).
		if runtime.GOOS != "windows" {
			if err := exec.Command("docker", "exec", cname, "chmod", "-R", "a+rwX", "/data").Run(); err != nil {
				_ = exec.Command("docker", "run", "--rm", "-v", dir+":/data", "--entrypoint", "chmod", image, "-R", "a+rwX", "/data").Run()
			}
		}
		_ = exec.Command("docker", "rm", "-f", cname).Run()
	})

	// 4. wait until the runner is online.
	deadline := time.Now().Add(60 * time.Second)
	for {
		l := mustOK(t, "runner", map[string]any{"action": "list", "repo": repo, "limit": 50})
		if strings.Contains(l, rname+" idle") || strings.Contains(l, rname+" active") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("runner did not come online:\n%s", l)
		}
		time.Sleep(2 * time.Second)
	}

	// 5. fail-fast verdict while the run is still going.
	w = mustOK(t, "ci_wait", map[string]any{"repo": repo, "sha": head, "timeout": 100})
	if !strings.Contains(w, "verdict: failed") {
		t.Fatalf("ci_wait verdict:\n%s", w)
	}
	fail := mustOK(t, "ci_failure", map[string]any{"repo": repo, "sha": head})
	for _, want := range []string{"bad", "before-failure"} {
		if !strings.Contains(fail, want) {
			t.Errorf("ci_failure lacks %q:\n%s", want, fail)
		}
	}

	// wait for the whole run to finish so job ok is final.
	var get string
	deadline = time.Now().Add(90 * time.Second)
	for {
		get = mustOK(t, "run", map[string]any{"action": "get", "repo": repo, "run": runID})
		jobs := reE2EJobRow.FindAllStringSubmatch(get, -1)
		done := len(jobs) == 2
		for _, j := range jobs {
			if j[3] == "running" || j[3] == "waiting" || j[3] == "blocked" {
				done = false
			}
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not finish:\n%s", get)
		}
		time.Sleep(2 * time.Second)
	}
	jobs := e2eJobs(get)
	okJob, badJob := jobs["ok"], jobs["bad"]
	if okJob == 0 || badJob == 0 {
		t.Fatalf("jobs ok/bad not found:\n%s", get)
	}
	if j := mustOK(t, "run", map[string]any{"action": "job", "repo": repo, "job": badJob}); !strings.Contains(j, "bad") {
		t.Errorf("run job:\n%s", j)
	}
	if lg := mustOK(t, "run", map[string]any{"action": "log", "repo": repo, "job": badJob, "step": "failed"}); !strings.Contains(lg, "before-failure") {
		t.Errorf("failed step log:\n%s", lg)
	}
	if lg := mustOK(t, "run", map[string]any{"action": "log", "repo": repo, "job": okJob, "grep": "hello"}); !strings.Contains(lg, "hello") {
		t.Errorf("grep log:\n%s", lg)
	}
	tail := mustOK(t, "run", map[string]any{"action": "log", "repo": repo, "job": badJob, "tail": 2})
	if strings.TrimSpace(tail) == "" {
		t.Errorf("tail log empty")
	}
	if nm := reE2ENext.FindStringSubmatch(tail); nm != nil {
		off, _ := strconv.Atoi(nm[1])
		follow := mustOK(t, "run", map[string]any{"action": "log", "repo": repo, "job": badJob, "offset": off})
		t.Logf("follow from %d: %d chars", off, len(follow))
	} else {
		t.Logf("tail output has no next_offset marker:\n%s", tail)
	}

	// Output size against the raw API JSON (numbers go into docs/VERIFIED.md).
	_, rawRuns := rawAPI(t, http.MethodGet, "/repos/"+repo+"/actions/runs", nil)
	_, rawRunJobs := rawAPI(t, http.MethodGet, "/repos/"+repo+"/actions/runs/"+strconv.Itoa(runID)+"/jobs", nil)
	_, rawLog := rawAPI(t, http.MethodGet, "/repos/"+repo+"/actions/jobs/"+strconv.Itoa(badJob)+"/logs", nil)
	t.Logf("SIZE ci_failure: tool %d B; raw runs+jobs+log %d B", len(fail), len(rawRuns)+len(rawRunJobs)+len(rawLog))
	t.Logf("SIZE run get: tool %d B; raw run jobs %d B", len(get), len(rawRunJobs))
	t.Logf("SIZE run log tail=2: tool %d B; raw job log %d B", len(tail), len(rawLog))

	// 6. rerun the failing job; it fails again but the machinery works.
	re := mustOK(t, "run", map[string]any{"action": "rerun_job", "repo": repo, "run": runID, "job": badJob})
	if !strings.HasPrefix(re, "ok:") {
		t.Errorf("rerun_job: %s", re)
	}
	w2 := mustOK(t, "ci_wait", map[string]any{"repo": repo, "run": runID, "timeout": 100})
	if !strings.Contains(w2, "verdict: ") || strings.Contains(w2, "verdict: timeout") {
		t.Errorf("ci_wait after rerun:\n%s", w2)
	}

	// run delete on the finished push run.
	if d := mustOK(t, "run", map[string]any{"action": "delete", "repo": repo, "run": runID}); d != "ok: deleted run "+strconv.Itoa(runID) {
		t.Errorf("delete: %s", d)
	}

	// cancel a fresh dispatch (race-tolerant: it may already be done).
	d1 := mustOK(t, "run", map[string]any{"action": "dispatch", "repo": repo, "workflow": "manual.yml"})
	c1 := e2eRunID(t, d1)
	if c := mustOK(t, "run", map[string]any{"action": "cancel", "repo": repo, "run": c1}); !strings.HasPrefix(c, "ok:") {
		t.Errorf("cancel: %s", c)
	}

	// dispatch with inputs and wait.
	d2 := mustOK(t, "run", map[string]any{
		"action": "dispatch", "repo": repo, "workflow": "manual.yml",
		"inputs": map[string]any{"env": "prod", "debug": true}, "wait": true, "timeout": 100,
	})
	if !strings.Contains(d2, "verdict: success") {
		t.Fatalf("dispatch wait:\n%s", d2)
	}
	r2 := e2eRunID(t, d2)
	g2 := mustOK(t, "run", map[string]any{"action": "get", "repo": repo, "run": r2})
	dj := e2eJobs(g2)["deploy"]
	if dj == 0 {
		t.Fatalf("deploy job missing:\n%s", g2)
	}
	if lg := mustOK(t, "run", map[string]any{"action": "log", "repo": repo, "job": dj}); !strings.Contains(lg, "deploying-to-prod") {
		t.Errorf("dispatch log lacks input:\n%s", lg)
	}

	// 7. workflows, artifacts, checks, PR.
	if wf := mustOK(t, "run", map[string]any{"action": "workflows", "repo": repo}); !strings.Contains(wf, "ci.yml") || !strings.Contains(wf, "manual.yml") || !strings.Contains(wf, "env") {
		t.Errorf("workflows:\n%s", wf)
	}
	mustOK(t, "run", map[string]any{"action": "artifacts", "repo": repo, "run": r2})
	if ch := mustOK(t, "checks", map[string]any{"action": "get", "repo": repo, "ref": "main"}); !strings.Contains(ch, "failure") && !strings.Contains(ch, "success") {
		t.Errorf("checks:\n%s", ch)
	}
	e2eFileCommit(t, repo, "main", "feature", "feature", map[string]string{"feature.txt": "x\n"})
	pr := mustOK(t, "pr", map[string]any{"action": "create", "repo": repo, "head": "feature", "base": "main", "title": "feature"})
	nm := regexp.MustCompile(`#(\d+)`).FindStringSubmatch(pr)
	if nm == nil {
		t.Fatalf("pr create:\n%s", pr)
	}
	n, _ := strconv.Atoi(nm[1])
	prList := mustOK(t, "pr", map[string]any{"action": "list", "repo": repo, "state": "all"})
	_, rawPRs := rawAPI(t, http.MethodGet, "/repos/"+repo+"/pulls?state=all", nil)
	t.Logf("SIZE pr list: tool %d B; raw %d B", len(prList), len(rawPRs))
	prGet := mustOK(t, "pr", map[string]any{"action": "get", "repo": repo, "n": n})
	_, rawPR := rawAPI(t, http.MethodGet, "/repos/"+repo+"/pulls/"+strconv.Itoa(n), nil)
	t.Logf("SIZE pr get: tool %d B; raw %d B", len(prGet), len(rawPR))
	pw := mustOK(t, "ci_wait", map[string]any{"repo": repo, "pr": n, "timeout": 100})
	if !strings.Contains(pw, "verdict: ") || strings.Contains(pw, "verdict: timeout") {
		t.Errorf("ci_wait pr:\n%s", pw)
	}
	mg := mustOK(t, "pr", map[string]any{"action": "merge", "repo": repo, "n": n, "when_green": true})
	if !strings.Contains(mg, "merged") && !strings.Contains(mg, "scheduled") {
		t.Errorf("merge when_green:\n%s", mg)
	}
	if strings.Contains(mg, "scheduled") {
		mustOK(t, "pr", map[string]any{"action": "automerge_cancel", "repo": repo, "n": n})
	}

	// 8. teardown assertions.
	if d := mustOK(t, "runner", map[string]any{"action": "delete", "repo": repo, "id": rid}); !strings.Contains(d, "deleted runner") {
		t.Errorf("runner delete: %s", d)
	}
	if l := mustOK(t, "runner", map[string]any{"action": "list", "repo": repo, "limit": 50}); strings.Contains(l, rname) {
		t.Errorf("runner still listed:\n%s", l)
	}
	docker(t, "rm", "-f", cname)
	if out := docker(t, "ps", "-aq", "--filter", "name="+cname); out != "" {
		t.Errorf("container %s still present", cname)
	}
}
