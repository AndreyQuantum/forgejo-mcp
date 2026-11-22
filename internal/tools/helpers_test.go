package tools_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

func mustVersion(t *testing.T, s string) forgejo.Version {
	t.Helper()
	v, err := forgejo.ParseVersion(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// toolList installs r on an in-memory server and returns what tools/list says.
func toolList(t testing.TB, r *tools.Registry, env *tools.Env) []*mcp.Tool {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "t"}, nil)
	if err := r.Install(srv, env); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "c"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res.Tools
}

func newHTTPServer(t testing.TB, h http.Handler) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}
