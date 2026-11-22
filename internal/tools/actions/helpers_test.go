package actions

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// rec is one request seen by the fake server.
type rec struct {
	Method string
	Path   string // escaped, without /api/v1
	Query  url.Values
	Body   string
	Header http.Header
}

func (r rec) key() string { return r.Method + " " + r.Path }

// fakeServer is a recording Forgejo stand-in. Routes are keyed
// "METHOD /escaped/path" (without /api/v1) and may be overwritten per test.
type fakeServer struct {
	t      *testing.T
	mu     sync.Mutex
	routes map[string]http.HandlerFunc
	reqs   []rec
}

const defaultSettings = `{"max_response_items":50,"default_paging_num":30}`

func newFake(t *testing.T) *fakeServer {
	t.Helper()
	s := &fakeServer{t: t, routes: map[string]http.HandlerFunc{}}
	s.reply("GET /version", 200, `{"version":"17.0.2+gitea-1.22.0"}`)
	s.reply("GET /settings/api", 200, defaultSettings)
	s.reply("GET /repos/o/r", 200, `{"full_name":"o/r","default_branch":"main","html_url":"http://example/o/r"}`)
	return s
}

// on registers a handler.
func (s *fakeServer) on(key string, h http.HandlerFunc) { s.routes[key] = h }

// reply registers a fixed JSON answer.
func (s *fakeServer) reply(key string, status int, body string) {
	s.on(key, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

// replyTotal is reply with an X-Total-Count header.
func (s *fakeServer) replyTotal(key string, total, body string) {
	s.on(key, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Total-Count", total)
		_, _ = io.WriteString(w, body)
	})
}

func (s *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v1")
	rc := rec{Method: r.Method, Path: path, Query: r.URL.Query(), Body: string(body), Header: r.Header.Clone()}
	s.mu.Lock()
	s.reqs = append(s.reqs, rc)
	h := s.routes[rc.key()]
	s.mu.Unlock()
	if h == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		_, _ = io.WriteString(w, `{"message":"not found"}`)
		return
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	h(w, r)
}

// seen returns the recorded requests of "METHOD /path" (in order).
func (s *fakeServer) seen(key string) []rec {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []rec
	for _, r := range s.reqs {
		if r.key() == key {
			out = append(out, r)
		}
	}
	return out
}

// only returns the single request of key or fails the test.
func (s *fakeServer) only(key string) rec {
	s.t.Helper()
	got := s.seen(key)
	if len(got) != 1 {
		s.t.Fatalf("want exactly one %s, got %d (all: %v)", key, len(got), s.keys())
	}
	return got[0]
}

func (s *fakeServer) keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.reqs))
	for i, r := range s.reqs {
		out[i] = r.key()
	}
	return out
}

// callRun calls the run tool against s through the real MCP stack.
func callRun(t *testing.T, s *fakeServer, args map[string]any, opts ...testutil.EnvOption) (string, bool) {
	t.Helper()
	_, text, isErr := callRunEnv(t, s, args, opts...)
	return text, isErr
}

func callRunEnv(t *testing.T, s *fakeServer, args map[string]any, opts ...testutil.EnvOption) (*tools.Env, string, bool) {
	t.Helper()
	opts = append([]testutil.EnvOption{testutil.WithRepo("o/r"), testutil.WithToken("tok-secret-123456")}, opts...)
	env := testutil.NewEnv(t, s, opts...)
	text, isErr := testutil.CallTool(t, env, runSpec(), args)
	return env, text, isErr
}

// runOK calls the tool and fails the test on an error result.
func runOK(t *testing.T, s *fakeServer, args map[string]any, opts ...testutil.EnvOption) string {
	t.Helper()
	text, isErr := callRun(t, s, args, opts...)
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}
	return text
}

// runErr calls the tool and fails the test unless the result is an error.
func runErr(t *testing.T, s *fakeServer, args map[string]any, opts ...testutil.EnvOption) string {
	t.Helper()
	text, isErr := callRun(t, s, args, opts...)
	if !isErr {
		t.Fatalf("expected an error, got: %s", text)
	}
	return text
}

func wantQuery(t *testing.T, r rec, want map[string][]string) {
	t.Helper()
	for k, v := range want {
		got := r.Query[k]
		if strings.Join(got, ",") != strings.Join(v, ",") {
			t.Errorf("%s query %s = %v, want %v (all: %v)", r.key(), k, got, v, r.Query)
		}
	}
}
