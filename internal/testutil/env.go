package testutil

import (
	"net/http"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/clock"
	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// FakeClock is the manually advanced clock used by tests.
type FakeClock = clock.Fake

// NewFakeClock returns a FakeClock starting at a fixed date.
func NewFakeClock() *FakeClock { return clock.NewFake(zeroTime) }

// EnvOption customises NewEnv.
type EnvOption func(*envOpts)

type envOpts struct {
	token string
	cfg   func(*config.Config)
	repo  string
}

// WithToken sets the configured token of the test client.
func WithToken(tok string) EnvOption { return func(o *envOpts) { o.token = tok } }

// WithConfig mutates the test config before the Env is built.
func WithConfig(f func(*config.Config)) EnvOption { return func(o *envOpts) { o.cfg = f } }

// WithRepo sets the default repository (FORGEJO_REPO).
func WithRepo(ownerName string) EnvOption { return func(o *envOpts) { o.repo = ownerName } }

// NewEnv starts an httptest server around handler and returns an Env whose
// forgejo.Client talks to it through an injected HTTPClient and a FakeClock.
// The server is closed with t.Cleanup. WP4 implements the body.
func NewEnv(t testing.TB, handler http.Handler, opts ...EnvOption) *tools.Env {
	t.Helper()
	panic("WP4")
}
