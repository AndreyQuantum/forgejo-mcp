package tools_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

type dummyParams struct {
	Action   string         `json:"action"`
	Repo     string         `json:"repo,omitempty" jsonschema:"owner/name, default detected"`
	N        int            `json:"n,omitempty" jsonschema:"PR number"`
	Body     string         `json:"body,omitempty" jsonschema:"comment text"`
	Labels   []string       `json:"labels,omitempty" jsonschema:"label names"`
	Draft    *bool          `json:"draft,omitempty" jsonschema:"create as draft"`
	Set      map[string]any `json:"set,omitempty" jsonschema:"fields to change"`
	Confirm  string         `json:"confirm,omitempty" jsonschema:"exact target name"`
	MaxChars int            `json:"max_chars,omitempty" jsonschema:"output size in chars"`
	Full     bool           `json:"full,omitempty" jsonschema:"lift per-field caps"`
}

func dummySpec() tools.Spec {
	run := func(c *tools.Call, p *dummyParams) (*tools.Result, error) {
		return &tools.Result{Text: "ok " + c.Action + " " + c.Owner + "/" + c.Repo + " n=" + strconv.Itoa(p.N)}, nil
	}
	return tools.Spec{
		Name: "dummy", Toolset: "pulls", Description: "Dummy tool for framework tests.",
		Params: dummyParams{}, RepoScoped: true,
		Actions: []tools.Action{
			tools.NewAction("get", tools.Read, run, tools.Summary("show one"), tools.Params("n"), tools.Required("n"),
				tools.Endpoints("GET /repos/{owner}/{repo}/pulls/{index}")),
			tools.NewAction("list", tools.Read, run, tools.Summary("list all"), tools.Params("n"),
				tools.Endpoints("GET /repos/{owner}/{repo}/pulls")),
			tools.NewAction("create", tools.Write, run, tools.Summary("create one"), tools.Params("body", "labels", "draft"),
				tools.Required("body"), tools.Endpoints("POST /repos/{owner}/{repo}/pulls"), tools.Needs("repo write")),
			tools.NewAction("remove", tools.Destructive, func(c *tools.Call, p *dummyParams) (*tools.Result, error) {
				if r := c.Confirm(p.Confirm, c.Owner+"/"+c.Repo); r != nil {
					return r, nil
				}
				return &tools.Result{Text: "ok removed"}, nil
			}, tools.Summary("delete"), tools.Params("confirm"), tools.Endpoints("DELETE /repos/{owner}/{repo}")),
			tools.NewAction("boom", tools.Read, func(*tools.Call, *dummyParams) (*tools.Result, error) {
				panic("token-secret-123 exploded")
			}, tools.Summary("panics")),
			tools.NewAction("size", tools.Read, func(c *tools.Call, p *dummyParams) (*tools.Result, error) {
				return &tools.Result{Text: "limit=" + strconv.Itoa(c.Limit) + " chars=" + strconv.Itoa(c.Chars(p.MaxChars, 12000))}, nil
			}, tools.Summary("limits"), tools.Params("max_chars", "full")),
			tools.NewAction("newer", tools.Read, run, tools.Summary("needs 99"), tools.MinMajor(99)),
			tools.NewAction("token", tools.Read, func(c *tools.Call, p *dummyParams) (*tools.Result, error) {
				return &tools.Result{Text: "echo " + c.Env.Client.Token()}, nil
			}, tools.Summary("echoes the token")),
		},
	}
}

func envOK(t *testing.T, opts ...testutil.EnvOption) *tools.Env {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/version" {
			_, _ = w.Write([]byte(`{"version":"17.0.2+gitea-1.22.0"}`))
			return
		}
		http.NotFound(w, r)
	})
	opts = append([]testutil.EnvOption{testutil.WithRepo("o/r")}, opts...)
	return testutil.NewEnv(t, h, opts...)
}

func TestSchema(t *testing.T) {
	s := dummySpec()
	sc, err := tools.BuildInputSchema(s, s.Actions)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(sc)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if got := m["required"]; !slices.Equal(anyStrings(got), []string{"action"}) {
		t.Errorf("required = %v", got)
	}
	if ap, ok := m["additionalProperties"]; !ok || ap == true {
		t.Errorf("additionalProperties = %v, want false schema", ap)
	}
	props := m["properties"].(map[string]any)
	enum := anyStrings(props["action"].(map[string]any)["enum"])
	want := []string{"get", "list", "create", "remove", "boom", "size", "newer", "token", "help"}
	if !slices.Equal(enum, want) {
		t.Errorf("enum = %v, want %v", enum, want)
	}
	desc := func(n string) string { return props[n].(map[string]any)["description"].(string) }
	for name, prefix := range map[string]string{
		"n":      "[get*,list] ",
		"body":   "[create*] ",
		"labels": "[create] ",
		"draft":  "[create] ",
	} {
		if !strings.HasPrefix(desc(name), prefix) {
			t.Errorf("%s description %q lacks prefix %q", name, desc(name), prefix)
		}
	}
	for name, p := range props {
		if name == "action" || name == "repo" || name == "help" {
			continue
		}
		if d := p.(map[string]any)["description"].(string); !strings.HasPrefix(d, "[") {
			t.Errorf("%s description %q has no [actions] prefix", name, d)
		}
	}
	// pointer and slice fields are not nullable unions
	if tp := props["draft"].(map[string]any)["type"]; tp != "boolean" {
		t.Errorf("draft type = %v", tp)
	}
	if tp := props["labels"].(map[string]any)["type"]; tp != "array" {
		t.Errorf("labels type = %v", tp)
	}
	if _, ok := props["set"]; !ok {
		t.Error("map[string]any param missing")
	}
}

func anyStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestSchemaSingle(t *testing.T) {
	type p struct {
		Verbose bool `json:"verbose,omitempty" jsonschema:"more detail"`
		Q       string
	}
	s := tools.Spec{
		Name: "single", Toolset: "meta", Description: "x", Params: p{}, Single: true,
		Actions: []tools.Action{tools.NewAction("run", tools.Read, func(*tools.Call, *p) (*tools.Result, error) { return nil, nil },
			tools.Params("verbose"), tools.Required("Q"))},
	}
	sc, err := tools.BuildInputSchema(s, s.Actions)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sc.Properties["action"]; ok {
		t.Error("single tool has an action property")
	}
	if _, ok := sc.Properties["help"]; ok {
		t.Error("single tool has a help property")
	}
	if sc.Properties["verbose"].Description != "more detail" {
		t.Errorf("single tool description is prefixed: %q", sc.Properties["verbose"].Description)
	}
}

func TestResolveToolsets(t *testing.T) {
	got, err := tools.ResolveToolsets("repos,pulls")
	if err != nil || !got["meta"] || !got["repos"] || !got["pulls"] || got["issues"] {
		t.Errorf("repos,pulls = %v, %v", got, err)
	}
	got, _ = tools.ResolveToolsets("all,-admin,-meta")
	if got["admin"] || !got["meta"] || !got["packages"] {
		t.Errorf("all,-admin,-meta = %v", got)
	}
	got, _ = tools.ResolveToolsets("repos,-repos,issues")
	if got["repos"] || !got["issues"] {
		t.Errorf("subtract = %v", got)
	}
	if _, err := tools.ResolveToolsets("repos,nope"); err == nil || !strings.Contains(err.Error(), "valid:") {
		t.Errorf("unknown toolset error = %v", err)
	}
}

func TestFilteringMatrix(t *testing.T) {
	build := func(mut func(*config.Config)) *tools.Registry {
		cfg := config.Defaults()
		mut(cfg)
		r := tools.NewRegistry()
		r.Add(dummySpec())
		r.Add(tools.Spec{
			Name: "writer", Toolset: "issues", Description: "w", Params: dummyParams{},
			Actions: []tools.Action{tools.NewAction("add", tools.Write, func(*tools.Call, *dummyParams) (*tools.Result, error) { return nil, nil },
				tools.Endpoints("POST /x"))},
		})
		r.Add(tools.Spec{
			Name: "adm", Toolset: "admin", Description: "a", Params: dummyParams{},
			Actions: []tools.Action{tools.NewAction("get", tools.Read, func(*tools.Call, *dummyParams) (*tools.Result, error) { return nil, nil },
				tools.Endpoints("GET /admin/users"))},
		})
		r.Add(tools.Spec{
			Name: "api", Toolset: "meta", Description: "api", Params: dummyParams{}, Single: true,
			Actions: []tools.Action{tools.NewAction("run", tools.Write, func(*tools.Call, *dummyParams) (*tools.Result, error) { return nil, nil },
				tools.ModeFor(func(any) tools.Mode { return tools.Read }))},
		})
		if err := r.Build(cfg); err != nil {
			t.Fatal(err)
		}
		return r
	}
	names := func(r *tools.Registry) []string {
		var out []string
		for _, s := range r.Enabled() {
			out = append(out, s.Name)
		}
		return out
	}
	actionsOf := func(r *tools.Registry, tool string) []string {
		var out []string
		for _, s := range r.Enabled() {
			if s.Name == tool {
				for _, a := range s.Actions {
					out = append(out, a.Name)
				}
			}
		}
		return out
	}

	r := build(func(*config.Config) {})
	if got := names(r); !slices.Equal(got, []string{"dummy", "writer", "api"}) {
		t.Errorf("default tools = %v (admin must be off)", got)
	}
	if _, ok := r.DeniedEndpoints()["GET /admin/users"]; !ok {
		t.Error("admin endpoint of a disabled toolset is not denied")
	}

	r = build(func(c *config.Config) { c.ReadOnly = true })
	if got := names(r); !slices.Equal(got, []string{"dummy", "api"}) {
		t.Errorf("read-only tools = %v (write-only tool must vanish, forgejo_api-like stays)", got)
	}
	for _, a := range actionsOf(r, "dummy") {
		if a == "create" || a == "remove" {
			t.Errorf("read-only still exposes %s", a)
		}
	}

	r = build(func(c *config.Config) { c.Deny = []string{"dummy.create", "writer.*"}; c.Toolsets = "all" })
	if got := names(r); !slices.Equal(got, []string{"dummy", "adm", "api"}) {
		t.Errorf("deny tools = %v", got)
	}
	if slices.Contains(actionsOf(r, "dummy"), "create") {
		t.Error("denied action still present")
	}
	if got := r.DeniedEndpoints()["POST /repos/{owner}/{repo}/pulls"]; got != "dummy.create" {
		t.Errorf("denied endpoint owner = %q", got)
	}
	if got := r.DeniedEndpoints()["POST /x"]; got != "writer.add" {
		t.Errorf("denied endpoint (tool.*) owner = %q", got)
	}

	r = build(func(c *config.Config) { c.Toolsets = "pulls" })
	if got := names(r); !slices.Equal(got, []string{"dummy", "api"}) {
		t.Errorf("toolset pulls tools = %v", got)
	}
	if got := r.CoveredBy("GET /repos/{owner}/{repo}/pulls"); got != "dummy.list" {
		t.Errorf("CoveredBy = %q", got)
	}
}

func TestSideEffectGets(t *testing.T) {
	r := tools.NewRegistry()
	r.Add(tools.Spec{
		Name: "runner", Toolset: "runners", Description: "r", Params: dummyParams{},
		Actions: []tools.Action{
			tools.NewAction("reg_token", tools.Write, func(*tools.Call, *dummyParams) (*tools.Result, error) { return nil, nil },
				tools.Endpoints("GET /repos/{owner}/{repo}/actions/runners/registration-token", "GET /orgs/{org}/actions/runners/registration-token")),
			tools.NewAction("list", tools.Read, func(*tools.Call, *dummyParams) (*tools.Result, error) { return nil, nil },
				tools.Endpoints("GET /repos/{owner}/{repo}/actions/runners")),
		},
	})
	if err := r.Build(config.Defaults()); err != nil {
		t.Fatal(err)
	}
	want := []string{"GET /orgs/{org}/actions/runners/registration-token", "GET /repos/{owner}/{repo}/actions/runners/registration-token"}
	if got := r.SideEffectGets(); !slices.Equal(got, want) {
		t.Errorf("SideEffectGets = %v", got)
	}
}

func TestBuildRejectsBadSpec(t *testing.T) {
	r := tools.NewRegistry()
	s := dummySpec()
	s.Description = strings.Repeat("x", 161)
	r.Add(s)
	if err := r.Build(config.Defaults()); err == nil || !strings.Contains(err.Error(), "description") {
		t.Errorf("long description accepted: %v", err)
	}
}

func TestHandlerOrder(t *testing.T) {
	spec := dummySpec()
	env := envOK(t)
	call := func(env *tools.Env, args map[string]any) (string, bool) {
		return testutil.CallTool(t, env, spec, args)
	}
	cases := []struct {
		name    string
		args    map[string]any
		want    string
		wantErr bool
	}{
		{"ok", map[string]any{"action": "get", "n": 5}, "ok get o/r n=5", false},
		{"explicit repo", map[string]any{"action": "get", "n": 5, "repo": "a/b"}, "ok get a/b n=5", false},
		{"unknown action", map[string]any{"action": "nope"}, "error: unknown action nope; valid: get,list,create,remove,boom,size,newer,token,help", true},
		{"missing action", map[string]any{}, "error: invalid: missing param action; valid:", true},
		{"missing required", map[string]any{"action": "get"}, "error: invalid: missing param n for dummy.get", true},
		{"type mismatch", map[string]any{"action": "get", "n": "x"}, "error: invalid: param n: expected integer", true},
		{"bad repo", map[string]any{"action": "list", "repo": "a/b c"}, "error: invalid: repo", true},
		{"ignored params", map[string]any{"action": "list", "body": "x", "zzz": 1}, "ok list o/r n=0\nnote: ignored params: body,zzz", false},
		{"min version", map[string]any{"action": "newer"}, "error: dummy.newer needs Forgejo >= 99 (server is 17.0.2", true},
		{"destructive no confirm", map[string]any{"action": "remove"}, "error: destructive: pass confirm=", true},
		{"destructive wrong confirm", map[string]any{"action": "remove", "confirm": "x/y"}, `error: destructive: pass confirm="o/r"`, true},
		{"destructive ok", map[string]any{"action": "remove", "confirm": "o/r"}, "ok removed", false},
		{"panic", map[string]any{"action": "boom"}, "error: internal: *** exploded", true},
	}
	envTok := envOK(t, testutil.WithToken("token-secret-123"))
	for _, tc := range cases {
		e := env
		if tc.name == "panic" {
			e = envTok
		}
		got, isErr := call(e, tc.args)
		if isErr != tc.wantErr || !strings.HasPrefix(got, tc.want) {
			t.Errorf("%s: got (%v) %q, want prefix %q", tc.name, isErr, got, tc.want)
		}
	}
	if got, _ := call(envTok, map[string]any{"action": "token"}); strings.Contains(got, "token-secret-123") {
		// the action echoes the token; the framework must redact it
		t.Errorf("token leaked: %q", got)
	}
}

func TestReadOnlyAndDenyMessages(t *testing.T) {
	spec := dummySpec()
	ro := envOK(t, testutil.WithConfig(func(c *config.Config) { c.ReadOnly = true }))
	got, isErr := testutil.CallTool(t, ro, spec, map[string]any{"action": "create", "body": "x"})
	if !isErr || got != "error: action create is disabled (read-only mode)" {
		t.Errorf("read-only: %q %v", got, isErr)
	}
	dn := envOK(t, testutil.WithConfig(func(c *config.Config) { c.Deny = []string{"dummy.get"} }))
	got, isErr = testutil.CallTool(t, dn, spec, map[string]any{"action": "get", "n": 1})
	if !isErr || got != "error: action get is disabled (denied)" {
		t.Errorf("deny: %q %v", got, isErr)
	}
	// the enum of the registered tool lists only allowed actions
	got, _ = testutil.CallTool(t, dn, spec, map[string]any{"action": "help"})
	if strings.Contains(got, "\nget [") || !strings.Contains(got, "list [R]") {
		t.Errorf("help shows hidden actions:\n%s", got)
	}
}

func TestHelp(t *testing.T) {
	spec := dummySpec()
	env := envOK(t)
	table, _ := testutil.CallTool(t, env, spec, map[string]any{"action": "help"})
	testutil.Golden(t, "help_table", table)
	one, _ := testutil.CallTool(t, env, spec, map[string]any{"action": "get", "help": "create"})
	testutil.Golden(t, "help_action", one)
	bad, _ := testutil.CallTool(t, env, spec, map[string]any{"action": "help", "help": "zzz"})
	if !strings.HasPrefix(bad, "error: unknown action zzz; valid:") {
		t.Errorf("help of unknown action = %q", bad)
	}
}

func TestLimitAndChars(t *testing.T) {
	spec := dummySpec()
	env := envOK(t, testutil.WithConfig(func(c *config.Config) { c.MaxOutput = 16000 }))
	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"action": "size"}, "limit=16000 chars=12000"},
		{map[string]any{"action": "size", "max_chars": 30000}, "limit=31500 chars=30000"},
		{map[string]any{"action": "size", "max_chars": 500000}, "limit=100000 chars=98500"},
		{map[string]any{"action": "size", "max_chars": 50}, "limit=16000 chars=200"},
		{map[string]any{"action": "size", "full": true}, "limit=40000 chars=12000"},
	}
	for _, tc := range cases {
		if got, _ := testutil.CallTool(t, env, spec, tc.args); got != tc.want {
			t.Errorf("%v: got %q, want %q", tc.args, got, tc.want)
		}
	}
}

func TestCap(t *testing.T) {
	c := &tools.Call{Limit: 1000}
	var lines []string
	for i := 0; i < 200; i++ {
		lines = append(lines, "line "+strconv.Itoa(i)+" aaaaaaaaaaaaaaaa")
	}
	body := strings.Join(lines, "\n")
	out := c.Cap(body + "\nnext_offset: 77\n[truncated: 1 of 2 chars; pass x]")
	if len([]rune(out)) > 1000 {
		t.Errorf("Cap result %d chars > limit", len([]rune(out)))
	}
	for _, want := range []string{"line 0 ", "next_offset: 77", "[truncated: 1 of 2 chars; pass x]", "[truncated: output cut at"} {
		if !strings.Contains(out, want) {
			t.Errorf("Cap lost %q:\n%s", want, out)
		}
	}
	tail := c.CapTail(body)
	if !strings.Contains(tail, "line 199 ") || strings.Contains(tail, "line 0 ") || !strings.HasPrefix(tail, "[truncated: first") {
		t.Errorf("CapTail wrong:\n%s", tail)
	}
	if got := (&tools.Call{Limit: 16000}).Chars(100000, 0); got != 14500 {
		t.Errorf("Chars clamp = %d", got)
	}
}

func TestInstructions(t *testing.T) {
	full := tools.Instructions(tools.InstructionsInfo{
		Host: "forgejo.example.com", Version: "17.0.2", DefaultRepo: "o/r", WaitMax: 55,
		Toolsets: map[string]bool{"meta": true, "pulls": true, "actions": true},
	})
	if n := len([]rune(full)); n > 700 {
		t.Errorf("instructions are %d chars", n)
	}
	testutil.Golden(t, "instructions_full", full)
	noActions := tools.Instructions(tools.InstructionsInfo{Host: "h", WaitMax: 55, Toolsets: map[string]bool{"meta": true, "repos": true}})
	if strings.Contains(noActions, "ci_wait") || strings.Contains(noActions, "ci_failure") || strings.Contains(noActions, "PR flow") {
		t.Errorf("hidden toolset sentences present:\n%s", noActions)
	}
	if !strings.Contains(noActions, "(Forgejo)") || !strings.Contains(noActions, "none: pass repo=") {
		t.Errorf("no-version/no-repo forms wrong:\n%s", noActions)
	}
}

func TestMinVersionHint404(t *testing.T) {
	var hits atomic.Int32
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/version" {
			hits.Add(1)
			_, _ = w.Write([]byte(`{"version":"15.0.0"}`))
			return
		}
		http.NotFound(w, r)
	})
	env := testutil.NewEnv(t, h, testutil.WithRepo("o/r"))
	spec := dummySpec()
	got, _ := testutil.CallTool(t, env, spec, map[string]any{"action": "newer"})
	if !strings.HasPrefix(got, "error: dummy.newer needs Forgejo >= 99 (server is 15.0.0") {
		t.Errorf("got %q", got)
	}
	// run.list needs 16 by the table: gated even without Action.MinMajor
	if msg := tools.CheckMinVersion("run", "list", 0, mustVersion(t, "15.0.0")); !strings.Contains(msg, ">= 16") {
		t.Errorf("table gate = %q", msg)
	}
}

func TestAnnotations(t *testing.T) {
	check := func(name string, readOnly bool, destructive *bool, s tools.Spec) {
		t.Helper()
		env := envOK(t)
		_ = env
		r := tools.NewRegistry()
		r.Add(s)
		if err := r.Build(config.Defaults()); err != nil {
			t.Fatal(err)
		}
		got := toolList(t, r, env)
		a := got[0].Annotations
		if a == nil || a.ReadOnlyHint != readOnly {
			t.Errorf("%s: ReadOnlyHint = %+v", name, a)
			return
		}
		if a.OpenWorldHint == nil || *a.OpenWorldHint {
			t.Errorf("%s: OpenWorldHint not an explicit false", name)
		}
		switch {
		case destructive == nil && a.DestructiveHint != nil:
			t.Errorf("%s: unexpected DestructiveHint", name)
		case destructive != nil && (a.DestructiveHint == nil || *a.DestructiveHint != *destructive):
			t.Errorf("%s: DestructiveHint = %v, want %v", name, a.DestructiveHint, *destructive)
		}
	}
	tr, fa := true, false
	check("dummy", false, &tr, dummySpec())
	rd := dummySpec()
	rd.Actions = rd.Actions[:2]
	check("readonly tool", true, nil, rd)
	wr := dummySpec()
	wr.Actions = wr.Actions[2:3]
	check("write tool", false, &fa, wr)
}
