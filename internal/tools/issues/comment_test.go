package issues

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

// commentsJSON returns n comments with ids 1..n.
func commentsJSON(n int) string {
	parts := make([]string, n)
	for i := 1; i <= n; i++ {
		parts[i-1] = fmt.Sprintf(`{"id":%d,"user":{"login":"u%d"},"body":"body %d","created_at":"2026-11-22T10:%02d:00Z","issue_url":"http://h/api/v1/repos/o/r/issues/12"}`, i, i, i, i%60)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func TestCommentListWindows(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{"GET /repos/o/r/issues/12/comments": jsonH(200, commentsJSON(15))})
	got := mustRun(t, f, "comment", map[string]any{"action": "list", "n": 12})
	testutil.Golden(t, "comment_list_newest", got)
	contains(t, got, "# comments 10/15 (newest 10; pass last=N or page=)", "c15 u15", "c6 u6")
	if strings.Contains(got, "c5 ") {
		t.Error("window must hold the newest 10 only")
	}
	if q := f.only("GET", "/repos/o/r/issues/12/comments").Query; q.Has("page") || q.Has("limit") {
		t.Errorf("server does not paginate comments, query %v", q)
	}

	got = mustRun(t, f, "comment", map[string]any{"action": "list", "n": 12, "last": 3})
	contains(t, got, "# comments 3/15 (newest 3;", "c15", "c13")

	got = mustRun(t, f, "comment", map[string]any{"action": "list", "n": 12, "page": 2, "limit": 5})
	testutil.Golden(t, "comment_list_page", got)

	got = mustRun(t, f, "comment", map[string]any{"action": "list", "n": 12, "from_id": 13})
	contains(t, got, "# comments 2/15 after c13", "c14", "c15")

	short := newFake(t, map[string]handlerFunc{"GET /repos/o/r/issues/12/comments": jsonH(200, commentsJSON(2))})
	eq(t, "all shown", strings.SplitN(mustRun(t, short, "comment", map[string]any{"action": "list", "n": 12}), "\n", 2)[0], "# comments 2/2")
}

func TestCommentListSinceAndCaps(t *testing.T) {
	long := strings.Repeat("w ", 400)
	f := newFake(t, map[string]handlerFunc{"GET /repos/o/r/issues/12/comments": jsonH(200, `[{"id":1,"user":{"login":"a"},"body":"`+long+`","created_at":"2026-11-22T10:00:00Z"}]`)})
	got := mustRun(t, f, "comment", map[string]any{"action": "list", "n": 12, "since": "2026-11-01T00:00:00Z"})
	eq(t, "since", f.only("GET", "/repos/o/r/issues/12/comments").Query.Get("since"), "2026-11-01T00:00:00Z")
	if n := len([]rune(strings.Split(got, "\n")[1])); n > 340 {
		t.Errorf("comment line too long: %d runes", n)
	}
	mustFail(t, f, "comment", map[string]any{"action": "list", "n": 12, "since": "yesterday"}, "invalid: since")
}

func TestCommentAddEdit(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{
		"POST /repos/o/r/issues/12/comments": jsonH(201, `{"id":1234}`),
		"PATCH /repos/o/r/issues/comments/9": jsonH(200, `{"id":9}`),
		"PATCH /repos/o/r/issues/comments/8": emptyH(204),
	})
	eq(t, "add", mustRun(t, f, "comment", map[string]any{"action": "add", "n": 12, "body": "hi"}), "ok: comment c1234 on #12")
	eq(t, "add body", f.only("POST", "/repos/o/r/issues/12/comments").Body, `{"body":"hi"}`)
	eq(t, "edit", mustRun(t, f, "comment", map[string]any{"action": "edit", "id": 9, "body": "new"}), "ok: edited comment c9")
	eq(t, "edit body", f.only("PATCH", "/repos/o/r/issues/comments/9").Body, `{"body":"new"}`)
	mustFail(t, f, "comment", map[string]any{"action": "edit", "id": 8, "body": "new"}, "not an editable comment")
	mustFail(t, f, "comment", map[string]any{"action": "add", "n": 12}, "missing param body")
}

func TestCommentDeleteFlows(t *testing.T) {
	// plain comment: GET 200, DELETE 204, GET 404 afterwards.
	var deleted bool
	f := newFake(t, map[string]handlerFunc{
		"GET /repos/o/r/issues/comments/9": func(w http.ResponseWriter, r *http.Request, b string) {
			if deleted {
				jsonH(404, `{"message":"gone"}`)(w, r, b)
				return
			}
			jsonH(200, `{"id":9}`)(w, r, b)
		},
		"DELETE /repos/o/r/issues/comments/9": func(w http.ResponseWriter, _ *http.Request, _ string) {
			deleted = true
			w.WriteHeader(204)
		},
	})
	eq(t, "ok", mustRun(t, f, "comment", map[string]any{"action": "delete", "id": 9}), "ok: deleted comment c9")

	// review comment: GET answers 204, no DELETE is sent.
	review := newFake(t, map[string]handlerFunc{
		"GET /repos/o/r/issues/comments/7":    emptyH(204),
		"DELETE /repos/o/r/issues/comments/7": emptyH(204),
	})
	mustFail(t, review, "comment", map[string]any{"action": "delete", "id": 7}, "not a plain comment; use review.comment_delete")
	review.none("DELETE", "/repos/o/r/issues/comments/7")

	// server answers 204 but the comment survives.
	stays := newFake(t, map[string]handlerFunc{
		"GET /repos/o/r/issues/comments/6":    jsonH(200, `{"id":6}`),
		"DELETE /repos/o/r/issues/comments/6": emptyH(204),
	})
	mustFail(t, stays, "comment", map[string]any{"action": "delete", "id": 6}, "not a plain comment")

	missing := newFake(t, nil)
	eq(t, "404", mustFail(t, missing, "comment", map[string]any{"action": "delete", "id": 5}, "404 comment not found"), "error: 404 comment not found")
}

func TestCommentTimelineUnknownTotal(t *testing.T) {
	events := `[
	 {"id":1,"type":"comment","user":{"login":"alice"},"created_at":"2026-11-20T09:00:00Z","body":"hello\nthere"},
	 null,
	 {"id":2,"type":"code","user":{"login":"x"},"created_at":"2026-11-20T09:00:00Z"},
	 {"id":3,"type":"label","user":{"login":"alice"},"created_at":"2026-11-20T09:01:00Z","body":"1","label":{"name":"bug"}},
	 {"id":4,"type":"label","user":{"login":"alice"},"created_at":"2026-11-20T09:02:00Z","body":"","label":{"name":"ui"}},
	 {"id":5,"type":"assignees","user":{"login":"alice"},"created_at":"2026-11-20T09:03:00Z","assignee":{"login":"bob"}},
	 {"id":6,"type":"assignees","user":{"login":"alice"},"created_at":"2026-11-20T09:04:00Z","assignee":{"login":"bob"},"removed_assignee":true},
	 {"id":7,"type":"change_title","user":{"login":"alice"},"created_at":"2026-11-20T09:05:00Z","old_title":"a","new_title":"b"},
	 {"id":8,"type":"milestone","user":{"login":"alice"},"created_at":"2026-11-20T09:06:00Z","milestone":{"title":"v1"}},
	 {"id":9,"type":"issue_ref","user":{"login":"alice"},"created_at":"2026-11-20T09:07:00Z","ref_issue":{"number":9,"repository":{"full_name":"o/r"}}},
	 {"id":10,"type":"review","user":{"login":"alice"},"created_at":"2026-11-20T09:08:00Z","review_id":77},
	 {"id":11,"type":"merge_pull","user":{"login":"alice"},"created_at":"2026-11-20T09:09:00Z"},
	 {"id":12,"type":"close","user":{"login":"alice"},"created_at":"2026-11-20T09:10:00Z"},
	 {"id":13,"type":"pull_push","user":{"login":"alice"},"created_at":"2026-11-20T09:11:00Z"},
	 {"id":14,"type":"add_dependency","user":{"login":"alice"},"created_at":"2026-11-20T09:12:00Z","dependent_issue":{"number":3,"repository":{"full_name":"o/r"}}}
	]`
	// X-Total-Count here is the length of the filtered page and must be ignored.
	f := newFake(t, map[string]handlerFunc{"GET /repos/o/r/issues/12/timeline": listH(12, events)})
	got := mustRun(t, f, "comment", map[string]any{"action": "timeline", "n": 12, "limit": 15})
	testutil.Golden(t, "comment_timeline", got)
	contains(t, got, "# timeline 13 page 1 next: page=2 (2 hidden)")

	filtered := mustRun(t, f, "comment", map[string]any{"action": "timeline", "n": 12, "limit": 15, "types": []string{"close", "merge_pull"}})
	contains(t, filtered, "# timeline 2 page 1 next: page=2 (2 hidden)", "e11", "e12")

	short := newFake(t, map[string]handlerFunc{"GET /repos/o/r/issues/12/timeline": listH(1, `[{"id":1,"type":"close","user":{"login":"a"}}]`)})
	contains(t, mustRun(t, short, "comment", map[string]any{"action": "timeline", "n": 12}), "# timeline 1 page 1\n")
}

func TestCommentTimelineSkipsFilteredEmptyPages(t *testing.T) {
	calls := 0
	f := newFake(t, map[string]handlerFunc{"GET /repos/o/r/issues/12/timeline": func(w http.ResponseWriter, r *http.Request, b string) {
		calls++
		switch r.URL.Query().Get("page") {
		case "1":
			jsonH(200, `[{"id":1,"type":"comment","body":"x"},{"id":2,"type":"comment","body":"y"}]`)(w, r, b)
		default:
			jsonH(200, `[{"id":3,"type":"close","user":{"login":"a"}}]`)(w, r, b)
		}
	}})
	got := mustRun(t, f, "comment", map[string]any{"action": "timeline", "n": 12, "limit": 2, "types": []string{"close"}})
	contains(t, got, "# timeline 1 page 2", "e3")
	if calls != 2 {
		t.Errorf("want 2 timeline requests, got %d", calls)
	}
}

func TestCommentFeed(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{"GET /repos/o/r/issues/comments": listH(40, `[
	  {"id":9,"user":{"login":"alice"},"body":"nice\nwork","created_at":"2026-11-22T10:15:00Z","issue_url":"http://h/api/v1/repos/o/r/issues/12"},
	  {"id":8,"user":{"login":"bob"},"body":"ok","created_at":"2026-11-22T10:10:00Z","issue_url":"http://h/api/v1/repos/o/r/issues/7"}]`)})
	got := mustRun(t, f, "comment", map[string]any{"action": "feed", "since": "2026-11-22T00:00:00Z", "limit": 2})
	testutil.Golden(t, "comment_feed", got)
	q := f.only("GET", "/repos/o/r/issues/comments").Query
	eq(t, "since", q.Get("since"), "2026-11-22T00:00:00Z")
	eq(t, "limit", q.Get("limit"), "2")
}

func TestCommentDeleteUnverifiable(t *testing.T) {
	calls := 0
	f := newFake(t, map[string]handlerFunc{
		"GET /repos/o/r/issues/comments/4": func(w http.ResponseWriter, r *http.Request, b string) {
			calls++
			if calls == 1 {
				jsonH(200, `{"id":4}`)(w, r, b)
				return
			}
			jsonH(500, `{"message":"boom"}`)(w, r, b)
		},
		"DELETE /repos/o/r/issues/comments/4": emptyH(204),
	})
	text := mustFail(t, f, "comment", map[string]any{"action": "delete", "id": 4}, "500 internal server error")
	if strings.Contains(text, "ok:") {
		t.Errorf("must not report success: %s", text)
	}
}
