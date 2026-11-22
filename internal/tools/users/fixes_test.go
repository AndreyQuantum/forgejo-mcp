package users

import (
	"strings"
	"testing"
)

func TestNoSideEffectGets(t *testing.T) {
	if got := registry(t, nil).SideEffectGets(); len(got) != 0 {
		t.Errorf("users area must not declare side-effect GETs, got %v", got)
	}
}

func TestTeamEditPermissionSendsUnits(t *testing.T) {
	f := newFake(map[string]reply{"GET /teams/5": {body: teamJSON}, "PATCH /teams/5": {body: teamJSON}})
	got, isErr := run(t, f, "org", map[string]any{"action": "team_edit", "team_id": 5, "permission": "read"})
	if isErr {
		t.Fatalf("unexpected error: %s", got)
	}
	want := `PATCH /teams/5 {"name":"devs","permission":"read","units_map":{"repo.code":"read","repo.issues":"read","repo.wiki":"read"}}`
	reqs := f.requests()
	if len(reqs) != 2 || reqs[1] != want {
		t.Errorf("requests = %q, want PATCH %s", reqs, want)
	}
}

func TestTeamUnitsBadKey(t *testing.T) {
	f := newFake(nil)
	got, isErr := run(t, f, "org", map[string]any{"action": "team_create", "org": "acme", "name": "x", "units_map": map[string]any{"issue": "read"}})
	if !isErr || !strings.Contains(got, `units_map key "issue"`) {
		t.Errorf("want invalid key error, got %v %s", isErr, got)
	}
}

func TestInactiveOnlyForAdminViewer(t *testing.T) {
	list := `[{"id":5,"login":"bob","full_name":"Bob","active":false,"created":"2026-02-03T04:05:06Z","last_login":"0001-01-01T00:00:00Z"}]`
	// non-admin viewer: placeholder flags are not shown
	f := newFake(map[string]reply{"GET /user": {body: `{"id":9,"login":"carol","active":true}`}, "GET /user/followers": {body: list, total: "1"}, "GET /users/bob": {body: strings.Replace(list, "[", "", 1)[:len(list)-2]}})
	got, _ := run(t, f, "user", map[string]any{"action": "followers"})
	if strings.Contains(got, "inactive") {
		t.Errorf("non-admin must not see inactive: %s", got)
	}
	got, _ = run(t, f, "user", map[string]any{"action": "get", "user": "bob"})
	if strings.Contains(got, "active: no") || strings.Contains(got, "last_login") {
		t.Errorf("placeholder fields leaked: %s", got)
	}
	// admin viewer sees the flag
	f = newFake(map[string]reply{"GET /user": {body: meAdmin}, "GET /user/followers": {body: list, total: "1"}})
	got, _ = run(t, f, "user", map[string]any{"action": "followers"})
	if !strings.Contains(got, "inactive") {
		t.Errorf("admin must see inactive: %s", got)
	}
}

func TestSubjectNumberOnlyForIssueAndPull(t *testing.T) {
	if n := subjectNumber("Repository", "https://x/o/2024"); n != 0 {
		t.Errorf("repository subject number = %d", n)
	}
	if n := subjectNumber("Pull", "https://x/api/v1/repos/o/r/issues/12"); n != 12 {
		t.Errorf("pull subject number = %d", n)
	}
}
