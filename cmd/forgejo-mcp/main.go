// Command forgejo-mcp is an MCP server for Forgejo (stdio, optional streamable
// HTTP). WP0 provides flag parsing only; WP4 wires config, registry and the
// transports.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime/debug"
)

// Set by the linker (-X main.version=... etc.).
var (
	version = ""
	commit  = ""
	date    = ""
)

func buildVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "dev"
}

type flags struct {
	transport string
	addr      string
	toolsets  string
	readOnly  bool
	showVer   bool
	check     bool
}

func parseFlags(args []string) (*flags, error) {
	var f flags
	fs := flag.NewFlagSet("forgejo-mcp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&f.transport, "transport", "stdio", "transport: stdio or http")
	fs.StringVar(&f.addr, "addr", "127.0.0.1:8787", "HTTP listen address (http transport)")
	fs.StringVar(&f.toolsets, "toolsets", "", "comma list of toolsets (overrides FORGEJO_TOOLSETS)")
	fs.BoolVar(&f.readOnly, "read-only", false, "hide and block mutating actions")
	fs.BoolVar(&f.showVer, "version", false, "print the version and exit")
	fs.BoolVar(&f.check, "check", false, "load config, call /version and /user, print a summary and exit")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if f.transport != "stdio" && f.transport != "http" {
		return nil, fmt.Errorf("invalid --transport %q (want stdio or http)", f.transport)
	}
	return &f, nil
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	f, err := parseFlags(args)
	if err != nil {
		// stdout is reserved for the MCP stream: errors go to stderr.
		if err != flag.ErrHelp {
			fmt.Fprintln(os.Stderr, "forgejo-mcp:", err)
		}
		return 2
	}
	if f.showVer {
		fmt.Printf("forgejo-mcp %s\n", buildVersion())
		return 0
	}
	// TODO(WP4): config.Load, forgejo.New, registry build, stdio|http run loop,
	// --check, signal handling, logging to stderr.
	fmt.Fprintln(os.Stderr, "forgejo-mcp: server wiring is not implemented yet")
	return 1
}
