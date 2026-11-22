package tools

import (
	"context"
	"fmt"
	"sync"

	"github.com/AndreyQuantum/forgejo-mcp/internal/clock"
	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/gitremote"
)

// Env is the shared runtime environment of every tool.
type Env struct {
	Client *forgejo.Client
	Cfg    *config.Config
	Repo   *gitremote.Detector
	Clock  clock.Clock
	// Memo holds in-process facts shared between tools, for example
	// Memo.Set("automerge:o/r#42") after a 201 from pr merge.
	Memo *Memo
}

// Memo is a mutex-protected string set.
type Memo struct {
	mu sync.Mutex
	m  map[string]struct{}
}

// Set records key.
func (m *Memo) Set(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m = map[string]struct{}{}
	}
	m.m[key] = struct{}{}
}

// Has reports whether key was recorded.
func (m *Memo) Has(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.m[key]
	return ok
}

// Call is the per-invocation context handed to Action.Run.
type Call struct {
	Ctx         context.Context
	Limit       int // effective output cap for this call (6.6)
	Env         *Env
	Action      string
	Owner, Repo string // resolved when RepoScoped (empty otherwise)
	// Progress sends a progress notification; no-op when the client sent no
	// progress token.
	Progress func(msg string, done, total float64)
}

// Result is a tool answer. Text is already capped by Cap/CapTail.
type Result struct {
	Text    string
	IsError bool
}

// Cap is the last-resort cap to Call.Limit, head-keeping, cut at a line
// boundary; trailer lines survive (see format.CapHead).
func (c *Call) Cap(s string) string { return format.CapHead(s, c.limit()) }

// CapTail is like Cap but keeps the END and cuts the beginning.
func (c *Call) CapTail(s string) string { return format.CapTail(s, c.limit()) }

// Chars returns the effective per-action size: requested (or def) clamped to
// [200, Limit-1500] (section 6.6).
func (c *Call) Chars(requested, def int) int {
	n := requested
	if n <= 0 {
		n = def
	}
	hi := c.limit() - 1500
	if hi < 200 {
		hi = 200
	}
	return min(max(n, 200), hi)
}

// limit is Limit with a safe default for hand-built Calls (tests).
func (c *Call) limit() int {
	if c.Limit <= 0 {
		return 16000
	}
	return c.Limit
}

// Confirm checks the `confirm` argument of a destructive action against the
// exact target name; it returns nil when they match.
func (c *Call) Confirm(got, want string) *Result {
	if got == want && want != "" {
		return nil
	}
	return c.Fail("destructive: pass confirm=%q", want)
}

// Fail returns an error Result with the text "error: <formatted>".
func (c *Call) Fail(format string, a ...any) *Result {
	return &Result{Text: "error: " + fmt.Sprintf(format, a...), IsError: true}
}
