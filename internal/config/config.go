// Package config parses environment variables and flags (DESIGN.md section 3).
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the validated process configuration.
type Config struct {
	URL          string // instance web root without trailing "/", "/api/v1" or ".git"
	Token        string // FORGEJO_TOKEN, or the content of TokenFile when that was used
	TokenFile    string
	Repo         string // default owner/name
	SSHHosts     []string
	Dir          string
	Toolsets     string // raw FORGEJO_TOOLSETS / --toolsets value; resolved by tools.ResolveToolsets
	ReadOnly     bool
	Deny         []string // "tool.action" or "tool.*"
	Timeout      time.Duration
	MaxOutput    int
	DefaultLimit int
	WaitDefault  time.Duration
	WaitMax      time.Duration
	SaveDir      string
	SaveCWD      bool
	HTTPFiles    bool
	Sudo         string
	CAFile       string
	TLSInsecure  bool
	MCPBearer    string
	Debug        bool

	Transport string // "stdio" | "http"
	Addr      string // HTTP listen address

	// Command-line only switches. When ShowVersion or Help is set the rest of
	// the configuration is not validated (FORGEJO_URL may be missing).
	ShowVersion bool
	Check       bool
	Help        bool
}

// Defaults returns the configuration with every default of section 3 applied.
func Defaults() *Config {
	return &Config{
		Toolsets:     "repos,pulls,issues,actions,runners,settings,users,packages",
		Timeout:      30 * time.Second,
		MaxOutput:    16000,
		DefaultLimit: 20,
		WaitDefault:  50 * time.Second,
		WaitMax:      55 * time.Second,
		Transport:    "stdio",
		Addr:         "127.0.0.1:8787",
	}
}

// Redacted returns a one-line summary that never contains secrets.
func (c *Config) Redacted() string {
	tok := "none"
	switch {
	case c.TokenFile != "":
		tok = "file"
	case c.Token != "":
		tok = "set"
	}
	return fmt.Sprintf("url=%s token=%s repo=%s toolsets=%s read_only=%t deny=%s transport=%s",
		c.URL, tok, c.Repo, c.Toolsets, c.ReadOnly, strings.Join(c.Deny, ","), c.Transport)
}

// String implements fmt.Stringer and redacts secrets.
func (c *Config) String() string { return c.Redacted() }

// PassThroughToken reports whether per-request Forgejo tokens may be accepted
// (HTTP mode): only when neither FORGEJO_TOKEN nor FORGEJO_TOKEN_FILE is set.
func (c *Config) PassThroughToken() bool { return c.Token == "" && c.TokenFile == "" }

// Usage is the --help text.
const Usage = `forgejo-mcp: MCP server for Forgejo (stdio, optional streamable HTTP)

Flags (override the environment):
  --transport stdio|http   transport (default stdio)
  --addr host:port         HTTP listen address (default 127.0.0.1:8787)
  --toolsets list          FORGEJO_TOOLSETS override
  --read-only              hide and block mutating actions
  --check                  load config, call /version and /user, print a summary, exit
  --version                print the version and exit
  --help                   this text

Environment: FORGEJO_URL (required), FORGEJO_TOKEN, FORGEJO_TOKEN_FILE,
FORGEJO_REPO, FORGEJO_SSH_HOSTS, FORGEJO_DIR, FORGEJO_TOOLSETS, FORGEJO_READ_ONLY,
FORGEJO_DENY, FORGEJO_TIMEOUT, FORGEJO_MAX_OUTPUT, FORGEJO_DEFAULT_LIMIT,
FORGEJO_WAIT_DEFAULT, FORGEJO_WAIT_MAX, FORGEJO_SAVE_DIR, FORGEJO_SAVE_CWD,
FORGEJO_HTTP_FILES, FORGEJO_SUDO, FORGEJO_CA_FILE, FORGEJO_TLS_INSECURE,
FORGEJO_MCP_BEARER, FORGEJO_DEBUG.
`

// Load builds a Config from args (flags) and getenv, applies every default and
// validates the result. Flags override the environment. Errors are meant to be
// printed to stderr with exit code 2; they never contain the token.
func Load(args []string, getenv func(string) string) (*Config, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	c := Defaults()

	// Flags first: --help and --version must work without a valid environment.
	var (
		transport, addr, toolsets string
		readOnly                  bool
	)
	fs := flag.NewFlagSet("forgejo-mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&transport, "transport", "", "")
	fs.StringVar(&addr, "addr", "", "")
	fs.StringVar(&toolsets, "toolsets", "", "")
	fs.BoolVar(&readOnly, "read-only", false, "")
	fs.BoolVar(&c.ShowVersion, "version", false, "")
	fs.BoolVar(&c.Check, "check", false, "")
	fs.BoolVar(&c.Help, "help", false, "")
	fs.BoolVar(&c.Help, "h", false, "")
	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("flags: %w", err)
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("flags: unexpected argument %q", fs.Arg(0))
	}
	if c.ShowVersion || c.Help {
		return c, nil
	}

	env := func(k string) string { return strings.TrimSpace(getenv(k)) }
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	// URL.
	if raw := env("FORGEJO_URL"); raw == "" {
		bad("FORGEJO_URL is required (e.g. https://forgejo.example.com)")
	} else if u, err := NormalizeURL(raw); err != nil {
		bad("FORGEJO_URL: %v", err)
	} else {
		c.URL = u
	}

	// Token.
	c.Token = strings.TrimSpace(getenv("FORGEJO_TOKEN"))
	if c.Token == "" {
		if p := env("FORGEJO_TOKEN_FILE"); p != "" {
			if abs, err := filepath.Abs(p); err == nil {
				p = abs
			}
			c.TokenFile = p
			tok, err := ReadTokenFile(p)
			if err != nil {
				errs = append(errs, err)
			} else {
				c.Token = tok
			}
		}
	}

	// Repo and detection.
	if v := env("FORGEJO_REPO"); v != "" {
		v = strings.TrimSuffix(strings.Trim(v, "/"), ".git")
		parts := strings.Split(v, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			bad("FORGEJO_REPO must be owner/name, got %q", v)
		} else {
			c.Repo = v
		}
	}
	c.SSHHosts = splitList(env("FORGEJO_SSH_HOSTS"))
	c.Dir = env("FORGEJO_DIR")

	// Toolsets, read-only, deny.
	if v := env("FORGEJO_TOOLSETS"); v != "" {
		c.Toolsets = v
	}
	if toolsets != "" {
		c.Toolsets = strings.TrimSpace(toolsets)
	}
	var err error
	if c.ReadOnly, err = parseBool("FORGEJO_READ_ONLY", env("FORGEJO_READ_ONLY")); err != nil {
		errs = append(errs, err)
	}
	if readOnly {
		c.ReadOnly = true
	}
	c.Deny = splitList(env("FORGEJO_DENY"))
	for _, d := range c.Deny {
		if !validDeny(d) {
			bad("FORGEJO_DENY: %q is not tool.action or tool.*", d)
		}
	}

	// Numbers and durations.
	if v := env("FORGEJO_TIMEOUT"); v != "" {
		if d, err := parseDuration(v); err != nil || d <= 0 {
			bad("FORGEJO_TIMEOUT: %q is not a positive duration (e.g. 30s)", v)
		} else {
			c.Timeout = d
		}
	}
	intVar := func(key string, dst *int, lo, hi int) {
		v := env(key)
		if v == "" {
			return
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < lo || n > hi {
			bad("%s: %q must be an integer in %d..%d", key, v, lo, hi)
			return
		}
		*dst = n
	}
	intVar("FORGEJO_MAX_OUTPUT", &c.MaxOutput, 1000, 100000)
	intVar("FORGEJO_DEFAULT_LIMIT", &c.DefaultLimit, 1, 100)
	wd, wm := int(c.WaitDefault/time.Second), int(c.WaitMax/time.Second)
	intVar("FORGEJO_WAIT_DEFAULT", &wd, 1, 600)
	intVar("FORGEJO_WAIT_MAX", &wm, 1, 600)
	c.WaitDefault, c.WaitMax = time.Duration(wd)*time.Second, time.Duration(wm)*time.Second
	if c.WaitDefault > c.WaitMax {
		bad("FORGEJO_WAIT_DEFAULT (%ds) must not exceed FORGEJO_WAIT_MAX (%ds)", wd, wm)
	}

	// Files and switches.
	if c.SaveDir = env("FORGEJO_SAVE_DIR"); c.SaveDir == "" {
		c.SaveDir = filepath.Join(os.TempDir(), "forgejo-mcp")
	}
	for _, b := range []struct {
		key string
		dst *bool
	}{
		{"FORGEJO_SAVE_CWD", &c.SaveCWD},
		{"FORGEJO_HTTP_FILES", &c.HTTPFiles},
		{"FORGEJO_TLS_INSECURE", &c.TLSInsecure},
		{"FORGEJO_DEBUG", &c.Debug},
	} {
		if *b.dst, err = parseBool(b.key, env(b.key)); err != nil {
			errs = append(errs, err)
		}
	}

	c.Sudo = env("FORGEJO_SUDO")
	c.CAFile = env("FORGEJO_CA_FILE")
	c.MCPBearer = strings.TrimSpace(getenv("FORGEJO_MCP_BEARER"))

	// Transport.
	if transport != "" {
		c.Transport = transport
	}
	if c.Transport != "stdio" && c.Transport != "http" {
		bad("invalid --transport %q (want stdio or http)", c.Transport)
	}
	if addr != "" {
		c.Addr = addr
	}
	if c.Transport == "http" {
		if _, _, err := net.SplitHostPort(c.Addr); err != nil {
			bad("invalid --addr %q: %v", c.Addr, err)
		} else if !IsLoopbackAddr(c.Addr) && c.MCPBearer == "" {
			bad("--addr %s is not loopback: set FORGEJO_MCP_BEARER", c.Addr)
		}
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return c, nil
}

// NormalizeURL validates a FORGEJO_URL value and strips a trailing "/",
// "/api/v1" and ".git". Sub-path installs are kept.
func NormalizeURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", errors.New("invalid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("scheme must be http or https")
	}
	if u.Hostname() == "" {
		return "", errors.New("missing host")
	}
	u.User = nil // never keep credentials
	u.RawQuery, u.Fragment, u.RawFragment = "", "", ""
	p := u.EscapedPath()
	for {
		before := p
		p = strings.TrimRight(p, "/")
		p = strings.TrimSuffix(p, "/api/v1")
		p = strings.TrimSuffix(p, ".git")
		if p == before {
			break
		}
	}
	u.Path, u.RawPath = "", ""
	if p != "" {
		if up, err := url.PathUnescape(p); err == nil {
			u.Path, u.RawPath = up, p
		}
	}
	return u.String(), nil
}

// IsLoopbackAddr reports whether a listen address binds loopback only. An
// empty host (":8787") or an unspecified address binds all interfaces.
func IsLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func validDeny(d string) bool {
	tool, act, ok := strings.Cut(d, ".")
	return ok && tool != "" && act != "" && !strings.Contains(act, ".") && !strings.ContainsAny(d, " \t")
}

func parseBool(key, v string) (bool, error) {
	switch strings.ToLower(v) {
	case "", "0", "false", "no", "off":
		return false, nil
	case "1", "true", "yes", "on":
		return true, nil
	}
	return false, fmt.Errorf("%s: %q is not a boolean (use 1 or 0)", key, v)
}

// parseDuration accepts Go durations ("30s", "1m") and bare seconds ("30").
func parseDuration(v string) (time.Duration, error) {
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	return time.ParseDuration(v)
}
