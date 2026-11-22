package content

import (
	"mime"
	"mime/multipart"
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

const (
	relJSON = `{"id":9,"tag_name":"v1.0","name":"First release","draft":false,"prerelease":false,
"target_commitish":"main","published_at":"2026-11-20T08:01:00Z","created_at":"2026-11-20T08:00:00Z",
"author":{"login":"alice"},"html_url":"https://forgejo.example.com/o/r/releases/tag/v1.0",
"body":"Notes line 1\nNotes line 2",
"assets":[{"id":4,"name":"file.zip","size":1234567,"download_count":7,"type":"attachment","browser_download_url":"https://forgejo.example.com/o/r/releases/download/v1.0/file.zip"},
{"id":5,"name":"link","size":0,"download_count":0,"type":"external","browser_download_url":"https://forgejo.example.com/attachments/ext-uuid"}]}`
	relListJSON = `[` + relJSON + `,{"id":8,"tag_name":"v0.9","name":"v0.9","draft":true,"prerelease":true,
"published_at":"","created_at":"2026-11-01T10:00:00Z","assets":[]}]`
)

func TestReleaseSpecContract(t *testing.T) {
	specContract(t, specOf(t, registerRelease))
	specContract(t, specOf(t, registerFile))
	specContract(t, specOf(t, registerCommit))
}

func TestReleaseList(t *testing.T) {
	te := newTool(t, registerRelease, map[string]http.HandlerFunc{
		"GET /repos/o/r/releases": jsTotal("2", relListJSON),
	})
	got := te.ok(map[string]any{"action": "list", "prerelease": true, "q": "v", "limit": 20})
	testutil.Golden(t, "release_list", got)
	q := te.fake.only(t, "GET", "/repos/o/r/releases").Query
	contains(t, q, "pre-release=true", "q=v", "limit=20", "page=1")
	lacks(t, q, "draft=")
}

func TestReleaseGet(t *testing.T) {
	te := newTool(t, registerRelease, map[string]http.HandlerFunc{
		"GET /repos/o/r/releases/9":            js(relJSON),
		"GET /repos/o/r/releases/latest":       js(relJSON),
		"GET /repos/o/r/releases/tags/v1.0":    js(relJSON),
		"GET /repos/o/r/releases/tags/v1%2Frc": js(relJSON),
	})
	got := te.ok(map[string]any{"action": "get", "id": 9})
	testutil.Golden(t, "release_get", got)
	te.ok(map[string]any{"action": "get", "tag": "latest"})
	te.ok(map[string]any{"action": "get", "tag": "v1.0"})
	// the tag is ONE path segment: a slash is escaped, not routed
	te.ok(map[string]any{"action": "get", "tag": "v1/rc"})
	te.fake.only(t, "GET", "/repos/o/r/releases/tags/v1%2Frc")
	contains(t, te.fail(map[string]any{"action": "get"}), "pass id, tag or tag=latest")
}

func TestReleaseGetBodyCap(t *testing.T) {
	long := strings.Repeat("x", 2600)
	te := newTool(t, registerRelease, map[string]http.HandlerFunc{
		"GET /repos/o/r/releases/9": js(`{"id":9,"tag_name":"v1","name":"v1","body":"` + long + `"}`),
	})
	got := te.ok(map[string]any{"action": "get", "id": 9})
	contains(t, got, "[truncated: 2000 of 2600 chars; pass full=true]")
	got = te.ok(map[string]any{"action": "get", "id": 9, "full": true})
	lacks(t, got, "[truncated:")
}

func TestReleaseCreate(t *testing.T) {
	te := newTool(t, registerRelease, map[string]http.HandlerFunc{
		"POST /repos/o/r/releases": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(relJSON))
		},
	})
	got := te.ok(map[string]any{
		"action": "create", "tag": "v1.0", "title": "First release", "body": "hi",
		"target": "main", "draft": false, "prerelease": true,
	})
	testutil.Golden(t, "release_create", got)
	body := jsonMap(t, te.fake.only(t, "POST", "/repos/o/r/releases").Body)
	want := map[string]any{
		"tag_name": "v1.0", "name": "First release", "body": "hi", "target_commitish": "main",
		"draft": false, "prerelease": true,
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("body[%s] = %v, want %v", k, body[k], v)
		}
	}
}

func TestReleaseCreateConflict(t *testing.T) {
	te := newTool(t, registerRelease, map[string]http.HandlerFunc{
		"POST /repos/o/r/releases": status(409, `{"message":"Release already exists"}`),
	})
	got := te.fail(map[string]any{"action": "create", "tag": "v1.0"})
	contains(t, got, "409", "hint: release exists for tag; use release.edit")
}

func TestReleaseEdit(t *testing.T) {
	te := newTool(t, registerRelease, map[string]http.HandlerFunc{
		"GET /repos/o/r/releases/tags/v1.0": js(relJSON),
		"PATCH /repos/o/r/releases/9":       js(relJSON),
	})
	// empty text cannot be set: the server would silently ignore it
	got := te.fail(map[string]any{"action": "edit", "id": 9, "body": ""})
	contains(t, got, "cannot be emptied")
	contains(t, te.fail(map[string]any{"action": "edit", "id": 9}), "nothing to edit")
	got = te.ok(map[string]any{"action": "edit", "tag": "v1.0", "title": "New", "draft": true})
	contains(t, got, "ok: edited release 9 v1.0: title,draft")
	body := jsonMap(t, te.fake.only(t, "PATCH", "/repos/o/r/releases/9").Body)
	if body["name"] != "New" || body["draft"] != true || len(body) != 2 {
		t.Errorf("patch body: %v", body)
	}
}

func TestReleaseDelete(t *testing.T) {
	te := newTool(t, registerRelease, map[string]http.HandlerFunc{
		"DELETE /repos/o/r/releases/9":         status(204, ""),
		"DELETE /repos/o/r/releases/tags/v1.0": status(204, ""),
	})
	got := te.ok(map[string]any{"action": "delete", "id": 9})
	contains(t, got, "ok: deleted release 9", "git tag may remain")
	te.ok(map[string]any{"action": "delete", "tag": "v1.0"})
	te.fake.only(t, "DELETE", "/repos/o/r/releases/tags/v1.0")
	contains(t, te.fail(map[string]any{"action": "delete"}), "pass id or a tag")
}

func TestReleaseAssets(t *testing.T) {
	te := newTool(t, registerRelease, map[string]http.HandlerFunc{
		"GET /repos/o/r/releases/9/assets":      js(`[{"id":4,"name":"file.zip","size":1234567,"download_count":7,"type":"attachment"},{"id":5,"name":"link","type":"external"}]`),
		"GET /repos/o/r/releases/tags/v1.0":     js(relJSON),
		"PATCH /repos/o/r/releases/9/assets/4":  status(201, `{"id":4,"name":"new.zip"}`),
		"DELETE /repos/o/r/releases/9/assets/4": status(204, ""),
	})
	got := te.ok(map[string]any{"action": "asset_list", "tag": "v1.0"})
	testutil.Golden(t, "release_asset_list", got)
	got = te.ok(map[string]any{"action": "asset_edit", "id": 9, "asset_id": 4, "name": "new.zip"})
	contains(t, got, "ok: renamed asset 4 to new.zip")
	body := jsonMap(t, te.fake.only(t, "PATCH", "/repos/o/r/releases/9/assets/4").Body)
	if body["name"] != "new.zip" {
		t.Errorf("body: %v", body)
	}
	contains(t, te.ok(map[string]any{"action": "asset_delete", "id": 9, "asset_id": 4}), "ok: deleted asset 4")
}

func TestAssetUploadFile(t *testing.T) {
	var gotType, gotQuery string
	var gotLen int64
	var gotBody []byte
	te := newTool(t, registerRelease, map[string]http.HandlerFunc{
		"POST /repos/o/r/releases/9/assets": func(w http.ResponseWriter, r *http.Request) {
			gotType, gotQuery, gotLen = r.Header.Get("Content-Type"), r.URL.RawQuery, r.ContentLength
			gotBody = readAll(r)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":6,"name":"out.bin","size":5,"type":"attachment"}`))
		},
	})
	if err := os.WriteFile(filepath.Join(te.env.Cfg.SaveDir, "out.bin"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := te.ok(map[string]any{"action": "asset_upload", "id": 9, "file": "out.bin"})
	contains(t, got, "ok: uploaded asset 6 out.bin 5B")
	if gotType != "application/octet-stream" || gotQuery != "name=out.bin" || gotLen != 5 || string(gotBody) != "hello" {
		t.Errorf("upload: type=%q query=%q len=%d body=%q", gotType, gotQuery, gotLen, gotBody)
	}
	// a path outside the save dir is refused before any request
	contains(t, te.fail(map[string]any{"action": "asset_upload", "id": 9, "file": "../escape.bin"}), "error:")
	contains(t, te.fail(map[string]any{"action": "asset_upload", "id": 9}), "pass file")
	contains(t, te.fail(map[string]any{"action": "asset_upload", "id": 9, "file": "a", "url": "https://x/y"}), "mutually exclusive")
}

// TestAssetUploadExternal pins quirk A3: an external asset is sent as
// multipart/form-data with an EMPTY form, never as a raw body.
func TestAssetUploadExternal(t *testing.T) {
	var ctype, query string
	var hasFile bool
	var length int64
	te := newTool(t, registerRelease, map[string]http.HandlerFunc{
		"POST /repos/o/r/releases/9/assets": func(w http.ResponseWriter, r *http.Request) {
			ctype, query, length = r.Header.Get("Content-Type"), r.URL.RawQuery, r.ContentLength
			mt, params, _ := mime.ParseMediaType(ctype)
			if mt == "multipart/form-data" {
				mr := multipart.NewReader(r.Body, params["boundary"])
				for {
					part, err := mr.NextPart()
					if err != nil {
						break
					}
					if part.FormName() == "attachment" {
						hasFile = true
					}
				}
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":7,"name":"tool.tgz","type":"external"}`))
		},
	})
	got := te.ok(map[string]any{"action": "asset_upload", "id": 9, "url": "https://cdn.example.com/dl/tool.tgz"})
	contains(t, got, "ok: linked external asset 7 tool.tgz")
	if !strings.HasPrefix(ctype, "multipart/form-data; boundary=") || hasFile || length <= 0 {
		t.Errorf("external upload: content-type %q, attachment part %v, length %d", ctype, hasFile, length)
	}
	contains(t, query, "name=tool.tgz", "external_url=https%3A%2F%2Fcdn.example.com%2Fdl%2Ftool.tgz")
	contains(t, te.fail(map[string]any{"action": "asset_upload", "id": 9, "url": "ftp://x/y"}), "http(s)")
}

func readAll(r *http.Request) []byte {
	var b []byte
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		b = append(b, buf[:n]...)
		if err != nil {
			return b
		}
	}
}

func TestAssetDownload(t *testing.T) {
	// the storage host: a presigned redirect target on ANOTHER host
	var mu sync.Mutex
	var storageAuth []string
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		storageAuth = append(storageAuth, r.Header.Get("Authorization")+"|"+r.Header.Get("Sudo"))
		mu.Unlock()
		_, _ = w.Write([]byte("PAYLOAD"))
	}))
	defer storage.Close()

	var instAuth string
	te := newTool(t, registerRelease, map[string]http.HandlerFunc{
		"GET /repos/o/r/releases/9/assets": js(`[
{"id":4,"name":"file.zip","size":7,"type":"attachment","browser_download_url":"https://evil.example.net/o/r/releases/download/v1.0/file.zip"},
{"id":5,"name":"link","type":"external","browser_download_url":"https://elsewhere.example.org/x.tgz"}]`),
		"GET /o/r/releases/download/v1.0/file.zip": func(w http.ResponseWriter, r *http.Request) {
			instAuth = r.Header.Get("Authorization")
			http.Redirect(w, r, storage.URL+"/presigned?sig=abc", http.StatusFound)
		},
	})
	got := te.ok(map[string]any{"action": "asset_download", "id": 9, "asset_id": 4, "save_to": "got/file.zip"})
	contains(t, got, "ok: saved", "(7B)")
	data, err := os.ReadFile(filepath.Join(te.env.Cfg.SaveDir, "got", "file.zip"))
	if err != nil || string(data) != "PAYLOAD" {
		t.Fatalf("saved file: %q %v", data, err)
	}
	// the instance request carried the token; the storage hop did not; the
	// foreign host named in browser_download_url was never contacted (only
	// its path was used, rebuilt on FORGEJO_URL)
	if instAuth != "token tok-secret-123456" {
		t.Errorf("instance Authorization = %q", instAuth)
	}
	if len(storageAuth) != 1 || storageAuth[0] != "|" {
		t.Errorf("storage hop credentials: %v", storageAuth)
	}
	// never overwrites
	contains(t, te.fail(map[string]any{"action": "asset_download", "id": 9, "asset_id": 4, "save_to": "got/file.zip"}), "exists")
	// external assets are not downloaded
	got = te.ok(map[string]any{"action": "asset_download", "id": 9, "asset_id": 5})
	contains(t, got, "external asset: https://elsewhere.example.org/x.tgz", "not downloaded")
	if n := len(te.fake.find("GET", "/x.tgz")); n != 0 {
		t.Errorf("external URL path requested on the instance")
	}
	contains(t, te.fail(map[string]any{"action": "asset_download", "id": 9, "asset_id": 99}), "asset 99")
}

func TestAssetDownloadDisabledInHTTPMode(t *testing.T) {
	te := newTool(t, registerRelease, map[string]http.HandlerFunc{
		"GET /repos/o/r/releases/9/assets": js(`[{"id":4,"name":"f","type":"attachment","browser_download_url":"https://x/o/r/releases/download/v1/f"}]`),
	}, withConfig(func(c *config.Config) { c.Transport = "http" }))
	contains(t, te.fail(map[string]any{"action": "asset_download", "id": 9, "asset_id": 4}), "disabled in HTTP mode")
}
