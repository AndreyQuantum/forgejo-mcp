package repos

import (
	"net/http"
	"strings"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

func TestBranchListAndGet(t *testing.T) {
	f := newFake(t).
		on("GET /repos/o/r/branches", withTotal(fixture(t, "branches.json"), "2")).
		on("GET /repos/o/r/branches/feat/search", jsonOK(`{"name":"feat/search","commit":{"id":"0123456789abcdef0123456789abcdef01234567","message":"Add search","timestamp":"2026-11-22T10:15:00Z"},"protected":true,"user_can_push":true,"user_can_merge":false,"required_approvals":2,"enable_status_check":true,"status_check_contexts":["ci/build"]}`))
	testutil.Golden(t, "branch_list", mustOK(t, f, "branch", map[string]any{"action": "list"}))
	if q := f.last().Query; q.Get("limit") != "20" || q.Get("page") != "1" {
		t.Fatalf("query %v", q)
	}
	testutil.Golden(t, "branch_get", mustOK(t, f, "branch", map[string]any{"action": "get", "name": "feat/search"}))
}

func TestBranchListFilterClientSide(t *testing.T) {
	f := newFake(t).onFunc("GET /repos/o/r/branches", func(r *http.Request) reply {
		if r.URL.Query().Get("page") == "1" {
			return reply{status: 200, body: fixture(t, "branches.json"), header: map[string]string{"X-Total-Count": "3"}}
		}
		return reply{status: 200, body: `[{"name":"feature/two","commit":{"id":"aaaaaaaa11","message":"m"}}]`, header: map[string]string{"X-Total-Count": "3"}}
	})
	out := mustOK(t, f, "branch", map[string]any{"action": "list", "q": "FEAT"})
	if !strings.HasPrefix(out, "# branches 1/1 page 1/1 (filtered client-side, scanned 2)") || !strings.Contains(out, "feat/search") || strings.Contains(out, "\nmain") {
		t.Fatalf("out:\n%s", out)
	}
	// the scan stops after branchScanPages pages
	f = newFake(t).on("GET /repos/o/r/branches", withTotal(fixture(t, "branches.json"), "100000"))
	out = mustOK(t, f, "branch", map[string]any{"action": "list", "q": "nomatch"})
	if !strings.Contains(out, "more branches not scanned") || len(f.requests()) != branchScanPages {
		t.Fatalf("out:\n%s\nrequests %d", out, len(f.requests()))
	}
}

func TestBranchMutations(t *testing.T) {
	f := newFake(t).
		on("POST /repos/o/r/branches", reply{status: 201, body: `{"name":"topic","commit":{"id":"8a1b2c3d4e5f6071"}}`}).
		on("PATCH /repos/o/r/branches/old/name", reply{status: 204}).
		on("DELETE /repos/o/r/branches/gone", reply{status: 204})
	out := mustOK(t, f, "branch", map[string]any{"action": "create", "name": "topic", "from": "v1.0"})
	if out != "ok: created topic from v1.0@8a1b2c3d\n" {
		t.Fatal(out)
	}
	if b := f.bodyMap(f.last()); b["new_branch_name"] != "topic" || b["old_ref_name"] != "v1.0" || len(b) != 2 {
		t.Fatalf("body %v", b)
	}
	out = mustOK(t, f, "branch", map[string]any{"action": "create", "name": "topic"})
	if out != "ok: created topic from default branch@8a1b2c3d\n" || len(f.bodyMap(f.last())) != 1 {
		t.Fatal(out)
	}
	if out := mustOK(t, f, "branch", map[string]any{"action": "rename", "name": "old/name", "new_name": "new"}); out != "ok: renamed branch old/name -> new\n" {
		t.Fatal(out)
	}
	if l := f.last(); l.Body != `{"name":"new"}` {
		t.Fatalf("%+v", l)
	}
	if out := mustOK(t, f, "branch", map[string]any{"action": "delete", "name": "gone"}); out != "ok: deleted branch gone\n" {
		t.Fatal(out)
	}
	f.on("DELETE /repos/o/r/branches/main", reply{status: 403, body: `{"message":"can not delete default branch"}`})
	out = mustErr(t, f, "branch", map[string]any{"action": "delete", "name": "main"})
	if !strings.Contains(out, "403") || !strings.Contains(out, "hint: the default branch and protected branches cannot be deleted") {
		t.Fatal(out)
	}
	// a bad base ref is a hidden 500 on the server
	f.on("POST /repos/o/r/branches", reply{status: 500, body: `{"message":""}`})
	out = mustErr(t, f, "branch", map[string]any{"action": "create", "name": "x", "from": "nope"})
	if !strings.Contains(out, "likely: unknown from ref") {
		t.Fatal(out)
	}
}

func TestBranchProtectReads(t *testing.T) {
	f := newFake(t).
		on("GET /repos/o/r/branch_protections", jsonOK(fixture(t, "protections.json"))).
		on("GET /repos/o/r/branch_protections/main", jsonOK(fixture(t, "protection_main.json"))).
		on("GET /repos/o/r/branch_protections/release%2F%2A", jsonOK(`{"rule_name":"release/*","enable_merge_whitelist":true,"merge_whitelist_usernames":["alice"]}`))
	testutil.Golden(t, "branch_protect_list", mustOK(t, f, "branch", map[string]any{"action": "protect_list"}))
	testutil.Golden(t, "branch_protect_get", mustOK(t, f, "branch", map[string]any{"action": "protect_get", "rule": "main"}))
	// the glob is ONE path segment
	out := mustOK(t, f, "branch", map[string]any{"action": "protect_get", "name": "release/*"})
	if !strings.Contains(out, "protection release/*") || f.last().Path != "/repos/o/r/branch_protections/release%2F%2A" {
		t.Fatalf("path %s\n%s", f.last().Path, out)
	}
	if out := mustErr(t, f, "branch", map[string]any{"action": "protect_get"}); !strings.Contains(out, "pass rule") {
		t.Fatal(out)
	}
}

func TestBranchProtectSetCreateAddsEnablePush(t *testing.T) {
	f := newFake(t).
		on("GET /repos/o/r/branch_protections/release%2F%2A", reply{status: 404, body: `{"message":"GetBranchProtection"}`}).
		on("POST /repos/o/r/branch_protections", reply{status: 201, body: `{}`})
	out := mustOK(t, f, "branch", map[string]any{
		"action": "protect_set", "rule": "release/*",
		"set": map[string]any{"enable_push_whitelist": true, "push_whitelist_usernames": []string{"alice"}, "required_approvals": 1},
	})
	want := "ok: protection release/* created: enable_push_whitelist,push_whitelist_usernames,required_approvals\nnote: enable_push:true added; whitelist options need it\n"
	if out != want {
		t.Fatalf("out %q", out)
	}
	b := f.bodyMap(f.last())
	if f.last().Method != "POST" || b["rule_name"] != "release/*" || b["enable_push"] != true || b["required_approvals"] != float64(1) {
		t.Fatalf("request %+v", f.last())
	}
}

func TestBranchProtectSetUpdateIsPatchWithSetOnly(t *testing.T) {
	f := newFake(t).
		on("GET /repos/o/r/branch_protections/main", jsonOK(`{"rule_name":"main"}`)).
		on("PATCH /repos/o/r/branch_protections/main", jsonOK(`{}`))
	out := mustOK(t, f, "branch", map[string]any{"action": "protect_set", "name": "main", "set": map[string]any{"required_approvals": 2}})
	if out != "ok: protection main updated: required_approvals\n" {
		t.Fatalf("out %q", out)
	}
	if l := f.last(); l.Method != "PATCH" || l.Body != `{"required_approvals":2}` {
		t.Fatalf("%+v", l)
	}
	// enable_push:false alone warns that it clears the whitelist
	out = mustOK(t, f, "branch", map[string]any{"action": "protect_set", "name": "main", "set": map[string]any{"enable_push": false}})
	if !strings.Contains(out, "also clears the push whitelist") {
		t.Fatal(out)
	}
}

func TestBranchProtectSetRejections(t *testing.T) {
	f := newFake(t)
	if out := mustErr(t, f, "branch", map[string]any{"action": "protect_set", "name": "main", "set": map[string]any{"enable_push": false, "enable_push_whitelist": true}}); !strings.Contains(out, "enable_push:false cannot be combined") {
		t.Fatal(out)
	}
	if out := mustErr(t, f, "branch", map[string]any{"action": "protect_set", "name": "main", "set": map[string]any{"nope": 1}}); !strings.Contains(out, "unknown set key nope; valid: enable_push,") {
		t.Fatal(out)
	}
	if out := mustErr(t, f, "branch", map[string]any{"action": "protect_set", "set": map[string]any{"required_approvals": 1}}); !strings.Contains(out, "pass rule") {
		t.Fatal(out)
	}
	if n := len(f.requests()); n != 0 {
		t.Fatalf("%d requests", n)
	}
}

func TestBranchProtectDelete(t *testing.T) {
	f := newFake(t).on("DELETE /repos/o/r/branch_protections/main", reply{status: 204})
	if out := mustOK(t, f, "branch", map[string]any{"action": "protect_delete", "rule": "main"}); out != "ok: deleted protection main\n" {
		t.Fatal(out)
	}
}
