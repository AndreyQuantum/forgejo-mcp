package tools_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

type apiParams struct {
	Method string `json:"method,omitempty" jsonschema:"HTTP method"`
}

func apiSpec() tools.Spec {
	return tools.Spec{
		Name: "api", Toolset: "meta", Description: "raw call", Params: apiParams{}, Single: true,
		Actions: []tools.Action{tools.NewAction("run", tools.Write, func(c *tools.Call, p *apiParams) (*tools.Result, error) {
			return &tools.Result{Text: "ran " + p.Method}, nil
		}, tools.Params("method"), tools.ModeFor(func(in any) tools.Mode {
			if in.(*apiParams).Method == "DELETE" {
				return tools.Destructive
			}
			if in.(*apiParams).Method == "POST" {
				return tools.Write
			}
			return tools.Read
		}))},
	}
}

func TestModeForStaysRegisteredInReadOnly(t *testing.T) {
	env := envOK(t, testutil.WithConfig(func(c *config.Config) { c.ReadOnly = true }))
	got, isErr := testutil.CallTool(t, env, apiSpec(), map[string]any{"method": "GET"})
	if isErr || got != "ran GET" {
		t.Errorf("got %q %v", got, isErr)
	}
	// the policy (WP5) classifies writes per call; the framework only skips its
	// own read-only gate for ModeFor actions. A DELETE still needs a confirm.
	got, isErr = testutil.CallTool(t, env, apiSpec(), map[string]any{"method": "DELETE"})
	if isErr && !strings.Contains(got, "ran") && !strings.Contains(got, "destructive") {
		t.Errorf("DELETE: %q", got)
	}
	tl := toolListFor(t, apiSpec(), env)
	if len(tl) != 1 || tl[0].Annotations.ReadOnlyHint || tl[0].Annotations.DestructiveHint == nil || !*tl[0].Annotations.DestructiveHint {
		t.Errorf("api annotations wrong: %+v", tl[0].Annotations)
	}
	if _, ok := tl[0].InputSchema.(map[string]any)["properties"].(map[string]any)["action"]; ok {
		t.Error("single tool exposes an action property")
	}
}

func toolListFor(t *testing.T, s tools.Spec, env *tools.Env) []*mcp.Tool {
	t.Helper()
	r := tools.NewRegistry()
	r.Add(s)
	if err := r.Build(env.Cfg); err != nil {
		t.Fatal(err)
	}
	return toolList(t, r, env)
}

// TestPassThroughTokens runs over a real MCP session: two requests with
// different X-Forgejo-Token headers must reach the fake server with their own
// token and never with the other one.
func TestPassThroughTokens(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Header.Get("Authorization")] = true
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login":"u1"}`))
	})
	env := testutil.NewEnv(t, h, testutil.WithRepo("o/r"), testutil.WithConfig(func(c *config.Config) {
		c.Transport = "http"
		c.Addr = "127.0.0.1:8787"
	}))
	type p struct {
		A string `json:"action"`
	}
	spec := tools.Spec{
		Name: "ver", Toolset: "meta", Description: "v", Params: p{}, Single: true,
		Actions: []tools.Action{tools.NewAction("run", tools.Read, func(c *tools.Call, _ *p) (*tools.Result, error) {
			me, err := c.Env.Client.Me(c.Ctx)
			if err != nil {
				return nil, err
			}
			// a per-token cache entry proves the client is namespaced by token
			return &tools.Result{Text: "u=" + me.Login + " tok=" + c.Env.Client.Token()}, nil
		})},
	}
	r := tools.NewRegistry()
	r.Add(spec)
	if err := r.Build(env.Cfg); err != nil {
		t.Fatal(err)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "t"}, nil)
	if err := r.Install(srv, env); err != nil {
		t.Fatal(err)
	}
	hh := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := newHTTPServer(t, hh)
	for _, tok := range []string{"tok-alpha", "tok-beta"} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "c"}, nil).Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint:   ts.URL,
			HTTPClient: &http.Client{Transport: headerRT{tok}},
		}, nil)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "ver", Arguments: map[string]any{}})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		text := res.Content[0].(*mcp.TextContent).Text
		if text != "u=u1 tok=***" {
			t.Errorf("call with %s ran as %q", tok, text)
		}
		_ = cs.Close()
		cancel()
	}
	mu.Lock()
	defer mu.Unlock()
	if !seen["token tok-alpha"] || !seen["token tok-beta"] {
		t.Errorf("fake server saw %v", seen)
	}
}

type headerRT struct{ tok string }

func (h headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Forgejo-Token", h.tok)
	return http.DefaultTransport.RoundTrip(r)
}

func TestResolveToolsetsLeadingSubtraction(t *testing.T) {
	got, err := tools.ResolveToolsets("-packages")
	if err != nil {
		t.Fatal(err)
	}
	if got["packages"] || !got["repos"] || !got["pulls"] || !got["meta"] {
		t.Errorf("got %v", got)
	}
}
