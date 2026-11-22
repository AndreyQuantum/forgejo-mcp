package issues

import (
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// rec is one request seen by the fake server.
type rec struct {
	Method string
	Path   string // escaped path without /api/v1
	Query  url.Values
	Body   string
	Header http.Header
}

func (r rec) String() string {
	q := ""
	if len(r.Query) > 0 {
		q = "?" + r.Query.Encode()
	}
	return r.Method + " " + r.Path + q
}

// handlerFunc answers one fake route.
type handlerFunc func(w http.ResponseWriter, r *http.Request, body string)

// fake is a recording fake Forgejo server. Routes are keyed "METHOD /path"
// (path relative to /api/v1, escaped form).
type fake struct {
	t      *testing.T
	mu     sync.Mutex
	routes map[string]handlerFunc
	reqs   []rec
}

func jsonH(status int, body string) handlerFunc {
	return func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func listH(total int, body string) handlerFunc {
	return func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Total-Count", strconv.Itoa(total))
		_, _ = io.WriteString(w, body)
	}
}

func emptyH(status int) handlerFunc {
	return func(w http.ResponseWriter, _ *http.Request, _ string) { w.WriteHeader(status) }
}

const repoJSON = `{"id":1,"full_name":"o/r","name":"r","html_url":"http://forgejo.example.com/o/r","owner":{"login":"o"}}`

func newFake(t *testing.T, routes map[string]handlerFunc) *fake {
	t.Helper()
	f := &fake{t: t, routes: map[string]handlerFunc{
		"GET /version":      jsonH(200, `{"version":"17.0.2+gitea-1.22.0"}`),
		"GET /settings/api": jsonH(200, `{"max_response_items":50,"default_paging_num":30}`),
		"GET /user":         jsonH(200, `{"id":7,"login":"me"}`),
		"GET /repos/o/r":    jsonH(200, repoJSON),
	}}
	for k, v := range routes {
		f.routes[k] = v
	}
	return f
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v1")
	rc := rec{Method: r.Method, Path: path, Query: r.URL.Query(), Body: string(b), Header: r.Header.Clone()}
	f.mu.Lock()
	f.reqs = append(f.reqs, rc)
	h := f.routes[r.Method+" "+path]
	f.mu.Unlock()
	if h == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		_, _ = io.WriteString(w, `{"message":"not found: `+r.Method+" "+path+`"}`)
		return
	}
	h(w, r, string(b))
}

// last returns the requests to path with method (in order).
func (f *fake) find(method, path string) []rec {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []rec
	for _, r := range f.reqs {
		if r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

// only returns the single request to method+path or fails.
func (f *fake) only(method, path string) rec {
	f.t.Helper()
	got := f.find(method, path)
	if len(got) != 1 {
		f.t.Fatalf("want exactly one %s %s, got %d; all requests: %v", method, path, len(got), f.all())
	}
	return got[0]
}

func (f *fake) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.reqs))
	for i, r := range f.reqs {
		out[i] = r.String()
	}
	return out
}

func (f *fake) none(method, path string) {
	f.t.Helper()
	if got := f.find(method, path); len(got) != 0 {
		f.t.Fatalf("unexpected %s %s", method, path)
	}
}

// spec returns the registered Spec of one tool.
func spec(t *testing.T, name string) tools.Spec {
	t.Helper()
	r := tools.NewRegistry()
	Register(r)
	for _, s := range r.Specs() {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("tool %s not registered", name)
	return tools.Spec{}
}

// run calls tool through the in-memory MCP stack against a fake server.
func run(t *testing.T, f *fake, tool string, args map[string]any, opts ...testutil.EnvOption) (string, bool) {
	t.Helper()
	opts = append([]testutil.EnvOption{testutil.WithRepo("o/r"), testutil.WithToken("tok-secret-123456")}, opts...)
	env := testutil.NewEnv(t, f, opts...)
	return testutil.CallTool(t, env, spec(t, tool), args)
}

func mustRun(t *testing.T, f *fake, tool string, args map[string]any, opts ...testutil.EnvOption) string {
	t.Helper()
	text, isErr := run(t, f, tool, args, opts...)
	if isErr {
		t.Fatalf("%s %v: unexpected error: %s\nrequests: %v", tool, args, text, f.all())
	}
	return text
}

func mustFail(t *testing.T, f *fake, tool string, args map[string]any, want string, opts ...testutil.EnvOption) string {
	t.Helper()
	text, isErr := run(t, f, tool, args, opts...)
	if !isErr {
		t.Fatalf("%s %v: want an error containing %q, got: %s", tool, args, want, text)
	}
	if !strings.Contains(text, want) {
		t.Fatalf("%s %v: error %q does not contain %q", tool, args, text, want)
	}
	return text
}

func withCfg(f func(*config.Config)) testutil.EnvOption { return testutil.WithConfig(f) }

func contains(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("output lacks %q:\n%s", w, got)
		}
	}
}

func eq(t *testing.T, name, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s:\n got  %q\n want %q", name, got, want)
	}
}

type configT = config.Config

func (f *fake) set(key string, h handlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[key] = h
}
