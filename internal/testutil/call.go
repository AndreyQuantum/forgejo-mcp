package testutil

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// zeroTime is the fixed start of every FakeClock.
var zeroTime = time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)

// CallTool registers spec on an in-process MCP server (mcp.NewInMemoryTransports),
// calls it with args through a real SDK client and returns the text of the
// first content block and the IsError flag. The registry is built from
// env.Cfg, so toolsets, FORGEJO_DENY and read-only apply; a tool that the
// filters remove yields the SDK's "unknown tool" error text with isErr true.
func CallTool(t testing.TB, env *tools.Env, spec tools.Spec, args map[string]any) (text string, isErr bool) {
	t.Helper()
	reg := tools.NewRegistry()
	reg.Add(spec)
	if err := reg.Build(env.Cfg); err != nil {
		t.Fatalf("registry build: %v", err)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "forgejo-mcp-test", Version: "test"}, nil)
	if err := reg.Install(srv, env); err != nil {
		t.Fatalf("registry install: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: spec.Name, Arguments: args})
	if err != nil {
		return "error: " + err.Error(), true
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}
