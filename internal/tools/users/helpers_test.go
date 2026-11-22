package users

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// reply is a canned answer of the fake server.
type reply struct {
	status int // 0 = 200
	body   string
	total  string // X-Total-Count
}

// fake is a recording Forgejo stand-in. Routes are keyed "METHOD /path".
type fake struct {
	mu     sync.Mutex
	routes map[string]reply
	reqs   []string
}

func newFake(routes map[string]reply) *fake {
	f := &fake{routes: map[string]reply{
		"GET /version":      {body: `{"version":"17.0.2+gitea-1.22.0"}`},
		"GET /settings/api": {body: `{"max_response_items":50,"default_paging_num":30}`},
	}}
	for k, v := range routes {
		f.routes[k] = v
	}
	return f
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	key := r.Method + " " + path
	f.mu.Lock()
	rep, found := f.routes[key]
	if path != "/version" && path != "/settings/api" {
		line := r.Method + " " + path
		if q := r.URL.Query().Encode(); q != "" {
			line += "?" + q
		}
		if len(body) > 0 {
			var v any
			if json.Unmarshal(body, &v) == nil {
				b, _ := json.Marshal(v)
				body = b
			}
			line += " " + string(body)
		}
		f.reqs = append(f.reqs, line)
	}
	f.mu.Unlock()
	if !found {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"not routed: ` + key + `"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if rep.total != "" {
		w.Header().Set("X-Total-Count", rep.total)
	}
	st := rep.status
	if st == 0 {
		st = http.StatusOK
	}
	w.WriteHeader(st)
	_, _ = w.Write([]byte(rep.body))
}

func (f *fake) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reqs...)
}

// spec returns the registered Spec of one tool of this package.
func spec(t testing.TB, name string) tools.Spec {
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

// run calls tool through the real MCP stack against the fake.
func run(t testing.TB, f *fake, tool string, args map[string]any) (string, bool) {
	t.Helper()
	env := testutil.NewEnv(t, f, testutil.WithToken("tok-secret-1234567890"))
	return testutil.CallTool(t, env, spec(t, tool), args)
}

const (
	meAdmin = `{"id":4,"login":"alice","full_name":"Alice A","email":"alice@example.com","is_admin":true,"active":true,"created":"2026-01-02T03:04:05Z","visibility":"public","followers_count":3,"following_count":2,"starred_repos_count":5,"avatar_url":"x","html_url":"y"}`
	meUser  = `{"id":5,"login":"bob","full_name":"Bob B","is_admin":false,"active":true,"created":"2026-02-03T04:05:06Z"}`
)
