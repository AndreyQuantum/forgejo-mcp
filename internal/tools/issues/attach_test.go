package issues

import (
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

const assetsJSON = `[
 {"id":4,"name":"shot.png","size":1234,"download_count":7,"browser_download_url":"http://forgejo.example.com/attachments/aaaa-bbbb","type":"attachment"},
 {"id":5,"name":"big.zip","size":2500000,"browser_download_url":"https://evil.example.org/x.zip","type":"external"}
]`

func TestAttachList(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{
		"GET /repos/o/r/issues/12/assets":          jsonH(200, assetsJSON),
		"GET /repos/o/r/issues/comments/55/assets": jsonH(200, `[]`),
	})
	testutil.Golden(t, "attach_list", mustRun(t, f, "attach", map[string]any{"action": "list", "n": 12}))
	eq(t, "comment", mustRun(t, f, "attach", map[string]any{"action": "list", "comment_id": 55}), "# attachments 0\n")
	mustFail(t, f, "attach", map[string]any{"action": "list"}, "pass n (issue) or comment_id")
}

func TestAttachUploadMultipart(t *testing.T) {
	var gotName, gotContent, gotCT string
	var gotLen int64
	f := newFake(t, map[string]handlerFunc{
		"POST /repos/o/r/issues/12/assets": func(w http.ResponseWriter, r *http.Request, body string) {
			gotCT = r.Header.Get("Content-Type")
			gotLen = r.ContentLength
			_, params, err := mime.ParseMediaType(gotCT)
			if err != nil {
				t.Errorf("content type %q: %v", gotCT, err)
			}
			mr := multipart.NewReader(strings.NewReader(body), params["boundary"])
			part, err := mr.NextPart()
			if err != nil {
				t.Errorf("multipart: %v", err)
				return
			}
			gotName = part.FormName() + "|" + part.FileName()
			b, _ := io.ReadAll(part)
			gotContent = string(b)
			jsonH(201, `{"id":9,"name":"report.txt","size":11}`)(w, r, body)
		},
	})
	opt := withCfg(func(c *config.Config) {
		if err := os.WriteFile(filepath.Join(c.SaveDir, "up.txt"), []byte("hello world"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	got := mustRun(t, f, "attach", map[string]any{"action": "upload", "n": 12, "file": "up.txt", "name": "report.txt"}, opt)
	eq(t, "result", got, "ok: uploaded attachment 9 report.txt (11B) to #12")
	eq(t, "part", gotName, "attachment|report.txt")
	eq(t, "content", gotContent, "hello world")
	if !strings.HasPrefix(gotCT, "multipart/form-data; boundary=") {
		t.Errorf("content type %q", gotCT)
	}
	if gotLen <= 11 {
		t.Errorf("content length %d: upload must not be chunked", gotLen)
	}
	eq(t, "name query", f.only("POST", "/repos/o/r/issues/12/assets").Query.Get("name"), "report.txt")
}

func TestAttachUploadRefusesOutsideAndDotPaths(t *testing.T) {
	f := newFake(t, nil)
	mustFail(t, f, "attach", map[string]any{"action": "upload", "n": 12, "file": "../secret.txt"}, "starts with a dot")
	mustFail(t, f, "attach", map[string]any{"action": "upload", "n": 12, "file": ".env"}, "starts with a dot")
	mustFail(t, f, "attach", map[string]any{"action": "upload", "n": 12, "file": "missing.txt"}, "not found")
	if len(f.find("POST", "/repos/o/r/issues/12/assets")) != 0 {
		t.Error("a refused upload must not reach the server")
	}
}

func TestAttachDownload(t *testing.T) {
	var saveDir string
	f := newFake(t, map[string]handlerFunc{
		"GET /repos/o/r/issues/12/assets": jsonH(200, assetsJSON),
		"GET /attachments/aaaa-bbbb": func(w http.ResponseWriter, _ *http.Request, _ string) {
			_, _ = io.WriteString(w, "PNGDATA")
		},
	})
	opt := withCfg(func(c *config.Config) { saveDir = c.SaveDir })
	got := mustRun(t, f, "attach", map[string]any{"action": "download", "n": 12, "id": 4, "save_to": "out/shot.png"}, opt)
	dest := filepath.Join(saveDir, "out", "shot.png")
	contains(t, got, "ok: saved attachment 4 shot.png to ", "(7B)")
	if b, err := os.ReadFile(dest); err != nil || string(b) != "PNGDATA" {
		t.Fatalf("saved file: %q %v", b, err)
	}
	dl := f.only("GET", "/attachments/aaaa-bbbb")
	if dl.Header.Get("Authorization") == "" {
		t.Error("an internal attachment is downloaded with the token")
	}

	// default destination is the attachment name
	f2 := newFake(t, map[string]handlerFunc{
		"GET /repos/o/r/issues/12/assets": jsonH(200, assetsJSON),
		"GET /attachments/aaaa-bbbb":      func(w http.ResponseWriter, _ *http.Request, _ string) { _, _ = io.WriteString(w, "X") },
	})
	mustRun(t, f2, "attach", map[string]any{"action": "download", "n": 12, "id": 4}, withCfg(func(c *config.Config) { saveDir = c.SaveDir }))
	if _, err := os.Stat(filepath.Join(saveDir, "shot.png")); err != nil {
		t.Errorf("default save name: %v", err)
	}
}

func TestAttachDownloadNeverOverwrites(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{
		"GET /repos/o/r/issues/12/assets": jsonH(200, assetsJSON),
		"GET /attachments/aaaa-bbbb":      func(w http.ResponseWriter, _ *http.Request, _ string) { _, _ = io.WriteString(w, "X") },
	})
	opt := withCfg(func(c *config.Config) {
		if err := os.WriteFile(filepath.Join(c.SaveDir, "shot.png"), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	mustFail(t, f, "attach", map[string]any{"action": "download", "n": 12, "id": 4}, "exists:", opt)
	f.none("GET", "/attachments/aaaa-bbbb") // refused before any bytes are fetched
}

func TestAttachDownloadExternalAndMissing(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{"GET /repos/o/r/issues/12/assets": jsonH(200, assetsJSON)})
	got := mustRun(t, f, "attach", map[string]any{"action": "download", "n": 12, "id": 5})
	eq(t, "external", got, "external asset: https://evil.example.org/x.zip (not downloaded; credentials are not sent to other hosts)")
	for _, r := range f.all() {
		if strings.Contains(r, "x.zip") {
			t.Errorf("external asset requested: %v", f.all())
		}
	}
	mustFail(t, f, "attach", map[string]any{"action": "download", "n": 12, "id": 99}, "404 attachment 99 not found")
}

func TestAttachDownloadUsesOnlyThePathOfForeignHostURLs(t *testing.T) {
	// An internal asset whose URL names another host (ROOT_URL differs from
	// FORGEJO_URL) is fetched from the instance host, never from that host.
	foreign := `[{"id":4,"name":"a.txt","size":1,"browser_download_url":"https://cdn.example.org/attachments/zzzz","type":"attachment"}]`
	f := newFake(t, map[string]handlerFunc{
		"GET /repos/o/r/issues/12/assets": jsonH(200, foreign),
		"GET /attachments/zzzz":           func(w http.ResponseWriter, _ *http.Request, _ string) { _, _ = io.WriteString(w, "Z") },
	})
	mustRun(t, f, "attach", map[string]any{"action": "download", "n": 12, "id": 4})
	f.only("GET", "/attachments/zzzz")
}

func TestAttachDownloadDisabledInHTTPMode(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{"GET /repos/o/r/issues/12/assets": jsonH(200, assetsJSON)})
	mustFail(t, f, "attach", map[string]any{"action": "download", "n": 12, "id": 4},
		"disabled in HTTP mode", withCfg(func(c *config.Config) { c.Transport = "http" }))
}

func TestAttachRenameDelete(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{
		"PATCH /repos/o/r/issues/12/assets/4":           jsonH(201, `{"id":4}`),
		"DELETE /repos/o/r/issues/12/assets/4":          emptyH(204),
		"DELETE /repos/o/r/issues/comments/55/assets/6": emptyH(204),
	})
	eq(t, "rename", mustRun(t, f, "attach", map[string]any{"action": "rename", "n": 12, "id": 4, "name": "new.png"}), "ok: renamed attachment 4 to new.png")
	eq(t, "body", f.only("PATCH", "/repos/o/r/issues/12/assets/4").Body, `{"name":"new.png"}`)
	eq(t, "delete", mustRun(t, f, "attach", map[string]any{"action": "delete", "n": 12, "id": 4}), "ok: deleted attachment 4")
	eq(t, "comment delete", mustRun(t, f, "attach", map[string]any{"action": "delete", "comment_id": 55, "id": 6}), "ok: deleted attachment 6")
}

func TestAttachUploadMalformedResponseOmitsID(t *testing.T) {
	f := newFake(t, map[string]handlerFunc{"POST /repos/o/r/issues/12/assets": jsonH(201, `not json`)})
	opt := withCfg(func(c *config.Config) {
		if err := os.WriteFile(filepath.Join(c.SaveDir, "up.txt"), []byte("hi"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	got := mustRun(t, f, "attach", map[string]any{"action": "upload", "n": 12, "file": "up.txt"}, opt)
	eq(t, "result", got, "ok: uploaded attachment up.txt (2B) to #12")
}
