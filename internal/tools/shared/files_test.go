package shared

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveAndOpenLocal(t *testing.T) {
	base := t.TempDir()
	r := &FileRoots{SaveDir: filepath.Join(base, "save")}

	final, n, err := r.SaveFile("out/a.txt", strings.NewReader("hello"))
	if err != nil || n != 5 {
		t.Fatalf("save: %v n=%d", err, n)
	}
	if want := filepath.Join(base, "save", "out", "a.txt"); final != want {
		t.Fatalf("final = %q, want %q", final, want)
	}
	if _, _, err := r.SaveFile("out/a.txt", strings.NewReader("x")); err == nil || !strings.HasPrefix(err.Error(), "exists: ") {
		t.Fatalf("overwrite: %v", err)
	}
	if b, _ := os.ReadFile(final); string(b) != "hello" {
		t.Fatalf("content = %q", b)
	}
	// absolute path under the root is accepted, and readable back
	f, size, err := r.OpenLocal(final)
	if err != nil || size != 5 {
		t.Fatalf("open: %v size=%d", err, size)
	}
	f.Close()
	if _, _, err := r.OpenLocal("missing.txt"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing: %v", err)
	}
}

func TestFileRootsRefusals(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	save := filepath.Join(base, "save")
	tokenFile := filepath.Join(save, "token")
	r := &FileRoots{SaveDir: save, TokenFile: tokenFile}
	if err := os.MkdirAll(save, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenFile, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{
		"", "../outside.txt", "a/../../outside.txt", ".git/config", "sub/.claude/x", ".mcp.json",
		outside, filepath.Join(save, ".hidden"), "token", tokenFile, "sub/..",
	} {
		if _, _, err := r.OpenLocal(p); err == nil {
			t.Errorf("OpenLocal(%q) accepted", p)
		}
		if _, _, err := r.SaveFile(p, strings.NewReader("x")); err == nil {
			t.Errorf("SaveFile(%q) accepted", p)
		}
	}
	if b, _ := os.ReadFile(outside); string(b) != "secret" {
		t.Fatal("outside file modified")
	}
	if b, _ := os.ReadFile(tokenFile); string(b) != "tok" {
		t.Fatal("token file modified")
	}

	dis := &FileRoots{SaveDir: save, Disabled: true}
	if _, _, err := dis.SaveFile("a", strings.NewReader("x")); err == nil || !strings.Contains(err.Error(), "FORGEJO_HTTP_FILES") {
		t.Fatalf("disabled: %v", err)
	}
}

func TestSymlinkEscapeRefused(t *testing.T) {
	base := t.TempDir()
	save := filepath.Join(base, "save")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(save, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(save, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	r := &FileRoots{SaveDir: save}
	if _, _, err := r.SaveFile("link/x.txt", strings.NewReader("x")); err == nil {
		t.Fatal("symlink escape accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "x.txt")); err == nil {
		t.Fatal("file written outside the root")
	}
}

func TestCWDRoot(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	save := filepath.Join(t.TempDir(), "save")
	if _, _, err := (&FileRoots{SaveDir: save}).SaveFile(filepath.Join(cwd, "f.txt"), strings.NewReader("x")); err == nil {
		t.Fatal("cwd accepted without AllowCWD")
	}
	final, _, err := (&FileRoots{SaveDir: save, AllowCWD: true}).SaveFile(filepath.Join(cwd, "f.txt"), strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(final); string(b) != "x" {
		t.Fatal("not written")
	}
}

func TestUploadSizeLimit(t *testing.T) {
	r := &FileRoots{SaveDir: t.TempDir()}
	p := filepath.Join(r.SaveDir, "big.bin")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxUpload + 1); err != nil {
		f.Close()
		t.Skip("sparse files unavailable")
	}
	f.Close()
	if _, _, err := r.OpenLocal("big.bin"); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v", err)
	}
}
