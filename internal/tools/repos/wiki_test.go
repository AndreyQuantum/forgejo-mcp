package repos

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestWikiListGetHistory(t *testing.T) {
	f := newFake(t).
		on("GET /repos/o/r/wiki/pages", withTotal(`[{"title":"Home","sub_url":"Home","last_commit":{"commiter":{"name":"Alice","date":"2026-11-22T10:15:00Z"}}},{"title":"My Page","sub_url":"My%20Page"}]`, "2")).
		on("GET /repos/o/r/wiki/page/Home", jsonOK(`{"title":"Home","sub_url":"Home","commit_count":4,"content_base64":"`+b64("# Home\nline two\n")+`","last_commit":{"commiter":{"date":"2026-11-22T10:15:00Z"}}}`)).
		on("GET /repos/o/r/wiki/page/My%20Page", jsonOK(`{"title":"My Page","sub_url":"My%20Page","content_base64":"`+b64("x")+`"}`)).
		on("GET /repos/o/r/wiki/revisions/Home", withTotal(`{"count":2,"commits":[{"sha":"8a1b2c3d4e5f","message":"Update Home\n\nbody","author":{"name":"Alice","date":"2026-11-22T10:15:00Z"}},{"sha":"1111111122","message":"Add Home","author":{"name":"Bob","date":"2026-11-20T08:01:00Z"}}]}`, "2"))
	testutil.Golden(t, "wiki_list", mustOK(t, f, "wiki", map[string]any{"action": "list"}))
	testutil.Golden(t, "wiki_get", mustOK(t, f, "wiki", map[string]any{"action": "get", "page_name": "Home"}))
	// the already-escaped sub_url is not escaped twice
	mustOK(t, f, "wiki", map[string]any{"action": "get", "page_name": "My%20Page"})
	mustOK(t, f, "wiki", map[string]any{"action": "get", "page_name": "My Page"})
	if f.count("GET", "/repos/o/r/wiki/page/My%20Page") != 2 {
		t.Fatalf("requests: %v", f.requests())
	}
	testutil.Golden(t, "wiki_history", mustOK(t, f, "wiki", map[string]any{"action": "history", "page_name": "Home"}))
}

func TestWikiGetTruncates(t *testing.T) {
	long := strings.Repeat("0123456789\n", 1000) // 11000 chars
	f := newFake(t).on("GET /repos/o/r/wiki/page/Big", jsonOK(`{"title":"Big","sub_url":"Big","content_base64":"`+b64(long)+`"}`))
	out := mustOK(t, f, "wiki", map[string]any{"action": "get", "page_name": "Big"})
	if !strings.HasSuffix(out, "[truncated: 6000 of 11000 chars; pass body_max=0 for up to 20000 chars]\n") {
		t.Fatalf("tail: %q", out[len(out)-120:])
	}
	out = mustOK(t, f, "wiki", map[string]any{"action": "get", "page_name": "Big", "body_max": 0})
	if strings.Contains(out, "[truncated") {
		t.Fatal("body_max=0 should lift the default cap")
	}
	out = mustOK(t, f, "wiki", map[string]any{"action": "get", "page_name": "Big", "body_max": 300})
	if !strings.Contains(out, "[truncated: 300 of 11000 chars") {
		t.Fatal("body_max=300 not honoured")
	}
}

func TestWikiWrites(t *testing.T) {
	f := newFake(t).
		on("POST /repos/o/r/wiki/new", reply{status: 201, body: `{"title":"Docs","sub_url":"Docs"}`}).
		on("PATCH /repos/o/r/wiki/page/Docs", jsonOK(`{"title":"Guide","sub_url":"Guide"}`)).
		on("GET /repos/o/r/wiki/page/Docs", jsonOK(`{"title":"Docs","sub_url":"Docs","content_base64":"`+b64("old text")+`"}`)).
		on("DELETE /repos/o/r/wiki/page/Docs", reply{status: 204})
	out := mustOK(t, f, "wiki", map[string]any{"action": "create", "title": "Docs", "content": "héllo", "message": "m"})
	if out != "ok: created wiki page Docs (sub_url Docs)\n" {
		t.Fatal(out)
	}
	b := f.bodyMap(f.last())
	if b["title"] != "Docs" || b["content_base64"] != b64("héllo") || b["message"] != "m" {
		t.Fatalf("body %v", b)
	}

	// edit with content
	mustOK(t, f, "wiki", map[string]any{"action": "edit", "page_name": "Docs", "content": "new text"})
	if f.bodyMap(f.last())["content_base64"] != b64("new text") {
		t.Fatalf("body %v", f.last().Body)
	}
	// the server turns an empty title into "unnamed": the current title is always sent
	if b := f.bodyMap(f.last()); b["title"] != "Docs" {
		t.Fatalf("title not preserved: %v", b)
	}
	// rename only: keeps the current content
	out = mustOK(t, f, "wiki", map[string]any{"action": "edit", "page_name": "Docs", "title": "Guide"})
	if out != "ok: edited wiki page Guide (sub_url Guide)\n" {
		t.Fatal(out)
	}
	b = f.bodyMap(f.last())
	if b["title"] != "Guide" || b["content_base64"] != b64("old text") {
		t.Fatalf("body %v", b)
	}
	if out := mustErr(t, f, "wiki", map[string]any{"action": "edit", "page_name": "Docs"}); !strings.Contains(out, "pass content and/or title") {
		t.Fatal(out)
	}
	if out := mustOK(t, f, "wiki", map[string]any{"action": "delete", "page_name": "Docs"}); out != "ok: deleted wiki page Docs\n" {
		t.Fatal(out)
	}
}
