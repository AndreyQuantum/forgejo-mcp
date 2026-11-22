package testutil

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/clock"
	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/gitremote"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// FakeClock is the manually advanced clock used by tests.
type FakeClock = clock.Fake

// NewFakeClock returns a FakeClock starting at a fixed date.
func NewFakeClock() *FakeClock { return clock.NewFake(zeroTime) }

// EnvOption customises NewEnv.
type EnvOption func(*envOpts)

type envOpts struct {
	realRetry bool
	token     string
	cfg       func(*config.Config)
	repo      string
}

// WithToken sets the configured token of the test client.
func WithToken(tok string) EnvOption { return func(o *envOpts) { o.token = tok } }

// WithRealRetryClock gives the forgejo.Client the wall clock, so retry
// backoff sleeps really elapse (about 0.3s and 0.9s). By default the client
// shares the FakeClock, which nobody advances: a fake server answering 429,
// 502, 503, 504 or dropping the connection on a GET would park in the backoff
// until the call context times out. Use this option (or Env.Clock.Advance)
// in error-mapping tests for those statuses.
func WithRealRetryClock() EnvOption { return func(o *envOpts) { o.realRetry = true } }

// WithConfig mutates the test config before the Env is built.
func WithConfig(f func(*config.Config)) EnvOption { return func(o *envOpts) { o.cfg = f } }

// WithRepo sets the default repository (FORGEJO_REPO).
func WithRepo(ownerName string) EnvOption { return func(o *envOpts) { o.repo = ownerName } }

// NewEnv starts an httptest server around handler and returns an Env whose
// forgejo.Client talks to it through an injected HTTPClient and a FakeClock.
// The server is closed with t.Cleanup. The repository detector starts in an
// empty temp directory, so only WithRepo or an explicit `repo` param resolves.
func NewEnv(t testing.TB, handler http.Handler, opts ...EnvOption) *tools.Env {
	t.Helper()
	var o envOpts
	for _, f := range opts {
		f(&o)
	}
	if handler == nil {
		handler = http.NotFoundHandler()
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	cfg := config.Defaults()
	cfg.URL = srv.URL
	cfg.Token = o.token
	cfg.Repo = o.repo
	cfg.Dir = t.TempDir()
	cfg.SaveDir = t.TempDir()
	cfg.Timeout = 5 * time.Second
	if o.cfg != nil {
		o.cfg(cfg)
	}

	fc := NewFakeClock()
	var clientClock clock.Clock = fc
	if o.realRetry {
		clientClock = clock.Real()
	}
	client := forgejo.New(forgejo.Config{
		BaseURL:    cfg.URL,
		Token:      cfg.Token,
		Timeout:    cfg.Timeout,
		Sudo:       cfg.Sudo,
		UserAgent:  "forgejo-mcp/test",
		HTTPClient: srv.Client(),
		Clock:      clientClock,
		Debug:      cfg.Debug,
	})
	det := gitremote.NewDetector(gitremote.Options{
		InstanceURL: cfg.URL,
		SSHHosts:    cfg.SSHHosts,
		DefaultRepo: cfg.Repo,
		Dir:         cfg.Dir,
	})
	return &tools.Env{Client: client, Cfg: cfg, Repo: det, Clock: fc, Memo: &tools.Memo{}}
}
