package actions

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

const artifactsBody = `[` +
	`{"id":3,"name":"dist.zip","size_in_bytes":1200000,"run_id":1234,"expired":false,"expires_at":"2026-12-22T10:00:00Z","archive_download_url":"http://x/y","created_at":"2026-11-22T10:00:00Z"},` +
	`{"id":2,"name":"coverage","size_in_bytes":512,"run_id":1230,"expired":true,"expires_at":"2026-11-20T10:00:00Z"}]`

func TestArtifactsRepoWideGolden(t *testing.T) {
	s := newFake(t)
	s.replyTotal("GET /repos/o/r/actions/artifacts", "2", artifactsBody)
	text := runOK(t, s, map[string]any{"action": "artifacts", "name": "dist.zip"})
	testutil.Golden(t, "artifacts", text)
	wantQuery(t, s.only("GET /repos/o/r/actions/artifacts"), map[string][]string{"name": {"dist.zip"}, "limit": {"30"}, "page": {"1"}})
}

func TestArtifactsOfRun(t *testing.T) {
	s := newFake(t)
	s.reply("GET /repos/o/r/actions/runs", 200,
		runsBody(1, runJSON(1234, 56, "success", "ci.yml", "wip", "push", "main", sha2, "alice", "2026-11-22T09:50:00Z", 0)))
	s.reply("GET /repos/o/r/actions/runs/1234", 200,
		runJSON(1234, 56, "success", "ci.yml", "wip", "push", "main", sha2, "alice", "2026-11-22T09:50:00Z", 0))
	s.replyTotal("GET /repos/o/r/actions/runs/1234/artifacts", "0", `[]`)
	text := runOK(t, s, map[string]any{"action": "artifacts", "number": 56})
	if text != "# artifacts 0/0" {
		t.Fatalf("got %q", text)
	}
}

func TestArtifactDelete(t *testing.T) {
	s := newFake(t)
	s.reply("DELETE /repos/o/r/actions/artifacts/3", 204, "")
	if got := runOK(t, s, map[string]any{"action": "artifact_delete", "id": 3}); got != "ok: deleted artifact 3" {
		t.Fatalf("got %q", got)
	}
}

func TestArtifactDownloadFollowsStorageRedirectWithoutCredentials(t *testing.T) {
	var mu sync.Mutex
	var storageAuth []string
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		storageAuth = append(storageAuth, r.Header.Get("Authorization")+"|"+r.Header.Get("Sudo"))
		mu.Unlock()
		_, _ = w.Write([]byte("PK-zip-bytes"))
	}))
	defer storage.Close()

	s := newFake(t)
	s.on("GET /repos/o/r/actions/artifacts/3/zip", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, storage.URL+"/bucket/blob?sig=abc", http.StatusFound)
	})
	env := testutil.NewEnv(t, s, testutil.WithRepo("o/r"), testutil.WithToken("tok-secret-123456"))
	text, isErr := testutil.CallTool(t, env, runSpec(), map[string]any{"action": "artifact_download", "id": 3, "save_to": "dist.zip"})
	if isErr {
		t.Fatalf("error: %s", text)
	}
	want := filepath.Join(env.Cfg.SaveDir, "dist.zip")
	if !strings.HasPrefix(text, "ok: saved ") || !strings.HasSuffix(text, " (12B)") || !strings.Contains(text, "dist.zip") {
		t.Fatalf("got %q", text)
	}
	got, err := os.ReadFile(want)
	if err != nil || string(got) != "PK-zip-bytes" {
		t.Fatalf("saved file %s: %q %v", want, got, err)
	}
	mu.Lock()
	gotAuth := append([]string(nil), storageAuth...)
	mu.Unlock()
	if len(gotAuth) != 1 || gotAuth[0] != "|" {
		t.Fatalf("storage host must receive no credentials, got %q", gotAuth)
	}
	if a := s.only("GET /repos/o/r/actions/artifacts/3/zip").Header.Get("Authorization"); a != "token tok-secret-123456" {
		t.Errorf("instance request lost its token: %q", a)
	}

	// a second download to the same path never overwrites
	text, isErr = testutil.CallTool(t, env, runSpec(), map[string]any{"action": "artifact_download", "id": 3, "save_to": "dist.zip"})
	if !isErr || !strings.HasPrefix(text, "error: exists:") {
		t.Fatalf("got %q err=%v", text, isErr)
	}
}

func TestArtifactDownloadErrorsLeaveNothingBehind(t *testing.T) {
	s := newFake(t)
	s.reply("GET /repos/o/r/actions/artifacts/9/zip", 404, `{"message":"not found"}`)
	env := testutil.NewEnv(t, s, testutil.WithRepo("o/r"))
	text, isErr := testutil.CallTool(t, env, runSpec(), map[string]any{"action": "artifact_download", "id": 9, "save_to": "x.zip"})
	if !isErr || !strings.Contains(text, "404") {
		t.Fatalf("got %q", text)
	}
	if _, err := os.Stat(filepath.Join(env.Cfg.SaveDir, "x.zip")); err == nil {
		t.Fatal("a failed download must not leave a file")
	}
}

func TestArtifactDownloadRefusesPathOutsideRoots(t *testing.T) {
	s := newFake(t)
	s.reply("GET /repos/o/r/actions/artifacts/3/zip", 200, "zip")
	outside := filepath.Join(t.TempDir(), "evil.zip")
	text := runErr(t, s, map[string]any{"action": "artifact_download", "id": 3, "save_to": outside})
	if _, err := os.Stat(outside); err == nil {
		t.Fatalf("file written outside the save roots: %s", text)
	}
}

func TestArtifactDownloadDisabledInHTTPMode(t *testing.T) {
	s := newFake(t)
	text := runErr(t, s, map[string]any{"action": "artifact_download", "id": 3, "save_to": "a.zip"},
		testutil.WithConfig(func(c *config.Config) { c.Transport = "http" }))
	if !strings.Contains(text, "disabled in HTTP mode") {
		t.Fatalf("got %q", text)
	}
	if n := len(s.seen("GET /repos/o/r/actions/artifacts/3/zip")); n != 0 {
		t.Fatalf("nothing may be requested")
	}
}
