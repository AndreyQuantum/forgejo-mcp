package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// ---- helpers (prefix pulls*: this file owns them) ---------------------------

type pullsRepo struct {
	owner, name string
}

func (r pullsRepo) full() string { return r.owner + "/" + r.name }

func (r pullsRepo) args(extra map[string]any) map[string]any {
	m := map[string]any{"repo": r.full()}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// pullsNewRepo creates an auto-initialised public repo with default branch
// main and deletes it with the test.
func pullsNewRepo(t *testing.T) pullsRepo {
	t.Helper()
	skipUnlessIntegration(t)
	st, b := rawAPI(t, http.MethodGet, "/user", nil)
	if st != http.StatusOK {
		t.Fatalf("GET /user -> %d", st)
	}
	var me struct {
		Login string `json:"login"`
	}
	_ = json.Unmarshal(b, &me)
	name := uniqueName("it")
	st, b = rawAPI(t, http.MethodPost, "/user/repos", map[string]any{
		"name": name, "auto_init": true, "default_branch": "main", "readme": "Default", "private": false,
	})
	if st != http.StatusCreated {
		t.Fatalf("create repo -> %d: %s", st, b)
	}
	r := pullsRepo{owner: me.Login, name: name}
	t.Cleanup(func() { rawAPI(t, http.MethodDelete, "/repos/"+r.owner+"/"+r.name, nil) })
	return r
}

// put creates or updates file path with content on branch; with newBranch it
// branches off branch first.
func (r pullsRepo) put(t *testing.T, branch, newBranch, path, content string) {
	t.Helper()
	api := "/repos/" + r.owner + "/" + r.name + "/contents/" + path
	body := map[string]any{
		"content": base64.StdEncoding.EncodeToString([]byte(content)),
		"message": "edit " + path,
		"branch":  branch,
	}
	if newBranch != "" {
		body["new_branch"] = newBranch
	}
	method := http.MethodPost
	if st, b := rawAPI(t, http.MethodGet, api+"?ref="+url.QueryEscape(branch), nil); st == http.StatusOK {
		var cur struct {
			SHA string `json:"sha"`
		}
		_ = json.Unmarshal(b, &cur)
		body["sha"] = cur.SHA
		method = http.MethodPut
	}
	if st, b := rawAPI(t, method, api, body); st != http.StatusCreated && st != http.StatusOK {
		t.Fatalf("%s %s on %s -> %d: %s", method, path, branch, st, b)
	}
}

// pullsOpen creates branch feat from main with one new file and opens a PR
// through the tool; it returns the PR number.
func (r pullsRepo) open(t *testing.T, feat string, extra map[string]any) int {
	t.Helper()
	r.put(t, "main", feat, feat+".txt", "hello from "+feat+"\n")
	args := r.args(map[string]any{"action": "create", "head": feat, "title": "PR " + feat})
	for k, v := range extra {
		args[k] = v
	}
	out := mustOK(t, "pr", args)
	var n int
	if _, err := fmt.Sscanf(out, "ok: created pr #%d", &n); err != nil {
		t.Fatalf("cannot read the PR number from %q", out)
	}
	return n
}

func (r pullsRepo) meta(t *testing.T) map[string]any {
	t.Helper()
	st, b := rawAPI(t, http.MethodGet, "/repos/"+r.full(), nil)
	if st != http.StatusOK {
		t.Fatalf("GET repo -> %d", st)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// pullsReviewer creates a second user with write access to r and returns a
// session authenticated as that user plus its login.
func pullsReviewer(t *testing.T, r pullsRepo) (*session, string) {
	t.Helper()
	requireAdmin(t)
	login := uniqueName("itrev")
	pw := "Pw-" + uniqueName("x") + uniqueName("y")
	if st, b := rawAPI(t, http.MethodPost, "/admin/users", map[string]any{
		"username": login, "email": login + "@example.invalid", "password": pw, "must_change_password": false,
	}); st != http.StatusCreated {
		t.Fatalf("create reviewer -> %d: %s", st, b)
	}
	t.Cleanup(func() { rawAPI(t, http.MethodDelete, "/admin/users/"+login+"?purge=true", nil) })
	if st, b := rawAPI(t, http.MethodPut, "/repos/"+r.full()+"/collaborators/"+login, map[string]any{"permission": "write"}); st != http.StatusNoContent {
		t.Fatalf("add collaborator -> %d: %s", st, b)
	}
	body, _ := json.Marshal(map[string]any{"name": "it", "scopes": []string{"all"}})
	req, err := http.NewRequest(http.MethodPost, instURL+"/api/v1/users/"+login+"/tokens", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(login, pw)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("create reviewer token: transport error")
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	var tok struct {
		SHA1 string `json:"sha1"`
	}
	_ = json.Unmarshal(rb, &tok)
	if resp.StatusCode != http.StatusCreated || tok.SHA1 == "" {
		t.Skipf("cannot create a token for the reviewer (status %d); basic auth may be disabled", resp.StatusCode)
	}
	return newSession(t, map[string]string{"FORGEJO_TOKEN": tok.SHA1}), login
}

func pullsHas(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("output lacks %q:\n%s", w, got)
		}
	}
}

func pullsHasNot(t *testing.T, got string, bad ...string) {
	t.Helper()
	for _, w := range bad {
		if strings.Contains(got, w) {
			t.Errorf("output contains %q:\n%s", w, got)
		}
	}
}

// ---- tests ---------------------------------------------------------------------

func TestPullsLifecycle(t *testing.T) {
	r := pullsNewRepo(t)
	if st, b := rawAPI(t, http.MethodPost, "/repos/"+r.full()+"/labels", map[string]any{"name": "bug", "color": "#d73a4a"}); st != http.StatusCreated {
		t.Fatalf("create label -> %d: %s", st, b)
	}
	r.put(t, "main", "feat-a", "a.txt", "line one\nline two\n")

	// unknown label is rejected locally, nothing is created
	out := mustErr(t, "pr", r.args(map[string]any{"action": "create", "head": "feat-a", "title": "x", "labels": []string{"nope"}}))
	pullsHas(t, out, `unknown label "nope"; known: bug`)

	out = mustOK(t, "pr", r.args(map[string]any{
		"action": "create", "head": "feat-a", "title": "Add a", "body": "Adds a.txt", "labels": []string{"bug"}, "draft": true,
	}))
	var n int
	if _, err := fmt.Sscanf(out, "ok: created pr #%d feat-a->main", &n); err != nil {
		t.Fatalf("create output %q", out)
	}
	pullsHas(t, out, "/pulls/")

	// a second create for the same head/base reports the existing PR
	out = mustErr(t, "pr", r.args(map[string]any{"action": "create", "head": "feat-a", "title": "again"}))
	pullsHas(t, out, fmt.Sprintf("PR already exists: #%d", n))

	out = mustOK(t, "pr", r.args(map[string]any{"action": "get", "n": n}))
	pullsHas(t, out, fmt.Sprintf("pr #%d draft | WIP: Add a", n), "feat-a->main", "[bug]", "ci: none", "reviews: none", "blocked_by: draft", "Adds a.txt")
	pullsHasNot(t, out, "ready: yes")

	out = mustOK(t, "pr", r.args(map[string]any{"action": "list"}))
	pullsHas(t, out, "# prs 1/1 page 1/1", fmt.Sprintf("#%d draft | WIP: Add a", n))
	out = mustOK(t, "pr", r.args(map[string]any{"action": "list", "labels": []string{"bug"}, "state": "merged"}))
	pullsHas(t, out, "# prs 0")

	out = mustOK(t, "pr", r.args(map[string]any{"action": "get", "head": "feat-a", "include": []string{"files", "commits"}}))
	pullsHas(t, out, fmt.Sprintf("pr #%d", n), "files: 1", "A a.txt +2 -0", "commits: 1")

	out = mustOK(t, "pr", r.args(map[string]any{"action": "edit", "n": n, "draft": false, "title": "Add a (ready)", "body": "new body"}))
	pullsHas(t, out, fmt.Sprintf("ok: edited pr #%d: title,draft,body", n))
	out = mustOK(t, "pr", r.args(map[string]any{"action": "get", "n": n, "include": []string{"body"}}))
	pullsHas(t, out, fmt.Sprintf("pr #%d open | Add a (ready)", n), "new body")

	out = mustOK(t, "pr", r.args(map[string]any{"action": "edit", "n": n, "labels": []string{}}))
	pullsHas(t, out, "ok: edited pr")
	out = mustOK(t, "pr", r.args(map[string]any{"action": "get", "n": n, "include": []string{"body"}}))
	pullsHasNot(t, out, "[bug]")

	out = mustOK(t, "pr", r.args(map[string]any{"action": "files", "n": n}))
	pullsHas(t, out, "# files 1/1 page 1/1 | +2 -0 in 1 files", "A a.txt +2 -0")
	out = mustOK(t, "pr", r.args(map[string]any{"action": "commits", "n": n}))
	pullsHas(t, out, "# commits 1/1", "| edit a.txt")
	out = mustOK(t, "pr", r.args(map[string]any{"action": "diff", "n": n}))
	pullsHas(t, out, "diff pr #", "=== A a.txt (+2 -0) ===", "+line one")
	out = mustOK(t, "pr", r.args(map[string]any{"action": "diff", "n": n, "paths": []string{"zzz*"}}))
	pullsHas(t, out, "no file matches paths", "files: a.txt")

	// pr get without n or head on a repo that is not the git checkout falls back to the default branch
	out = mustErr(t, "pr", r.args(map[string]any{"action": "get"}))
	pullsHas(t, out, "no open pr for main; pass n or head")
}

func TestPullsDiffCaps(t *testing.T) {
	r := pullsNewRepo(t)
	big := strings.Repeat("some line of text\n", 200)
	r.put(t, "main", "feat-big", "f0.txt", big)
	for i := 1; i < 5; i++ {
		r.put(t, "feat-big", "", fmt.Sprintf("f%d.txt", i), big)
	}
	out := mustOK(t, "pr", r.args(map[string]any{"action": "create", "head": "feat-big", "title": "big"}))
	var n int
	_, _ = fmt.Sscanf(out, "ok: created pr #%d", &n)
	out = mustOK(t, "pr", r.args(map[string]any{"action": "diff", "n": n, "max_chars": 2000}))
	pullsHas(t, out, "diff pr #", "[truncated:", "pass paths=[...]")
	out = mustOK(t, "pr", r.args(map[string]any{"action": "diff", "n": n, "paths": []string{"f3.txt"}, "max_chars": 50000}))
	pullsHas(t, out, "=== A f3.txt (+200 -0) ===")
	pullsHasNot(t, out, "f1.txt")
	out = mustOK(t, "pr", r.args(map[string]any{"action": "files", "n": n, "limit": 2}))
	pullsHas(t, out, "# files 2/5 page 1/3 next: page=2", "on this page")
}

func TestPullsMergeEveryEnabledStyle(t *testing.T) {
	r := pullsNewRepo(t)
	meta := r.meta(t)
	styles := map[string]string{
		"merge": "allow_merge_commits", "rebase": "allow_rebase", "rebase-merge": "allow_rebase_explicit",
		"squash": "allow_squash_merge", "fast-forward-only": "allow_fast_forward_only_merge",
	}
	tested := 0
	for style, key := range styles {
		if on, _ := meta[key].(bool); !on {
			// a disabled style is refused locally with the allowed list
			n := r.open(t, "feat-off-"+style, nil)
			out := mustErr(t, "pr", r.args(map[string]any{"action": "merge", "n": n, "method": style}))
			pullsHas(t, out, fmt.Sprintf("style %s disabled in this repo; allowed: ", style))
			continue
		}
		// fast-forward-only and rebase need a branch based on the current main
		n := r.open(t, "feat-"+strings.ReplaceAll(style, "-", ""), nil)
		out := mustOK(t, "pr", r.args(map[string]any{"action": "merge", "n": n, "method": style}))
		pullsHas(t, out, fmt.Sprintf("ok: merged pr #%d", n), style)
		got := mustOK(t, "pr", r.args(map[string]any{"action": "get", "n": n}))
		pullsHas(t, got, fmt.Sprintf("pr #%d merged", n), "merged: by ")
		tested++
	}
	if tested == 0 {
		t.Fatal("no merge style is enabled on the test repo")
	}
}

func TestPullsMergeStateErrors(t *testing.T) {
	r := pullsNewRepo(t)

	// closed, unmerged
	n := r.open(t, "feat-closed", nil)
	mustOK(t, "pr", r.args(map[string]any{"action": "edit", "n": n, "state": "closed"}))
	out := mustErr(t, "pr", r.args(map[string]any{"action": "merge", "n": n}))
	pullsHas(t, out, fmt.Sprintf("pr #%d is closed, not merged; reopen with pr edit state=open", n))
	mustOK(t, "pr", r.args(map[string]any{"action": "edit", "n": n, "state": "open"}))

	// head_sha guard
	out = mustErr(t, "pr", r.args(map[string]any{"action": "merge", "n": n, "head_sha": "0000000000000000000000000000000000000000"}))
	pullsHas(t, out, "head moved (")

	// draft
	d := r.open(t, "feat-draft", map[string]any{"draft": true})
	out = mustErr(t, "pr", r.args(map[string]any{"action": "merge", "n": d}))
	pullsHas(t, out, fmt.Sprintf("pr #%d is a draft", d))

	// merge with delete_branch, then merge again
	out = mustOK(t, "pr", r.args(map[string]any{"action": "merge", "n": n, "method": "merge", "delete_branch": true}))
	pullsHas(t, out, fmt.Sprintf("ok: merged pr #%d as ", n), "branch deleted")
	if st, _ := rawAPI(t, http.MethodGet, "/repos/"+r.full()+"/branches/feat-closed", nil); st != http.StatusNotFound {
		t.Errorf("head branch should be deleted, GET -> %d", st)
	}
	out = mustErr(t, "pr", r.args(map[string]any{"action": "merge", "n": n}))
	pullsHas(t, out, fmt.Sprintf("pr #%d is already merged (merge_commit ", n))
}

func TestPullsMergeWhenGreenWithoutChecks(t *testing.T) {
	r := pullsNewRepo(t)
	n := r.open(t, "feat-green", nil)
	out := mustOK(t, "pr", r.args(map[string]any{"action": "merge", "n": n, "when_green": true}))
	switch {
	case strings.Contains(out, fmt.Sprintf("ok: merged pr #%d", n)):
	case strings.Contains(out, "ok: auto-merge scheduled"):
		t.Logf("server scheduled the merge instead of merging at once: %s", out)
		pullsHas(t, out, "note: no checks exist for this head")
		out = mustOK(t, "pr", r.args(map[string]any{"action": "automerge_cancel", "n": n}))
		pullsHas(t, out, "ok: auto-merge cancelled")
		// a second cancel reports that nothing is scheduled
		pullsHas(t, mustErr(t, "pr", r.args(map[string]any{"action": "automerge_cancel", "n": n})), "no auto-merge was scheduled")
	default:
		t.Fatalf("unexpected result: %s", out)
	}
}

func TestPullsAutomergeCancelNothingScheduled(t *testing.T) {
	r := pullsNewRepo(t)
	n := r.open(t, "feat-nocancel", nil)
	out := mustErr(t, "pr", r.args(map[string]any{"action": "automerge_cancel", "n": n}))
	pullsHas(t, out, "no auto-merge was scheduled")
}

func TestPullsUpdateBranch(t *testing.T) {
	r := pullsNewRepo(t)
	n := r.open(t, "feat-behind", nil)
	// main moves ahead, so the PR branch is behind
	r.put(t, "main", "", "other.txt", "other\n")
	out := mustOK(t, "pr", r.args(map[string]any{"action": "update", "n": n}))
	pullsHas(t, out, fmt.Sprintf("ok: branch updated (merge) for pr #%d", n))
	out = mustOK(t, "pr", r.args(map[string]any{"action": "update", "n": n}))
	pullsHas(t, out, fmt.Sprintf("ok: pr #%d already up to date", n))

	// a merged PR cannot be updated
	mustOK(t, "pr", r.args(map[string]any{"action": "merge", "n": n, "method": "merge"}))
	out = mustErr(t, "pr", r.args(map[string]any{"action": "update", "n": n}))
	pullsHas(t, out, "422 pr is merged or closed; nothing to update")
}

func TestPullsMergeConflict(t *testing.T) {
	r := pullsNewRepo(t)
	r.put(t, "main", "", "shared.txt", "base\n")
	r.put(t, "main", "feat-c1", "shared.txt", "change one\n")
	r.put(t, "main", "feat-c2", "shared.txt", "change two\n")
	var nums []int
	for _, b := range []string{"feat-c1", "feat-c2"} {
		out := mustOK(t, "pr", r.args(map[string]any{"action": "create", "head": b, "title": b}))
		var n int
		_, _ = fmt.Sscanf(out, "ok: created pr #%d", &n)
		nums = append(nums, n)
	}
	mustOK(t, "pr", r.args(map[string]any{"action": "merge", "n": nums[0], "method": "merge"}))
	out := mustErr(t, "pr", r.args(map[string]any{"action": "merge", "n": nums[1], "method": "merge"}))
	if !strings.Contains(out, "conflict") {
		t.Errorf("expected a conflict error, got: %s", out)
	}
	out = mustOK(t, "pr", r.args(map[string]any{"action": "list"}))
	pullsHas(t, out, fmt.Sprintf("#%d open | feat-c2", nums[1]))
}

func TestPullsReviews(t *testing.T) {
	r := pullsNewRepo(t)
	rev, reviewer := pullsReviewer(t, r)
	n := r.open(t, "feat-rev", nil)

	// the PR author cannot approve: refused locally
	out := mustErr(t, "review", r.args(map[string]any{"action": "create", "n": n, "event": "approve"}))
	pullsHas(t, out, fmt.Sprintf("you are the author of pr #%d", n))

	// request the reviewer, see them as pending
	out = mustOK(t, "review", r.args(map[string]any{"action": "request", "n": n, "reviewers": []string{reviewer}}))
	pullsHas(t, out, "ok: requested "+reviewer)
	out = mustOK(t, "pr", r.args(map[string]any{"action": "get", "n": n}))
	pullsHas(t, out, "pending: "+reviewer)
	out = mustOK(t, "review", r.args(map[string]any{"action": "list", "n": n}))
	pullsHas(t, out, "# reviews 0/0", "requested: "+reviewer)

	// the reviewer requests changes with an inline comment
	out, isErr := rev.call(t, "review", r.args(map[string]any{
		"action": "create", "n": n, "event": "request_changes", "body": "please fix",
		"comments": []map[string]any{{"path": "feat-rev.txt", "line": 1, "body": "rename this"}},
	}))
	if isErr {
		t.Fatalf("request_changes: %s", out)
	}
	var rid int
	if _, err := fmt.Sscanf(out, "ok: review r%d REQUEST_CHANGES", &rid); err != nil {
		t.Fatalf("review output %q", out)
	}
	out = mustOK(t, "review", r.args(map[string]any{"action": "list", "n": n, "verbose": true}))
	pullsHas(t, out, fmt.Sprintf("r%d %s request_changes", rid, reviewer), "please fix", "feat-rev.txt:1", "rename this")
	out = mustOK(t, "review", r.args(map[string]any{"action": "list", "n": n, "with_comments": false}))
	pullsHasNot(t, out, "rename this")
	out = mustOK(t, "pr", r.args(map[string]any{"action": "get", "n": n}))
	pullsHas(t, out, "reviews: "+reviewer+" request_changes")

	// a pending review of the reviewer is hidden from the author until submitted
	out, isErr = rev.call(t, "review", r.args(map[string]any{"action": "create", "n": n, "event": "pending", "body": "draft"}))
	if isErr || !strings.Contains(out, "PENDING") {
		t.Fatalf("pending review: %s", out)
	}
	var pid int
	_, _ = fmt.Sscanf(out, "ok: review r%d PENDING", &pid)
	if out = mustOK(t, "review", r.args(map[string]any{"action": "list", "n": n})); strings.Contains(out, "draft") && !strings.Contains(out, "pending") {
		t.Errorf("unexpected: %s", out)
	}
	out, isErr = rev.call(t, "review", r.args(map[string]any{"action": "submit", "n": n, "review_id": pid, "event": "approve", "body": "now fine"}))
	if isErr {
		t.Fatalf("submit: %s", out)
	}
	pullsHas(t, out, fmt.Sprintf("ok: review r%d APPROVED", pid))
	out = mustOK(t, "pr", r.args(map[string]any{"action": "get", "n": n}))
	pullsHas(t, out, "reviews: "+reviewer+" approve")

	// admin dismisses and deletes
	out = mustOK(t, "review", r.args(map[string]any{"action": "dismiss", "n": n, "review_id": pid, "message": "outdated"}))
	pullsHas(t, out, fmt.Sprintf("ok: dismissed review r%d", pid))
	out = mustOK(t, "review", r.args(map[string]any{"action": "list", "n": n}))
	pullsHas(t, out, fmt.Sprintf("r%d %s approve", pid, reviewer), "dismissed")
	out = mustOK(t, "review", r.args(map[string]any{"action": "undismiss", "n": n, "review_id": pid}))
	pullsHas(t, out, fmt.Sprintf("ok: restored review r%d", pid))

	out, isErr = rev.call(t, "review", r.args(map[string]any{"action": "delete", "n": n, "review_id": rid}))
	if isErr {
		t.Fatalf("delete: %s", out)
	}
	pullsHas(t, out, fmt.Sprintf("ok: deleted review r%d", rid))

	// unrequest on a PR with no pending request is not an error path we rely on; request + unrequest round trip
	out = mustOK(t, "review", r.args(map[string]any{"action": "unrequest", "n": n, "reviewers": []string{reviewer}}))
	pullsHas(t, out, "ok: unrequested "+reviewer)
}

func TestPullsReadOnlyHidesWrites(t *testing.T) {
	skipUnlessIntegration(t)
	s := newSession(t, map[string]string{"FORGEJO_READ_ONLY": "1"})
	out, isErr := s.call(t, "pr", map[string]any{"action": "merge", "n": 1, "repo": "o/r"})
	if !isErr || !strings.Contains(out, "disabled (read-only mode)") {
		t.Errorf("merge in read-only mode: %q", out)
	}
	out, isErr = s.call(t, "review", map[string]any{"action": "create", "n": 1, "event": "approve", "repo": "o/r"})
	if !isErr || !strings.Contains(out, "disabled (read-only mode)") {
		t.Errorf("review create in read-only mode: %q", out)
	}
	out, isErr = s.call(t, "pr", map[string]any{"action": "help"})
	if isErr || strings.Contains(out, "merge [W]") || !strings.Contains(out, "list [R]") {
		t.Errorf("read-only help: %q", out)
	}
}
