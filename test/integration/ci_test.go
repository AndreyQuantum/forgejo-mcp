package integration

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// ciMkRepo creates a private repo with one commit on main and removes it when
// the test ends. It returns "owner/name".
func ciMkRepo(t *testing.T) string {
	t.Helper()
	skipUnlessIntegration(t)
	name := uniqueName("itci")
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
	return r.FullName
}

// ciHead returns the head commit id of a branch.
func ciHead(t *testing.T, repo, branch string) string {
	t.Helper()
	st, body := rawAPI(t, http.MethodGet, "/repos/"+repo+"/branches/"+branch, nil)
	if st != http.StatusOK {
		t.Fatalf("branch %s: %d %s", branch, st, body)
	}
	var b struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(body, &b); err != nil || b.Commit.ID == "" {
		t.Fatalf("branch body: %s", body)
	}
	return b.Commit.ID
}

// ciStatus posts a commit status straight to the API.
func ciStatus(t *testing.T, repo, sha, state, context string) {
	t.Helper()
	st, body := rawAPI(t, http.MethodPost, "/repos/"+repo+"/statuses/"+sha, map[string]any{"state": state, "context": context, "description": "d-" + state})
	if st != http.StatusCreated {
		t.Fatalf("status: %d %s", st, body)
	}
}

// ciPutFile creates a file on a branch and returns the new head sha.
func ciPutFile(t *testing.T, repo, branch, path, content string) string {
	t.Helper()
	st, body := rawAPI(t, http.MethodPost, "/repos/"+repo+"/contents/"+path, map[string]any{
		"content": base64.StdEncoding.EncodeToString([]byte(content)),
		"message": "add " + path, "branch": branch,
	})
	if st != http.StatusCreated {
		t.Fatalf("put %s: %d %s", path, st, body)
	}
	return ciHead(t, repo, branch)
}

// ciMkPR creates branch "feature" with one extra commit and an open PR from
// it into main. It returns the PR number and the head sha.
func ciMkPR(t *testing.T, repo string) (int, string) {
	t.Helper()
	st, body := rawAPI(t, http.MethodPost, "/repos/"+repo+"/branches", map[string]any{"new_branch_name": "feature", "old_branch_name": "main"})
	if st != http.StatusCreated {
		t.Fatalf("branch: %d %s", st, body)
	}
	head := ciPutFile(t, repo, "feature", "feature.txt", "x\n")
	st, body = rawAPI(t, http.MethodPost, "/repos/"+repo+"/pulls", map[string]any{"title": "feature", "head": "feature", "base": "main"})
	if st != http.StatusCreated {
		t.Fatalf("pr: %d %s", st, body)
	}
	var pr struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(body, &pr); err != nil || pr.Number == 0 {
		t.Fatalf("pr body: %s", body)
	}
	return pr.Number, head
}

func ciSplit(repo string) (string, string) {
	o, n, _ := strings.Cut(repo, "/")
	return o, n
}

func TestCIActionsConfigRepo(t *testing.T) {
	repo := ciMkRepo(t)

	out := mustOK(t, "actions_config", map[string]any{"action": "set", "repo": repo, "kind": "secret", "name": "deploy_key", "value": "hunter2-secret"})
	if !strings.HasPrefix(out, "ok: secret DEPLOY_KEY set (created") || strings.Contains(out, "hunter2") {
		t.Fatalf("set secret: %q", out)
	}
	out = mustOK(t, "actions_config", map[string]any{"action": "set", "repo": repo, "kind": "secret", "name": "DEPLOY_KEY", "value": "other"})
	if !strings.Contains(out, "updated") {
		t.Fatalf("update secret: %q", out)
	}
	golden(t, "ci_config_secrets", mustOK(t, "actions_config", map[string]any{"action": "list", "repo": repo, "kind": "secret"}))

	out = mustOK(t, "actions_config", map[string]any{"action": "set", "repo": repo, "kind": "variable", "name": "region", "value": "eu-1"})
	if !strings.HasPrefix(out, "ok: variable REGION=eu-1 created") {
		t.Fatalf("create variable: %q", out)
	}
	out = mustOK(t, "actions_config", map[string]any{"action": "set", "repo": repo, "kind": "variable", "name": "region", "value": "eu-2"})
	if !strings.HasPrefix(out, "ok: variable REGION=eu-2 updated") {
		t.Fatalf("update variable: %q", out)
	}
	if got := mustOK(t, "actions_config", map[string]any{"action": "get", "repo": repo, "kind": "variable", "name": "REGION"}); got != "REGION=eu-2" {
		t.Fatalf("get variable: %q", got)
	}
	golden(t, "ci_config_variables", mustOK(t, "actions_config", map[string]any{"action": "list", "repo": repo, "kind": "variable"}))

	if got := mustErr(t, "actions_config", map[string]any{"action": "get", "repo": repo, "kind": "secret", "name": "DEPLOY_KEY"}); !strings.Contains(got, "write-only") {
		t.Fatalf("secret get: %q", got)
	}

	mustOK(t, "actions_config", map[string]any{"action": "delete", "repo": repo, "kind": "secret", "name": "DEPLOY_KEY"})
	mustOK(t, "actions_config", map[string]any{"action": "delete", "repo": repo, "kind": "variable", "name": "region"})
	if got := mustOK(t, "actions_config", map[string]any{"action": "list", "repo": repo, "kind": "variable"}); !strings.HasPrefix(got, "# variables 0") {
		t.Fatalf("after delete: %q", got)
	}
	if got := mustErr(t, "actions_config", map[string]any{"action": "delete", "repo": repo, "kind": "secret", "name": "NOPE"}); !strings.HasPrefix(got, "error: 404") {
		t.Fatalf("delete missing: %q", got)
	}
}

func TestCIActionsConfigOrgAndUser(t *testing.T) {
	org := settingsMkOrg(t)
	mustOK(t, "actions_config", map[string]any{"action": "set", "org": org, "kind": "variable", "name": "ORG_VAR", "value": "v1"})
	if got := mustOK(t, "actions_config", map[string]any{"action": "list", "org": org, "kind": "variable"}); !strings.Contains(got, "ORG_VAR=v1") {
		t.Fatalf("org list: %q", got)
	}
	mustOK(t, "actions_config", map[string]any{"action": "set", "scope": "org", "org": org, "kind": "secret", "name": "ORG_SECRET", "value": "s"})
	if got := mustOK(t, "actions_config", map[string]any{"action": "list", "org": org, "kind": "secret"}); !strings.Contains(got, "ORG_SECRET") {
		t.Fatalf("org secrets: %q", got)
	}
	mustOK(t, "actions_config", map[string]any{"action": "delete", "org": org, "kind": "variable", "name": "ORG_VAR"})

	name := strings.ToUpper(strings.ReplaceAll(uniqueName("ituser"), "-", "_"))
	mustOK(t, "actions_config", map[string]any{"action": "set", "scope": "user", "kind": "variable", "name": name, "value": "uv"})
	t.Cleanup(func() {
		call(t, "actions_config", map[string]any{"action": "delete", "scope": "user", "kind": "variable", "name": name})
	})
	if got := mustOK(t, "actions_config", map[string]any{"action": "list", "scope": "user", "kind": "variable"}); !strings.Contains(got, name+"=uv") {
		t.Fatalf("user list: %q", got)
	}
	mustOK(t, "actions_config", map[string]any{"action": "delete", "scope": "user", "kind": "variable", "name": name})
}

func TestCIChecks(t *testing.T) {
	repo := ciMkRepo(t)
	sha := ciHead(t, repo, "main")

	if got := mustOK(t, "checks", map[string]any{"action": "get", "repo": repo, "ref": "main"}); !strings.HasPrefix(got, "state: none sha ") {
		t.Fatalf("no statuses: %q", got)
	}

	out := mustOK(t, "checks", map[string]any{"action": "set", "repo": repo, "ref": "main", "state": "pending", "context": "ci/external", "description": "queued"})
	if !strings.HasPrefix(out, "ok: status ci/external=pending on "+sha[:8]) {
		t.Fatalf("set: %q", out)
	}
	mustOK(t, "checks", map[string]any{"action": "set", "repo": repo, "sha": sha[:10], "state": "failure", "context": "ci/lint", "description": "2 issues", "target_url": "https://ci.example.com/lint/1"})
	mustOK(t, "checks", map[string]any{"action": "set", "repo": repo, "sha": sha, "state": "success", "context": "ci/unit"})

	got := mustOK(t, "checks", map[string]any{"action": "get", "repo": repo, "sha": sha})
	golden(t, "ci_checks_get", maskSHA(got, sha))
	if !strings.Contains(got, "state: failure (1/3 ok)") {
		t.Fatalf("get: %q", got)
	}
	// the latest status per context wins
	mustOK(t, "checks", map[string]any{"action": "set", "repo": repo, "ref": "main", "state": "success", "context": "ci/lint"})
	mustOK(t, "checks", map[string]any{"action": "set", "repo": repo, "ref": "main", "state": "success", "context": "ci/external"})
	if got := mustOK(t, "checks", map[string]any{"action": "get", "repo": repo, "ref": "main"}); got != "state: success (3/3 ok) sha "+sha[:8] {
		t.Fatalf("all green: %q", got)
	}
	list := mustOK(t, "checks", map[string]any{"action": "list", "repo": repo, "ref": "main"})
	golden(t, "ci_checks_list", list)
	if got := mustOK(t, "checks", map[string]any{"action": "list", "repo": repo, "ref": "main", "state": "failure"}); !strings.Contains(got, "failure ci/lint | 2 issues") {
		t.Fatalf("filtered list: %q", got)
	}
	if got := mustErr(t, "checks", map[string]any{"action": "set", "repo": repo, "ref": "main", "state": "bogus", "context": "x"}); !strings.HasPrefix(got, "error: invalid:") {
		t.Fatalf("bad state: %q", got)
	}
	if got := mustErr(t, "checks", map[string]any{"action": "get", "repo": repo, "ref": "no-such-branch"}); !strings.HasPrefix(got, "error: 404") {
		t.Fatalf("missing ref: %q", got)
	}
}

func TestCIFailureExternalStatuses(t *testing.T) {
	repo := ciMkRepo(t)
	sha := ciHead(t, repo, "main")
	ciStatus(t, repo, sha, "failure", "ext/build")
	ciStatus(t, repo, sha, "pending", "ext/deploy")

	got := mustOK(t, "ci_failure", map[string]any{"repo": repo, "ref": "main"})
	for _, want := range []string{"no Actions run for ref main (" + sha[:8] + ")", "external checks failing:", "failure ext/build | d-failure"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if got := mustOK(t, "ci_failure", map[string]any{"repo": repo, "sha": sha[:9]}); !strings.Contains(got, "ext/build") {
		t.Fatalf("short sha: %q", got)
	}
	if got := mustErr(t, "ci_failure", map[string]any{"repo": repo, "run": 1, "pr": 2}); !strings.HasPrefix(got, "error: invalid:") {
		t.Fatalf("two selectors: %q", got)
	}
	if got := mustErr(t, "ci_failure", map[string]any{"repo": repo, "run": 999999}); !strings.HasPrefix(got, "error: 404") {
		t.Fatalf("missing run: %q", got)
	}
}

func TestCIWaitRefVerdicts(t *testing.T) {
	repo := ciMkRepo(t)
	sha := ciHead(t, repo, "main")

	// failed: fail-fast returns at once, no Actions run -> external digest
	ciStatus(t, repo, sha, "failure", "ext/build")
	start := time.Now()
	got := mustOK(t, "ci_wait", map[string]any{"repo": repo, "ref": "main", "timeout": 20})
	if !strings.HasPrefix(got, "verdict: failed | waited ") || !strings.Contains(got, "ext/build") {
		t.Fatalf("failed:\n%s", got)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatalf("fail-fast took %s", time.Since(start))
	}

	// pending -> timeout with resume line echoing the selector
	repo2 := ciMkRepo(t)
	sha2 := ciHead(t, repo2, "main")
	ciStatus(t, repo2, sha2, "pending", "ext/slow")
	got = mustOK(t, "ci_wait", map[string]any{"repo": repo2, "ref": "main", "timeout": 4})
	if !strings.HasPrefix(got, "verdict: timeout | waited ") || !strings.Contains(got, "resume: ci_wait ref=main") {
		t.Fatalf("timeout:\n%s", got)
	}

	// pending then success while waiting -> green
	done := make(chan struct{})
	posted := make(chan int, 1)
	go func() {
		defer close(done)
		time.Sleep(3 * time.Second)
		st, _ := rawAPI(t, http.MethodPost, "/repos/"+repo2+"/statuses/"+sha2, map[string]any{"state": "success", "context": "ext/slow", "description": "d-success"})
		posted <- st
	}()
	got = mustOK(t, "ci_wait", map[string]any{"repo": repo2, "sha": sha2, "timeout": 30})
	<-done
	if st := <-posted; st != http.StatusCreated {
		t.Fatalf("status POST from goroutine: %d", st)
	}
	if !strings.HasPrefix(got, "verdict: green | waited ") || !strings.Contains(got, "ci: success 1/1") {
		t.Fatalf("green:\n%s", got)
	}
}

func TestCIWaitNoChecks(t *testing.T) {
	repo := ciMkRepo(t)
	got := mustOK(t, "ci_wait", map[string]any{"repo": repo, "ref": "main", "timeout": 40})
	if !strings.HasPrefix(got, "verdict: no_checks | waited ") {
		t.Fatalf("no_checks:\n%s", got)
	}
}

func TestCIWaitPR(t *testing.T) {
	repo := ciMkRepo(t)
	n, head := ciMkPR(t, repo)
	ciStatus(t, repo, head, "success", "ext/build")

	got := mustOK(t, "ci_wait", map[string]any{"repo": repo, "pr": n, "timeout": 20})
	golden(t, "ci_wait_pr_green", maskSHA(maskWaited(got), head))
	if !strings.HasPrefix(got, "verdict: green | waited ") || !strings.Contains(got, "pr #") || !strings.Contains(got, "mergeable: yes") {
		t.Fatalf("pr green:\n%s", got)
	}

	ciStatus(t, repo, head, "failure", "ext/lint")
	got = mustOK(t, "ci_wait", map[string]any{"repo": repo, "pr": n, "timeout": 20})
	if !strings.HasPrefix(got, "verdict: failed | waited ") || !strings.Contains(got, "ext/lint") {
		t.Fatalf("pr failed:\n%s", got)
	}
	if got := mustOK(t, "ci_failure", map[string]any{"repo": repo, "pr": n}); !strings.Contains(got, "ext/lint") {
		t.Fatalf("ci_failure pr:\n%s", got)
	}

	// close the PR: immediate `closed` verdict even though CI is red
	owner, name := ciSplit(repo)
	if st, body := rawAPI(t, http.MethodPatch, "/repos/"+owner+"/"+name+"/pulls/"+itoaCI(n), map[string]any{"state": "closed"}); st != http.StatusCreated {
		t.Fatalf("close pr: %d %s", st, body)
	}
	got = mustOK(t, "ci_wait", map[string]any{"repo": repo, "pr": n, "timeout": 10})
	if !strings.HasPrefix(got, "verdict: closed | waited ") {
		t.Fatalf("pr closed:\n%s", got)
	}
}

const ciWorkflow = `name: CI
on: [push]
jobs:
  build:
    runs-on: docker
    steps:
      - run: echo hello
`

// TestCIWaitQueuedRun uses a real queued run (no runner is registered, so it
// stays waiting): the wait times out and explains the stuck queue.
func TestCIWaitQueuedRun(t *testing.T) {
	repo := ciMkRepo(t)
	sha := ciPutFile(t, repo, "main", ".forgejo/workflows/ci.yml", ciWorkflow)

	var runID int
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && runID == 0 {
		st, body := rawAPI(t, http.MethodGet, "/repos/"+repo+"/actions/runs?head_sha="+sha, nil)
		if st == http.StatusOK {
			var r struct {
				Runs []struct {
					ID int `json:"id"`
				} `json:"workflow_runs"`
			}
			if json.Unmarshal(body, &r) == nil && len(r.Runs) > 0 {
				runID = r.Runs[0].ID
			}
		}
		if runID == 0 {
			time.Sleep(time.Second)
		}
	}
	if runID == 0 {
		t.Skip("the instance created no Actions run for the pushed workflow (Actions disabled?)")
	}

	got := mustOK(t, "ci_wait", map[string]any{"repo": repo, "run": runID, "timeout": 5})
	if !strings.HasPrefix(got, "verdict: timeout | waited ") || !strings.Contains(got, "resume: ci_wait run="+itoaCI(runID)) {
		t.Fatalf("run timeout:\n%s", got)
	}
	t.Logf("queued run wait:\n%s", got)

	got = mustOK(t, "ci_wait", map[string]any{"repo": repo, "sha": sha, "timeout": 5})
	if !strings.HasPrefix(got, "verdict: timeout | waited ") || !strings.Contains(got, "resume: ci_wait sha=") {
		t.Fatalf("sha timeout:\n%s", got)
	}

	// nothing has failed: ci_failure reports the run as pending
	got = mustOK(t, "ci_failure", map[string]any{"repo": repo, "sha": sha})
	if !strings.Contains(got, "pending: ci.yml") && !strings.Contains(got, "no failed run") {
		t.Fatalf("ci_failure on a queued run:\n%s", got)
	}
}

func itoaCI(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// TestCIReadOnly: checks set and actions_config set/delete are hidden in
// read-only mode while the read actions keep working.
func TestCIReadOnly(t *testing.T) {
	skipUnlessIntegration(t)
	repo := ciMkRepo(t)
	s := newSession(t, map[string]string{"FORGEJO_READ_ONLY": "1"})
	if got, isErr := s.call(t, "checks", map[string]any{"action": "set", "repo": repo, "ref": "main", "state": "success", "context": "x"}); !isErr || !strings.Contains(got, "read-only") {
		t.Fatalf("set: err=%v %q", isErr, got)
	}
	if got, isErr := s.call(t, "actions_config", map[string]any{"action": "set", "repo": repo, "kind": "secret", "name": "A", "value": "v"}); !isErr || !strings.Contains(got, "read-only") {
		t.Fatalf("config set: err=%v %q", isErr, got)
	}
	if got, isErr := s.call(t, "checks", map[string]any{"action": "get", "repo": repo, "ref": "main"}); isErr {
		t.Fatalf("get: %q", got)
	}
}

// maskSHA replaces the 8-char abbreviation of sha, which stabilize cannot
// mask when it happens to consist of digits only.
func maskSHA(got, sha string) string { return strings.ReplaceAll(got, sha[:8], "<sha>") }

var waitedRe = regexp.MustCompile(`waited \d+s`)

// maskWaited hides the wall-clock duration of a real-time wait.
func maskWaited(got string) string { return waitedRe.ReplaceAllString(got, "waited <dur>") }
