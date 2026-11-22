package meta

import (
	"strings"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

func TestCanonicalize(t *testing.T) {
	ok := []struct{ in, canon, api string }{
		{"/repos/o/r", "/repos/o/r", ""},
		{"//repos/o/r/", "/repos/o/r", ""},
		{"/repos//o///r//", "/repos/o/r", ""},
		{"/api/v1/repos/o/r", "/repos/o/r", ""},
		{"//api/v1//repos/o/r/", "/repos/o/r", ""},
		{"/forgejo/v1/admin/x", "/admin/x", "forgejo"},
		{"/api/forgejo/v1/admin/x", "/admin/x", "forgejo"},
		{"/repos/o/r/contents/a%20b.txt", "/repos/o/r/contents/a%20b.txt", ""},
		{"  /version ", "/version", ""},
		{"/rep%6Fs/o/r", "/repos/o/r", ""},
		{"/repos/o/r/actions/runners/registration%2Dtoken", "/repos/o/r/actions/runners/registration-token", ""},
	}
	for _, c := range ok {
		canon, api, err := canonicalize(c.in)
		if err != nil || canon != c.canon || api != c.api {
			t.Errorf("canonicalize(%q) = %q %q %v, want %q %q", c.in, canon, api, err, c.canon, c.api)
		}
	}
	bad := []string{
		"", "/", "/api/v1", "repos/o/r", "https://evil.example/repos/o/r", "http://x/y", "file:///etc/passwd",
		"/repos/o/r?x=1", "/repos/o/r#frag", "/repos/../admin/users", "/repos/./o", "/repos/%2e%2e/admin",
		"/repos/o%2fr", "/repos/o%5cr", `/repos\o`, "/repos/o/r\x00", "/repos/%00", "/repos/o/r\n",
	}
	for _, in := range bad {
		if _, _, err := canonicalize(in); err == nil {
			t.Errorf("canonicalize(%q) accepted", in)
		}
	}
}

func evalWith(t *testing.T, mutate func(*config.Config), method, path, confirm string) decision {
	t.Helper()
	cfg := config.Defaults()
	cfg.URL = "http://example.invalid"
	if mutate != nil {
		mutate(cfg)
	}
	reg := newRegistry(t, cfg, true)
	return (&policy{reg: reg}).evaluate(cfg, method, path, confirm)
}

func TestForgejoAPIPolicy(t *testing.T) {
	type tc struct {
		name    string
		mutate  func(*config.Config)
		method  string
		path    string
		confirm string
		wantErr string // substring; "" = allowed
		wantOp  string
	}
	allToolsets := func(c *config.Config) { c.Toolsets = "all" }
	cases := []tc{
		{name: "plain get", method: "GET", path: "/repos/o/r", wantOp: "GET /repos/{owner}/{repo}"},
		{name: "slash variant get", method: "GET", path: "//repos/o/r/", wantOp: "GET /repos/{owner}/{repo}"},
		{name: "pasted api prefix", method: "GET", path: "/api/v1/repos/o/r", wantOp: "GET /repos/{owner}/{repo}"},
		{name: "bad method", method: "TRACE", path: "/repos/o/r", wantErr: "invalid: method"},
		{name: "absolute url", method: "GET", path: "https://x.example/repos/o/r", wantErr: "invalid"},
		{name: "query in path", method: "GET", path: "/repos/o/r?a=b", wantErr: "invalid"},

		// deny by endpoint
		{
			name: "deny covers endpoint", mutate: func(c *config.Config) { c.Deny = []string{"repo.get"} },
			method: "GET", path: "/repos/o/r", wantErr: "blocked by FORGEJO_DENY (repo.get)",
		},
		{
			name: "deny slash variant", mutate: func(c *config.Config) { c.Deny = []string{"repo.get"} },
			method: "GET", path: "//repos/o/r/", wantErr: "blocked by FORGEJO_DENY (repo.get)",
		},
		{
			name: "deny wildcard", mutate: func(c *config.Config) { c.Deny = []string{"label.*"} },
			method: "POST", path: "/repos/o/r/labels", wantErr: "blocked by FORGEJO_DENY (label.create)",
		},
		{
			name: "deny leaves others", mutate: func(c *config.Config) { c.Deny = []string{"label.create"} },
			method: "GET", path: "/repos/o/r/labels", wantOp: "GET /repos/{owner}/{repo}/labels",
		},
		{
			name: "toolset off blocks", mutate: func(c *config.Config) { c.Toolsets = "repos,pulls" },
			method: "GET", path: "/repos/o/r/labels", wantErr: "blocked by toolset off (label.list)",
		},
		{name: "admin off by default", method: "GET", path: "/admin/users", wantErr: "blocked by toolset off (admin"},
		{name: "admin path unknown op still blocked", method: "GET", path: "/admin/never/heard/of", wantErr: "blocked by toolset off (admin)"},
		{name: "admin on", mutate: allToolsets, method: "GET", path: "/admin/users", wantOp: "GET /admin/users"},
		{
			name: "unmatched non-get with deny", mutate: func(c *config.Config) { c.Deny = []string{"repo.delete"} },
			method: "POST", path: "/forgejo/v1/whatever", wantErr: "path not in the endpoint index",
		},
		{
			name: "unmatched get with deny passes", mutate: func(c *config.Config) { c.Deny = []string{"repo.delete"} },
			method: "GET", path: "/forgejo/v1/whatever",
		},
		{
			name: "unmatched non-get with toolset off", mutate: func(c *config.Config) { c.Toolsets = "repos" },
			method: "POST", path: "/forgejo/v1/whatever", wantErr: "path not in the endpoint index",
		},
		{name: "unmatched non-get default config", method: "POST", path: "/forgejo/v1/whatever"},

		// read-only
		{name: "ro get", mutate: func(c *config.Config) { c.ReadOnly = true }, method: "GET", path: "/repos/o/r"},
		{name: "ro head", mutate: func(c *config.Config) { c.ReadOnly = true }, method: "HEAD", path: "/repos/o/r"},
		{
			name: "ro post refused", mutate: func(c *config.Config) { c.ReadOnly = true },
			method: "POST", path: "/repos/o/r/labels", wantErr: "read-only mode allows only GET/HEAD",
		},
		{
			name: "ro delete refused", mutate: func(c *config.Config) { c.ReadOnly = true },
			method: "DELETE", path: "/repos/o/r/labels/1", wantErr: "read-only mode",
		},
		{name: "ro markdown post ok", mutate: func(c *config.Config) { c.ReadOnly = true }, method: "POST", path: "/markdown"},
		{name: "ro markdown raw ok", mutate: func(c *config.Config) { c.ReadOnly = true }, method: "POST", path: "/markdown/raw"},
		{name: "ro markup ok", mutate: func(c *config.Config) { c.ReadOnly = true }, method: "POST", path: "/markup"},
		{
			name: "ro registration token get blocked (action derived)", mutate: func(c *config.Config) { c.ReadOnly = true },
			method: "GET", path: "/repos/o/r/actions/runners/registration-token", wantErr: "side effects",
		},
		{
			name: "ro registration token get blocked (pattern)", mutate: func(c *config.Config) { c.ReadOnly = true },
			method: "GET", path: "/orgs/x/actions/runners/registration-token", wantErr: "side effects",
		},
		{
			name: "ro legacy admin registration token", mutate: func(c *config.Config) { c.ReadOnly = true; c.Toolsets = "all" },
			method: "GET", path: "/admin/runners/registration-token", wantErr: "side effects",
		},
		{name: "registration token ok when not read-only", method: "GET", path: "/repos/o/r/actions/runners/registration-token"},
		{
			name: "ro unmatched post refused", mutate: func(c *config.Config) { c.ReadOnly = true },
			method: "POST", path: "/forgejo/v1/x", wantErr: "read-only",
		},

		// destructive confirm
		{name: "delete repo needs confirm", method: "DELETE", path: "/repos/o/r", wantErr: `destructive: pass confirm="/repos/o/r"`},
		{name: "delete repo wrong confirm", method: "DELETE", path: "/repos/o/r", confirm: "o/r", wantErr: "destructive"},
		{name: "delete repo confirmed", method: "DELETE", path: "/repos/o/r", confirm: "/repos/o/r"},
		{
			name: "delete repo slash variant needs canonical confirm", method: "DELETE", path: "//repos/o/r/",
			confirm: "//repos/o/r/", wantErr: `destructive: pass confirm="/repos/o/r"`,
		},
		{name: "delete repo slash variant confirmed", method: "DELETE", path: "//repos/o/r/", confirm: "/repos/o/r"},
		{name: "delete org", method: "DELETE", path: "/orgs/acme", wantErr: "destructive"},
		{name: "delete team", method: "DELETE", path: "/teams/3", wantErr: "destructive"},
		{name: "delete issue", method: "DELETE", path: "/repos/o/r/issues/5", wantErr: "destructive"},
		{name: "delete package", method: "DELETE", path: "/packages/o/npm/foo/1.0.0", wantErr: "destructive"},
		{name: "delete admin user", mutate: allToolsets, method: "DELETE", path: "/admin/users/bob", wantErr: "destructive"},
		{name: "delete label is plain write", method: "DELETE", path: "/repos/o/r/labels/1"},
		{name: "unmatched delete repo-shaped", method: "DELETE", path: "/forgejo/v1/repos/o/r", wantErr: "destructive"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := evalWith(t, c.mutate, c.method, c.path, c.confirm)
			if c.wantErr == "" {
				if d.Err != "" {
					t.Fatalf("unexpected refusal: %s", d.Err)
				}
				got := ""
				if d.Op != nil {
					got = d.Op.Key()
				}
				if c.wantOp != "" && got != c.wantOp {
					t.Errorf("op = %q, want %q", got, c.wantOp)
				}
				return
			}
			if !strings.Contains(d.Err, c.wantErr) {
				t.Errorf("err = %q, want substring %q", d.Err, c.wantErr)
			}
			if !strings.HasPrefix(d.Err, "error: ") {
				t.Errorf("err %q lacks the error: prefix", d.Err)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	cfg := config.Defaults()
	pol := &policy{reg: newRegistry(t, cfg, true)}
	cases := []struct {
		method, path string
		want         tools.Mode
	}{
		{"GET", "/repos/o/r", tools.Read},
		{"head", "/repos/o/r", tools.Read},
		{"POST", "/repos/o/r/labels", tools.Write},
		{"PATCH", "/repos/o/r", tools.Write},
		{"DELETE", "/repos/o/r/labels/1", tools.Write},
		{"DELETE", "/repos/o/r", tools.Destructive},
		{"DELETE", "//repos/o/r/", tools.Destructive},
		{"POST", "/markdown", tools.Read},
		{"POST", "/markdown/raw", tools.Read},
		{"POST", "../x", tools.Write},
	}
	for _, c := range cases {
		if got := pol.classify(c.method, c.path); got != c.want {
			t.Errorf("classify(%s %s) = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}

func TestEncodedUnreservedPolicy(t *testing.T) {
	p := &policy{}
	d := p.evaluate(&config.Config{}, "DELETE", "/rep%6Fs/o/r", "")
	if !strings.Contains(d.Err, "destructive") {
		t.Errorf("encoded DELETE not confirm-gated: %+v", d)
	}
	d = p.evaluate(&config.Config{ReadOnly: true}, "GET", "/repos/o/r/actions/runners/registration%2Dtoken", "")
	if !strings.Contains(d.Err, "read-only") {
		t.Errorf("encoded registration-token not blocked: %+v", d)
	}
	if d = p.evaluate(&config.Config{}, "HEAD", "/repos/o/r", ""); d.Op == nil {
		t.Errorf("HEAD did not match the GET template")
	}
}
