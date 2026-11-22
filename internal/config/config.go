// Package config parses environment variables and flags (DESIGN.md section 3).
// WP0 defines the Config contract; WP1 implements Load and validation.
package config

import (
	"fmt"
	"strings"
	"time"
)

// Config is the validated process configuration.
type Config struct {
	URL          string // instance web root without trailing "/", "/api/v1" or ".git"
	Token        string
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

// Load builds a Config from args (flags) and getenv. WP1 implements it.
func Load(args []string, getenv func(string) string) (*Config, error) {
	panic("WP1")
}

// Redacted returns a one-line summary that never contains secrets.
func (c *Config) Redacted() string {
	tok := "none"
	switch {
	case c.Token != "":
		tok = "set"
	case c.TokenFile != "":
		tok = "file"
	}
	return fmt.Sprintf("url=%s token=%s repo=%s toolsets=%s read_only=%t deny=%s transport=%s",
		c.URL, tok, c.Repo, c.Toolsets, c.ReadOnly, strings.Join(c.Deny, ","), c.Transport)
}

// String implements fmt.Stringer and redacts secrets.
func (c *Config) String() string { return c.Redacted() }
