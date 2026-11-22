package ci

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/clock"
	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// routes is a tiny fake Forgejo: keys are "METHOD /path" (without /api/v1),
// values are a body string (status 200) or an http.HandlerFunc. Unknown
// routes answer 404 and are recorded.
type routes map[string]any

type fakeServer struct {
	mu      sync.Mutex
	routes  routes
	calls   []string
	bodies  []string
	missing []string
}

func newFake(r routes) *fakeServer {
	f := &fakeServer{routes: r}
	if _, ok := f.routes["GET /settings/api"]; !ok {
		f.routes["GET /settings/api"] = `{"default_paging_num":20,"max_response_items":50}`
	}
	return f
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	key := r.Method + " " + strings.TrimPrefix(r.URL.EscapedPath(), "/api/v1")
	f.calls = append(f.calls, key)
	body, _ := io.ReadAll(r.Body)
	f.bodies = append(f.bodies, string(body))
	h, ok := f.routes[key]
	if !ok {
		f.missing = append(f.missing, r.URL.RequestURI())
	}
	f.mu.Unlock()
	switch v := h.(type) {
	case string:
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(v))
	case func(w http.ResponseWriter, r *http.Request):
		v(w, r)
	case http.HandlerFunc:
		v(w, r)
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"not found"}`))
	}
}

func (f *fakeServer) called(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == key {
			n++
		}
	}
	return n
}

func (f *fakeServer) body(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, c := range f.calls {
		if c == key {
			return f.bodies[i]
		}
	}
	return ""
}

// recClock is a fake clock whose Sleep advances time at once.
type recClock struct {
	*clock.Fake
	mu     sync.Mutex
	sleeps []time.Duration
}

func newRecClock() *recClock { return &recClock{Fake: clock.NewFake(time.Time{})} }

func (c *recClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.sleeps = append(c.sleeps, d)
	c.mu.Unlock()
	c.Advance(d)
	return nil
}

// env builds a test Env around the fake with repo o/r as default and a
// recording clock.
func (f *fakeServer) env(t *testing.T, opts ...testutil.EnvOption) *tools.Env {
	t.Helper()
	opts = append([]testutil.EnvOption{testutil.WithRepo("o/r")}, opts...)
	env := testutil.NewEnv(t, f, opts...)
	env.Clock = newRecClock()
	return env
}

// specOf registers the tools of this package and returns the named Spec.
func specOf(t *testing.T, name string) tools.Spec {
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

// call runs one tool through the in-memory MCP stack.
func call(t *testing.T, env *tools.Env, tool string, args map[string]any) (string, bool) {
	t.Helper()
	return testutil.CallTool(t, env, specOf(t, tool), args)
}

// gitDir makes env's detector resolve a checkout of o/r on branch br
// (remote = the fake server itself).
func gitDir(t *testing.T, env *tools.Env, br string) {
	t.Helper()
	dir := filepath.Join(env.Cfg.Dir, ".git")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf("[remote \"origin\"]\n\turl = %s/o/r.git\n", env.Cfg.URL)
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte("ref: refs/heads/"+br+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// withCfg returns an env option mutating the config.
func withCfg(f func(*config.Config)) testutil.EnvOption { return testutil.WithConfig(f) }

const sha40 = "8a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8091"

// envNoRepo is like env but without a default repository.
func (f *fakeServer) envNoRepo(t *testing.T) *tools.Env {
	t.Helper()
	env := testutil.NewEnv(t, f)
	env.Clock = newRecClock()
	return env
}

func withReadOnly() testutil.EnvOption {
	return testutil.WithConfig(func(c *config.Config) { c.ReadOnly = true })
}
