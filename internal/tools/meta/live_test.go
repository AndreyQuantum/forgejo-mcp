package meta

import (
	"os"
	"strings"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/clock"
	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/gitremote"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// liveEnv builds an Env against FORGEJO_TEST_URL / FORGEJO_TEST_TOKEN or skips.
func liveEnv(t *testing.T) (*tools.Env, *tools.Registry) {
	t.Helper()
	url, tok := os.Getenv("FORGEJO_TEST_URL"), os.Getenv("FORGEJO_TEST_TOKEN")
	if url == "" || tok == "" {
		t.Skip("FORGEJO_TEST_URL / FORGEJO_TEST_TOKEN not set")
	}
	cfg := config.Defaults()
	cfg.URL, cfg.Token = url, tok
	cfg.SaveDir = t.TempDir()
	reg := newRegistry(t, cfg, false)
	env := &tools.Env{
		Client: forgejo.New(forgejo.Config{BaseURL: url, Token: tok, Timeout: cfg.Timeout, UserAgent: "forgejo-mcp/test"}),
		Cfg:    cfg,
		Repo:   gitremote.NewDetector(gitremote.Options{InstanceURL: url, Dir: t.TempDir()}),
		Clock:  clock.Real(),
		Memo:   &tools.Memo{},
	}
	return env, reg
}

func TestLiveMeta(t *testing.T) {
	env, reg := liveEnv(t)
	out, isErr := callReg(t, env, reg, "forgejo_info", map[string]any{"verbose": true})
	if isErr || !strings.HasPrefix(out, "forgejo 17.") {
		t.Fatalf("info: %v\n%s", isErr, out)
	}
	t.Logf("info:\n%s", out)

	out, isErr = callReg(t, env, reg, "forgejo_api", map[string]any{"method": "GET", "path": "/user", "pick": []any{"login", "is_admin"}})
	if isErr || !strings.Contains(out, `"login"`) {
		t.Fatalf("api /user: %v\n%s", isErr, out)
	}
	out, isErr = callReg(t, env, reg, "forgejo_api", map[string]any{"method": "GET", "path": "/repos/search", "query": map[string]any{"limit": 2}, "pick": []any{"full_name"}})
	if isErr {
		t.Fatalf("api repos/search: %s", out)
	}
	t.Logf("repos/search:\n%s", out)
	out, isErr = callReg(t, env, reg, "forgejo_api", map[string]any{"method": "GET", "path": "/orgs", "all_pages": true, "max_pages": 2})
	if isErr {
		t.Fatalf("api orgs: %s", out)
	}
	t.Logf("orgs:\n%s", out)
	out, isErr = callReg(t, env, reg, "forgejo_api", map[string]any{"method": "POST", "path": "/markdown", "body": map[string]any{"Text": "**hi**", "Mode": "markdown"}})
	if isErr || !strings.Contains(out, "<strong>hi</strong>") {
		t.Fatalf("markdown: %v\n%s", isErr, out)
	}
	out, isErr = callReg(t, env, reg, "forgejo_api", map[string]any{"method": "GET", "path": "/repos/nobody-x/none-y"})
	if !isErr || !strings.HasPrefix(out, "error: 404") {
		t.Fatalf("404: %v\n%s", isErr, out)
	}
	t.Logf("404:\n%s", out)
	out, isErr = callReg(t, env, reg, "forgejo_endpoints", map[string]any{"q": "runner registration token"})
	if isErr {
		t.Fatal(out)
	}
	t.Logf("endpoints:\n%s", out)
}
