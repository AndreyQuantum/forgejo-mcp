package ci

import (
	"net/http"
	"strings"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

const statusPath = "GET /repos/o/r/commits/" + sha40 + "/status"

func checksRoutes(statuses string) routes {
	return routes{
		"GET /repos/o/r":         `{"default_branch":"main"}`,
		"GET /repos/o/r/commits": `[{"sha":"` + sha40 + `"}]`,
		statusPath:               `{"statuses":` + statuses + `}`,
	}
}

const mixedStatuses = `[
 {"id":1,"status":"failure","context":"ci / build (push)","description":"Failing after 12s","target_url":"https://forgejo.example.com/o/r/actions/runs/56"},
 {"id":2,"status":"pending","context":"ci / test (push)"},
 {"id":3,"status":"success","context":"lint"}]`

func TestChecksGetFailing(t *testing.T) {
	got, isErr := call(t, newFake(checksRoutes(mixedStatuses)).env(t), "checks", map[string]any{"action": "get", "ref": "main"})
	if isErr {
		t.Fatal(got)
	}
	testutil.Golden(t, "checks_get_failing", got)
}

func TestChecksGetGreenIsOneLine(t *testing.T) {
	r := checksRoutes(`[{"id":1,"status":"success","context":"a"},{"id":2,"status":"success","context":"b"}]`)
	got, isErr := call(t, newFake(r).env(t), "checks", map[string]any{"action": "get", "ref": "main"})
	if isErr || got != "state: success (2/2 ok) sha 8a1b2c3d" {
		t.Fatalf("err=%v %q", isErr, got)
	}
}

func TestChecksGetNoneAndDefaultTarget(t *testing.T) {
	got, isErr := call(t, newFake(checksRoutes(`[]`)).env(t), "checks", map[string]any{"action": "get"})
	want := "target: branch main (default)\nstate: none sha 8a1b2c3d"
	if isErr || got != want {
		t.Fatalf("err=%v\n%s", isErr, got)
	}
}

func TestChecksGetVerboseRuns(t *testing.T) {
	r := checksRoutes(`[{"id":1,"status":"success","context":"a"}]`)
	r["GET /repos/o/r/actions/runs"] = `{"workflow_runs":[` + runJSON(1236, 58, "ci.yml", "success") + `],"total_count":1}`
	got, isErr := call(t, newFake(r).env(t), "checks", map[string]any{"action": "get", "sha": sha40, "verbose": true})
	if isErr {
		t.Fatal(got)
	}
	testutil.Golden(t, "checks_get_verbose", got)
}

func TestChecksGetSHAAndRefExclusive(t *testing.T) {
	got, isErr := call(t, newFake(routes{}).env(t), "checks", map[string]any{"action": "get", "sha": sha40, "ref": "main"})
	if !isErr || !strings.HasPrefix(got, "error: invalid:") {
		t.Fatalf("err=%v %q", isErr, got)
	}
}

func TestChecksList(t *testing.T) {
	var query string
	r := routes{
		"GET /repos/o/r/commits/main/statuses": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			query = r.URL.RawQuery
			w.Header().Set("X-Total-Count", "3")
			_, _ = w.Write([]byte(mixedStatuses))
		}),
	}
	got, isErr := call(t, newFake(r).env(t), "checks", map[string]any{"action": "list", "ref": "main", "state": "failure", "limit": 2})
	if isErr {
		t.Fatal(got)
	}
	testutil.Golden(t, "checks_list", got)
	for _, want := range []string{"state=failure", "limit=2", "page=1"} {
		if !strings.Contains(query, want) {
			t.Errorf("query %q lacks %s", query, want)
		}
	}
	if got, isErr := call(t, newFake(routes{}).env(t), "checks", map[string]any{"action": "list", "ref": "main", "state": "bogus"}); !isErr || !strings.Contains(got, "valid: pending,success") {
		t.Errorf("bad state: %q", got)
	}
}

func TestChecksSet(t *testing.T) {
	r := checksRoutes(`[]`)
	r["POST /repos/o/r/statuses/"+sha40] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":9}`))
	})
	f := newFake(r)
	got, isErr := call(t, f.env(t), "checks", map[string]any{
		"action": "set", "ref": "main", "state": "success", "context": "ci/external",
		"description": "all good", "target_url": "https://ci.example.com/1",
	})
	if isErr || got != "ok: status ci/external=success on 8a1b2c3d" {
		t.Fatalf("err=%v %q", isErr, got)
	}
	body := f.body("POST /repos/o/r/statuses/" + sha40)
	for _, want := range []string{`"state":"success"`, `"context":"ci/external"`, `"description":"all good"`, `"target_url":"https://ci.example.com/1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body %s lacks %s", body, want)
		}
	}
}

func TestChecksSetInvalid(t *testing.T) {
	env := newFake(routes{}).env(t)
	for name, args := range map[string]map[string]any{
		"bad state":  {"action": "set", "ref": "main", "state": "ok", "context": "x"},
		"no target":  {"action": "set", "state": "success", "context": "x"},
		"both":       {"action": "set", "ref": "a", "sha": sha40, "state": "success", "context": "x"},
		"no context": {"action": "set", "ref": "main", "state": "success"},
	} {
		got, isErr := call(t, env, "checks", args)
		if !isErr || !strings.HasPrefix(got, "error: invalid:") {
			t.Errorf("%s: err=%v %q", name, isErr, got)
		}
	}
}

func TestChecksReadOnlyHidesSet(t *testing.T) {
	env := newFake(routes{}).env(t, withReadOnly())
	got, isErr := call(t, env, "checks", map[string]any{"action": "set", "ref": "main", "state": "success", "context": "x"})
	if !isErr || !strings.Contains(got, "read-only") {
		t.Fatalf("err=%v %q", isErr, got)
	}
}

func TestChecksFullSHA256(t *testing.T) {
	long := strings.Repeat("cd", 32)
	r := routes{"GET /repos/o/r/commits/" + long + "/status": `{"statuses":[{"id":1,"status":"success","context":"a"}]}`}
	got, isErr := call(t, newFake(r).env(t), "checks", map[string]any{"action": "get", "sha": long})
	if isErr || !strings.HasPrefix(got, "state: success") {
		t.Fatalf("err=%v %q", isErr, got)
	}
}

func TestChecksListEscapesRefSlash(t *testing.T) {
	r := routes{"GET /repos/o/r/commits/release%2F1.0/statuses": `[]`}
	f := newFake(r)
	got, isErr := call(t, f.env(t), "checks", map[string]any{"action": "list", "ref": "release/1.0"})
	_ = got
	if len(f.missing) != 0 {
		t.Fatalf("err=%v unmatched requests: %v", isErr, f.missing)
	}
	if f.called("GET /repos/o/r/commits/release%2F1.0/statuses") != 1 {
		t.Fatalf("calls: %v", f.calls)
	}
}
