package content

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// rec is one recorded request of the fake server.
type rec struct {
	Method, Path, Query string
	Body                []byte
	Header              http.Header
}

// fake is a recording fake Forgejo. Routes are keyed "METHOD /path" (the
// escaped path without /api/v1).
type fake struct {
	mu     sync.Mutex
	log    []rec
	routes map[string]http.HandlerFunc
}

func newFake(routes map[string]http.HandlerFunc) *fake {
	f := &fake{routes: map[string]http.HandlerFunc{
		"GET /version":      js(`{"version":"17.0.2+gitea-1.22.0"}`),
		"GET /settings/api": js(`{"max_response_items":50,"default_paging_num":30}`),
		"GET /repos/o/r":    js(`{"full_name":"o/r","default_branch":"main","html_url":"https://forgejo.example.com/o/r"}`),
	}}
	for k, v := range routes {
		f.routes[k] = v
	}
	return f
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v1")
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.log = append(f.log, rec{r.Method, p, r.URL.RawQuery, body, r.Header.Clone()})
	f.mu.Unlock()
	r.Body = io.NopCloser(bytes.NewReader(body))
	if h, ok := f.routes[r.Method+" "+p]; ok {
		h(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"message":"not found"}`))
}

// find returns the recorded requests of method and exact path.
func (f *fake) find(method, path string) []rec {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []rec
	for _, r := range f.log {
		if r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

// only returns the single request of method+path (fails otherwise).
func (f *fake) only(t *testing.T, method, path string) rec {
	t.Helper()
	rs := f.find(method, path)
	if len(rs) != 1 {
		t.Fatalf("%s %s: %d requests, want 1 (log: %v)", method, path, len(rs), f.paths())
	}
	return rs[0]
}

func (f *fake) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.log {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

func js(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func jsTotal(total, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Total-Count", total)
		_, _ = w.Write([]byte(body))
	}
}

func status(code int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

func text(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}
}

// specOf builds the Spec registered by register.
func specOf(t testing.TB, register func(*tools.Registry)) tools.Spec {
	t.Helper()
	r := tools.NewRegistry()
	register(r)
	if len(r.Specs()) != 1 {
		t.Fatalf("expected one spec, got %d", len(r.Specs()))
	}
	return r.Specs()[0]
}

type toolEnv struct {
	t    *testing.T
	env  *tools.Env
	spec tools.Spec
	fake *fake
}

// newTool wires a fake server and an Env around one tool.
func newTool(t *testing.T, register func(*tools.Registry), routes map[string]http.HandlerFunc, opts ...testutil.EnvOption) *toolEnv {
	t.Helper()
	f := newFake(routes)
	opts = append([]testutil.EnvOption{testutil.WithRepo("o/r"), testutil.WithToken("tok-secret-123456")}, opts...)
	env := testutil.NewEnv(t, f, opts...)
	return &toolEnv{t: t, env: env, spec: specOf(t, register), fake: f}
}

func (te *toolEnv) call(args map[string]any) (string, bool) {
	te.t.Helper()
	return testutil.CallTool(te.t, te.env, te.spec, args)
}

func (te *toolEnv) ok(args map[string]any) string {
	te.t.Helper()
	out, isErr := te.call(args)
	if isErr {
		te.t.Fatalf("unexpected error for %v:\n%s", args, out)
	}
	return out
}

func (te *toolEnv) fail(args map[string]any) string {
	te.t.Helper()
	out, isErr := te.call(args)
	if !isErr {
		te.t.Fatalf("expected an error for %v, got:\n%s", args, out)
	}
	return out
}

func jsonMap(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("body is not a JSON object: %v: %s", err, b)
	}
	return m
}

func contains(t *testing.T, got string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(got, s) {
			t.Errorf("output lacks %q:\n%s", s, got)
		}
	}
}

func lacks(t *testing.T, got string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if strings.Contains(got, s) {
			t.Errorf("output must not contain %q:\n%s", s, got)
		}
	}
}

// withSaveDir overrides the config save dir so tests can inspect files.
func withConfig(f func(*config.Config)) testutil.EnvOption { return testutil.WithConfig(f) }

// specContract checks the invariants every Spec of the area must hold:
// every Params/Required name is a field of the params struct, every action
// has endpoints and a summary, descriptions fit the budget.
func specContract(t *testing.T, s tools.Spec) {
	t.Helper()
	typ := reflect.TypeOf(s.Params)
	fields := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		fields[name] = true
	}
	for _, a := range s.Actions {
		if a.Summary == "" || len(a.Endpoints) == 0 {
			t.Errorf("%s.%s: missing summary or endpoints", s.Name, a.Name)
		}
		for _, n := range append(append([]string{}, a.Params...), a.Required...) {
			if !fields[n] {
				t.Errorf("%s.%s: param %q is not a field of the params struct", s.Name, a.Name, n)
			}
		}
		// every endpoint of a write action must be a mutation: helper GETs
		// would otherwise block reads through forgejo_api when it is denied
		if a.Mode != tools.Read {
			for _, ep := range a.Endpoints {
				if strings.HasPrefix(ep, "GET ") {
					t.Errorf("%s.%s: write action lists the GET endpoint %q", s.Name, a.Name, ep)
				}
			}
		}
	}
	if n := len([]rune(s.Description)); n > tools.BudgetDescriptionChars {
		t.Errorf("%s: description has %d chars", s.Name, n)
	}
	sc, err := tools.BuildInputSchema(s, s.Actions)
	if err != nil {
		t.Fatalf("%s: schema: %v", s.Name, err)
	}
	raw, _ := json.Marshal(sc)
	if len(raw) > tools.BudgetToolBytes {
		t.Errorf("%s: schema is %d bytes, budget %d", s.Name, len(raw), tools.BudgetToolBytes)
	}
}
