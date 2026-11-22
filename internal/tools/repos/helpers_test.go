package repos

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// reply is a canned answer of the fake server.
type reply struct {
	status int
	body   string
	header map[string]string
}

func jsonOK(body string) reply { return reply{status: 200, body: body} }

func withTotal(body string, total string) reply {
	return reply{status: 200, body: body, header: map[string]string{"X-Total-Count": total}}
}

// rec is one recorded request.
type rec struct {
	Method string
	Path   string // escaped path without /api/v1
	Query  url.Values
	Body   string
}

// fake is a recording Forgejo stand-in: routes are keyed "METHOD /escaped/path".
type fake struct {
	t      *testing.T
	mu     sync.Mutex
	routes map[string]func(r *http.Request) reply
	reqs   []rec
}

func newFake(t *testing.T) *fake {
	t.Helper()
	return &fake{t: t, routes: map[string]func(*http.Request) reply{}}
}

// on registers a static answer.
func (f *fake) on(key string, r reply) *fake {
	f.routes[key] = func(*http.Request) reply { return r }
	return f
}

func (f *fake) onFunc(key string, fn func(r *http.Request) reply) *fake {
	f.routes[key] = fn
	return f
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v1")
	w.Header().Set("Content-Type", "application/json")
	switch path {
	case "/version":
		_, _ = w.Write([]byte(`{"version":"17.0.2+gitea-1.22.0"}`))
		return
	case "/settings/api":
		_, _ = w.Write([]byte(`{"max_response_items":50,"default_paging_num":30}`))
		return
	}
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.reqs = append(f.reqs, rec{r.Method, path, r.URL.Query(), string(b)})
	fn := f.routes[r.Method+" "+path]
	f.mu.Unlock()
	if fn == nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"no route in fake: ` + r.Method + " " + path + `"}`))
		return
	}
	rp := fn(r)
	for k, v := range rp.header {
		w.Header().Set(k, v)
	}
	if rp.status == 0 {
		rp.status = 200
	}
	w.WriteHeader(rp.status)
	_, _ = w.Write([]byte(rp.body))
}

// requests returns the recorded requests (version and settings excluded).
func (f *fake) requests() []rec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rec(nil), f.reqs...)
}

// last returns the last recorded request.
func (f *fake) last() rec {
	rs := f.requests()
	if len(rs) == 0 {
		f.t.Fatal("no request recorded")
	}
	return rs[len(rs)-1]
}

func (f *fake) count(method, path string) int {
	n := 0
	for _, r := range f.requests() {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

func (f *fake) bodyMap(r rec) map[string]any {
	f.t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(r.Body), &m); err != nil {
		f.t.Fatalf("body of %s %s is not a JSON object: %q", r.Method, r.Path, r.Body)
	}
	return m
}

// specOf returns the Spec of a tool of this package.
func specOf(t testing.TB, name string) tools.Spec {
	t.Helper()
	r := tools.NewRegistry()
	Register(r)
	for _, s := range r.Specs() {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no tool %s", name)
	return tools.Spec{}
}

// run calls tool through the real in-memory MCP stack.
func run(t *testing.T, f *fake, tool string, args map[string]any, opts ...testutil.EnvOption) (string, bool) {
	t.Helper()
	opts = append([]testutil.EnvOption{testutil.WithToken("tok-secret-1234567890"), testutil.WithRepo("o/r")}, opts...)
	env := testutil.NewEnv(t, f, opts...)
	return testutil.CallTool(t, env, specOf(t, tool), args)
}

func mustOK(t *testing.T, f *fake, tool string, args map[string]any, opts ...testutil.EnvOption) string {
	t.Helper()
	out, isErr := run(t, f, tool, args, opts...)
	if isErr {
		t.Fatalf("%s %v failed:\n%s", tool, args, out)
	}
	return out
}

func mustErr(t *testing.T, f *fake, tool string, args map[string]any, opts ...testutil.EnvOption) string {
	t.Helper()
	out, isErr := run(t, f, tool, args, opts...)
	if !isErr {
		t.Fatalf("%s %v: expected an error, got:\n%s", tool, args, out)
	}
	return out
}

func newConfig(readOnly bool) *config.Config {
	c := config.Defaults()
	c.ReadOnly = readOnly
	return c
}
