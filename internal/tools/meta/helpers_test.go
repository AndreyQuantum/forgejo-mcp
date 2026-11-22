package meta

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared"
)

// fakeSpec builds a stand-in for another area's tool so policy tests can
// exercise deny, toolsets and destructive templates.
func fakeSpecs() []tools.Spec {
	noop := func(*tools.Call, *struct {
		Action string `json:"action"`
	},
	) (*tools.Result, error) {
		return &tools.Result{Text: "ok"}, nil
	}
	type pp = struct {
		Action string `json:"action"`
	}
	return []tools.Spec{
		{
			Name: "repo", Toolset: "repos", Description: "fake repo tool", Params: pp{}, RepoScoped: true,
			Actions: []tools.Action{
				tools.NewAction("get", tools.Read, noop, tools.Endpoints("GET /repos/{owner}/{repo}")),
				tools.NewAction("delete", tools.Destructive, noop, tools.Endpoints("DELETE /repos/{owner}/{repo}")),
			},
		},
		{
			Name: "label", Toolset: "issues", Description: "fake label tool", Params: pp{}, RepoScoped: true,
			Actions: []tools.Action{
				tools.NewAction("list", tools.Read, noop, tools.Endpoints("GET /repos/{owner}/{repo}/labels")),
				tools.NewAction("create", tools.Write, noop, tools.Endpoints("POST /repos/{owner}/{repo}/labels")),
			},
		},
		{
			Name: "runner", Toolset: "runners", Description: "fake runner tool", Params: pp{}, RepoScoped: true,
			Actions: []tools.Action{
				tools.NewAction("reg_token", tools.Write, noop, tools.Endpoints("GET /repos/{owner}/{repo}/actions/runners/registration-token")),
			},
		},
		{
			Name: "admin", Toolset: "admin", Description: "fake admin tool", Params: pp{},
			Actions: []tools.Action{
				tools.NewAction("users", tools.Read, noop, tools.Endpoints("GET /admin/users")),
			},
		},
	}
}

// newRegistry builds a registry with the meta tools plus the fake specs.
func newRegistry(t testing.TB, cfg *config.Config, withFakes bool) *tools.Registry {
	t.Helper()
	r := tools.NewRegistry()
	Register(r)
	if withFakes {
		for _, s := range fakeSpecs() {
			r.Add(s)
		}
	}
	if err := r.Build(cfg); err != nil {
		t.Fatalf("build: %v", err)
	}
	return r
}

// callReg calls one tool of reg through an in-memory MCP session.
func callReg(t testing.TB, env *tools.Env, reg *tools.Registry, tool string, args map[string]any) (string, bool) {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "t"}, nil)
	if err := reg.Install(srv, env); err != nil {
		t.Fatalf("install: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "t"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return "error: " + err.Error(), true
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

// envFor builds a test Env around handler with cfg tweaks.
func envFor(t testing.TB, h http.Handler, f func(*config.Config), opts ...testutil.EnvOption) (*tools.Env, *tools.Registry) {
	t.Helper()
	opts = append(opts, testutil.WithToken("tok-secret-1234567890"), testutil.WithConfig(func(c *config.Config) {
		if f != nil {
			f(c)
		}
	}))
	env := testutil.NewEnv(t, h, opts...)
	return env, newRegistry(t, env.Cfg, true)
}

const (
	versionJSON = `{"version":"17.0.2+gitea-1.22.0"}`
	settingsAPI = `{"max_response_items":50,"default_paging_num":30}`
)

// baseMux serves /version and /settings/api and delegates the rest.
func baseMux(extra map[string]http.HandlerFunc) http.Handler {
	mux := http.NewServeMux()
	defaults := map[string]http.HandlerFunc{
		"/api/v1/version":      jsonReply(versionJSON),
		"/api/v1/settings/api": jsonReply(settingsAPI),
	}
	for p, h := range extra {
		defaults[p] = h
	}
	for p, h := range defaults {
		mux.HandleFunc(p, h)
	}
	return mux
}

func jsonReply(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func testutilRepo(ownerName string) testutil.EnvOption { return testutil.WithRepo(ownerName) }

// newEnvNoToken builds an Env without a token.
func newEnvNoToken(t testing.TB, h http.Handler) *tools.Env {
	t.Helper()
	return testutil.NewEnv(t, h)
}

// fileRootsWork reports whether shared.FileRoots is implemented (WP6 may
// still be a stub that panics).
func fileRootsWork(env *tools.Env) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	r := &shared.FileRoots{SaveDir: env.Cfg.SaveDir}
	_, _, err := r.SaveFile("probe.tmp", strings.NewReader(""))
	return err == nil
}
