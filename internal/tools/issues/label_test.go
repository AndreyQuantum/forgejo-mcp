package issues

import (
	"net/http"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

func TestLabelList(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{
		"GET /repos/o/r/labels": listH(3, `[
		 {"id":5,"name":"bug","color":"d73a4a","description":"Something is broken"},
		 {"id":6,"name":"scope/a","color":"0075ca","exclusive":true},
		 {"id":7,"name":"old","color":"cccccc","is_archived":true,"description":"`+long80()+`"}]`),
		"GET /orgs/acme/labels": listH(1, `[{"id":1,"name":"org-wide","color":"ffffff"}]`),
	})
	got := mustRun(t, f, "label", map[string]any{"action": "list", "sort": "mostissues"})
	testutil.Golden(t, "label_list", got)
	eq(t, "sort", f.only("GET", "/repos/o/r/labels").Query.Get("sort"), "mostissues")
	org := mustRun(t, f, "label", map[string]any{"action": "list", "org": "acme"})
	contains(t, org, "# labels 1/1 page 1/1", "id 1 | org-wide | #ffffff")
	mustFail(t, f, "label", map[string]any{"action": "list", "sort": "zzz"}, "invalid: sort")
}

func long80() string {
	s := ""
	for len(s) < 90 {
		s += "word "
	}
	return s
}

func TestLabelCreate(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{
		"POST /repos/o/r/labels": jsonH(201, `{"id":9,"name":"new"}`),
		"POST /orgs/acme/labels": jsonH(201, `{"id":10,"name":"o"}`),
	})
	eq(t, "create", mustRun(t, f, "label", map[string]any{"action": "create", "name": "new", "color": "#ff0000", "description": "d", "exclusive": true}), "ok: created label new (id 9)")
	eq(t, "body", f.only("POST", "/repos/o/r/labels").Body, `{"color":"#ff0000","description":"d","exclusive":true,"name":"new"}`)
	mustRun(t, f, "label", map[string]any{"action": "create", "name": "o", "org": "acme"})
	eq(t, "org body", f.only("POST", "/orgs/acme/labels").Body, `{"color":"ededed","name":"o"}`)
	mustFail(t, f, "label", map[string]any{"action": "create"}, "missing param name")
}

func TestLabelEditKeepsArchivedFlag(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{
		"GET /repos/o/r/labels":     listH(2, `[{"id":5,"name":"bug"},{"id":6,"name":"old"}]`),
		"GET /repos/o/r/labels/6":   jsonH(200, `{"id":6,"name":"old","is_archived":true}`),
		"PATCH /repos/o/r/labels/6": jsonH(200, `{"id":6,"name":"older"}`),
	})
	got := mustRun(t, f, "label", map[string]any{"action": "edit", "name": "old", "new_name": "older", "color": "00ff00"})
	eq(t, "result", got, "ok: edited label older (id 6): name,color")
	eq(t, "patch", f.only("PATCH", "/repos/o/r/labels/6").Body, `{"color":"00ff00","is_archived":true,"name":"older"}`)

	// explicit archived=false wins over the current value.
	mustRun(t, f, "label", map[string]any{"action": "edit", "id": 6, "archived": false})
	reqs := f.find("PATCH", "/repos/o/r/labels/6")
	eq(t, "unarchive", reqs[1].Body, `{"is_archived":false}`)
	mustFail(t, f, "label", map[string]any{"action": "edit", "id": 6}, "nothing to edit")
	mustFail(t, f, "label", map[string]any{"action": "edit", "name": "ghost", "color": "fff"}, `unknown label "ghost"; known: bug,old`)
	mustFail(t, f, "label", map[string]any{"action": "edit", "color": "fff"}, "pass id or name")
}

func TestLabelDeleteAndCacheInvalidation(t *testing.T) {
	labels := `[{"id":5,"name":"bug"}]`
	f := newFake(t, map[string]handlerFunc{
		"GET /repos/o/r/labels":      listH(1, labels),
		"GET /orgs/o/labels":         jsonH(404, `{"message":"x"}`),
		"DELETE /repos/o/r/labels/5": emptyH(204),
		"POST /repos/o/r/labels":     jsonH(201, `{"id":6,"name":"new"}`),
	})
	// Warm the shared label cache through issue edit validation, then mutate
	// labels: the next resolution must hit the server again.
	env := testutil.NewEnv(t, f, testutil.WithRepo("o/r"), testutil.WithToken("tok-secret-123456"))
	call := func(tool string, args map[string]any) string {
		text, isErr := testutil.CallTool(t, env, spec(t, tool), args)
		if isErr {
			t.Fatalf("%s: %s", tool, text)
		}
		return text
	}
	f.set("PATCH /repos/o/r/issues/1", jsonH(201, `{}`))
	f.set("POST /repos/o/r/issues/1/labels", jsonH(200, `[{"name":"bug"}]`))
	call("issue", map[string]any{"action": "edit", "n": 1, "add_labels": []string{"bug"}})
	call("issue", map[string]any{"action": "edit", "n": 1, "add_labels": []string{"bug"}})
	if n := len(f.find("GET", "/repos/o/r/labels")); n != 1 {
		t.Fatalf("label list should be cached, got %d fetches", n)
	}
	eq(t, "delete", call("label", map[string]any{"action": "delete", "name": "bug"}), "ok: deleted label id 5")
	call("issue", map[string]any{"action": "edit", "n": 1, "add_labels": []string{"bug"}})
	if n := len(f.find("GET", "/repos/o/r/labels")); n < 3 {
		t.Errorf("cache was not invalidated after delete (fetches: %d)", n)
	}
	_ = http.StatusOK
}
