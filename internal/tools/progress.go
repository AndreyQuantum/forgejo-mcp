package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// progressFunc returns the Call.Progress function for one request: it sends
// notifications/progress when the client supplied a progress token and is a
// no-op otherwise. Send errors are ignored (a dropped notification must never
// fail a tool call).
func progressFunc(ctx context.Context, req *mcp.CallToolRequest) func(msg string, done, total float64) {
	noop := func(string, float64, float64) {}
	if req == nil || req.Params == nil || req.Session == nil {
		return noop
	}
	tok := req.Params.GetProgressToken()
	if tok == nil {
		return noop
	}
	ss := req.Session
	return func(msg string, done, total float64) {
		_ = ss.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
			ProgressToken: tok,
			Message:       msg,
			Progress:      done,
			Total:         total,
		})
	}
}
