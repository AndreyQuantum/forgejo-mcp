package testutil

import (
	"testing"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

var zeroTime time.Time

// CallTool registers spec on an in-process MCP server (mcp.NewInMemoryTransports),
// calls it with args through a real SDK client and returns the text of the
// first content block and the IsError flag. WP4 implements the body.
func CallTool(t testing.TB, env *tools.Env, spec tools.Spec, args map[string]any) (text string, isErr bool) {
	t.Helper()
	panic("WP4")
}
