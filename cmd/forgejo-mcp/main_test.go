package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func buildBinary(t *testing.T) string {
	t.Helper()
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	bin := filepath.Join(t.TempDir(), "forgejo-mcp"+exe)
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func cleanEnv(extra ...string) []string {
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "FORGEJO_") {
			env = append(env, e)
		}
	}
	return append(env, extra...)
}

func TestStdioSmoke(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/version":
			_, _ = w.Write([]byte(`{"version":"17.0.2"}`))
		case "/api/v1/user":
			_, _ = w.Write([]byte(`{"login":"alice","id":4}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer fake.Close()
	bin := buildBinary(t)

	cmd := exec.Command(bin)
	cmd.Env = cleanEnv("FORGEJO_URL="+fake.URL, "FORGEJO_TOKEN=secret-token-xyz", "FORGEJO_DIR="+t.TempDir())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	// The child writes stderr from an os/exec copy goroutine while the test
	// reads it, so the buffer needs a lock.
	var stderr lockedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	defer func() {
		_ = stdin.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
		}
	}()

	lines := make(chan string, 16)
	go func() {
		defer close(done)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	send := func(s string) {
		if _, err := stdin.Write([]byte(s + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	recv := func() map[string]any {
		t.Helper()
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("stdout closed; stderr: %s", stderr.String())
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(l), &m); err != nil {
				t.Fatalf("stdout line is not protocol JSON: %q", l)
			}
			if m["jsonrpc"] != "2.0" {
				t.Fatalf("not a JSON-RPC message: %q", l)
			}
			return m
		case <-time.After(20 * time.Second):
			t.Fatalf("timeout waiting for a reply; stderr: %s", stderr.String())
		}
		return nil
	}

	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"1"}}}`)
	init := recv()
	res, _ := init["result"].(map[string]any)
	if res == nil || res["instructions"] == nil || !strings.Contains(res["instructions"].(string), "forgejo-mcp for 127.0.0.1:") {
		t.Fatalf("initialize result: %v", init)
	}
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	list := recv()
	if list["id"] != float64(2) {
		t.Fatalf("tools/list reply: %v", list)
	}
	if strings.Contains(stderr.String(), "secret-token-xyz") {
		t.Error("token leaked to stderr")
	}
}

func TestVersionAndConfigErrors(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "--version")
	cmd.Env = cleanEnv()
	out, err := cmd.Output()
	if err != nil || !strings.HasPrefix(string(out), "forgejo-mcp ") {
		t.Errorf("--version: %q %v", out, err)
	}

	cmd = exec.Command(bin)
	cmd.Env = cleanEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	var ee *exec.ExitError
	ok := errors.As(err, &ee)
	if !ok || ee.ExitCode() != 2 {
		t.Errorf("missing FORGEJO_URL: err=%v want exit 2", err)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "FORGEJO_URL") {
		t.Errorf("stdout %q stderr %q", stdout.String(), stderr.String())
	}
}

func TestCheck(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/version":
			_, _ = w.Write([]byte(`{"version":"17.0.2"}`))
		case "/api/v1/user":
			_, _ = w.Write([]byte(`{"login":"alice","id":4,"is_admin":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer fake.Close()
	bin := buildBinary(t)
	cmd := exec.Command(bin, "--check")
	cmd.Env = cleanEnv("FORGEJO_URL="+fake.URL, "FORGEJO_TOKEN=tok")
	out, err := cmd.Output()
	if err != nil || !strings.HasPrefix(string(out), "ok: forgejo 17.0.2") || !strings.Contains(string(out), "alice (admin)") {
		t.Errorf("--check: %q %v", out, err)
	}
}

// lockedBuffer is a bytes.Buffer safe for one writer and concurrent readers.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
