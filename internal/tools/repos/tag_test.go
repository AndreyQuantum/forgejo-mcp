package repos

import (
	"strings"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

func TestTagListAndGet(t *testing.T) {
	annotated := `{"name":"v1.1.0","message":"Release 1.1\n\nnotes","id":"aaaa1111bbbb2222cccc3333dddd4444eeee5555","commit":{"sha":"1234567890abcdef1234567890abcdef12345678"}}`
	light := `{"name":"v1.0.0","message":"","id":"1111222233334444555566667777888899990000","commit":{"sha":"1111222233334444555566667777888899990000"}}`
	f := newFake(t).
		on("GET /repos/o/r/tags", withTotal(fixture(t, "tags.json"), "2")).
		on("GET /repos/o/r/tags/v1.1.0", jsonOK(annotated)).
		on("GET /repos/o/r/tags/v1.0.0", jsonOK(light)).
		on("GET /repos/o/r/tags/rel/x", jsonOK(light)).
		on("GET /repos/o/r/git/tags/aaaa1111bbbb2222cccc3333dddd4444eeee5555", jsonOK(`{"tag":"v1.1.0","sha":"aaaa1111bbbb2222cccc3333dddd4444eeee5555","message":"Release 1.1\n\nnotes\n","tagger":{"name":"Alice","date":"2026-11-22T10:15:00Z"},"object":{"type":"commit","sha":"1234567890abcdef1234567890abcdef12345678"}}`))
	testutil.Golden(t, "tag_list", mustOK(t, f, "tag", map[string]any{"action": "list"}))

	testutil.Golden(t, "tag_get_annotated", mustOK(t, f, "tag", map[string]any{"action": "get", "name": "v1.1.0"}))
	if f.count("GET", "/repos/o/r/git/tags/aaaa1111bbbb2222cccc3333dddd4444eeee5555") != 0 {
		t.Fatal("git/tags requested without verbose")
	}
	testutil.Golden(t, "tag_get_annotated_verbose", mustOK(t, f, "tag", map[string]any{"action": "get", "name": "v1.1.0", "verbose": true}))

	// a lightweight tag never triggers the git/tags call (it answers 400)
	n := len(f.requests())
	out := mustOK(t, f, "tag", map[string]any{"action": "get", "name": "v1.0.0", "verbose": true})
	testutil.Golden(t, "tag_get_lightweight", out)
	if len(f.requests()) != n+1 {
		t.Fatalf("lightweight get made %d requests", len(f.requests())-n)
	}
	// wildcard route keeps the slash
	mustOK(t, f, "tag", map[string]any{"action": "get", "name": "rel/x"})
}

func TestTagCreateDelete(t *testing.T) {
	f := newFake(t).
		on("POST /repos/o/r/tags", reply{status: 201, body: `{"name":"v2","commit":{"sha":"abcdef0123456789"}}`}).
		on("DELETE /repos/o/r/tags/v2", reply{status: 204})
	out := mustOK(t, f, "tag", map[string]any{"action": "create", "name": "v2", "target": "main", "message": "two"})
	if out != "ok: created tag v2 at abcdef01 (annotated)\n" {
		t.Fatal(out)
	}
	if b := f.bodyMap(f.last()); b["tag_name"] != "v2" || b["target"] != "main" || b["message"] != "two" {
		t.Fatalf("body %v", b)
	}
	out = mustOK(t, f, "tag", map[string]any{"action": "create", "name": "v2"})
	if out != "ok: created tag v2 at abcdef01 (lightweight)\n" || len(f.bodyMap(f.last())) != 1 {
		t.Fatal(out)
	}
	if out := mustOK(t, f, "tag", map[string]any{"action": "delete", "name": "v2"}); out != "ok: deleted tag v2\n" {
		t.Fatal(out)
	}
	f.on("DELETE /repos/o/r/tags/v3", reply{status: 409, body: `{"message":"a tag attached to a release cannot be deleted directly"}`})
	out = mustErr(t, f, "tag", map[string]any{"action": "delete", "name": "v3"})
	if !strings.Contains(out, "hint: a tag attached to a release cannot be deleted directly; delete the release first with release.delete") {
		t.Fatal(out)
	}
}

func TestTagProtection(t *testing.T) {
	rules := `[{"id":3,"name_pattern":"v*","whitelist_usernames":["alice"],"whitelist_teams":["devs"]},{"id":4,"name_pattern":"/^rel/","whitelist_usernames":["bob"]}]`
	f := newFake(t).
		on("GET /repos/o/r/tag_protections", jsonOK(rules)).
		on("GET /repos/o/r/tag_protections/3", jsonOK(`{"id":3,"name_pattern":"v*","whitelist_usernames":["alice"],"whitelist_teams":["devs"]}`)).
		on("POST /repos/o/r/tag_protections", reply{status: 201, body: `{"id":9,"name_pattern":"v*"}`}).
		on("PATCH /repos/o/r/tag_protections/3", jsonOK(`{}`)).
		on("DELETE /repos/o/r/tag_protections/3", reply{status: 204})
	testutil.Golden(t, "tag_protect_list", mustOK(t, f, "tag", map[string]any{"action": "protect_list"}))
	if out := mustOK(t, f, "tag", map[string]any{"action": "protect_get", "rule_id": 3}); out != "tag protection id 3 | v* | users:alice teams:devs\n" {
		t.Fatal(out)
	}
	out := mustOK(t, f, "tag", map[string]any{"action": "protect_set", "set": map[string]any{"name_pattern": "v*", "whitelist_usernames": []string{"alice"}}})
	if out != "ok: tag protection created: id 9 v*\n" || f.last().Method != "POST" {
		t.Fatal(out)
	}
	out = mustOK(t, f, "tag", map[string]any{"action": "protect_set", "rule_id": 3, "set": map[string]any{"whitelist_teams": []string{"devs"}}})
	if out != "ok: tag protection 3 updated: whitelist_teams\n" || f.last().Method != "PATCH" || f.last().Body != `{"whitelist_teams":["devs"]}` {
		t.Fatalf("%q %+v", out, f.last())
	}
	if out := mustOK(t, f, "tag", map[string]any{"action": "protect_delete", "rule_id": 3}); out != "ok: deleted tag protection 3\n" {
		t.Fatal(out)
	}
	// local validation, same text as the server
	n := len(f.requests())
	if out := mustErr(t, f, "tag", map[string]any{"action": "protect_set", "set": map[string]any{"name_pattern": "v*"}}); !strings.Contains(out, "both whitelist_usernames and whitelist_teams are empty") {
		t.Fatal(out)
	}
	if out := mustErr(t, f, "tag", map[string]any{"action": "protect_set", "set": map[string]any{"whitelist_usernames": []string{"a"}}}); !strings.Contains(out, "name_pattern is required") {
		t.Fatal(out)
	}
	if out := mustErr(t, f, "tag", map[string]any{"action": "protect_set", "rule_id": 3, "set": map[string]any{"whitelist_usernames": []string{}, "whitelist_teams": []string{}}}); !strings.Contains(out, "are empty") {
		t.Fatal(out)
	}
	if len(f.requests()) != n {
		t.Fatal("invalid calls reached the server")
	}
}
