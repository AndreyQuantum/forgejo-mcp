package ci

import (
	"net/http"
	"strings"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

func TestConfigListSecrets(t *testing.T) {
	r := routes{"GET /repos/o/r/actions/secrets": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Total-Count", "2")
		_, _ = w.Write([]byte(`[{"name":"DEPLOY_KEY","created":"2026-11-20T10:00:00Z"},{"name":"NPM_TOKEN"}]`))
	})}
	got, isErr := call(t, newFake(r).env(t), "actions_config", map[string]any{"action": "list", "kind": "secret"})
	if isErr {
		t.Fatal(got)
	}
	testutil.Golden(t, "config_list_secrets", got)
}

func TestConfigListVariablesOrgScope(t *testing.T) {
	r := routes{"GET /orgs/acme/actions/variables": `[{"name":"REGION","data":"eu-1"},{"name":"MODE","data":"line one\nline two"}]`}
	got, isErr := call(t, newFake(r).envNoRepo(t), "actions_config", map[string]any{"action": "list", "kind": "variable", "org": "acme"})
	if isErr {
		t.Fatal(got)
	}
	testutil.Golden(t, "config_list_variables_org", got)
}

func TestConfigListUserScope(t *testing.T) {
	r := routes{"GET /user/actions/secrets": `[{"name":"A"}]`}
	got, isErr := call(t, newFake(r).envNoRepo(t), "actions_config", map[string]any{"action": "list", "kind": "secret", "scope": "user"})
	if isErr || !strings.Contains(got, "A") {
		t.Fatalf("err=%v %q", isErr, got)
	}
}

func TestConfigGetVariable(t *testing.T) {
	r := routes{"GET /repos/o/r/actions/variables/REGION": `{"name":"REGION","data":"eu-1"}`}
	got, isErr := call(t, newFake(r).env(t), "actions_config", map[string]any{"action": "get", "kind": "variable", "name": "REGION"})
	if isErr || got != "REGION=eu-1" {
		t.Fatalf("err=%v %q", isErr, got)
	}
	got, isErr = call(t, newFake(r).env(t), "actions_config", map[string]any{"action": "get", "kind": "secret", "name": "X"})
	if !isErr || !strings.Contains(got, "write-only") {
		t.Fatalf("secret get: err=%v %q", isErr, got)
	}
}

func TestConfigSetSecretCreatedAndUpdated(t *testing.T) {
	for status, want := range map[int]string{
		http.StatusCreated:   "ok: secret DEPLOY_KEY set (created, o/r)",
		http.StatusNoContent: "ok: secret DEPLOY_KEY set (updated, o/r)",
	} {
		r := routes{"PUT /repos/o/r/actions/secrets/deploy_key": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })}
		f := newFake(r)
		got, isErr := call(t, f.env(t), "actions_config", map[string]any{"action": "set", "kind": "secret", "name": "deploy_key", "value": "s3cr3t-value"})
		if isErr || got != want {
			t.Fatalf("status %d: err=%v %q", status, isErr, got)
		}
		if strings.Contains(got, "s3cr3t") {
			t.Fatal("secret value must never be echoed")
		}
		if body := f.body("PUT /repos/o/r/actions/secrets/deploy_key"); body != `{"data":"s3cr3t-value"}` {
			t.Fatalf("body %s", body)
		}
	}
}

func TestConfigSetVariableCreateThenUpdate(t *testing.T) {
	// 404 on the probe -> POST (create)
	f := newFake(routes{"POST /repos/o/r/actions/variables/region": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })})
	got, isErr := call(t, f.env(t), "actions_config", map[string]any{"action": "set", "kind": "variable", "name": "region", "value": "eu-1"})
	if isErr || got != "ok: variable REGION=eu-1 created (o/r)" {
		t.Fatalf("create: err=%v %q", isErr, got)
	}
	if f.called("POST /repos/o/r/actions/variables/region") != 1 || f.body("POST /repos/o/r/actions/variables/region") != `{"value":"eu-1"}` {
		t.Fatalf("calls %v", f.calls)
	}

	// 200 on the probe -> PUT (update)
	f = newFake(routes{
		"GET /repos/o/r/actions/variables/region": `{"name":"REGION","data":"eu-1"}`,
		"PUT /repos/o/r/actions/variables/region": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }),
	})
	got, isErr = call(t, f.env(t), "actions_config", map[string]any{"action": "set", "kind": "variable", "name": "region", "value": strings.Repeat("x", 60)})
	want := "ok: variable REGION=" + strings.Repeat("x", 39) + "… updated (o/r)"
	if isErr || got != want {
		t.Fatalf("update: err=%v %q (want %q)", isErr, got, want)
	}
	if f.called("PUT /repos/o/r/actions/variables/region") != 1 {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestConfigSetVariableProbeError(t *testing.T) {
	f := newFake(routes{"GET /repos/o/r/actions/variables/REGION": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"forbidden"}`))
	})})
	got, isErr := call(t, f.env(t), "actions_config", map[string]any{"action": "set", "kind": "variable", "name": "REGION", "value": "v"})
	if !isErr || !strings.Contains(got, "403") || !strings.Contains(got, "repo owner") {
		t.Fatalf("err=%v %q", isErr, got)
	}
	if f.called("POST /repos/o/r/actions/variables/REGION")+f.called("PUT /repos/o/r/actions/variables/REGION") != 0 {
		t.Fatal("must not write after a failed probe")
	}
}

func TestConfigDelete(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	f := newFake(routes{"DELETE /repos/o/r/actions/secrets/OLD": ok, "DELETE /orgs/acme/actions/variables/REGION": ok})
	got, isErr := call(t, f.env(t), "actions_config", map[string]any{"action": "delete", "kind": "secret", "name": "OLD"})
	if isErr || got != "ok: deleted secret OLD (o/r)" {
		t.Fatalf("err=%v %q", isErr, got)
	}
	got, isErr = call(t, f.env(t), "actions_config", map[string]any{"action": "delete", "kind": "variable", "name": "REGION", "scope": "org", "org": "acme"})
	if isErr || got != "ok: deleted variable REGION (org acme)" {
		t.Fatalf("err=%v %q", isErr, got)
	}
}

func TestConfigInvalid(t *testing.T) {
	env := newFake(routes{}).env(t)
	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"bad kind":        {map[string]any{"action": "list", "kind": "key"}, "invalid: kind"},
		"bad scope":       {map[string]any{"action": "list", "kind": "secret", "scope": "site"}, "invalid: scope"},
		"org without org": {map[string]any{"action": "list", "kind": "secret", "scope": "org"}, "scope=org needs org"},
		"missing kind":    {map[string]any{"action": "list"}, "missing param kind"},
		"missing value":   {map[string]any{"action": "set", "kind": "secret", "name": "A"}, "missing param value"},
		"empty value":     {map[string]any{"action": "set", "kind": "secret", "name": "A", "value": ""}, "missing param value"},
	} {
		got, isErr := call(t, env, "actions_config", tc.args)
		if !isErr || !strings.Contains(got, tc.want) {
			t.Errorf("%s: err=%v %q (want %q)", name, isErr, got, tc.want)
		}
	}
}

func TestConfigReadOnly(t *testing.T) {
	env := newFake(routes{}).env(t, withReadOnly())
	got, isErr := call(t, env, "actions_config", map[string]any{"action": "set", "kind": "secret", "name": "A", "value": "v"})
	if !isErr || !strings.Contains(got, "read-only") {
		t.Fatalf("err=%v %q", isErr, got)
	}
}

func TestConfigGetVariableEscapesName(t *testing.T) {
	f := newFake(routes{"GET /repos/o/r/actions/variables/a%20b": `{"name":"a b","data":"x"}`})
	call(t, f.env(t), "actions_config", map[string]any{"action": "get", "kind": "variable", "name": "a b"})
	if f.called("GET /repos/o/r/actions/variables/a%20b") != 1 {
		t.Fatalf("calls: %v missing: %v", f.calls, f.missing)
	}
}
