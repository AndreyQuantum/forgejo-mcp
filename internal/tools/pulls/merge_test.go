package pulls

import (
	"net/http"
	"strings"
	"testing"
)

const (
	pr42  = "GET /repos/o/r/pulls/42"
	merge = "POST /repos/o/r/pulls/42/merge"
)

func openPR42() string { return mkPR(42, nil) }

func mergedPR42() string {
	return mkPR(42, func(m map[string]any) {
		m["state"], m["merged"] = "closed", true
		m["merge_commit_sha"] = "9f9f9f9f" + "0000000000000000000000000000000"
		m["merged_by"] = map[string]any{"login": "bob"}
		m["merged_at"] = "2026-11-22T11:00:00Z"
	})
}

func TestMergeOK200(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{
		pr42:  seq(jsonBody(openPR42()), jsonBody(mergedPR42())),
		merge: status(200, ``),
	})
	out := mustOK(t, env, "pr", map[string]any{
		"action": "merge", "n": 42, "method": "squash", "merge_title": "T", "merge_message": "M",
		"head_sha": "8a1b2c3d", "force": true, "merge_commit_id": "abc", "delete_branch": true,
	})
	if out != "ok: merged pr #42 as 9f9f9f9f (squash, branch deleted)" {
		t.Errorf("got %q", out)
	}
	body := jsonOf(t, f.last(t, "POST", "/repos/o/r/pulls/42/merge"))
	want := map[string]any{
		"Do": "squash", "MergeTitleField": "T", "MergeMessageField": "M", "MergeCommitID": "abc",
		"head_commit_id": "8a1b2c3d" + strings.Repeat("e", 32), "force_merge": true, "delete_branch_after_merge": true,
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("body[%s] = %v, want %v (body %v)", k, body[k], v, body)
		}
	}
	if _, ok := body["merge_when_checks_succeed"]; ok {
		t.Errorf("empty merge_when_checks_succeed must be omitted: %v", body)
	}
}

func TestMergeDefaultsFromRepoMeta(t *testing.T) {
	meta := `{"full_name":"o/r","default_branch":"main","allow_merge_commits":true,"allow_rebase":true,"allow_rebase_explicit":false,
"allow_squash_merge":true,"default_merge_style":"rebase-update-only","default_delete_branch_after_merge":true}`
	env, f := newFake(t, map[string]http.HandlerFunc{
		pr42:             seq(jsonBody(openPR42()), jsonBody(mergedPR42())),
		merge:            status(200, ``),
		"GET /repos/o/r": jsonBody(meta),
	})
	out := mustOK(t, env, "pr", map[string]any{"action": "merge", "n": 42})
	if out != "ok: merged pr #42 as 9f9f9f9f (rebase, branch deleted)" {
		t.Errorf("got %q", out)
	}
	body := jsonOf(t, f.last(t, "POST", "/repos/o/r/pulls/42/merge"))
	if body["Do"] != "rebase" || body["delete_branch_after_merge"] != true {
		t.Errorf("body %v", body)
	}
}

func TestMergeScheduled201(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{
		pr42:  jsonBody(openPR42()),
		merge: status(201, ``),
	})
	out := mustOK(t, env, "pr", map[string]any{"action": "merge", "n": 42, "when_green": true})
	contains(t, out, "ok: auto-merge scheduled for pr #42", "pr automerge_cancel")
	if !env.Memo.Has("automerge:o/r#42") {
		t.Error("memo key automerge:o/r#42 not set")
	}
	body := jsonOf(t, f.last(t, "POST", "/repos/o/r/pulls/42/merge"))
	if body["merge_when_checks_succeed"] != true {
		t.Errorf("body %v", body)
	}
	if f.count("GET", "/repos/o/r/pulls/42") != 1 {
		t.Errorf("a scheduled merge must not re-read the PR:\n%s", f.dump())
	}
}

func TestMerge405Empty(t *testing.T) {
	env, _ := newFake(t, map[string]http.HandlerFunc{
		pr42:  jsonBody(openPR42()),
		merge: status(405, `{"message":""}`),
	})
	out := mustErr(t, env, "pr", map[string]any{"action": "merge", "n": 42})
	contains(t, out, "error: 405 pr already merged")
}

func TestMerge405TryLaterThenOK(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{
		pr42: seq(jsonBody(openPR42()), jsonBody(mergedPR42())),
		merge: seq(
			status(405, `{"message":"Please try again later"}`),
			status(405, `{"message":"Please try again later"}`),
			status(405, `{"message":"Please try again later"}`),
			status(200, ``),
		),
	})
	autoAdvance(t, env)
	out := mustOK(t, env, "pr", map[string]any{"action": "merge", "n": 42})
	contains(t, out, "ok: merged pr #42")
	if n := f.count("POST", "/repos/o/r/pulls/42/merge"); n != 4 {
		t.Errorf("merge POSTs = %d, want 4 (1 + 3 retries)", n)
	}
}

func TestMerge405TryLaterGivesUp(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{
		pr42:  jsonBody(openPR42()),
		merge: status(405, `{"message":"Please try again later"}`),
	})
	autoAdvance(t, env)
	out := mustErr(t, env, "pr", map[string]any{"action": "merge", "n": 42})
	contains(t, out, "405 merge check still running")
	if n := f.count("POST", "/repos/o/r/pulls/42/merge"); n != 4 {
		t.Errorf("merge POSTs = %d, want 4", n)
	}
}

func TestMerge405Messages(t *testing.T) {
	for _, tc := range []struct{ msg, hint string }{
		{"Does not have enough approvals yet", "needs N approvals; see pr get"},
		{"Work in progress PRs cannot be merged", "pr edit draft=false"},
		{"User not allowed to merge PR", "needs write access or merge whitelist; force needs admin"},
	} {
		env, _ := newFake(t, map[string]http.HandlerFunc{
			pr42:  jsonBody(openPR42()),
			merge: status(405, `{"message":"`+tc.msg+`"}`),
		})
		out := mustErr(t, env, "pr", map[string]any{"action": "merge", "n": 42})
		contains(t, out, "error: 405", tc.msg, "hint: "+tc.hint)
	}
}

func TestMerge409Conflict(t *testing.T) {
	body := `{"Style":"merge","StdOut":"CONFLICT (content): Merge conflict in a.go\nCONFLICT (content): Merge conflict in b/c.go\n","StdErr":"","Err":{}}`
	env, _ := newFake(t, map[string]http.HandlerFunc{
		pr42:  jsonBody(openPR42()),
		merge: status(409, body),
	})
	out := mustErr(t, env, "pr", map[string]any{"action": "merge", "n": 42})
	if out != "error: merge conflict in a.go, b/c.go; update the branch (pr update) or resolve locally" {
		t.Errorf("got %q", out)
	}
}

func TestMerge409EmptyBodyRereadsMerged(t *testing.T) {
	env, _ := newFake(t, map[string]http.HandlerFunc{
		pr42:  seq(jsonBody(openPR42()), jsonBody(mergedPR42())),
		merge: status(409, `{}`),
	})
	out := mustOK(t, env, "pr", map[string]any{"action": "merge", "n": 42})
	if out != "ok: pr #42 is already merged as 9f9f9f9f" {
		t.Errorf("got %q", out)
	}
}

func TestMerge409EmptyBodyNotMerged(t *testing.T) {
	env, _ := newFake(t, map[string]http.HandlerFunc{
		pr42:  jsonBody(openPR42()),
		merge: status(409, `{}`),
	})
	out := mustErr(t, env, "pr", map[string]any{"action": "merge", "n": 42})
	contains(t, out, "error: 409 merge conflict", "pr update")
}

func TestMerge409ManuallyMergedWrongCommit(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{
		pr42:  jsonBody(openPR42()),
		merge: status(409, `{}`),
	})
	out := mustErr(t, env, "pr", map[string]any{"action": "merge", "n": 42, "method": "manually-merged", "merge_commit_id": "dead"})
	contains(t, out, "error: 409 merge_commit_id does not match a commit on the base branch")
	if f.count("GET", "/repos/o/r/pulls/42") != 1 {
		t.Errorf("manually-merged must not re-read the PR:\n%s", f.dump())
	}
}

func TestMerge409AlreadyScheduled(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{
		pr42:  jsonBody(openPR42()),
		merge: status(409, `{"message":"auto merge is already scheduled"}`),
	})
	out := mustErr(t, env, "pr", map[string]any{"action": "merge", "n": 42, "when_green": true})
	contains(t, out, "error: 409 auto-merge already scheduled; pr automerge_cancel first")
	if f.count("GET", "/repos/o/r/pulls/42") != 1 {
		t.Errorf("a message-carrying 409 must be passed through:\n%s", f.dump())
	}
}

func TestMerge409MessagePassthrough(t *testing.T) {
	env, _ := newFake(t, map[string]http.HandlerFunc{
		pr42:  jsonBody(openPR42()),
		merge: status(409, `{"message":"head out of date"}`),
	})
	out := mustErr(t, env, "pr", map[string]any{"action": "merge", "n": 42, "head_sha": "8a1b2c3d"})
	contains(t, out, "409", "head out of date", "re-fetch the PR and retry")
}

func TestMergeDeleteBranch403AfterMerge(t *testing.T) {
	env, _ := newFake(t, map[string]http.HandlerFunc{
		pr42:  seq(jsonBody(openPR42()), jsonBody(mergedPR42())),
		merge: status(403, `{"message":"the head branch is protected"}`),
	})
	out := mustOK(t, env, "pr", map[string]any{"action": "merge", "n": 42, "delete_branch": true})
	if out != "ok: merged pr #42 as 9f9f9f9f (merge); branch not deleted (the head branch is protected)" {
		t.Errorf("got %q", out)
	}
}

func TestMergeDeleteBranch500NotMerged(t *testing.T) {
	env, _ := newFake(t, map[string]http.HandlerFunc{
		pr42:  jsonBody(openPR42()),
		merge: status(500, `{"message":""}`),
	})
	out := mustErr(t, env, "pr", map[string]any{"action": "merge", "n": 42, "delete_branch": true})
	contains(t, out, "error: 500 server error", "at: POST /repos/o/r/pulls/42/merge")
	contains(t, out, "likely:")
}

func TestMerge403WithoutDeleteBranchIsAnError(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{
		pr42:  jsonBody(openPR42()),
		merge: status(403, `{"message":"no"}`),
	})
	out := mustErr(t, env, "pr", map[string]any{"action": "merge", "n": 42})
	contains(t, out, "error: 403")
	if f.count("GET", "/repos/o/r/pulls/42") != 1 {
		t.Errorf("no re-read expected without delete_branch:\n%s", f.dump())
	}
}

func TestMergePrechecks(t *testing.T) {
	cases := []struct {
		name string
		pr   string
		args map[string]any
		want string
	}{
		{"merged", mergedPR42(), map[string]any{}, "error: pr #42 is already merged (merge_commit 9f9f9f9f)"},
		{
			"closed unmerged", mkPR(42, func(m map[string]any) { m["state"] = "closed" }),
			map[string]any{},
			"error: pr #42 is closed, not merged; reopen with pr edit state=open",
		},
		{
			"draft", mkPR(42, func(m map[string]any) { m["draft"] = true }),
			map[string]any{},
			"error: pr #42 is a draft (title prefix WIP); pr edit draft=false",
		},
		{"head moved", openPR42(), map[string]any{"head_sha": "ffffffff"}, "error: head moved (8a1b2c3d); re-check and retry"},
		{
			"style disabled", openPR42(),
			map[string]any{"method": "fast-forward-only"},
			"error: style fast-forward-only disabled in this repo; allowed: merge,rebase,rebase-merge,squash",
		},
		{"unknown style", openPR42(), map[string]any{"method": "yolo"}, "error: invalid: method \"yolo\""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, f := newFake(t, map[string]http.HandlerFunc{pr42: jsonBody(tc.pr), merge: status(200, ``)})
			args := map[string]any{"action": "merge", "n": 42}
			for k, v := range tc.args {
				args[k] = v
			}
			out := mustErr(t, env, "pr", args)
			contains(t, out, tc.want)
			if f.count("POST", "/repos/o/r/pulls/42/merge") != 0 {
				t.Errorf("a pre-check failure must not POST:\n%s", f.dump())
			}
		})
	}
}

func TestMergeManuallyMergedIsNeverPrechecked(t *testing.T) {
	// a repo that disables every style still lets manually-merged through
	meta := `{"full_name":"o/r","default_branch":"main"}`
	env, f := newFake(t, map[string]http.HandlerFunc{
		pr42:             seq(jsonBody(openPR42()), jsonBody(mergedPR42())),
		merge:            status(200, ``),
		"GET /repos/o/r": jsonBody(meta),
	})
	out := mustOK(t, env, "pr", map[string]any{"action": "merge", "n": 42, "method": "manually-merged", "merge_commit_id": "abc"})
	contains(t, out, "ok: merged pr #42")
	if jsonOf(t, f.last(t, "POST", "/repos/o/r/pulls/42/merge"))["Do"] != "manually-merged" {
		t.Error("Do must be manually-merged")
	}
}

func TestMerge405NotAllowedStyleFromServer(t *testing.T) {
	env, _ := newFake(t, map[string]http.HandlerFunc{
		pr42:  jsonBody(openPR42()),
		merge: status(405, `{"message":"manually-merged is not an allowed merge style for this repository"}`),
	})
	out := mustErr(t, env, "pr", map[string]any{"action": "merge", "n": 42, "method": "manually-merged"})
	contains(t, out, "not an allowed merge style")
}

func TestAutomergeCancel(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{
		"DELETE /repos/o/r/pulls/42/merge": status(204, ``),
		"DELETE /repos/o/r/pulls/43/merge": status(404, `{"message":"The target couldn't be found."}`),
	})
	out := mustOK(t, env, "pr", map[string]any{"action": "automerge_cancel", "n": 42})
	if out != "ok: auto-merge cancelled for pr #42" {
		t.Errorf("got %q", out)
	}
	out = mustErr(t, env, "pr", map[string]any{"action": "automerge_cancel", "n": 43})
	contains(t, out, "no auto-merge was scheduled")
	_ = f
}

func TestUpdateBranch(t *testing.T) {
	moved := mkPR(42, func(m map[string]any) {
		m["head"] = map[string]any{"label": "fix/login", "ref": "fix/login", "sha": "1111111111111111111111111111111111111111", "repo": map[string]any{"full_name": "o/r"}}
	})
	env, f := newFake(t, map[string]http.HandlerFunc{
		pr42:                              seq(jsonBody(openPR42()), jsonBody(moved)),
		"POST /repos/o/r/pulls/42/update": status(200, ``),
	})
	out := mustOK(t, env, "pr", map[string]any{"action": "update", "n": 42, "style": "rebase"})
	contains(t, out, "ok: branch updated (rebase) for pr #42")
	if q := f.last(t, "POST", "/repos/o/r/pulls/42/update").Query; q != "style=rebase" {
		t.Errorf("query %q", q)
	}

	env, _ = newFake(t, map[string]http.HandlerFunc{
		pr42:                              jsonBody(openPR42()),
		"POST /repos/o/r/pulls/42/update": status(200, ``),
	})
	out = mustOK(t, env, "pr", map[string]any{"action": "update", "n": 42})
	contains(t, out, "ok: pr #42 already up to date")

	env, _ = newFake(t, map[string]http.HandlerFunc{
		pr42:                              jsonBody(mergedPR42()),
		"POST /repos/o/r/pulls/42/update": status(422, `{"message":"%!s(<nil>)"}`),
	})
	out = mustErr(t, env, "pr", map[string]any{"action": "update", "n": 42})
	contains(t, out, "error: 422 pr is merged or closed; nothing to update")
}

func TestMerge405RetriesWhileNotMergeable(t *testing.T) {
	// while the conflict check runs the PR reads mergeable=false: all retries must still run
	checking := mkPR(42, func(m map[string]any) { m["mergeable"] = false })
	env, f := newFake(t, map[string]http.HandlerFunc{
		pr42:  jsonBody(checking),
		merge: status(405, `{"message":"Please try again later"}`),
	})
	autoAdvance(t, env)
	out := mustErr(t, env, "pr", map[string]any{"action": "merge", "n": 42})
	contains(t, out, "405 merge check still running", "pr update")
	if n := f.count("POST", "/repos/o/r/pulls/42/merge"); n != 4 {
		t.Errorf("merge POSTs = %d, want 4 (1 + 3 retries)", n)
	}
}

func TestMergeShortHeadSHASendsFullSHA(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{
		pr42:  seq(jsonBody(openPR42()), jsonBody(mergedPR42())),
		merge: status(200, ``),
	})
	mustOK(t, env, "pr", map[string]any{"action": "merge", "n": 42, "method": "merge", "head_sha": "8a1b"})
	body := jsonOf(t, f.last(t, "POST", "/repos/o/r/pulls/42/merge"))
	if want := "8a1b2c3d" + strings.Repeat("e", 32); body["head_commit_id"] != want {
		t.Errorf("head_commit_id = %v, want %s", body["head_commit_id"], want)
	}
}

func TestMergeManuallyMergedNeverDeletesBranch(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{
		pr42:  seq(jsonBody(openPR42()), jsonBody(mergedPR42())),
		merge: status(200, ``),
	})
	out := mustOK(t, env, "pr", map[string]any{"action": "merge", "n": 42, "method": "manually-merged", "merge_commit_id": "abc", "delete_branch": true})
	notContains(t, out, "branch deleted")
	if _, ok := jsonOf(t, f.last(t, "POST", "/repos/o/r/pulls/42/merge"))["delete_branch_after_merge"]; ok {
		t.Error("delete_branch_after_merge must not be sent for manually-merged")
	}
}

func TestMergeScheduledWithoutChecksAddsNote(t *testing.T) {
	sha := "8a1b2c3d" + "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	env, _ := newFake(t, map[string]http.HandlerFunc{
		pr42:  jsonBody(openPR42()),
		merge: status(201, ``),
		"GET /repos/o/r/commits/" + sha + "/status": jsonBody(`{"statuses":[]}`),
	})
	out := mustOK(t, env, "pr", map[string]any{"action": "merge", "n": 42, "when_green": true})
	contains(t, out, "ok: auto-merge scheduled for pr #42", "note: no checks exist for this head")

	env, _ = newFake(t, map[string]http.HandlerFunc{
		pr42:  jsonBody(openPR42()),
		merge: status(201, ``),
		"GET /repos/o/r/commits/" + sha + "/status": jsonBody(`{"statuses":[{"id":1,"status":"pending","context":"ci"}]}`),
	})
	out = mustOK(t, env, "pr", map[string]any{"action": "merge", "n": 42, "when_green": true})
	notContains(t, out, "note:")
}
