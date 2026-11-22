package issues

import (
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

func TestMilestoneListAlwaysSendsState(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{"GET /repos/o/r/milestones": listH(3, `[
	 {"id":3,"title":"v1.2","state":"open","open_issues":6,"closed_issues":4,"due_on":"2026-12-01T00:00:00Z"},
	 {"id":4,"title":"later","state":"open","open_issues":0,"closed_issues":0,"due_on":"9999-12-31T23:59:59Z"},
	 {"id":2,"title":"v1.1","state":"closed","open_issues":0,"closed_issues":9}]`)})
	got := mustRun(t, f, "milestone", map[string]any{"action": "list"})
	testutil.Golden(t, "milestone_list", got)
	eq(t, "default state", f.only("GET", "/repos/o/r/milestones").Query.Get("state"), "open")
	mustRun(t, f, "milestone", map[string]any{"action": "list", "state": "all"})
	eq(t, "state all", f.find("GET", "/repos/o/r/milestones")[1].Query.Get("state"), "all")
	mustFail(t, f, "milestone", map[string]any{"action": "list", "state": "x"}, "invalid: state")
}

func TestMilestoneCreateEditDelete(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{
		"POST /repos/o/r/milestones":         jsonH(201, `{"id":8,"title":"v2"}`),
		"PATCH /repos/o/r/milestones/8":      jsonH(200, `{"id":8,"title":"v2.0"}`),
		"PATCH /repos/o/r/milestones/v2%2F0": jsonH(200, `{"id":8,"title":"v2/0"}`),
		"DELETE /repos/o/r/milestones/8":     emptyH(204),
		"GET /repos/o/r/milestones":          listH(2, `[{"id":8,"title":"v2"},{"id":9,"title":"v2/0"}]`),
		"PATCH /repos/o/r/milestones/9":      jsonH(200, `{"id":9,"title":"v2/0"}`),
	})
	eq(t, "create", mustRun(t, f, "milestone", map[string]any{"action": "create", "title": "v2", "description": "d", "due": "2027-01-31"}), "ok: created milestone v2 (id 8)")
	eq(t, "create body", f.only("POST", "/repos/o/r/milestones").Body, `{"description":"d","due_on":"2027-01-31T00:00:00Z","title":"v2"}`)

	eq(t, "close", mustRun(t, f, "milestone", map[string]any{"action": "edit", "id": 8, "state": "closed", "new_title": "v2.0"}), "ok: edited milestone v2.0 (id 8): title,state=closed")
	eq(t, "edit body", f.only("PATCH", "/repos/o/r/milestones/8").Body, `{"state":"closed","title":"v2.0"}`)
	mustRun(t, f, "milestone", map[string]any{"action": "edit", "title": "v2/0", "description": "x"})
	eq(t, "title resolved to id", f.only("PATCH", "/repos/o/r/milestones/9").Body, `{"description":"x"}`)
	mustFail(t, f, "milestone", map[string]any{"action": "edit", "id": 8}, "nothing to edit")
	mustFail(t, f, "milestone", map[string]any{"action": "edit", "state": "open"}, "pass id or title")
	mustFail(t, f, "milestone", map[string]any{"action": "edit", "id": 8, "state": "all"}, "invalid: state")

	eq(t, "delete id", mustRun(t, f, "milestone", map[string]any{"action": "delete", "id": 8}), "ok: deleted milestone id 8")
	eq(t, "delete title", mustRun(t, f, "milestone", map[string]any{"action": "delete", "title": "v2"}), "ok: deleted milestone id 8")
	mustFail(t, f, "milestone", map[string]any{"action": "delete", "title": "nope"}, `unknown milestone "nope"`)
	mustFail(t, f, "milestone", map[string]any{"action": "delete"}, "pass id or title")
}

func TestMilestoneEditNumericTitleIsATitle(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{
		"GET /repos/o/r/milestones":     listH(2, `[{"id":3,"title":"sprint"},{"id":4,"title":"3"}]`),
		"PATCH /repos/o/r/milestones/4": jsonH(200, `{"id":4,"title":"3"}`),
	})
	mustRun(t, f, "milestone", map[string]any{"action": "edit", "title": "3", "state": "closed"})
	eq(t, "body", f.only("PATCH", "/repos/o/r/milestones/4").Body, `{"state":"closed"}`)
	f.none("PATCH", "/repos/o/r/milestones/3")
}
