package pulls

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

func TestPRList(t *testing.T) {
	fork := mkPR(40, func(m map[string]any) {
		m["title"] = "WIP: new cache"
		m["draft"] = true
		m["mergeable"] = false
		m["user"] = map[string]any{"login": "bob"}
		m["head"] = map[string]any{"label": "cache", "ref": "cache", "sha": "c0ffee00" + strings.Repeat("0", 32), "repo": map[string]any{"full_name": "bob/r"}}
		m["labels"] = []any{}
		m["assignees"] = []any{}
		m["updated_at"] = "2026-11-20T08:01:00Z"
	})
	env, f := newFake(t, map[string]http.HandlerFunc{
		"GET /repos/o/r/pulls": withTotal(3, "["+mkPR(42, nil)+",null,"+fork+"]"),
	})
	out := mustOK(t, env, "pr", map[string]any{"action": "list", "labels": []string{"bug"}, "milestone": "v1.2", "author": "alice", "base": "refs/heads/main", "sort": "recentupdate", "limit": 3})
	testutil.Golden(t, "pr_list", out)
	q := f.last(t, "GET", "/repos/o/r/pulls").Query
	for _, want := range []string{"state=open", "labels=5", "milestone=3", "poster=alice", "base=main", "sort=recentupdate", "limit=3", "page=1"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q lacks %s", q, want)
		}
	}
}

func TestPRListMergedFilterAndNextPage(t *testing.T) {
	open := mkPR(2, func(m map[string]any) { m["state"] = "closed" })
	merged := mkPR(3, func(m map[string]any) { m["state"], m["merged"] = "closed", true })
	page := 0
	env, f := newFake(t, map[string]http.HandlerFunc{
		"GET /repos/o/r/pulls": func(w http.ResponseWriter, r *http.Request) {
			page++
			w.Header().Set("X-Total-Count", "4")
			if page == 1 {
				fmt.Fprint(w, "["+open+","+open+"]") // raw page full, nothing merged
				return
			}
			fmt.Fprint(w, "["+merged+","+open+"]")
		},
	})
	out := mustOK(t, env, "pr", map[string]any{"action": "list", "state": "merged", "limit": 2})
	if !strings.Contains(out, "#3 merged") || strings.Contains(out, "#2 ") {
		t.Errorf("merged filter:\n%s", out)
	}
	if !strings.HasPrefix(out, "# prs 1 page 2 next: page=3") {
		t.Errorf("header: %q", strings.SplitN(out, "\n", 2)[0])
	}
	if q := f.last(t, "GET", "/repos/o/r/pulls").Query; !strings.Contains(q, "state=closed") {
		t.Errorf("merged must ask the server for closed: %s", q)
	}
}

func TestPRListUnknownLabelAndState(t *testing.T) {
	env, _ := newFake(t, nil)
	out := mustErr(t, env, "pr", map[string]any{"action": "list", "labels": []string{"nope"}})
	contains(t, out, `unknown label "nope"; known: bug,ui`)
	out = mustErr(t, env, "pr", map[string]any{"action": "list", "state": "weird"})
	contains(t, out, "invalid: state")
}

func TestPRListEmpty(t *testing.T) {
	env, _ := newFake(t, map[string]http.HandlerFunc{"GET /repos/o/r/pulls": withTotal(0, "[]")})
	if out := mustOK(t, env, "pr", map[string]any{"action": "list"}); out != "# prs 0/0" {
		t.Errorf("got %q", out)
	}
}

// getRoutes serves a failing-CI PR with reviews and a protected base.
func getRoutes() map[string]http.HandlerFunc {
	sha := "8a1b2c3d" + strings.Repeat("e", 32)
	return map[string]http.HandlerFunc{
		pr42: jsonBody(mkPR(42, func(m map[string]any) {
			m["requested_reviewers"] = []any{map[string]any{"login": "carol"}}
			m["milestone"] = map[string]any{"title": "v1.2"}
		})),
		"GET /repos/o/r/commits/" + sha + "/status": jsonBody(`{"statuses":[
			{"id":1,"status":"success","context":"lint (push)","created_at":"2026-11-22T09:00:00Z"},
			{"id":2,"status":"success","context":"test (push)","created_at":"2026-11-22T09:00:00Z"},
			{"id":3,"status":"success","context":"docs (push)","created_at":"2026-11-22T09:00:00Z"},
			{"id":4,"status":"failure","context":"ci / build (push)","description":"Failing after 12s","created_at":"2026-11-22T09:00:00Z"}]}`),
		"GET /repos/o/r/pulls/42/reviews": func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Query().Get("page") {
			case "1":
			case "2":
				fmt.Fprint(w, `[{"id":15,"user":{"login":"erin"},"state":"COMMENT","submitted_at":"2026-11-22T08:40:00Z"}]`)
				return
			default:
				fmt.Fprint(w, `[]`)
				return
			}
			fmt.Fprint(w, `[
			{"id":10,"user":{"login":"alice2"},"state":"COMMENT","submitted_at":"2026-11-22T08:00:00Z"},
			{"id":11,"user":{"login":"bob"},"state":"APPROVED","official":true,"submitted_at":"2026-11-22T08:10:00Z"},
			{"id":12,"user":{"login":"dave"},"state":"REQUEST_CHANGES","official":true,"body":"no","submitted_at":"2026-11-22T08:20:00Z"},
			{"id":13,"user":{"login":"bob"},"state":"REQUEST_CHANGES","official":true,"body":"still no","submitted_at":"2026-11-22T08:30:00Z"},
			{"id":14,"team":{"name":"devs"},"state":"REQUEST_REVIEW"}]`)
		},
		"GET /repos/o/r/branches/main": jsonBody(`{"name":"main","required_approvals":2}`),
	}
}

func TestPRGetDefaultSections(t *testing.T) {
	env, _ := newFake(t, getRoutes())
	out := mustOK(t, env, "pr", map[string]any{"action": "get", "n": 42})
	testutil.Golden(t, "pr_get", out)
	contains(t, out, "erin")
	contains(t, out, "blocked_by: ci:failure, request_changes:bob, request_changes:dave, approvals:0/2")
	notContains(t, out, "ready: yes")
}

func TestPRGetReady(t *testing.T) {
	routes := getRoutes()
	sha := "8a1b2c3d" + strings.Repeat("e", 32)
	routes["GET /repos/o/r/commits/"+sha+"/status"] = jsonBody(`{"statuses":[{"id":1,"status":"success","context":"ci","created_at":"2026-11-22T09:00:00Z"}]}`)
	routes["GET /repos/o/r/pulls/42/reviews"] = seq(jsonBody(`[{"id":11,"user":{"login":"bob"},"state":"APPROVED","official":true}]`), jsonBody(`[]`))
	routes["GET /repos/o/r/branches/main"] = jsonBody(`{"name":"main","required_approvals":1}`)
	routes[pr42] = jsonBody(mkPR(42, nil))
	env, _ := newFake(t, routes)
	out := mustOK(t, env, "pr", map[string]any{"action": "get", "n": 42, "include": []string{"ci", "reviews"}})
	contains(t, out, "ci: success 1/1", "reviews: bob approve", "ready: yes")
	notContains(t, out, "body:", "blocked_by")
}

func TestPRGetMergedAndClosedDefaults(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{pr42: jsonBody(mergedPR42())})
	out := mustOK(t, env, "pr", map[string]any{"action": "get", "n": 42})
	contains(t, out, "pr #42 merged | Fix thing 42", "merged: by bob 2026-11-22 11:00 as 9f9f9f9f", "body:")
	notContains(t, out, "ci:", "reviews:", "mergeable:")
	if f.count("GET", "/repos/o/r/pulls/42") != 1 || len(f.calls) != 1 {
		t.Errorf("a merged PR needs only the PR request:\n%s", f.dump())
	}
}

func TestPRGetIncludeFilesCommitsComments(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{
		pr42: jsonBody(openPR42()),
		"GET /repos/o/r/pulls/42/files": withTotal(2, `[{"filename":"a.go","status":"changed","additions":4,"deletions":1},
			{"filename":"new.go","previous_filename":"old.go","status":"renamed","additions":0,"deletions":0}]`),
		"GET /repos/o/r/pulls/42/commits": withTotal(1, `[{"sha":"77aa11bb77aa11bb77aa11bb","commit":{"message":"Fix race\n\nlong","author":{"name":"Alice A","date":"2026-11-21T09:00:00Z"}},"author":{"login":"alice"}}]`),
		"GET /repos/o/r/issues/42/comments": jsonBody(`[{"id":1,"user":{"login":"x"},"body":"one","created_at":"2026-11-21T10:00:00Z"},
			{"id":2,"user":{"login":"y"},"body":"two\nlines","created_at":"2026-11-21T11:00:00Z"}]`),
	})
	out := mustOK(t, env, "pr", map[string]any{"action": "get", "n": 42, "include": []string{"files", "commits", "comments"}})
	testutil.Golden(t, "pr_get_include", out)
	if q := f.last(t, "GET", "/repos/o/r/pulls/42/commits").Query; !strings.Contains(q, "verification=false") || !strings.Contains(q, "files=false") {
		t.Errorf("commits query %q", q)
	}
}

func TestPRGetBadInclude(t *testing.T) {
	env, _ := newFake(t, map[string]http.HandlerFunc{pr42: jsonBody(openPR42())})
	out := mustErr(t, env, "pr", map[string]any{"action": "get", "n": 42, "include": []string{"diff"}})
	contains(t, out, `invalid: include "diff"; valid: body,files,commits,ci,reviews,comments`)
}

func TestPRGetBodyCap(t *testing.T) {
	long := strings.Repeat("x", 3000)
	env, _ := newFake(t, map[string]http.HandlerFunc{pr42: jsonBody(mkPR(42, func(m map[string]any) { m["body"] = long; m["state"] = "closed" }))})
	out := mustOK(t, env, "pr", map[string]any{"action": "get", "n": 42})
	contains(t, out, "[truncated: 1500 of 3000 chars; pass body_max=0 or full=true]")
	out = mustOK(t, env, "pr", map[string]any{"action": "get", "n": 42, "body_max": 0})
	notContains(t, out, "[truncated")
	out = mustOK(t, env, "pr", map[string]any{"action": "get", "n": 42, "body_max": 100})
	contains(t, out, "[truncated: 100 of 3000 chars")
}

func TestPRGetByHeadWithBaseUsesBaseHeadRoute(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{
		"GET /repos/o/r/pulls/release%2F1.0/bob:feat/x": jsonBody(openPR42()),
	})
	out := mustOK(t, env, "pr", map[string]any{"action": "get", "head": "bob:feat/x", "base": "release/1.0", "include": []string{"body"}})
	contains(t, out, "pr #42 open")
	_ = f.last(t, "GET", "/repos/o/r/pulls/release%2F1.0/bob:feat/x")
}

func TestPRGetByForkHeadWithoutBaseUsesDefaultBranch(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{
		"GET /repos/o/r/pulls/main/bob:feat": jsonBody(openPR42()),
	})
	mustOK(t, env, "pr", map[string]any{"action": "get", "head": "bob:feat", "include": []string{"body"}})
	_ = f.last(t, "GET", "/repos/o/r/pulls/main/bob:feat")
}

func TestPRGetByPlainHeadListsAllStates(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{
		"GET /repos/o/r/pulls": jsonBody("[null," + openPR42() + "]"),
	})
	out := mustOK(t, env, "pr", map[string]any{"action": "get", "head": "fix/login", "include": []string{"body"}})
	contains(t, out, "pr #42 open")
	if q := f.last(t, "GET", "/repos/o/r/pulls").Query; !strings.Contains(q, "head=fix%2Flogin") || !strings.Contains(q, "state=all") || !strings.Contains(q, "limit=5") {
		t.Errorf("query %q", q)
	}
	env, _ = newFake(t, map[string]http.HandlerFunc{"GET /repos/o/r/pulls": jsonBody("[]")})
	out = mustErr(t, env, "pr", map[string]any{"action": "get", "head": "nope"})
	contains(t, out, "no pr found for head nope")
}

func TestPRGetWithoutTargetNeedsBranch(t *testing.T) {
	env, _ := newFake(t, nil)
	out := mustErr(t, env, "pr", map[string]any{"action": "get"})
	contains(t, out, "no open pr for main; pass n or head", "target: branch main (default)")
}

func TestPRCreate(t *testing.T) {
	created := mkPR(43, nil)
	env, f := newFake(t, map[string]http.HandlerFunc{
		"POST /repos/o/r/pulls":                        status(201, created),
		"POST /repos/o/r/pulls/43/requested_reviewers": status(201, `[]`),
	})
	out := mustOK(t, env, "pr", map[string]any{
		"action": "create", "head": "fix/login", "title": "Fix login", "body": "text", "draft": true,
		"labels": []string{"bug", "ui"}, "assignees": []string{"bob"}, "milestone": "v1.2", "due": "2026-12-01",
		"reviewers": []string{"alice"}, "team_reviewers": []string{"devs"},
	})
	if out != "ok: created pr #43 fix/login->main  https://forge.example/o/r/pulls/43" {
		t.Errorf("got %q", out)
	}
	body := jsonOf(t, f.last(t, "POST", "/repos/o/r/pulls"))
	if body["head"] != "fix/login" || body["base"] != "main" || body["title"] != "WIP: Fix login" || body["body"] != "text" ||
		body["milestone"] != float64(3) || body["due_date"] != "2026-12-01T00:00:00Z" {
		t.Errorf("body %v", body)
	}
	if fmt.Sprint(body["labels"]) != "[5 6]" || fmt.Sprint(body["assignees"]) != "[bob]" {
		t.Errorf("labels/assignees %v", body)
	}
	rb := jsonOf(t, f.last(t, "POST", "/repos/o/r/pulls/43/requested_reviewers"))
	if fmt.Sprint(rb["reviewers"]) != "[alice]" || fmt.Sprint(rb["team_reviewers"]) != "[devs]" {
		t.Errorf("reviewers body %v", rb)
	}
}

func TestPRCreateReviewerFailureIsAWarning(t *testing.T) {
	env, _ := newFake(t, map[string]http.HandlerFunc{
		"POST /repos/o/r/pulls":                        status(201, mkPR(43, nil)),
		"POST /repos/o/r/pulls/43/requested_reviewers": status(422, `{"message":"reviewer is the author"}`),
	})
	out := mustOK(t, env, "pr", map[string]any{"action": "create", "head": "fix/login", "title": "t", "reviewers": []string{"alice"}})
	contains(t, out, "ok: created pr #43", "warn: reviewers not requested: 422 invalid: reviewer is the author")
}

func TestPRCreateValidation(t *testing.T) {
	env, _ := newFake(t, nil)
	contains(t, mustErr(t, env, "pr", map[string]any{"action": "create", "title": "t"}), "missing param head")
	contains(t, mustErr(t, env, "pr", map[string]any{"action": "create", "head": "main", "title": "t"}), "head and base are both main")
	contains(t, mustErr(t, env, "pr", map[string]any{"action": "create", "head": "x"}), "missing param title")
	contains(t, mustErr(t, env, "pr", map[string]any{"action": "create", "head": "x", "title": "t", "labels": []string{"zzz"}}), `unknown label "zzz"`)
}

func TestPRCreateExisting409(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{
		"POST /repos/o/r/pulls": status(409, `{"message":"pull request already exists for these targets"}`),
		"GET /repos/o/r/pulls":  jsonBody("[" + mkPR(41, nil) + "]"),
	})
	out := mustErr(t, env, "pr", map[string]any{"action": "create", "head": "fix/login", "title": "t"})
	if out != "error: PR already exists: #41" {
		t.Errorf("got %q", out)
	}
	q := f.last(t, "GET", "/repos/o/r/pulls").Query
	if !strings.Contains(q, "head=fix%2Flogin") || !strings.Contains(q, "base=main") || !strings.Contains(q, "state=open") {
		t.Errorf("lookup query %q", q)
	}
}

func TestPREditFields(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{"PATCH /repos/o/r/pulls/42": status(201, openPR42())})
	out := mustOK(t, env, "pr", map[string]any{
		"action": "edit", "n": 42, "title": "New", "body": "", "base": "dev", "state": "closed", "labels": []string{"ui"},
		"assignees": []string{}, "milestone": "v1.2", "unset_due": true, "allow_maintainer_edit": true,
	})
	contains(t, out, "ok: edited pr #42: title,body,base,state,labels,assignees,milestone,due,allow_maintainer_edit")
	body := jsonOf(t, f.last(t, "PATCH", "/repos/o/r/pulls/42"))
	if body["title"] != "New" || body["body"] != "" || body["base"] != "dev" || body["state"] != "closed" ||
		fmt.Sprint(body["labels"]) != "[6]" || fmt.Sprint(body["assignees"]) != "[]" || body["milestone"] != float64(3) ||
		body["unset_due_date"] != true || body["allow_maintainer_edit"] != true {
		t.Errorf("body %v", body)
	}
	if _, ok := body["due_date"]; ok {
		t.Errorf("unset_due must not send due_date: %v", body)
	}
	if f.count("PATCH", "/repos/o/r/issues/42") != 0 {
		t.Errorf("unexpected requests:\n%s", f.dump())
	}
}

func TestPREditDraftToggle(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{
		pr42:                        jsonBody(mkPR(42, func(m map[string]any) { m["title"] = "Fix thing" })),
		"PATCH /repos/o/r/pulls/42": status(201, openPR42()),
	})
	mustOK(t, env, "pr", map[string]any{"action": "edit", "n": 42, "draft": true})
	if got := jsonOf(t, f.last(t, "PATCH", "/repos/o/r/pulls/42"))["title"]; got != "WIP: Fix thing" {
		t.Errorf("draft=true title %v", got)
	}

	env, f = newFake(t, map[string]http.HandlerFunc{
		pr42:                        jsonBody(mkPR(42, func(m map[string]any) { m["title"] = "[WIP] Fix thing"; m["draft"] = true })),
		"PATCH /repos/o/r/pulls/42": status(201, openPR42()),
	})
	mustOK(t, env, "pr", map[string]any{"action": "edit", "n": 42, "draft": false})
	if got := jsonOf(t, f.last(t, "PATCH", "/repos/o/r/pulls/42"))["title"]; got != "Fix thing" {
		t.Errorf("draft=false title %v", got)
	}

	// changing only the title keeps the draft prefix
	mustOK(t, env, "pr", map[string]any{"action": "edit", "n": 42, "title": "Renamed"})
	if got := jsonOf(t, f.last(t, "PATCH", "/repos/o/r/pulls/42"))["title"]; got != "WIP: Renamed" {
		t.Errorf("title of a draft %v", got)
	}
}

func TestPREditMilestoneNoneUsesIssueRoute(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{"PATCH /repos/o/r/issues/42": status(201, `{}`)})
	out := mustOK(t, env, "pr", map[string]any{"action": "edit", "n": 42, "milestone": "none"})
	contains(t, out, "ok: edited pr #42: milestone")
	if got := f.last(t, "PATCH", "/repos/o/r/issues/42").Body; got != `{"milestone":0}` {
		t.Errorf("issue body %q", got)
	}
	if f.count("PATCH", "/repos/o/r/pulls/42") != 0 {
		t.Errorf("pulls PATCH must not be sent for milestone=none only:\n%s", f.dump())
	}
}

func TestPREditNothingAndBadState(t *testing.T) {
	env, _ := newFake(t, nil)
	contains(t, mustErr(t, env, "pr", map[string]any{"action": "edit", "n": 42}), "nothing to edit")
	contains(t, mustErr(t, env, "pr", map[string]any{"action": "edit", "n": 42, "state": "merged"}), "invalid: state")
}

func TestPREdit412MergedPR(t *testing.T) {
	env, _ := newFake(t, map[string]http.HandlerFunc{"PATCH /repos/o/r/pulls/42": status(412, `{"message":"pull request is already merged"}`)})
	out := mustErr(t, env, "pr", map[string]any{"action": "edit", "n": 42, "state": "open"})
	contains(t, out, "412", "merged PRs cannot be reopened")
}

func TestPRFiles(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{
		"GET /repos/o/r/pulls/42/files": withTotal(3, `[{"filename":"a.go","status":"changed","additions":4,"deletions":1},
			{"filename":"b.go","status":"added","additions":100,"deletions":0},
			{"filename":"new.go","previous_filename":"old.go","status":"renamed","additions":2,"deletions":2}]`),
	})
	out := mustOK(t, env, "pr", map[string]any{"action": "files", "n": 42, "whitespace": "ignore-all"})
	testutil.Golden(t, "pr_files", out)
	q := f.last(t, "GET", "/repos/o/r/pulls/42/files").Query
	if !strings.Contains(q, "whitespace=ignore-all") || !strings.Contains(q, "limit=50") {
		t.Errorf("query %q", q)
	}
}

func TestPRCommits(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{
		"GET /repos/o/r/pulls/42/commits": withTotal(2, `[
			{"sha":"77aa11bb77aa11bb","commit":{"message":"Fix race\n\nbody","author":{"name":"Alice A","date":"2026-11-21T09:00:00Z"}},"author":{"login":"alice"}},
			{"sha":"88bb22cc88bb22cc","commit":{"message":"Tidy","author":{"name":"Bob B","date":"2026-11-22T09:30:00Z"}}}]`),
	})
	out := mustOK(t, env, "pr", map[string]any{"action": "commits", "n": 42, "limit": 2})
	testutil.Golden(t, "pr_commits", out)
	q := f.last(t, "GET", "/repos/o/r/pulls/42/commits").Query
	if !strings.Contains(q, "verification=false") || !strings.Contains(q, "files=false") {
		t.Errorf("query %q", q)
	}
}

const sampleDiff = `diff --git a/a.go b/a.go
index 111..222 100644
--- a/a.go
+++ b/a.go
@@ -1,3 +1,4 @@
 package a
+var x = 1
 func f() {}
diff --git a/docs/readme.md b/docs/readme.md
index 333..444 100644
--- a/docs/readme.md
+++ b/docs/readme.md
@@ -1 +1,2 @@
 # doc
+more
diff --git a/logo.png b/logo.png
new file mode 100644
index 0000000..555
Binary files /dev/null and b/logo.png differ
`

func diffRoute() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, sampleDiff)
	}
}

func TestPRDiff(t *testing.T) {
	env, _ := newFake(t, map[string]http.HandlerFunc{"GET /repos/o/r/pulls/42.diff": diffRoute()})
	out := mustOK(t, env, "pr", map[string]any{"action": "diff", "n": 42})
	testutil.Golden(t, "pr_diff", out)
	out = mustOK(t, env, "pr", map[string]any{"action": "diff", "n": 42, "paths": []string{"*.md"}})
	contains(t, out, "diff pr #42: 1 files +1 -0", "=== M docs/readme.md (+1 -0) ===")
	notContains(t, out, "a.go")
	out = mustOK(t, env, "pr", map[string]any{"action": "diff", "n": 42, "paths": []string{"*.rs"}})
	contains(t, out, "no file matches paths [*.rs]; files: a.go, docs/readme.md, logo.png")
}

func TestPRDiffCapAndTrailer(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 6; i++ {
		fmt.Fprintf(&sb, "diff --git a/f%d.go b/f%d.go\n--- a/f%d.go\n+++ b/f%d.go\n@@ -1 +1 @@\n-old\n+%s\n", i, i, i, i, strings.Repeat("n", 400))
	}
	env, _ := newFake(t, map[string]http.HandlerFunc{
		"GET /repos/o/r/pulls/42.diff": func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, sb.String()) },
	})
	out := mustOK(t, env, "pr", map[string]any{"action": "diff", "n": 42, "max_chars": 1000})
	contains(t, out, "[truncated:", "files shown", "omitted: ", "pass paths=[...] or raise max_chars")
}

func TestPRDiffEmpty(t *testing.T) {
	env, _ := newFake(t, map[string]http.HandlerFunc{"GET /repos/o/r/pulls/42.diff": status(200, ``)})
	if out := mustOK(t, env, "pr", map[string]any{"action": "diff", "n": 42}); out != "diff pr #42: empty" {
		t.Errorf("got %q", out)
	}
}

func TestPRIgnoredParamsAreReported(t *testing.T) {
	env, _ := newFake(t, map[string]http.HandlerFunc{"GET /repos/o/r/pulls": withTotal(0, "[]")})
	out := mustOK(t, env, "pr", map[string]any{"action": "list", "method": "squash"})
	contains(t, out, "note: ignored params: method")
}

func TestPRHelpAndRequired(t *testing.T) {
	env, _ := newFake(t, nil)
	out := mustOK(t, env, "pr", map[string]any{"action": "help"})
	contains(t, out, "merge [W]", "when_green", "automerge_cancel [W]")
	contains(t, mustErr(t, env, "pr", map[string]any{"action": "merge"}), "missing param n")
}

func TestPRReadOnlyHidesWrites(t *testing.T) {
	env, _ := newFake(t, nil, testutil.WithConfig(func(c *config.Config) { c.ReadOnly = true }))
	out := mustErr(t, env, "pr", map[string]any{"action": "merge", "n": 1})
	contains(t, out, "disabled (read-only mode)")
}
