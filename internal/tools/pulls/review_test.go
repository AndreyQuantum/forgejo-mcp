package pulls

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

const reviews = "/repos/o/r/pulls/42/reviews"

func reviewRoutes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET " + reviews: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("page") != "1" {
				fmt.Fprint(w, `[]`)
				return
			}
			fmt.Fprint(w, `[
			{"id":77,"user":{"login":"alice"},"state":"APPROVED","official":true,"commit_id":"8a1b2c3d8a1b2c3d","comments_count":2,"submitted_at":"2026-11-22T10:15:00Z","body":"Looks good"},
			{"id":78,"user":{"login":"bob"},"state":"REQUEST_CHANGES","stale":true,"dismissed":true,"commit_id":"1111111111","submitted_at":"2026-11-22T10:20:00Z","body":"fix the\nrace"},
			{"id":79,"user":{"login":"carol"},"state":"REQUEST_REVIEW"},
			{"id":80,"team":{"name":"devs"},"state":"REQUEST_REVIEW"},
			{"id":81,"user":{"login":"eve"},"state":"PENDING","body":"draft of eve"},
			{"id":82,"user":{"login":"me"},"state":"PENDING","body":"my draft"}]`)
		},
		"GET " + reviews + "/77/comments": jsonBody(`[
			{"id":9,"path":"src/a.go","position":42,"body":"nit","user":{"login":"alice"},"resolver":{"login":"bob"},"diff_hunk":"@@ -1 +1 @@\n-a\n+b\n c\n d"},
			{"id":10,"path":"src/a.go","position":0,"original_position":17,"body":"why","user":{"login":"alice"}}]`),
	}
}

func TestReviewList(t *testing.T) {
	env, f := newFake(t, reviewRoutes())
	out := mustOK(t, env, "review", map[string]any{"action": "list", "n": 42})
	testutil.Golden(t, "review_list", out)
	notContains(t, out, "eve", "REQUEST_REVIEW")
	if f.count("GET", reviews+"/77/comments") != 1 || f.count("GET", reviews+"/78/comments") != 0 {
		t.Errorf("comments are fetched only for reviews with comments_count>0:\n%s", f.dump())
	}
}

func TestReviewListFilters(t *testing.T) {
	env, f := newFake(t, reviewRoutes())
	out := mustOK(t, env, "review", map[string]any{"action": "list", "n": 42, "only_unresolved": true, "verbose": true})
	notContains(t, out, "nit", "resolved by")
	contains(t, out, "rc10 src/a.go:-17 alice | why")
	out = mustOK(t, env, "review", map[string]any{"action": "list", "n": 42, "with_comments": false})
	notContains(t, out, "rc9")
	_ = f
	out = mustOK(t, env, "review", map[string]any{"action": "list", "n": 42, "limit": 1, "page": 2})
	contains(t, out, "# reviews 1/3 page 2/3 next: page=3", "r78 bob request_changes")
}

func TestReviewListVerboseHunk(t *testing.T) {
	env, _ := newFake(t, reviewRoutes())
	out := mustOK(t, env, "review", map[string]any{"action": "list", "n": 42, "verbose": true})
	contains(t, out, "    +b\n     c\n     d")
}

func TestReviewCreate(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{
		pr42:              jsonBody(openPR42()), // authored by alice, we are "me"
		"POST " + reviews: jsonBody(`{"id":77,"state":"APPROVED"}`),
	})
	out := mustOK(t, env, "review", map[string]any{
		"action": "create", "n": 42, "event": "approve", "body": "lgtm", "commit_id": "abc",
		"comments": []map[string]any{
			{"path": "a.go", "line": 7, "body": "new side"},
			{"path": "b.go", "line": 3, "side": "old", "body": "old side", "extra_lines": 2},
		},
	})
	if out != "ok: review r77 APPROVED" {
		t.Errorf("got %q", out)
	}
	body := jsonOf(t, f.last(t, "POST", reviews))
	if body["event"] != "APPROVED" || body["body"] != "lgtm" || body["commit_id"] != "abc" {
		t.Errorf("body %v", body)
	}
	cs := body["comments"].([]any)
	c0, c1 := cs[0].(map[string]any), cs[1].(map[string]any)
	if c0["new_position"] != float64(7) || c0["old_position"] != nil || c1["old_position"] != float64(3) || c1["new_position"] != nil || c1["extra_lines_count"] != float64(2) {
		t.Errorf("comments %v", cs)
	}
}

func TestReviewCreateEvents(t *testing.T) {
	for event, want := range map[string]string{"request_changes": "REQUEST_CHANGES", "comment": "COMMENT", "pending": "PENDING"} {
		env, f := newFake(t, map[string]http.HandlerFunc{
			pr42:              jsonBody(openPR42()),
			"POST " + reviews: jsonBody(`{"id":5,"state":"` + want + `"}`),
		})
		out := mustOK(t, env, "review", map[string]any{"action": "create", "n": 42, "event": event, "body": "text"})
		contains(t, out, "ok: review r5 "+want)
		if got := jsonOf(t, f.last(t, "POST", reviews))["event"]; got != want {
			t.Errorf("event %s -> %v", event, got)
		}
	}
	env, _ := newFake(t, map[string]http.HandlerFunc{"POST " + reviews: jsonBody(`{"id":5,"state":"PENDING"}`)})
	out := mustOK(t, env, "review", map[string]any{"action": "create", "n": 42, "event": "pending"})
	contains(t, out, "ok: review r5 PENDING", "review submit n=42 review_id=5")
}

func TestReviewCreatePrechecks(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{
		pr42: jsonBody(mkPR(42, func(m map[string]any) { m["user"] = map[string]any{"login": "ME"} })), // own PR, case-insensitive
	})
	out := mustErr(t, env, "review", map[string]any{"action": "create", "n": 42, "event": "approve"})
	contains(t, out, "you are the author of pr #42")
	out = mustErr(t, env, "review", map[string]any{"action": "create", "n": 42, "event": "request_changes", "body": "x"})
	contains(t, out, "you are the author of pr #42")
	contains(t, mustErr(t, env, "review", map[string]any{"action": "create", "n": 42, "event": "request_changes"}), "request_changes needs body")
	contains(t, mustErr(t, env, "review", map[string]any{"action": "create", "n": 42, "event": "comment"}), "comment needs body or inline comments")
	contains(t, mustErr(t, env, "review", map[string]any{"action": "create", "n": 42, "event": "nope"}), "invalid: event")
	contains(t, mustErr(t, env, "review", map[string]any{"action": "create", "n": 42, "event": "comment", "comments": []map[string]any{{"path": "a", "body": "b"}}}), "comments[0] needs path, line >= 1 and body")
	contains(t, mustErr(t, env, "review", map[string]any{"action": "create", "n": 42, "event": "comment", "comments": []map[string]any{{"path": "a", "body": "b", "line": 1, "side": "left"}}}), "comments[0].side")
	if f.count("POST", reviews) != 0 {
		t.Errorf("a failed pre-check must not POST:\n%s", f.dump())
	}
	// a comment review on your own PR is fine
	env, f = newFake(t, map[string]http.HandlerFunc{
		pr42:              jsonBody(mkPR(42, func(m map[string]any) { m["user"] = map[string]any{"login": "me"} })),
		"POST " + reviews: jsonBody(`{"id":6,"state":"COMMENT"}`),
	})
	mustOK(t, env, "review", map[string]any{"action": "create", "n": 42, "event": "comment", "body": "note"})
	if f.count("POST", reviews) != 1 {
		t.Error("comment review must be sent")
	}
}

func TestReviewCreateServer422(t *testing.T) {
	env, _ := newFake(t, map[string]http.HandlerFunc{
		pr42:              jsonBody(openPR42()),
		"POST " + reviews: status(422, `{"message":"approve your own pull is not allowed"}`),
	})
	out := mustErr(t, env, "review", map[string]any{"action": "create", "n": 42, "event": "approve"})
	contains(t, out, "error: 422 invalid: approve your own pull is not allowed")
}

func TestReviewSubmit(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{"POST " + reviews + "/77": jsonBody(`{"id":77,"state":"REQUEST_CHANGES"}`)})
	out := mustOK(t, env, "review", map[string]any{"action": "submit", "n": 42, "review_id": 77, "event": "request_changes", "body": "no"})
	if out != "ok: review r77 REQUEST_CHANGES" {
		t.Errorf("got %q", out)
	}
	body := jsonOf(t, f.last(t, "POST", reviews+"/77"))
	if body["event"] != "REQUEST_CHANGES" || body["body"] != "no" {
		t.Errorf("body %v", body)
	}
	contains(t, mustErr(t, env, "review", map[string]any{"action": "submit", "n": 42, "review_id": 77, "event": "pending"}), "invalid: event")
	contains(t, mustErr(t, env, "review", map[string]any{"action": "submit", "n": 42, "event": "comment"}), "missing param review_id")
}

func TestReviewDismissUndismissDelete(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{
		"POST " + reviews + "/77/dismissals":   jsonBody(`{"id":77}`),
		"POST " + reviews + "/77/undismissals": jsonBody(`{"id":77}`),
		"DELETE " + reviews + "/77":            status(204, ``),
		"POST " + reviews + "/78/dismissals":   status(403, `{"message":"Must be repo admin"}`),
	})
	out := mustOK(t, env, "review", map[string]any{"action": "dismiss", "n": 42, "review_id": 77, "message": "outdated"})
	if out != "ok: dismissed review r77" {
		t.Errorf("got %q", out)
	}
	if got := jsonOf(t, f.last(t, "POST", reviews+"/77/dismissals"))["message"]; got != "outdated" {
		t.Errorf("message %v", got)
	}
	if out = mustOK(t, env, "review", map[string]any{"action": "undismiss", "n": 42, "review_id": 77}); out != "ok: restored review r77" {
		t.Errorf("got %q", out)
	}
	if out = mustOK(t, env, "review", map[string]any{"action": "delete", "n": 42, "review_id": 77}); out != "ok: deleted review r77" {
		t.Errorf("got %q", out)
	}
	out = mustErr(t, env, "review", map[string]any{"action": "dismiss", "n": 42, "review_id": 78})
	contains(t, out, "error: 403", "Must be repo admin")
}

func TestReviewRequestUnrequest(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{
		"POST /repos/o/r/pulls/42/requested_reviewers":   status(201, `[]`),
		"DELETE /repos/o/r/pulls/42/requested_reviewers": status(204, ``),
	})
	out := mustOK(t, env, "review", map[string]any{"action": "request", "n": 42, "reviewers": []string{"alice"}, "team_reviewers": []string{"devs"}})
	if out != "ok: requested alice,team:devs" {
		t.Errorf("got %q", out)
	}
	body := jsonOf(t, f.last(t, "POST", "/repos/o/r/pulls/42/requested_reviewers"))
	if fmt.Sprint(body["reviewers"]) != "[alice]" || fmt.Sprint(body["team_reviewers"]) != "[devs]" {
		t.Errorf("body %v", body)
	}
	out = mustOK(t, env, "review", map[string]any{"action": "unrequest", "n": 42, "reviewers": []string{"alice"}})
	if out != "ok: unrequested alice" {
		t.Errorf("got %q", out)
	}
	if b := f.last(t, "DELETE", "/repos/o/r/pulls/42/requested_reviewers").Body; !strings.Contains(b, `"reviewers":["alice"]`) {
		t.Errorf("DELETE body %q", b)
	}
	contains(t, mustErr(t, env, "review", map[string]any{"action": "request", "n": 42}), "pass reviewers or team_reviewers")
}

func TestReviewRequest422(t *testing.T) {
	env, _ := newFake(t, map[string]http.HandlerFunc{
		"POST /repos/o/r/pulls/42/requested_reviewers": status(422, `{"message":"review request is not valid"}`),
	})
	out := mustErr(t, env, "review", map[string]any{"action": "request", "n": 42, "reviewers": []string{"alice"}})
	contains(t, out, "error: 422 invalid: review request is not valid")
}

func TestReviewCommentDelete(t *testing.T) {
	env, f := newFake(t, map[string]http.HandlerFunc{"DELETE " + reviews + "/77/comments/9": status(204, ``)})
	out := mustOK(t, env, "review", map[string]any{"action": "comment_delete", "n": 42, "review_id": 77, "id": 9})
	if out != "ok: deleted review comment rc9" {
		t.Errorf("got %q", out)
	}
	_ = f.last(t, "DELETE", reviews+"/77/comments/9")
}
