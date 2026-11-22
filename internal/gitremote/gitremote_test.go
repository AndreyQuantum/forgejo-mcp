package gitremote

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseRemoteURL(t *testing.T) {
	tests := []struct {
		in, host, path string
		bad            bool
	}{
		{"https://git.example.com/o/r.git", "git.example.com", "o/r.git", false},
		{"https://user:pw@Git.Example.com:8443/o/r", "git.example.com:8443", "o/r", false},
		{"http://git.example.com/sub/o/r/", "git.example.com", "sub/o/r/", false},
		{"ssh://git@git-ssh.example.com:2222/o/r.git", "git-ssh.example.com:2222", "o/r.git", false},
		{"ssh://git@host/o/r.git", "host", "o/r.git", false},
		{"git://host.example/o/r.git", "host.example", "o/r.git", false},
		{"git@github.com:o/r.git", "github.com", "o/r.git", false},
		{"host.example:o/r", "host.example", "o/r", false},
		{"git@host:/abs/o/r", "host", "abs/o/r", false},
		{"ssh://[::1]:2222/o/r", "[::1]:2222", "o/r", false},
		{"", "", "", true},
		{"/local/path", "", "", true},
		{"./rel:x", "", "", true},
		{`C:\repos\x`, "", "", true},
		{"file:///tmp/x", "", "", true},
		{"https://", "", "", true},
		{"noscheme", "", "", true},
	}
	for _, tt := range tests {
		h, p, err := ParseRemoteURL(tt.in)
		if tt.bad {
			if err == nil {
				t.Errorf("%q: expected error, got %q %q", tt.in, h, p)
			}
			continue
		}
		if err != nil || h != tt.host || p != tt.path {
			t.Errorf("%q: got %q %q %v; want %q %q", tt.in, h, p, err, tt.host, tt.path)
		}
	}
}

func TestParseRemoteErrorHidesCredentials(t *testing.T) {
	_, _, err := ParseRemoteURL("https://user:secretpw@host/%zz")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "secretpw") {
		t.Errorf("error leaks credentials: %v", err)
	}
}

func FuzzParseRemoteURL(f *testing.F) {
	for _, s := range []string{
		"https://u:p@h.example/o/r.git", "git@github.com:o/r.git", "ssh://git@h:22/o/r",
		"", ":", "a:b", "://", "[::1]:x", "C:\\x", "https://[::1", "host:/",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		h, p, err := ParseRemoteURL(s)
		if err != nil {
			return
		}
		if h == "" {
			t.Fatalf("empty host for %q", s)
		}
		if strings.HasPrefix(p, "/") {
			t.Fatalf("leading slash in path %q for %q", p, s)
		}
		if strings.Contains(h, "@") {
			t.Fatalf("credentials in host %q for %q", h, s)
		}
	})
}

// repo creates a checkout with the given git config and HEAD; returns its dir.
func repo(t *testing.T, config, head string) string {
	t.Helper()
	dir := t.TempDir()
	g := filepath.Join(dir, ".git")
	if err := os.MkdirAll(g, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(g, "config"), config)
	write(t, filepath.Join(g, "HEAD"), head)
	return dir
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func remote(name, url string) string {
	return "[remote \"" + name + "\"]\n\turl = " + url + "\n\tfetch = +refs/heads/*:refs/remotes/" + name + "/*\n"
}

func TestResolveGit(t *testing.T) {
	tests := []struct {
		name     string
		config   string
		instance string
		ssh      []string
		want     string // owner/name@remote, or "" for no match
		errHas   string
	}{
		{"https origin", remote("origin", "https://git.example.com/o/r.git"), "https://git.example.com", nil, "o/r@origin", ""},
		{"github scp ignored", remote("origin", "git@github.com:o/r.git"), "https://git.example.com", nil, "", "remotes seen: origin(host github.com)"},
		{"scp on instance host", remote("origin", "git@git.example.com:o/r.git"), "https://git.example.com", nil, "o/r@origin", ""},
		{"ssh with ssh host and port", remote("origin", "ssh://git@git-ssh.example.com:2222/o/r.git"), "https://git.example.com", []string{"git-ssh.example.com:2222"}, "o/r@origin", ""},
		{"ssh wrong port", remote("origin", "ssh://git@git-ssh.example.com:2200/o/r.git"), "https://git.example.com", []string{"git-ssh.example.com:2222"}, "", "remotes seen"},
		{"ssh host without port entry", remote("origin", "ssh://git@git-ssh.example.com:2200/o/r.git"), "https://git.example.com", []string{"git-ssh.example.com"}, "o/r@origin", ""},
		{"scp with port entry", remote("origin", "git@git-ssh.example.com:o/r.git"), "https://git.example.com", []string{"git-ssh.example.com:2222"}, "o/r@origin", ""},
		{"https remote does not use ssh port entry", remote("origin", "https://git-ssh.example.com/o/r"), "https://git.example.com", []string{"git-ssh.example.com:2222"}, "", "remotes seen"},
		{"uppercase host", remote("origin", "https://GIT.Example.COM/o/r"), "https://git.example.com", nil, "o/r@origin", ""},
		{"trailing slash", remote("origin", "https://git.example.com/o/r/"), "https://git.example.com", nil, "o/r@origin", ""},
		{"credentials", remote("origin", "https://bob:hunter2@git.example.com/o/r.git"), "https://git.example.com", nil, "o/r@origin", ""},
		{"sub-path instance", remote("origin", "https://example.com/forgejo/o/r.git"), "https://example.com/forgejo", nil, "o/r@origin", ""},
		{"sub-path instance, deeper path", remote("origin", "https://example.com/forgejo/o/r/extra"), "https://example.com/forgejo", nil, "o/r@origin", ""},
		{"sub-path instance, outside prefix", remote("origin", "https://example.com/other/o/r"), "https://example.com/forgejo", nil, "", "remotes seen"},
		{"too many segments without sub-path", remote("origin", "https://git.example.com/o/r/extra"), "https://git.example.com", nil, "", "remotes seen"},
		{"one segment", remote("origin", "https://git.example.com/o"), "https://git.example.com", nil, "", "remotes seen"},
		{
			"origin before upstream before others",
			remote("zeta", "https://git.example.com/z/z") + remote("upstream", "https://git.example.com/u/u") + remote("origin", "https://git.example.com/o/o"),
			"https://git.example.com", nil, "o/o@origin", "",
		},
		{
			"upstream before alphabetical",
			remote("zeta", "https://git.example.com/z/z") + remote("alpha", "https://git.example.com/a/a") + remote("upstream", "https://git.example.com/u/u"),
			"https://git.example.com", nil, "u/u@upstream", "",
		},
		{
			"alphabetical among others",
			remote("zeta", "https://git.example.com/z/z") + remote("alpha", "https://git.example.com/a/a"),
			"https://git.example.com", nil, "a/a@alpha", "",
		},
		{
			"first matching remote wins past a foreign origin",
			remote("origin", "git@github.com:o/r.git") + remote("fj", "https://git.example.com/me/r"),
			"https://git.example.com", nil, "me/r@fj", "",
		},
		{
			"insteadOf",
			"[url \"git@git.example.com:\"]\n\tinsteadOf = fj:\n" + remote("origin", "fj:o/r.git"),
			"https://git.example.com", nil, "o/r@origin", "",
		},
		{
			"insteadOf longest prefix wins",
			"[url \"https://wrong.example.com/\"]\n\tinsteadOf = fj\n[url \"https://git.example.com/\"]\n\tinsteadOf = fj:\n" + remote("origin", "fj:o/r"),
			"https://git.example.com", nil, "o/r@origin", "",
		},
		{
			"pushInsteadOf as second candidate",
			"[url \"ssh://git@git.example.com/\"]\n\tpushInsteadOf = https://mirror.example.org/\n" + remote("origin", "https://mirror.example.org/o/r"),
			"https://git.example.com", nil, "o/r@origin", "",
		},
		{
			"comments and CRLF",
			"# c\r\n; c\r\n[remote \"origin\"] ; trailing\r\n\turl = https://git.example.com/o/r.git # note\r\n",
			"https://git.example.com", nil, "o/r@origin", "",
		},
		{"no remotes", "[core]\n\tbare = false\n", "https://git.example.com", nil, "", "no default repo: pass repo=owner/name or set FORGEJO_REPO"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := repo(t, tt.config, "ref: refs/heads/main\n")
			d := NewDetector(Options{InstanceURL: tt.instance, SSHHosts: tt.ssh, Dir: dir})
			o, n, src, err := d.Resolve("")
			if tt.want == "" {
				if err == nil || !strings.Contains(err.Error(), tt.errHas) || !strings.HasPrefix(err.Error(), "no default repo: pass repo=owner/name or set FORGEJO_REPO") {
					t.Fatalf("err = %v, want containing %q", err, tt.errHas)
				}
				if strings.Contains(err.Error(), "hunter2") {
					t.Fatalf("leak: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := o + "/" + n + "@" + strings.TrimPrefix(src, "git:"); got != tt.want || !strings.HasPrefix(src, "git:") {
				t.Fatalf("got %s (src %s), want %s", got, src, tt.want)
			}
			if go1, gn, ok := d.GitRepo(); !ok || go1 != o || gn != n {
				t.Fatalf("GitRepo = %s/%s %v", go1, gn, ok)
			}
		})
	}
}

func TestResolveNoGit(t *testing.T) {
	d := NewDetector(Options{InstanceURL: "https://git.example.com", Dir: t.TempDir()})
	_, _, _, err := d.Resolve("")
	if err == nil || err.Error() != "no default repo: pass repo=owner/name or set FORGEJO_REPO" {
		t.Fatalf("err = %v", err)
	}
	if _, _, ok := d.GitRepo(); ok {
		t.Error("GitRepo should report none")
	}
	if b := d.CurrentBranch(); b != "" {
		t.Errorf("branch = %q", b)
	}
}

func TestResolveParamAndEnv(t *testing.T) {
	d := NewDetector(Options{InstanceURL: "https://git.example.com/forgejo", DefaultRepo: "own/def", Dir: t.TempDir()})
	tests := []struct {
		in, want string
		bad      bool
	}{
		{"a/b", "a/b", false},
		{"a/b.git", "a/b", false},
		{" a/b/ ", "a/b", false},
		{"b", "own/b", false},
		{"https://git.example.com/forgejo/a/b", "a/b", false},
		{"https://git.example.com/forgejo/a/b.git", "a/b", false},
		{"https://GIT.example.com/forgejo/a/b/pulls/3", "a/b", false},
		{"https://git.example.com/forgejo/a", "", true},
		{"https://git.example.com/other/a/b", "", true},
		{"https://elsewhere.com/forgejo/a/b", "", true},
		{"a/b/c", "", true},
	}
	for _, tt := range tests {
		o, n, src, err := d.Resolve(tt.in)
		if tt.bad {
			if err == nil {
				t.Errorf("%q: expected error, got %s/%s", tt.in, o, n)
			}
			continue
		}
		if err != nil || o+"/"+n != tt.want || src != "param" {
			t.Errorf("%q: got %s/%s %s %v; want %s", tt.in, o, n, src, err, tt.want)
		}
	}
	o, n, src, err := d.Resolve("")
	if err != nil || o != "own" || n != "def" || src != "env" {
		t.Errorf("env default: %s/%s %s %v", o, n, src, err)
	}
}

func TestNameAloneUsesGitOwner(t *testing.T) {
	dir := repo(t, remote("origin", "https://git.example.com/gitowner/proj.git"), "ref: refs/heads/main\n")
	d := NewDetector(Options{InstanceURL: "https://git.example.com", Dir: dir})
	o, n, src, err := d.Resolve("other")
	if err != nil || o != "gitowner" || n != "other" || src != "param" {
		t.Fatalf("%s/%s %s %v", o, n, src, err)
	}
	d2 := NewDetector(Options{InstanceURL: "https://git.example.com", Dir: t.TempDir()})
	if _, _, _, err := d2.Resolve("other"); err == nil {
		t.Fatal("name alone without any default must fail")
	}
}

func TestDetectionCaching(t *testing.T) {
	dir := repo(t, "[core]\n", "ref: refs/heads/main\n")
	d := NewDetector(Options{InstanceURL: "https://git.example.com", Dir: dir})
	now := time.Unix(1_000_000, 0)
	d.now = func() time.Time { return now }

	if _, _, _, err := d.Resolve(""); err == nil {
		t.Fatal("expected failure")
	}
	// Fix the config; within 30 s the failure is still served.
	write(t, filepath.Join(dir, ".git", "config"), remote("origin", "https://git.example.com/o/r"))
	now = now.Add(29 * time.Second)
	if _, _, _, err := d.Resolve(""); err == nil {
		t.Fatal("failure should be cached for 30s")
	}
	now = now.Add(2 * time.Second)
	o, n, _, err := d.Resolve("")
	if err != nil || o != "o" || n != "r" {
		t.Fatalf("after retry window: %s/%s %v", o, n, err)
	}
	// Success is cached for the process life even when the config changes.
	write(t, filepath.Join(dir, ".git", "config"), remote("origin", "https://git.example.com/x/y"))
	now = now.Add(time.Hour)
	if o, n, _, _ := d.Resolve(""); o != "o" || n != "r" {
		t.Fatalf("success must stay cached, got %s/%s", o, n)
	}
}

func TestWalkUpFromSubdir(t *testing.T) {
	dir := repo(t, remote("origin", "https://git.example.com/o/r"), "ref: refs/heads/feat/x\n")
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	d := NewDetector(Options{InstanceURL: "https://git.example.com", Dir: sub})
	if o, n, ok := d.GitRepo(); !ok || o != "o" || n != "r" {
		t.Fatalf("GitRepo = %s/%s %v", o, n, ok)
	}
	if b := d.CurrentBranch(); b != "feat/x" {
		t.Errorf("branch = %q", b)
	}
}

func TestCurrentBranch(t *testing.T) {
	dir := repo(t, remote("origin", "https://git.example.com/o/r"), "ref: refs/heads/main\n")
	d := NewDetector(Options{InstanceURL: "https://git.example.com", Dir: dir})
	if b := d.CurrentBranch(); b != "main" {
		t.Errorf("branch = %q", b)
	}
	write(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/other\r\n")
	if b := d.CurrentBranch(); b != "other" {
		t.Errorf("branch is read live, got %q", b)
	}
	write(t, filepath.Join(dir, ".git", "HEAD"), "0123456789abcdef0123456789abcdef01234567\n")
	if b := d.CurrentBranch(); b != "" {
		t.Errorf("detached HEAD: branch = %q", b)
	}
	write(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/tags/v1\n")
	if b := d.CurrentBranch(); b != "" {
		t.Errorf("non-branch ref: branch = %q", b)
	}
}

func TestLinkedWorktree(t *testing.T) {
	main := repo(t, remote("origin", "https://git.example.com/o/r"), "ref: refs/heads/main\n")
	wtGit := filepath.Join(main, ".git", "worktrees", "wt1")
	write(t, filepath.Join(wtGit, "HEAD"), "ref: refs/heads/wt-branch\n")
	write(t, filepath.Join(wtGit, "commondir"), "../..\n")
	wt := t.TempDir()
	write(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.ToSlash(wtGit)+"\n")

	d := NewDetector(Options{InstanceURL: "https://git.example.com", Dir: wt})
	if o, n, ok := d.GitRepo(); !ok || o != "o" || n != "r" {
		t.Fatalf("worktree GitRepo = %s/%s %v", o, n, ok)
	}
	if b := d.CurrentBranch(); b != "wt-branch" {
		t.Errorf("worktree branch = %q", b)
	}
}

func TestGitFileRelativeAndSubmodule(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "real", "config"), remote("origin", "https://git.example.com/o/sub"))
	write(t, filepath.Join(root, "real", "HEAD"), "ref: refs/heads/dev\n")
	write(t, filepath.Join(root, "checkout", ".git"), "gitdir: ../real\n")
	d := NewDetector(Options{InstanceURL: "https://git.example.com", Dir: filepath.Join(root, "checkout")})
	if o, n, ok := d.GitRepo(); !ok || o != "o" || n != "sub" {
		t.Fatalf("GitRepo = %s/%s %v", o, n, ok)
	}
	if b := d.CurrentBranch(); b != "dev" {
		t.Errorf("branch = %q", b)
	}
}

func TestBadGitFile(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, ".git"), "garbage\n")
	d := NewDetector(Options{InstanceURL: "https://git.example.com", Dir: dir})
	if _, _, ok := d.GitRepo(); ok {
		t.Fatal("garbage .git file must not detect a repo")
	}
}

func TestParseGitConfig(t *testing.T) {
	cfg := "[Remote \"Origin\"]\n  URL = \"https://h/o/r\"\n  url = https://h/o/r2\n[core]\n  x\n[remote.legacy]\n  url = https://h/l/l\n"
	secs := parseGitConfig(cfg)
	if len(secs) != 3 {
		t.Fatalf("sections = %d", len(secs))
	}
	if secs[0].Name != "remote" || secs[0].Sub != "Origin" || len(secs[0].Keys["url"]) != 2 || secs[0].Keys["url"][0] != "https://h/o/r" {
		t.Errorf("section 0: %+v", secs[0])
	}
	if secs[1].Keys["x"][0] != "true" {
		t.Errorf("bare key: %+v", secs[1])
	}
	if secs[2].Name != "remote" || secs[2].Sub != "legacy" {
		t.Errorf("legacy form: %+v", secs[2])
	}
}

func TestLocateFollowsGitSymlink(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real-git")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(work, ".git")); err != nil {
		t.Skip("symlinks not available: " + err.Error())
	}
	d := NewDetector(Options{InstanceURL: "https://forge.example.com", Dir: work})
	loc, err := d.locate()
	if err != nil || loc.gitDir != filepath.Join(work, ".git") {
		t.Errorf("loc=%+v err=%v", loc, err)
	}
}
