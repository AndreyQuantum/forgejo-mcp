package pulls

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/clock"
	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// seen is one request the fake server received.
type seen struct {
	Method string
	Path   string // escaped path without /api/v1
	Query  string // canonical (sorted) query
	Body   string
}

func (s seen) String() string {
	q := ""
	if s.Query != "" {
		q = "?" + s.Query
	}
	return s.Method + " " + s.Path + q
}

// fake is a recording fake Forgejo server.
type fake struct {
	mu     sync.Mutex
	calls  []seen
	routes map[string]http.HandlerFunc
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v1")
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, seen{Method: r.Method, Path: path, Query: r.URL.Query().Encode(), Body: string(body)})
	h := f.routes[r.Method+" "+path]
	f.mu.Unlock()
	if h == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"no such route in the fake: ` + r.Method + " " + path + `"}`))
		return
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	h(w, r)
}

// requests returns the recorded requests of method+path (path without query).
func (f *fake) requests(method, path string) []seen {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []seen
	for _, c := range f.calls {
		if c.Method == method && c.Path == path {
			out = append(out, c)
		}
	}
	return out
}

func (f *fake) count(method, path string) int { return len(f.requests(method, path)) }

func (f *fake) last(t *testing.T, method, path string) seen {
	t.Helper()
	rs := f.requests(method, path)
	if len(rs) == 0 {
		t.Fatalf("no %s %s request; saw:\n%s", method, path, f.dump())
	}
	return rs[len(rs)-1]
}

func (f *fake) dump() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var b strings.Builder
	for _, c := range f.calls {
		b.WriteString(c.String() + "\n")
	}
	return b.String()
}

// jsonBody serves body with status 200.
func jsonBody(body string) http.HandlerFunc { return status(200, body) }

func status(code int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

// withTotal serves body with an X-Total-Count header.
func withTotal(total int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Total-Count", fmt.Sprint(total))
		_, _ = w.Write([]byte(body))
	}
}

// seq answers successive requests with successive handlers; the last repeats.
func seq(hs ...http.HandlerFunc) http.HandlerFunc {
	var mu sync.Mutex
	i := 0
	return func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		h := hs[min(i, len(hs)-1)]
		i++
		mu.Unlock()
		h(w, r)
	}
}

// repoJSON is the repo meta served for GET /repos/o/r.
const repoJSON = `{"full_name":"o/r","default_branch":"main","html_url":"https://forge.example/o/r",
"allow_merge_commits":true,"allow_rebase":true,"allow_rebase_explicit":true,"allow_squash_merge":true,
"allow_fast_forward_only_merge":false,"default_merge_style":"merge","default_delete_branch_after_merge":false}`

// mkPR returns a PR JSON object; mod may adjust the generic fields.
func mkPR(n int, mod func(m map[string]any)) string {
	m := map[string]any{
		"number":     n,
		"title":      fmt.Sprintf("Fix thing %d", n),
		"state":      "open",
		"draft":      false,
		"merged":     false,
		"mergeable":  true,
		"user":       map[string]any{"login": "alice", "email": "a@example.invalid"},
		"base":       map[string]any{"label": "main", "ref": "main", "sha": "b" + strings.Repeat("0", 39), "repo": map[string]any{"full_name": "o/r"}},
		"head":       map[string]any{"label": "fix/login", "ref": "fix/login", "sha": "8a1b2c3d" + strings.Repeat("e", 32), "repo": map[string]any{"full_name": "o/r"}},
		"labels":     []any{map[string]any{"name": "bug"}},
		"assignees":  []any{map[string]any{"login": "bob"}},
		"created_at": "2026-11-21T09:00:00Z",
		"updated_at": "2026-11-22T10:15:00Z",
		"html_url":   fmt.Sprintf("https://forge.example/o/r/pulls/%d", n),
		"body":       "Fixes the race.",
		"additions":  12, "deletions": 3, "changed_files": 2, "comments": 3,
	}
	if mod != nil {
		mod(m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// newFake builds a fake server with the default routes plus extra, an Env
// around it and the registered tool specs.
func newFake(t *testing.T, extra map[string]http.HandlerFunc, opts ...testutil.EnvOption) (*tools.Env, *fake) {
	t.Helper()
	f := &fake{routes: map[string]http.HandlerFunc{
		"GET /version":              jsonBody(`{"version":"17.0.2+gitea-1.22.0"}`),
		"GET /settings/api":         jsonBody(`{"default_paging_num":30,"max_response_items":50}`),
		"GET /user":                 jsonBody(`{"id":1,"login":"me"}`),
		"GET /repos/o/r":            jsonBody(repoJSON),
		"GET /repos/o/r/labels":     jsonBody(`[{"id":5,"name":"bug"},{"id":6,"name":"ui"}]`),
		"GET /orgs/o/labels":        status(404, `{"message":"not found"}`),
		"GET /repos/o/r/milestones": jsonBody(`[{"id":3,"title":"v1.2","state":"open"}]`),
	}}
	for k, v := range extra {
		f.routes[k] = v
	}
	opts = append([]testutil.EnvOption{testutil.WithRepo("o/r"), testutil.WithToken("tok-secret")}, opts...)
	return testutil.NewEnv(t, f, opts...), f
}

func specOf(t *testing.T, name string) tools.Spec {
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

// run calls tool through the real MCP stack.
func run(t *testing.T, env *tools.Env, tool string, args map[string]any) (string, bool) {
	t.Helper()
	return testutil.CallTool(t, env, specOf(t, tool), args)
}

func mustOK(t *testing.T, env *tools.Env, tool string, args map[string]any) string {
	t.Helper()
	out, isErr := run(t, env, tool, args)
	if isErr {
		t.Fatalf("%s %v failed: %s", tool, args, out)
	}
	return out
}

func mustErr(t *testing.T, env *tools.Env, tool string, args map[string]any) string {
	t.Helper()
	out, isErr := run(t, env, tool, args)
	if !isErr {
		t.Fatalf("%s %v: expected an error, got: %s", tool, args, out)
	}
	return out
}

// autoAdvance moves the fake clock forward whenever a timer is pending, so
// sleeps in the code under test return at once. It stops with the test.
func autoAdvance(t *testing.T, env *tools.Env) {
	t.Helper()
	fc, ok := env.Clock.(*clock.Fake)
	if !ok {
		t.Fatalf("env clock is %T", env.Clock)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for ctx.Err() == nil {
			if fc.BlockUntil(ctx, 1) {
				fc.Advance(10 * time.Second)
			}
		}
	}()
}

func contains(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("output lacks %q:\n%s", w, got)
		}
	}
}

func notContains(t *testing.T, got string, bad ...string) {
	t.Helper()
	for _, w := range bad {
		if strings.Contains(got, w) {
			t.Errorf("output contains %q:\n%s", w, got)
		}
	}
}

// jsonOf decodes a recorded request body.
func jsonOf(t *testing.T, s seen) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s.Body), &m); err != nil {
		t.Fatalf("body of %s is not a JSON object: %q", s, s.Body)
	}
	return m
}
