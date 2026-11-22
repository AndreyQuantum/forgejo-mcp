// Command forgejo-mcp is an MCP server for Forgejo (stdio, optional streamable
// HTTP). stdout carries only the MCP stream; every log line goes to stderr.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AndreyQuantum/forgejo-mcp/internal/clock"
	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/gitremote"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/all"
)

// Set by the linker (-X main.version=... etc.).
var (
	version = ""
	commit  = ""
	date    = ""
)

// versionLine is the --version output: version plus commit and date when set.
func versionLine() string {
	s := "forgejo-mcp " + buildVersion()
	switch {
	case commit != "" && date != "":
		s += " (" + commit + ", " + date + ")"
	case commit != "":
		s += " (" + commit + ")"
	case date != "":
		s += " (" + date + ")"
	}
	return s
}

func buildVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

func main() {
	os.Exit(run(os.Args[1:], os.Getenv))
}

func run(args []string, getenv func(string) string) int {
	cfg, err := config.Load(args, getenv)
	if err != nil {
		// stdout is reserved for the MCP stream: errors go to stderr.
		fmt.Fprintln(os.Stderr, "forgejo-mcp:", err)
		return 2
	}
	switch {
	case cfg.Help:
		fmt.Fprint(os.Stdout, config.Usage)
		return 0
	case cfg.ShowVersion:
		fmt.Fprintln(os.Stdout, versionLine())
		return 0
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := forgejo.New(forgejo.Config{
		BaseURL:     cfg.URL,
		Token:       cfg.Token,
		Timeout:     cfg.Timeout,
		Sudo:        cfg.Sudo,
		CAFile:      cfg.CAFile,
		InsecureTLS: cfg.TLSInsecure,
		UserAgent:   "forgejo-mcp/" + buildVersion(),
		Clock:       clock.Real(),
		Debug:       cfg.Debug,
	})

	if cfg.Check {
		return check(ctx, cfg, client)
	}

	det := gitremote.NewDetector(gitremote.Options{
		InstanceURL: cfg.URL,
		SSHHosts:    cfg.SSHHosts,
		DefaultRepo: cfg.Repo,
		Dir:         cfg.Dir,
	})
	env := &tools.Env{Client: client, Cfg: cfg, Repo: det, Clock: clock.Real(), Memo: &tools.Memo{}}

	reg := tools.NewRegistry()
	all.RegisterAll(reg)
	if err := reg.Build(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "forgejo-mcp:", err)
		return 2
	}

	defRepo := cfg.Repo
	if defRepo == "" {
		if o, n, ok := det.GitRepo(); ok {
			defRepo = o + "/" + n
		}
	}
	opts := &mcp.ServerOptions{
		Instructions: tools.Instructions(tools.InstructionsInfo{
			Host:        tools.HostOf(cfg.URL),
			DefaultRepo: defRepo,
			Toolsets:    reg.EnabledToolsets(),
			WaitMax:     int(cfg.WaitMax / time.Second),
		}),
	}
	if cfg.Debug {
		opts.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "forgejo-mcp", Version: buildVersion()}, opts)
	if err := reg.Install(srv, env); err != nil {
		fmt.Fprintln(os.Stderr, "forgejo-mcp:", err)
		return 1
	}
	if cfg.Token == "" && (cfg.Transport != "http" || !cfg.PassThroughToken()) {
		fmt.Fprintln(os.Stderr, "forgejo-mcp: warning: no FORGEJO_TOKEN; running anonymously (public repos, read-only)")
	}

	switch cfg.Transport {
	case "http":
		return serveHTTP(ctx, cfg, srv)
	default:
		if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "forgejo-mcp:", err)
			return 1
		}
		return 0
	}
}

// check implements --check: /version and /user, one summary line, exit 0/1.
func check(ctx context.Context, cfg *config.Config, c *forgejo.Client) int {
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout*2)
	defer cancel()
	v, err := c.ServerVersion(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, c.Redact(err.Error()))
		return 1
	}
	who := "anonymous"
	if cfg.Token != "" {
		me, err := c.Me(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, c.Redact(err.Error()))
			return 1
		}
		who = me.Login
		if me.IsAdmin {
			who += " (admin)"
		}
	}
	fmt.Fprintf(os.Stdout, "ok: forgejo %s at %s as %s\n", v.String(), cfg.URL, who)
	return 0
}

// serveHTTP runs the streamable HTTP transport on cfg.Addr at /mcp.
func serveHTTP(ctx context.Context, cfg *config.Config, srv *mcp.Server) int {
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{
		PropagateRequestCancellation: true,
		SessionTimeout:               30 * time.Minute,
	})
	var handler http.Handler = h
	if cfg.MCPBearer != "" {
		handler = bearerAuth(cfg.MCPBearer, h)
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	hs := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: long SSE responses (ci_wait) would be cut.
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "forgejo-mcp:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "forgejo-mcp: listening on http://%s/mcp\n", ln.Addr())
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
		return 0
	case err := <-errc:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "forgejo-mcp:", err)
			return 1
		}
		return 0
	}
}

// bearerAuth requires `Authorization: Bearer <want>` (constant-time compare).
func bearerAuth(want string, next http.Handler) http.Handler {
	wantSum := sha256.Sum256([]byte(want))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		gotSum := sha256.Sum256([]byte(strings.TrimSpace(got)))
		if !ok || subtle.ConstantTimeCompare(gotSum[:], wantSum[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
