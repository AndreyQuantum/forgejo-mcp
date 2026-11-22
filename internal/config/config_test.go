package config

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(nil, env(map[string]string{"FORGEJO_URL": "https://git.example.com/"}))
	if err != nil {
		t.Fatal(err)
	}
	d := Defaults()
	if c.URL != "https://git.example.com" {
		t.Errorf("URL = %q", c.URL)
	}
	if c.Timeout != d.Timeout || c.MaxOutput != 16000 || c.DefaultLimit != 20 ||
		c.WaitDefault != 50*time.Second || c.WaitMax != 55*time.Second ||
		c.Transport != "stdio" || c.Addr != "127.0.0.1:8787" || c.ReadOnly {
		t.Errorf("defaults not applied: %+v", c)
	}
	if c.SaveDir != filepath.Join(os.TempDir(), "forgejo-mcp") {
		t.Errorf("SaveDir = %q", c.SaveDir)
	}
	if !c.PassThroughToken() {
		t.Error("no token configured: pass-through must be allowed")
	}
}

func TestNormalizeURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://forgejo.example.com", "https://forgejo.example.com"},
		{"https://forgejo.example.com/", "https://forgejo.example.com"},
		{"https://forgejo.example.com/api/v1", "https://forgejo.example.com"},
		{"https://forgejo.example.com/api/v1/", "https://forgejo.example.com"},
		{"https://forgejo.example.com/o/r.git", "https://forgejo.example.com/o/r"},
		{"http://localhost:3000///", "http://localhost:3000"},
		{"https://example.com/forgejo", "https://example.com/forgejo"},
		{"https://example.com/forgejo/api/v1", "https://example.com/forgejo"},
		{"HTTPS://Example.COM:8443/x/", "https://Example.COM:8443/x"},
		{"https://u:secret@example.com/?a=1#f", "https://example.com"},
	}
	for _, tt := range tests {
		got, err := NormalizeURL(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("NormalizeURL(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
	for _, bad := range []string{"", "forgejo.example.com", "ftp://x.com", "https://", "file:///etc"} {
		if _, err := NormalizeURL(bad); err == nil {
			t.Errorf("NormalizeURL(%q) should fail", bad)
		}
	}
}

func TestLoadErrors(t *testing.T) {
	base := map[string]string{"FORGEJO_URL": "https://x.example.com"}
	with := func(kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	tests := []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"no url", nil, map[string]string{}, "FORGEJO_URL is required"},
		{"bad scheme", nil, map[string]string{"FORGEJO_URL": "ftp://x"}, "scheme"},
		{"bad repo", nil, with("FORGEJO_REPO", "justname"), "FORGEJO_REPO"},
		{"bad deny", nil, with("FORGEJO_DENY", "repo"), "FORGEJO_DENY"},
		{"bad bool", nil, with("FORGEJO_READ_ONLY", "maybe"), "FORGEJO_READ_ONLY"},
		{"bad timeout", nil, with("FORGEJO_TIMEOUT", "soon"), "FORGEJO_TIMEOUT"},
		{"wait order", nil, with("FORGEJO_WAIT_DEFAULT", "60", "FORGEJO_WAIT_MAX", "55"), "must not exceed"},
		{"wait over 600", nil, with("FORGEJO_WAIT_MAX", "601"), "FORGEJO_WAIT_MAX"},
		{"limit 0", nil, with("FORGEJO_DEFAULT_LIMIT", "0"), "FORGEJO_DEFAULT_LIMIT"},
		{"transport", []string{"--transport", "ws"}, base, "--transport"},
		{"non-loopback needs bearer", []string{"--transport", "http", "--addr", "0.0.0.0:8787"}, base, "FORGEJO_MCP_BEARER"},
		{"empty host needs bearer", []string{"--transport=http", "--addr=:8787"}, base, "FORGEJO_MCP_BEARER"},
		{"unknown flag", []string{"--nope"}, base, "flags"},
		{"positional", []string{"x"}, base, "unexpected argument"},
	}
	for _, tt := range tests {
		_, err := Load(tt.args, env(tt.env))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want containing %q", tt.name, err, tt.want)
		}
	}
}

func TestLoadFlagsOverrideEnv(t *testing.T) {
	c, err := Load(
		[]string{"--transport", "http", "--addr", "[::1]:9000", "--toolsets", "repos,-admin", "--read-only"},
		env(map[string]string{
			"FORGEJO_URL":      "https://x.example.com",
			"FORGEJO_TOOLSETS": "all",
			"FORGEJO_DENY":     "repo.delete, admin.*",
		}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Transport != "http" || c.Addr != "[::1]:9000" || c.Toolsets != "repos,-admin" || !c.ReadOnly {
		t.Errorf("flags not applied: %+v", c)
	}
	if len(c.Deny) != 2 || c.Deny[0] != "repo.delete" || c.Deny[1] != "admin.*" {
		t.Errorf("Deny = %v", c.Deny)
	}
}

func TestLoadHTTPWithBearerNonLoopback(t *testing.T) {
	c, err := Load([]string{"--transport", "http", "--addr", "0.0.0.0:8787"},
		env(map[string]string{"FORGEJO_URL": "https://x.example.com", "FORGEJO_MCP_BEARER": "b"}))
	if err != nil || c.MCPBearer != "b" {
		t.Fatalf("err=%v cfg=%+v", err, c)
	}
}

func TestLoadVersionAndHelpSkipValidation(t *testing.T) {
	c, err := Load([]string{"--version"}, env(nil))
	if err != nil || !c.ShowVersion {
		t.Fatalf("--version: %v %+v", err, c)
	}
	c, err = Load([]string{"--help"}, env(nil))
	if err != nil || !c.Help {
		t.Fatalf("--help: %v %+v", err, c)
	}
}

func TestLoadValues(t *testing.T) {
	c, err := Load([]string{"--check"}, env(map[string]string{
		"FORGEJO_URL":          "https://x.example.com",
		"FORGEJO_REPO":         "o/r.git",
		"FORGEJO_SSH_HOSTS":    "git-ssh.example.com:2222, other",
		"FORGEJO_TIMEOUT":      "45",
		"FORGEJO_MAX_OUTPUT":   "20000",
		"FORGEJO_WAIT_DEFAULT": "100",
		"FORGEJO_WAIT_MAX":     "300",
		"FORGEJO_SAVE_CWD":     "true",
		"FORGEJO_TLS_INSECURE": "1",
		"FORGEJO_SUDO":         "bob",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Check || c.Repo != "o/r" || len(c.SSHHosts) != 2 || c.SSHHosts[0] != "git-ssh.example.com:2222" ||
		c.Timeout != 45*time.Second || c.MaxOutput != 20000 || c.WaitDefault != 100*time.Second ||
		c.WaitMax != 300*time.Second || !c.SaveCWD || !c.TLSInsecure || c.Sudo != "bob" {
		t.Errorf("unexpected: %+v", c)
	}
}

func TestTokenPrecedenceAndRedaction(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "tok")
	if err := os.WriteFile(f, []byte("filetoken123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(nil, env(map[string]string{"FORGEJO_URL": "https://x.example.com", "FORGEJO_TOKEN": "envtoken456", "FORGEJO_TOKEN_FILE": f}))
	if err != nil || c.Token != "envtoken456" || c.TokenFile != "" {
		t.Fatalf("env must win: %v %+v", err, c)
	}
	c, err = Load(nil, env(map[string]string{"FORGEJO_URL": "https://x.example.com", "FORGEJO_TOKEN_FILE": f}))
	if err != nil || c.Token != "filetoken123" || c.TokenFile == "" || c.PassThroughToken() {
		t.Fatalf("file token: %v %+v", err, c)
	}
	for _, s := range []string{c.String(), c.Redacted()} {
		if strings.Contains(s, "filetoken123") || strings.Contains(s, "envtoken456") {
			t.Errorf("token leaked: %s", s)
		}
	}
	if !strings.Contains(c.Redacted(), "token=file") {
		t.Errorf("Redacted = %s", c.Redacted())
	}
}

func TestTokenFileErrorNeverLeaksContent(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "tok")
	if err := os.WriteFile(f, []byte("super secret value with spaces"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(nil, env(map[string]string{"FORGEJO_URL": "https://x.example.com", "FORGEJO_TOKEN_FILE": f}))
	if err == nil || !strings.Contains(err.Error(), "FORGEJO_TOKEN_FILE") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("error leaks content: %v", err)
	}
	_, err = Load(nil, env(map[string]string{"FORGEJO_URL": "https://x.example.com", "FORGEJO_TOKEN_FILE": filepath.Join(dir, "missing")}))
	if err == nil {
		t.Error("missing file must be an error")
	}
}

func utf16Bytes(s string, be bool, bom bool) []byte {
	var out []byte
	put := func(u uint16) {
		if be {
			out = append(out, byte(u>>8), byte(u))
		} else {
			out = append(out, byte(u), byte(u>>8))
		}
	}
	if bom {
		put(0xFEFF)
	}
	for _, u := range utf16.Encode([]rune(s)) {
		put(u)
	}
	return out
}

func TestDecodeToken(t *testing.T) {
	const tok = "a1b2C3d4e5f6"
	tests := []struct {
		name string
		in   []byte
		ok   bool
	}{
		{"plain", []byte(tok), true},
		{"newline", []byte(tok + "\n"), true},
		{"crlf", []byte(tok + "\r\n"), true},
		{"surrounding whitespace", []byte("  \t" + tok + " \r\n\r\n"), true},
		{"utf8 bom", append([]byte{0xEF, 0xBB, 0xBF}, []byte(tok+"\r\n")...), true},
		{"utf16le bom crlf", utf16Bytes(tok+"\r\n", false, true), true},
		{"utf16be bom", utf16Bytes(tok+"\n", true, true), true},
		{"empty", []byte(" \r\n"), false},
		{"inner space", []byte("ab cd"), false},
		{"inner newline", []byte("ab\ncd"), false},
		{"non-ascii", []byte("tok\xc3\xa9n"), false},
		{"nul", []byte("ab\x00cd"), false},
		{"utf16 without bom (nul bytes)", utf16Bytes(tok, false, false), false},
		{"utf16 odd length", append(utf16Bytes(tok, false, true), 0x41), false},
	}
	for _, tt := range tests {
		got, err := DecodeToken(tt.in)
		if tt.ok && (err != nil || got != tok) {
			t.Errorf("%s: got %q, %v", tt.name, got, err)
		}
		if !tt.ok && err == nil {
			t.Errorf("%s: expected error, got %q", tt.name, got)
		}
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	yes := []string{"127.0.0.1:8787", "localhost:80", "[::1]:80", "127.1.2.3:1"}
	no := []string{"0.0.0.0:80", ":8787", "[::]:80", "192.168.1.5:80", "example.com:80", "bad"}
	for _, a := range yes {
		if !IsLoopbackAddr(a) {
			t.Errorf("%s should be loopback", a)
		}
	}
	for _, a := range no {
		if IsLoopbackAddr(a) {
			t.Errorf("%s should not be loopback", a)
		}
	}
}

func FuzzNormalizeURL(f *testing.F) {
	for _, s := range []string{"https://a.b", "http://a/api/v1/", "https://a/x.git", "::", "https://[::1]:3000/p"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out, err := NormalizeURL(s)
		if err != nil {
			return
		}
		pu, perr := url.Parse(out)
		if perr != nil {
			t.Fatalf("output %q does not parse: %v", out, perr)
		}
		if p := pu.Path; strings.HasSuffix(p, "/") || strings.HasSuffix(p, "/api/v1") || strings.HasSuffix(p, ".git") {
			t.Fatalf("not normalised: %q -> %q", s, out)
		}
		again, err := NormalizeURL(out)
		if err != nil || again != out {
			t.Fatalf("not idempotent: %q -> %q -> %q (%v)", s, out, again, err)
		}
	})
}

func FuzzDecodeToken(f *testing.F) {
	f.Add([]byte("abc"))
	f.Add([]byte{0xFF, 0xFE, 'a', 0})
	f.Fuzz(func(t *testing.T, b []byte) {
		tok, err := DecodeToken(b)
		if err != nil {
			return
		}
		for i := 0; i < len(tok); i++ {
			if tok[i] < 0x21 || tok[i] > 0x7e {
				t.Fatalf("non-printable byte in %q", tok)
			}
		}
	})
}
