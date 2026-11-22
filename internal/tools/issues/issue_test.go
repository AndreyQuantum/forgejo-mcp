package issues

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

const issueList = `[
 {"id":11,"number":12,"title":"Login fails on Safari","state":"open","user":{"login":"alice"},
  "labels":[{"id":1,"name":"bug"},{"id":2,"name":"ui"}],"assignees":[{"login":"bob"},{"login":"eve"}],
  "milestone":{"id":3,"title":"v1.2","state":"open"},"comments":3,"updated_at":"2026-11-22T10:15:00Z",
  "repository":{"full_name":"o/r"}},
 {"id":12,"number":13,"title":"Add dark mode","state":"closed","user":{"login":"carol"},"labels":[],"assignees":[],
  "comments":0,"updated_at":"2026-11-20T08:00:00Z","pull_request":{"merged":true,"draft":false},
  "repository":{"full_name":"o/other"}},
 {"id":13,"number":14,"title":"WIP refactor","state":"open","user":{"login":"dave"},"labels":[],"assignees":[],
  "updated_at":"2026-11-21T09:00:00Z","pull_request":{"merged":false,"draft":true},"repository":{"full_name":"o/r"}},
 null
]`

func labelsRoutes(m map[string]handlerFunc) map[string]handlerFunc {
	out := map[string]handlerFunc{
		"GET /repos/o/r/labels":     listH(2, `[{"id":1,"name":"bug","color":"d73a4a"},{"id":2,"name":"ui","color":"0075ca"}]`),
		"GET /orgs/o/labels":        jsonH(404, `{"message":"not an org"}`),
		"GET /repos/o/r/milestones": listH(1, `[{"id":3,"title":"v1.2","state":"open"}]`),
	}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func TestIssueList(t *testing.T) {
	f := newFake(t, labelsRoutes(map[string]handlerFunc{
		"GET /repos/o/r/issues": listH(134, issueList),
	}))
	got := mustRun(t, f, "issue", map[string]any{
		"action": "list", "labels": []string{"bug"}, "milestone": "v1.2", "author": "alice",
		"assigned_to": "bob", "mentioned": "eve", "sort": "recentupdate", "q": "login",
		"since": "2026-11-01",
	})
	r := f.only("GET", "/repos/o/r/issues")
	want := map[string]string{
		"state": "open", "type": "issues", "labels": "bug", "milestones": "3", "created_by": "alice",
		"assigned_by": "bob", "mentioned_by": "eve", "sort": "recentupdate", "q": "login",
		"since": "2026-11-01T00:00:00Z", "page": "1", "limit": "20",
	}
	for k, v := range want {
		if r.Query.Get(k) != v {
			t.Errorf("query %s = %q, want %q", k, r.Query.Get(k), v)
		}
	}
	testutil.Golden(t, "issue_list", got)
}

func TestIssueListUnknownLabelAndMilestone(t *testing.T) {
	f := newFake(t, labelsRoutes(nil))
	text := mustFail(t, f, "issue", map[string]any{"action": "list", "labels": []string{"buug"}}, `unknown label "buug"; known: bug,ui`)
	_ = text
	f.none("GET", "/repos/o/r/issues")
	mustFail(t, f, "issue", map[string]any{"action": "list", "milestone": "v9"}, `unknown milestone "v9"`)
	f.none("GET", "/repos/o/r/issues")
}

func TestIssueListEmptyAndTypePulls(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{"GET /repos/o/r/issues": listH(0, `[]`)})
	got := mustRun(t, f, "issue", map[string]any{"action": "list", "type": "pulls", "state": "all"})
	eq(t, "empty", got, "# prs 0/0\n")
	r := f.only("GET", "/repos/o/r/issues")
	eq(t, "type", r.Query.Get("type"), "pulls")
	eq(t, "state", r.Query.Get("state"), "all")
	f2 := newFake(t, map[string]handlerFunc{"GET /repos/o/r/issues": listH(0, `[]`)})
	mustRun(t, f2, "issue", map[string]any{"action": "list", "type": "all"})
	if f2.only("GET", "/repos/o/r/issues").Query.Has("type") {
		t.Error("type=all must not send a type filter")
	}
	mustFail(t, f2, "issue", map[string]any{"action": "list", "state": "bogus"}, "invalid: state")
}

func TestIssueSearch(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{"GET /repos/issues/search": listH(4, issueList)})
	got := mustRun(t, f, "issue", map[string]any{
		"action": "search", "q": "safari", "flags": []string{"assigned", "review_requested"},
		"owner": "o", "labels": []string{"bug", "ui"}, "limit": 10, "repo": "x/y",
	})
	r := f.only("GET", "/repos/issues/search")
	for k, v := range map[string]string{
		"q": "safari", "assigned": "true", "review_requested": "true", "owner": "o",
		"labels": "bug,ui", "limit": "10", "page": "1", "state": "open",
	} {
		if r.Query.Get(k) != v {
			t.Errorf("query %s = %q, want %q", k, r.Query.Get(k), v)
		}
	}
	if r.Query.Has("type") {
		t.Error("search default type is all: no type filter expected")
	}
	f.none("GET", "/repos/x/y")
	testutil.Golden(t, "issue_search", got)
	mustFail(t, f, "issue", map[string]any{"action": "search", "flags": []string{"nope"}}, `invalid: flag "nope"`)
}

const issueGet = `{"id":11,"number":12,"title":"Login fails on Safari","state":"open","user":{"login":"alice"},
 "labels":[{"id":1,"name":"bug","color":"d73a4a"}],"assignees":[{"login":"bob"}],
 "milestone":{"id":3,"title":"v1.2","state":"open"},"comments":3,"created_at":"2026-11-20T09:00:00Z",
 "updated_at":"2026-11-22T10:15:00Z","due_date":"2026-12-01T23:59:59Z","is_locked":true,"ref":"refs/heads/main",
 "html_url":"http://forgejo.example.com/o/r/issues/12","body":"line one\nline two",
 "assets":[{"id":4,"name":"shot.png","size":1234}],"repository":{"full_name":"o/r"}}`

func TestIssueGetWithIncludes(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{
		"GET /repos/o/r/issues/12": jsonH(200, issueGet),
		"GET /repos/o/r/issues/12/comments": jsonH(200, `[
		 {"id":1,"user":{"login":"a"},"body":"first","created_at":"2026-11-20T10:00:00Z"},
		 {"id":2,"user":{"login":"b"},"body":"second\nline","created_at":"2026-11-21T10:00:00Z"}]`),
		"GET /repos/o/r/issues/12/timeline": listH(2, `[
		 {"id":50,"type":"label","user":{"login":"alice"},"created_at":"2026-11-20T09:01:00Z","body":"1","label":{"name":"bug"}},
		 null,
		 {"id":51,"type":"close","user":{"login":"bob"},"created_at":"2026-11-21T09:01:00Z"}]`),
	})
	got := mustRun(t, f, "issue", map[string]any{"action": "get", "n": 12, "include": []string{"comments", "timeline"}, "verbose": true})
	testutil.Golden(t, "issue_get_includes", got)
	if q := f.only("GET", "/repos/o/r/issues/12/timeline").Query; q.Get("limit") != "20" || q.Get("page") != "1" {
		t.Errorf("timeline query %v", q)
	}
}

func TestIssueGetPlainAndPR(t *testing.T) {
	pr := `{"id":9,"number":7,"title":"Fix","state":"open","user":{"login":"a"},"pull_request":{"merged":false,"draft":true},"body":""}`
	f := newFake(t, map[string]handlerFunc{
		"GET /repos/o/r/issues/12": jsonH(200, issueGet),
		"GET /repos/o/r/issues/7":  jsonH(200, pr),
		"GET /repos/o/r/issues/8":  jsonH(200, `{}`),
	})
	testutil.Golden(t, "issue_get_plain", mustRun(t, f, "issue", map[string]any{"action": "get", "n": 12}))
	got := mustRun(t, f, "issue", map[string]any{"action": "get", "n": 7})
	contains(t, got, "pr #7", "state: open draft")
	mustFail(t, f, "issue", map[string]any{"action": "get", "n": 8}, "empty issue object")
	mustFail(t, f, "issue", map[string]any{"action": "get", "n": 12, "include": []string{"zzz"}}, "invalid: include")
}

func TestIssueGetBodyCaps(t *testing.T) {
	long := strings.Repeat("x", 2000)
	f := newFake(t, map[string]handlerFunc{
		"GET /repos/o/r/issues/1": jsonH(200, `{"id":1,"number":1,"title":"t","state":"open","body":"`+long+`"}`),
	})
	got := mustRun(t, f, "issue", map[string]any{"action": "get", "n": 1})
	contains(t, got, "[truncated: 1500 of 2000 chars; pass body_max=0 or full=true]")
	got = mustRun(t, f, "issue", map[string]any{"action": "get", "n": 1, "body_max": 0})
	if strings.Contains(got, "truncated") {
		t.Errorf("body_max=0 must lift the cap:\n%s", got)
	}
	got = mustRun(t, f, "issue", map[string]any{"action": "get", "n": 1, "body_max": 300})
	contains(t, got, "[truncated: 300 of 2000 chars")
}

func TestIssueCreate(t *testing.T) {
	f := newFake(t, labelsRoutes(map[string]handlerFunc{
		"POST /repos/o/r/issues": jsonH(201, `{"id":5,"number":12,"html_url":"http://h/o/r/issues/12","labels":[{"name":"bug"}],"assignees":[{"login":"bob"}],"milestone":{"id":3,"title":"v1.2"}}`),
	}))
	got := mustRun(t, f, "issue", map[string]any{
		"action": "create", "title": "T", "body": "B", "labels": []string{"bug"}, "milestone": "v1.2",
		"assignees": []string{"bob"}, "due": "2026-12-01", "ref": "refs/heads/main", "closed": true,
	})
	eq(t, "result", got, "ok: created issue #12 http://h/o/r/issues/12")
	var body map[string]any
	if err := json.Unmarshal([]byte(f.only("POST", "/repos/o/r/issues").Body), &body); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(body)
	eq(t, "body", string(b), `{"assignees":["bob"],"body":"B","closed":true,"due_date":"2026-12-01T00:00:00Z","labels":[1],"milestone":3,"ref":"refs/heads/main","title":"T"}`)
}

func TestIssueCreateIgnoredFieldsNote(t *testing.T) {
	f := newFake(t, labelsRoutes(map[string]handlerFunc{
		"POST /repos/o/r/issues": jsonH(201, `{"id":5,"number":13,"html_url":"u","labels":[],"assignees":[]}`),
	}))
	got := mustRun(t, f, "issue", map[string]any{"action": "create", "title": "T", "labels": []string{"bug"}, "assignees": []string{"bob"}})
	contains(t, got, "ok: created issue #13 u", "note: server ignored labels/assignees (no write access)")
	mustFail(t, f, "issue", map[string]any{"action": "create"}, "missing param title")
	mustFail(t, f, "issue", map[string]any{"action": "create", "title": "T", "labels": []string{"nope"}}, `unknown label "nope"`)
	mustFail(t, f, "issue", map[string]any{"action": "create", "title": "T", "due": "tomorrow"}, "invalid: due")
}

func TestIssueEdit(t *testing.T) {
	f := newFake(t, labelsRoutes(map[string]handlerFunc{
		"PATCH /repos/o/r/issues/12":            jsonH(201, `{"number":12}`),
		"POST /repos/o/r/issues/12/labels":      jsonH(200, `[{"id":1,"name":"bug"}]`),
		"DELETE /repos/o/r/issues/12/labels/ui": emptyH(204),
	}))
	got := mustRun(t, f, "issue", map[string]any{
		"action": "edit", "n": 12, "title": "New", "body": "", "closed": true, "milestone": "none",
		"assignees": []string{}, "unset_due": true, "add_labels": []string{"bug"}, "remove_labels": []string{"ui"},
	})
	eq(t, "result", got, "ok: edited #12: title,body,state=closed,milestone=none,assignees,due=none,labels+bug,labels-ui")
	eq(t, "patch", f.only("PATCH", "/repos/o/r/issues/12").Body, `{"assignees":[],"body":"","milestone":0,"state":"closed","title":"New","unset_due_date":true}`)
	eq(t, "add", f.only("POST", "/repos/o/r/issues/12/labels").Body, `{"labels":[1]}`)
	f.only("DELETE", "/repos/o/r/issues/12/labels/ui")
}

func TestIssueEditSetLabelsAndMilestone(t *testing.T) {
	f := newFake(t, labelsRoutes(map[string]handlerFunc{
		"PATCH /repos/o/r/issues/12":      jsonH(201, `{}`),
		"PUT /repos/o/r/issues/12/labels": jsonH(200, `[{"id":1,"name":"bug"}]`),
	}))
	got := mustRun(t, f, "issue", map[string]any{"action": "edit", "n": 12, "milestone": "v1.2", "set_labels": []string{"bug", "ui"}, "state": "open", "due": "2026-12-24"})
	contains(t, got, "ok: edited #12: state=open,milestone,due,labels=bug,ui", "note: label ui was not applied")
	eq(t, "patch", f.only("PATCH", "/repos/o/r/issues/12").Body, `{"due_date":"2026-12-24T00:00:00Z","milestone":3,"state":"open"}`)
	eq(t, "put", f.only("PUT", "/repos/o/r/issues/12/labels").Body, `{"labels":[1,2]}`)
	// clearing all labels sends an empty list and no PATCH.
	f2 := newFake(t, labelsRoutes(map[string]handlerFunc{"PUT /repos/o/r/issues/12/labels": jsonH(200, `[]`)}))
	mustRun(t, f2, "issue", map[string]any{"action": "edit", "n": 12, "set_labels": []string{}})
	eq(t, "clear", f2.only("PUT", "/repos/o/r/issues/12/labels").Body, `{"labels":[]}`)
	f2.none("PATCH", "/repos/o/r/issues/12")
}

func TestIssueEditValidatesBeforeWriting(t *testing.T) {
	f := newFake(t, labelsRoutes(map[string]handlerFunc{"PATCH /repos/o/r/issues/12": jsonH(201, `{}`)}))
	mustFail(t, f, "issue", map[string]any{"action": "edit", "n": 12, "title": "x", "add_labels": []string{"nope"}}, `unknown label "nope"`)
	f.none("PATCH", "/repos/o/r/issues/12")
	mustFail(t, f, "issue", map[string]any{"action": "edit", "n": 12}, "nothing to edit")
	mustFail(t, f, "issue", map[string]any{"action": "edit", "n": 12, "state": "all"}, "invalid: state")
}

func TestIssueDelete(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{"DELETE /repos/o/r/issues/12": emptyH(204)})
	mustFail(t, f, "issue", map[string]any{"action": "delete", "n": 12}, `destructive: pass confirm=`)
	mustFail(t, f, "issue", map[string]any{"action": "delete", "n": 12, "confirm": "o/r#13"}, `destructive: pass confirm="o/r#12"`)
	f.none("DELETE", "/repos/o/r/issues/12")
	eq(t, "ok", mustRun(t, f, "issue", map[string]any{"action": "delete", "n": 12, "confirm": "o/r#12"}), "ok: deleted #12")
	f.only("DELETE", "/repos/o/r/issues/12")
}

func TestIssueLockUnlock(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{
		"PUT /repos/o/r/issues/12/lock":    emptyH(204),
		"DELETE /repos/o/r/issues/12/lock": emptyH(204),
	})
	eq(t, "lock", mustRun(t, f, "issue", map[string]any{"action": "lock", "n": 12}), "ok: locked #12 (resolved)")
	eq(t, "body", f.only("PUT", "/repos/o/r/issues/12/lock").Body, `{"reason":"resolved"}`)
	f2 := newFake(t, map[string]handlerFunc{"PUT /repos/o/r/issues/12/lock": emptyH(204)})
	mustRun(t, f2, "issue", map[string]any{"action": "lock", "n": 12, "reason": "too heated"})
	eq(t, "reason", f2.only("PUT", "/repos/o/r/issues/12/lock").Body, `{"reason":"too heated"}`)
	eq(t, "unlock", mustRun(t, f, "issue", map[string]any{"action": "unlock", "n": 12}), "ok: unlocked #12")

	conflict := newFake(t, map[string]handlerFunc{
		"PUT /repos/o/r/issues/12/lock":    emptyH(409),
		"DELETE /repos/o/r/issues/12/lock": emptyH(409),
	})
	eq(t, "already locked", mustRun(t, conflict, "issue", map[string]any{"action": "lock", "n": 12}), "ok: #12 already locked")
	eq(t, "already unlocked", mustRun(t, conflict, "issue", map[string]any{"action": "unlock", "n": 12}), "ok: #12 already unlocked")
}

func TestIssuePin(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{
		"POST /repos/o/r/issues/12/pin":   emptyH(204),
		"DELETE /repos/o/r/issues/12/pin": emptyH(204),
	})
	eq(t, "pin", mustRun(t, f, "issue", map[string]any{"action": "pin", "n": 12}), "ok: pinned #12")
	eq(t, "unpin", mustRun(t, f, "issue", map[string]any{"action": "unpin", "n": 12}), "ok: unpinned #12")
}

func TestIssueLinkUsesCanonicalRepoName(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{
		"GET /repos/o/r":                           jsonH(200, `{"id":1,"full_name":"O/R","html_url":"h"}`),
		"POST /repos/o/r/issues/12/dependencies":   jsonH(201, `{}`),
		"POST /repos/o/r/issues/12/blocks":         jsonH(201, `{}`),
		"DELETE /repos/o/r/issues/12/dependencies": jsonH(201, `{}`),
	})
	got := mustRun(t, f, "issue", map[string]any{"action": "link", "n": 12, "relation": "blocked_by", "other": "#9"})
	eq(t, "link", got, "ok: #12 blocked_by #9")
	eq(t, "body", f.only("POST", "/repos/o/r/issues/12/dependencies").Body, `{"index":9,"owner":"O","repo":"R"}`)
	got = mustRun(t, f, "issue", map[string]any{"action": "link", "n": 12, "relation": "blocks", "other": "o/r#14"})
	eq(t, "blocks", got, "ok: #12 blocks #14")
	eq(t, "blocks body", f.only("POST", "/repos/o/r/issues/12/blocks").Body, `{"index":14,"owner":"O","repo":"R"}`)
	got = mustRun(t, f, "issue", map[string]any{"action": "unlink", "n": 12, "relation": "blocked_by", "other": "#9"})
	eq(t, "unlink", got, "ok: #12 no longer blocked_by #9")
	eq(t, "unlink body", f.only("DELETE", "/repos/o/r/issues/12/dependencies").Body, `{"index":9,"owner":"O","repo":"R"}`)
}

func TestIssueLinkCrossRepoAndErrors(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{"POST /repos/o/r/issues/12/dependencies": jsonH(201, `{}`)})
	got := mustRun(t, f, "issue", map[string]any{"action": "link", "n": 12, "relation": "blocked_by", "other": "x/y#3"})
	eq(t, "cross", got, "ok: #12 blocked_by x/y#3")
	eq(t, "body", f.only("POST", "/repos/o/r/issues/12/dependencies").Body, `{"index":3,"owner":"x","repo":"y"}`)
	mustFail(t, f, "issue", map[string]any{"action": "link", "n": 12, "relation": "sideways", "other": "#3"}, "invalid: relation")
	mustFail(t, f, "issue", map[string]any{"action": "link", "n": 12, "relation": "blocks", "other": "3"}, "invalid: other")
	f500 := newFake(t, map[string]handlerFunc{"POST /repos/o/r/issues/12/dependencies": jsonH(500, `{}`)})
	mustFail(t, f500, "issue", map[string]any{"action": "link", "n": 12, "relation": "blocked_by", "other": "#3"}, "circular")
}

func TestIssueLinks(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{
		"GET /repos/o/r/issues/12/dependencies": jsonH(200, `[{"number":9,"title":"Base work","state":"open","repository":{"full_name":"x/y"}}]`),
		"GET /repos/o/r/issues/12/blocks":       jsonH(200, `[{"number":14,"title":"Follow-up","state":"closed","repository":{"full_name":"o/r"}}]`),
	})
	testutil.Golden(t, "issue_links", mustRun(t, f, "issue", map[string]any{"action": "links", "n": 12}))
	none := newFake(t, map[string]handlerFunc{
		"GET /repos/o/r/issues/12/dependencies": jsonH(200, `[]`),
		"GET /repos/o/r/issues/12/blocks":       jsonH(200, `[]`),
	})
	eq(t, "none", mustRun(t, none, "issue", map[string]any{"action": "links", "n": 12}), "#12 has no dependencies")
	off := newFake(t, map[string]handlerFunc{
		"GET /repos/o/r/issues/12/blocks": jsonH(200, `[]`),
	})
	contains(t, mustRun(t, off, "issue", map[string]any{"action": "links", "n": 12}), "blocked_by: unavailable")
}

func TestIssueReact(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{
		"POST /repos/o/r/issues/12/reactions":          jsonH(201, `{}`),
		"DELETE /repos/o/r/issues/12/reactions":        emptyH(200),
		"POST /repos/o/r/issues/comments/55/reactions": jsonH(201, `{}`),
	})
	eq(t, "add", mustRun(t, f, "issue", map[string]any{"action": "react", "n": 12, "content": "+1"}), "ok: reacted +1 on #12")
	eq(t, "add body", f.only("POST", "/repos/o/r/issues/12/reactions").Body, `{"content":"+1"}`)
	eq(t, "remove", mustRun(t, f, "issue", map[string]any{"action": "react", "n": 12, "content": "+1", "remove": true}), "ok: removed reaction +1 from #12")
	eq(t, "remove body", f.only("DELETE", "/repos/o/r/issues/12/reactions").Body, `{"content":"+1"}`)
	eq(t, "comment", mustRun(t, f, "issue", map[string]any{"action": "react", "comment_id": 55, "content": "heart"}), "ok: reacted heart on comment c55")
	mustFail(t, f, "issue", map[string]any{"action": "react", "content": "+1"}, "pass n (issue) or comment_id")
}

func TestIssueSubscribe(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{
		"PUT /repos/o/r/issues/12/subscriptions/me":    emptyH(201),
		"DELETE /repos/o/r/issues/12/subscriptions/me": emptyH(201),
	})
	eq(t, "sub", mustRun(t, f, "issue", map[string]any{"action": "subscribe", "n": 12}), "ok: subscribed to #12 as me")
	eq(t, "unsub", mustRun(t, f, "issue", map[string]any{"action": "unsubscribe", "n": 12}), "ok: unsubscribed from #12 as me")
}

func TestIssueTemplatesAndAssignable(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{
		"GET /repos/o/r/issue_templates": jsonH(200, `[{"name":"Bug report","title":"[BUG] ","labels":["bug"],
		  "body":[{"type":"markdown"},{"id":"what","type":"textarea","validations":{"required":true}},{"id":"ver","type":"input"}]}, null]`),
		"GET /repos/o/r/assignees": jsonH(200, `[{"login":"alice"},{"login":"bob"}]`),
	})
	testutil.Golden(t, "issue_templates", mustRun(t, f, "issue", map[string]any{"action": "templates"}))
	eq(t, "assignable", mustRun(t, f, "issue", map[string]any{"action": "assignable"}), "# assignees 2\nalice,bob")
}

func TestIssueReadOnlyHidesWrites(t *testing.T) {
	f := newFake(t, nil)
	text, isErr := run(t, f, "issue", map[string]any{"action": "create", "title": "x"}, withCfg(func(c *configT) { c.ReadOnly = true }))
	if !isErr || !strings.Contains(text, "create") {
		t.Fatalf("read-only create: %q %v", text, isErr)
	}
	if got := f.all(); len(got) != 0 {
		t.Errorf("read-only call hit the server: %v", got)
	}
}

func TestIssueUnknownParamIsReported(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{"POST /repos/o/r/issues/12/pin": emptyH(204)})
	got := mustRun(t, f, "issue", map[string]any{"action": "pin", "n": 12, "title": "zzz"})
	contains(t, got, "ok: pinned #12", "note: ignored params: title")
}

func TestIssueAPIErrorMapping(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{"GET /repos/o/r/issues/12": jsonH(404, `{"message":"issue does not exist [id: 0, repo_id: 1, index: 12]"}`)})
	text := mustFail(t, f, "issue", map[string]any{"action": "get", "n": 12}, "404")
	if strings.Contains(text, "tok-secret") {
		t.Error("token leaked")
	}
	_ = http.StatusOK
}

func TestIssueListSendsCanonicalNames(t *testing.T) {
	f := newFake(t, labelsRoutes(map[string]handlerFunc{"GET /repos/o/r/issues": listH(0, `[]`)}))
	mustRun(t, f, "issue", map[string]any{"action": "list", "labels": []string{"BUG", " ui "}, "milestone": " V1.2"})
	r := f.only("GET", "/repos/o/r/issues")
	eq(t, "labels", r.Query.Get("labels"), "bug,ui")
	eq(t, "milestones", r.Query.Get("milestones"), "3")
}

func TestIssueEditLabelsUseIDsAndCanonicalNames(t *testing.T) {
	f := newFake(t, labelsRoutes(map[string]handlerFunc{
		"GET /repos/o/r/labels":                           listH(3, `[{"id":1,"name":"bug"},{"id":2,"name":"ui"},{"id":5,"name":"scope/name"}]`),
		"GET /orgs/o/labels":                              listH(1, `[{"id":9,"name":"Org Label"}]`),
		"POST /repos/o/r/issues/12/labels":                jsonH(200, `[]`),
		"DELETE /repos/o/r/issues/12/labels/scope%2Fname": emptyH(204),
		"DELETE /repos/o/r/issues/12/labels/Org%20Label":  emptyH(204),
	}))
	got := mustRun(t, f, "issue", map[string]any{
		"action": "edit", "n": 12, "add_labels": []string{"Bug", " UI"}, "remove_labels": []string{"SCOPE/NAME", "org label"},
	})
	eq(t, "post", f.only("POST", "/repos/o/r/issues/12/labels").Body, `{"labels":[1,2]}`)
	// an empty answer must still produce the "not applied" notes
	contains(t, got, "ok: edited #12: labels+bug,ui,labels-scope/name,labels-Org Label", "note: label bug was not applied", "note: label ui was not applied")
	f.only("DELETE", "/repos/o/r/issues/12/labels/scope%2Fname")
	f.only("DELETE", "/repos/o/r/issues/12/labels/Org%20Label")
}

func TestIssueSearchMilestoneAndBefore(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{"GET /repos/issues/search": listH(0, `[]`)})
	mustFail(t, f, "issue", map[string]any{"action": "search", "milestone": "3"}, "titles only")
	f.none("GET", "/repos/issues/search")
	got := mustRun(t, f, "issue", map[string]any{"action": "search", "milestone": "v1.2", "before": "2026-11-01"})
	eq(t, "milestones", f.only("GET", "/repos/issues/search").Query.Get("milestones"), "v1.2")
	eq(t, "before", f.only("GET", "/repos/issues/search").Query.Get("before"), "2026-11-01T00:00:00Z")
	if strings.Contains(got, "ignored params") {
		t.Errorf("before is a search param: %s", got)
	}
}
