package content

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// numbered returns "line 1\nline 2\n..." with n lines.
func numbered(n int) string {
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	return sb.String()
}

func rawFile(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Etag", `"abc"`)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}
}

func TestFileRead(t *testing.T) {
	te := newTool(t, registerFile, map[string]http.HandlerFunc{
		"GET /repos/o/r/raw/src/a.go": rawFile(numbered(10)),
	})
	got := te.ok(map[string]any{"action": "read", "path": "src/a.go", "ref": "main"})
	testutil.Golden(t, "file_read", got)
	q := te.fake.only(t, "GET", "/repos/o/r/raw/src/a.go").Query
	if q != "ref=main" {
		t.Errorf("query %q", q)
	}

	got = te.ok(map[string]any{"action": "read", "path": "src/a.go", "start_line": 3, "end_line": 5, "numbers": true})
	testutil.Golden(t, "file_read_range", got)

	got = te.ok(map[string]any{"action": "read", "path": "src/a.go", "grep": "line (2|9)$"})
	testutil.Golden(t, "file_read_grep", got)

	got = te.ok(map[string]any{"action": "read", "path": "src/a.go", "grep": "nothing-here"})
	contains(t, got, "matches 0 of 10 lines")

	got = te.ok(map[string]any{"action": "read", "path": "src/a.go", "start_line": 99})
	contains(t, got, "start_line 99 is beyond the end")
}

func TestFileReadTruncation(t *testing.T) {
	te := newTool(t, registerFile, map[string]http.HandlerFunc{
		"GET /repos/o/r/raw/big.txt": rawFile(numbered(2000)),
	})
	got := te.ok(map[string]any{"action": "read", "path": "big.txt", "max_chars": 300})
	// header + 300 chars of body + the marker
	contains(t, got, "file big.txt", "[truncated: 300 of ", "pass start_line/end_line, grep or max_chars")
	if !strings.Contains(got, "lines 1-") || strings.Contains(got, "lines 1-2000/2000") {
		t.Errorf("shown range must reflect the cut:\n%s", got)
	}
}

func TestFileReadBinaryAndEmpty(t *testing.T) {
	te := newTool(t, registerFile, map[string]http.HandlerFunc{
		"GET /repos/o/r/raw/a.bin":   rawFile("PK\x00\x03binary"),
		"GET /repos/o/r/raw/e.txt":   rawFile(""),
		"GET /repos/o/r/raw/dir/x/y": rawFile("deep\n"),
	})
	got := te.ok(map[string]any{"action": "read", "path": "a.bin"})
	contains(t, got, "file a.bin", "binary")
	lacks(t, got, "PK")
	contains(t, te.ok(map[string]any{"action": "read", "path": "e.txt"}), "empty")
	// leading slash and a path with several segments
	contains(t, te.ok(map[string]any{"action": "read", "path": "/dir/x/y"}), "deep")
}

func TestFileReadETagCache(t *testing.T) {
	var hits, conditional atomic.Int32
	te := newTool(t, registerFile, map[string]http.HandlerFunc{
		"GET /repos/o/r/raw/a.txt": func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			if r.Header.Get("If-None-Match") == `"v1"` {
				conditional.Add(1)
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("Etag", `"v1"`)
			_, _ = w.Write([]byte("hello\n"))
		},
	})
	first := te.ok(map[string]any{"action": "read", "path": "a.txt"})
	second := te.ok(map[string]any{"action": "read", "path": "a.txt"})
	if first != second || !strings.Contains(second, "hello") {
		t.Errorf("a 304 must reuse the cached body:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if hits.Load() != 2 || conditional.Load() != 1 {
		t.Errorf("hits=%d conditional=%d, want 2 and 1", hits.Load(), conditional.Load())
	}

	// a second token never shares the cache entry: its request is unconditional
	other := *te.env
	other.Client = te.env.Client.WithToken("another-token-9876")
	out, isErr := testutil.CallTool(t, &other, te.spec, map[string]any{"action": "read", "path": "a.txt"})
	if isErr || !strings.Contains(out, "hello") {
		t.Fatalf("second token read: %v %s", isErr, out)
	}
	if conditional.Load() != 1 {
		t.Errorf("the second token sent If-None-Match from the first token's cache")
	}
}

func TestFileReadDirectoryFallsBackToLs(t *testing.T) {
	te := newTool(t, registerFile, map[string]http.HandlerFunc{
		"GET /repos/o/r/raw/src":      status(404, `{"message":"getBlobForEntry","errors":[]}`),
		"GET /repos/o/r/contents/src": js(`[{"name":"a.go","type":"file","size":10},{"name":"sub","type":"dir"}]`),
	})
	got := te.ok(map[string]any{"action": "read", "path": "src"})
	contains(t, got, "# entries 2", "d sub/", "f a.go 10B")
}

func TestFileReadMissing(t *testing.T) {
	te := newTool(t, registerFile, nil)
	got := te.fail(map[string]any{"action": "read", "path": "nope.txt"})
	contains(t, got, "404")
}

func TestFileLs(t *testing.T) {
	te := newTool(t, registerFile, map[string]http.HandlerFunc{
		"GET /repos/o/r/contents": js(`[
{"name":"main.go","path":"main.go","type":"file","size":1234},
{"name":"src","path":"src","type":"dir"},
{"name":"link","path":"link","type":"symlink","target":"src/a.go"},
{"name":"vendor","path":"vendor","type":"submodule"},
{"name":"docs","path":"docs","type":"dir"}]`),
		"GET /repos/o/r/contents/main.go": js(`{"name":"main.go","type":"file","size":1}`),
	})
	got := te.ok(map[string]any{"action": "ls", "ref": "main"})
	testutil.Golden(t, "file_ls", got)
	contains(t, te.fail(map[string]any{"action": "ls", "path": "main.go"}), "path is a file; use action=read")
}

const treeJSON = `{"tree":[
{"path":"README.md","type":"blob"},
{"path":"docs","type":"tree"},
{"path":"docs/guide.md","type":"blob"},
{"path":"docs/img","type":"tree"},
{"path":"docs/img/logo.png","type":"blob"},
{"path":"src","type":"tree"},
{"path":"src/a.go","type":"blob"},
{"path":"src/b_test.go","type":"blob"},
{"path":"src/pkg","type":"tree"},
{"path":"src/pkg/c.go","type":"blob"}],"truncated":false}`

func TestFileTree(t *testing.T) {
	te := newTool(t, registerFile, map[string]http.HandlerFunc{
		"GET /repos/o/r/git/trees/main": js(treeJSON),
	})
	got := te.ok(map[string]any{"action": "tree"})
	testutil.Golden(t, "file_tree", got)
	q := te.fake.find("GET", "/repos/o/r/git/trees/main")[0].Query
	contains(t, q, "recursive=true", "per_page=1000", "page=1")

	got = te.ok(map[string]any{"action": "tree", "path": "src", "depth": 1})
	testutil.Golden(t, "file_tree_prefix_depth", got)

	got = te.ok(map[string]any{"action": "tree", "glob": "*.go"})
	contains(t, got, "# paths 3/3", "src/a.go", "src/b_test.go", "src/pkg/c.go")
	lacks(t, got, "README")

	got = te.ok(map[string]any{"action": "tree", "glob": "docs/**"})
	contains(t, got, "docs/guide.md", "docs/img/logo.png")
	lacks(t, got, "src/")

	got = te.ok(map[string]any{"action": "tree", "limit": 4, "page": 2})
	contains(t, got, "page 2/3", "next: page=3")

	got = te.ok(map[string]any{"action": "tree", "recursive": false})
	contains(t, got, "README.md", "docs/", "src/")
	lacks(t, got, "docs/guide.md")
}

func TestFileTreeFollowsServerPages(t *testing.T) {
	var calls atomic.Int32
	te := newTool(t, registerFile, map[string]http.HandlerFunc{
		"GET /repos/o/r/git/trees/main": func(w http.ResponseWriter, r *http.Request) {
			n := calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("page") == "1" {
				_, _ = w.Write([]byte(`{"tree":[{"path":"a","type":"blob"}],"truncated":true}`))
				return
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"tree":[{"path":"b%d","type":"blob"}],"truncated":false}`, n)))
		},
	})
	got := te.ok(map[string]any{"action": "tree"})
	contains(t, got, "# paths 2/2", "a\n", "b2")
}

func TestFileTreeBadRef(t *testing.T) {
	te := newTool(t, registerFile, map[string]http.HandlerFunc{
		"GET /repos/o/r/git/trees/nope": status(400, `{"message":"sha not found"}`),
	})
	got := te.fail(map[string]any{"action": "tree", "ref": "nope"})
	contains(t, got, "400", "hint: check ref")
}

func TestFileWriteCreateUpdate(t *testing.T) {
	te := newTool(t, registerFile, map[string]http.HandlerFunc{
		// create: the probe says 404
		"GET /repos/o/r/contents/new.txt":  status(404, `{"message":"not found"}`),
		"POST /repos/o/r/contents/new.txt": status(201, `{"content":{"sha":"1b2c3d4e5f60"},"commit":{"sha":"8a1b2c3d4e5f6789"}}`),
		// update: the probe finds the blob
		"GET /repos/o/r/contents/old.txt": js(`{"name":"old.txt","type":"file","sha":"oldsha1","content":"` + b64("old") + `"}`),
		"PUT /repos/o/r/contents/old.txt": js(`{"content":{"sha":"newsha22"},"commit":{"sha":"9a9a9a9a9a9a"}}`),
		// explicit sha: no probe at all
		"PUT /repos/o/r/contents/direct.txt": js(`{"content":{"sha":"dd"},"commit":{"sha":"abcdef0123"}}`),
	})
	got := te.ok(map[string]any{"action": "write", "path": "new.txt", "message": "add", "content": "hi\n", "branch": "dev"})
	testutil.Golden(t, "file_write_create", got)
	body := jsonMap(t, te.fake.only(t, "POST", "/repos/o/r/contents/new.txt").Body)
	if body["content"] != b64("hi\n") || body["message"] != "add" || body["branch"] != "dev" {
		t.Errorf("create body: %v", body)
	}
	if _, has := body["sha"]; has {
		t.Errorf("create must not send sha: %v", body)
	}
	if q := te.fake.find("GET", "/repos/o/r/contents/new.txt")[0].Query; q != "ref=dev" {
		t.Errorf("probe query %q", q)
	}

	got = te.ok(map[string]any{"action": "write", "path": "old.txt", "message": "upd", "content": "new", "new_branch": "feat"})
	contains(t, got, "ok: updated old.txt on feat @9a9a9a9a (blob newsha22)")
	body = jsonMap(t, te.fake.only(t, "PUT", "/repos/o/r/contents/old.txt").Body)
	if body["sha"] != "oldsha1" || body["new_branch"] != "feat" {
		t.Errorf("update body: %v", body)
	}

	te.ok(map[string]any{"action": "write", "path": "direct.txt", "message": "x", "content_base64": b64("z"), "sha": "known"})
	if n := len(te.fake.find("GET", "/repos/o/r/contents/direct.txt")); n != 0 {
		t.Errorf("an explicit sha must skip the probe, got %d GETs", n)
	}
}

func TestFileWriteEmptyRepoAndEmptyFile(t *testing.T) {
	// an empty repository answers `[]` for every path: that means "create"
	te := newTool(t, registerFile, map[string]http.HandlerFunc{
		"GET /repos/o/r/contents/a.txt":  js(`[]`),
		"POST /repos/o/r/contents/a.txt": status(201, `{"content":{"sha":"e69de29b"},"commit":{"sha":"11223344556677"}}`),
	})
	empty := ""
	got := te.ok(map[string]any{"action": "write", "path": "a.txt", "message": "init", "content": empty})
	contains(t, got, "ok: created a.txt on main @11223344")
	body := jsonMap(t, te.fake.only(t, "POST", "/repos/o/r/contents/a.txt").Body)
	if body["content"] != "" {
		t.Errorf("empty content must be sent as an empty string: %v", body)
	}
}

func TestFileWriteValidation(t *testing.T) {
	te := newTool(t, registerFile, map[string]http.HandlerFunc{
		"GET /repos/o/r/contents/dir": js(`[{"name":"x","type":"file"}]`),
	})
	contains(t, te.fail(map[string]any{"action": "write", "path": "a", "message": "m"}), "pass content or content_base64")
	contains(t, te.fail(map[string]any{"action": "write", "path": "a", "message": "m", "content": "x", "content_base64": "eA=="}), "not both")
	contains(t, te.fail(map[string]any{"action": "write", "path": "a", "message": "m", "content_base64": "!!notbase64"}), "not valid base64")
	contains(t, te.fail(map[string]any{"action": "write", "path": "dir", "message": "m", "content": "x"}), "is a directory")
}

func TestFileWriteRename(t *testing.T) {
	te := newTool(t, registerFile, map[string]http.HandlerFunc{
		"GET /repos/o/r/contents/old.txt": js(`{"name":"old.txt","type":"file","sha":"blob-old","content":"` + b64("keep me") + `"}`),
		"PUT /repos/o/r/contents/new.txt": js(`{"content":{"sha":"b2"},"commit":{"sha":"deadbeef0000"}}`),
	})
	got := te.ok(map[string]any{"action": "write", "path": "new.txt", "from_path": "old.txt", "message": "mv"})
	contains(t, got, "ok: renamed old.txt -> new.txt on main @deadbeef")
	body := jsonMap(t, te.fake.only(t, "PUT", "/repos/o/r/contents/new.txt").Body)
	if body["from_path"] != "old.txt" || body["sha"] != "blob-old" || body["content"] != b64("keep me") {
		t.Errorf("rename must carry the existing content and the source blob sha: %v", body)
	}
	contains(t, te.fail(map[string]any{"action": "write", "path": "x", "from_path": "missing", "message": "mv"}), "from_path missing")
}

func TestFileWriteErrors(t *testing.T) {
	te := newTool(t, registerFile, map[string]http.HandlerFunc{
		"GET /repos/o/r/contents/a.txt":  status(404, `{"message":"not found"}`),
		"POST /repos/o/r/contents/a.txt": status(403, `{"message":"branch protected"}`),
		"PUT /repos/o/r/contents/b.txt":  status(409, `{"message":"sha does not match"}`),
	})
	got := te.fail(map[string]any{"action": "write", "path": "a.txt", "message": "m", "content": "x"})
	contains(t, got, "403", "hint: use new_branch, then pr create")
	got = te.fail(map[string]any{"action": "write", "path": "b.txt", "message": "m", "content": "x", "sha": "stale"})
	contains(t, got, "409", "hint: file changed since read; re-read and retry")
}

func TestFileDelete(t *testing.T) {
	te := newTool(t, registerFile, map[string]http.HandlerFunc{
		"GET /repos/o/r/contents/a.txt":    js(`{"name":"a.txt","type":"file","sha":"blob-a"}`),
		"DELETE /repos/o/r/contents/a.txt": js(`{"content":null,"commit":{"sha":"5566778899aa"}}`),
		"DELETE /repos/o/r/contents/b.txt": status(400, `{"message":"sha does not match"}`),
	})
	got := te.ok(map[string]any{"action": "delete", "path": "a.txt", "message": "rm", "new_branch": "cleanup"})
	contains(t, got, "ok: deleted a.txt on cleanup @55667788")
	body := jsonMap(t, te.fake.only(t, "DELETE", "/repos/o/r/contents/a.txt").Body)
	if body["sha"] != "blob-a" || body["message"] != "rm" || body["new_branch"] != "cleanup" {
		t.Errorf("delete body: %v", body)
	}
	// a stale sha answers 400 here (not 409)
	got = te.fail(map[string]any{"action": "delete", "path": "b.txt", "message": "rm", "sha": "stale"})
	contains(t, got, "400", "hint: file changed since read; re-read and retry")
	contains(t, te.fail(map[string]any{"action": "delete", "path": "ghost.txt", "message": "rm"}), "ghost.txt is not a file")
}

func TestFileCommit(t *testing.T) {
	te := newTool(t, registerFile, map[string]http.HandlerFunc{
		"GET /repos/o/r/contents/up.txt": js(`{"name":"up.txt","type":"file","sha":"sha-up"}`),
		"GET /repos/o/r/contents/rm.txt": js(`{"name":"rm.txt","type":"file","sha":"sha-rm"}`),
		"GET /repos/o/r/contents/mv.txt": js(`{"name":"mv.txt","type":"file","sha":"sha-mv","content":"` + b64("moved body") + `"}`),
		"POST /repos/o/r/contents":       status(201, `{"files":[],"commit":{"sha":"0123456789abcdef"}}`),
	})
	empty := ""
	got := te.ok(map[string]any{
		"action": "commit", "message": "bulk", "branch": "dev",
		"ops": []map[string]any{
			{"op": "create", "path": "new.txt", "content": "n"},
			{"op": "create", "path": "empty.txt", "content": empty},
			{"op": "update", "path": "up.txt", "content_base64": b64("u")},
			{"op": "delete", "path": "rm.txt"},
			{"op": "rename", "path": "moved.txt", "from_path": "mv.txt"},
		},
	})
	contains(t, got, "ok: committed 5 files on dev @01234567")
	body := jsonMap(t, te.fake.only(t, "POST", "/repos/o/r/contents").Body)
	if body["message"] != "bulk" || body["branch"] != "dev" {
		t.Errorf("body: %v", body)
	}
	files := body["files"].([]any)
	want := []map[string]any{
		{"operation": "create", "path": "new.txt", "content": b64("n")},
		{"operation": "create", "path": "empty.txt"},
		{"operation": "update", "path": "up.txt", "content": b64("u"), "sha": "sha-up"},
		{"operation": "delete", "path": "rm.txt", "sha": "sha-rm"},
		{"operation": "update", "path": "moved.txt", "from_path": "mv.txt", "sha": "sha-mv", "content": b64("moved body")},
	}
	if len(files) != len(want) {
		t.Fatalf("files: %v", files)
	}
	for i, w := range want {
		g := files[i].(map[string]any)
		for k, v := range w {
			if g[k] != v {
				t.Errorf("files[%d][%s] = %v, want %v", i, k, g[k], v)
			}
		}
	}
	for _, g := range te.fake.find("GET", "/repos/o/r/contents/up.txt") {
		if g.Query != "ref=dev" {
			t.Errorf("sha lookup query %q", g.Query)
		}
	}
}

func TestFileCommitValidation(t *testing.T) {
	te := newTool(t, registerFile, map[string]http.HandlerFunc{
		"GET /repos/o/r/contents/ghost.txt": status(404, `{"message":"not found"}`),
	})
	ops := func(o ...map[string]any) map[string]any {
		return map[string]any{"action": "commit", "message": "m", "ops": o}
	}
	contains(t, te.fail(ops(map[string]any{"op": "frobnicate", "path": "a"})), "valid: create,update,delete,rename")
	contains(t, te.fail(ops(map[string]any{"op": "create", "path": "a"})), "content or content_base64 is required")
	contains(t, te.fail(ops(map[string]any{"op": "rename", "path": "a"})), "from_path is required")
	contains(t, te.fail(ops(map[string]any{"op": "delete", "path": "ghost.txt"})), "ghost.txt is not a file")
	if n := len(te.fake.find("POST", "/repos/o/r/contents")); n != 0 {
		t.Errorf("an invalid batch must not commit")
	}
}

func TestFileCommitManyLookupsAreBounded(t *testing.T) {
	var cur, peak atomic.Int32
	routes := map[string]http.HandlerFunc{
		"POST /repos/o/r/contents": status(201, `{"commit":{"sha":"aaaaaaaaaaaa"}}`),
	}
	var ops []map[string]any
	for i := 0; i < 20; i++ {
		p := fmt.Sprintf("f%d.txt", i)
		routes["GET /repos/o/r/contents/"+p] = func(w http.ResponseWriter, r *http.Request) {
			n := cur.Add(1)
			for {
				pk := peak.Load()
				if n <= pk || peak.CompareAndSwap(pk, n) {
					break
				}
			}
			defer cur.Add(-1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"type":"file","sha":"s"}`))
		}
		ops = append(ops, map[string]any{"op": "delete", "path": p})
	}
	te := newTool(t, registerFile, routes)
	got := te.ok(map[string]any{"action": "commit", "message": "m", "ops": ops})
	contains(t, got, "ok: committed 20 files")
	if peak.Load() > 8 {
		t.Errorf("parallel lookups peaked at %d, limit is 8", peak.Load())
	}
}

func TestFilePatch(t *testing.T) {
	te := newTool(t, registerFile, map[string]http.HandlerFunc{
		"POST /repos/o/r/diffpatch": status(201, `{"commit":{"sha":"cafebabe0000"}}`),
	})
	diff := "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-x\n+y\n"
	got := te.ok(map[string]any{"action": "patch", "diff": diff, "message": "fix"})
	contains(t, got, "ok: applied patch @cafebabe on main")
	body := jsonMap(t, te.fake.only(t, "POST", "/repos/o/r/diffpatch").Body)
	// the branch is ALWAYS sent (the handler does not default it) and sha is a dummy
	if body["content"] != diff || body["branch"] != "main" || body["sha"] != "x" || body["message"] != "fix" {
		t.Errorf("diffpatch body: %v", body)
	}
}

func TestFilePatchDoesNotApply(t *testing.T) {
	te := newTool(t, registerFile, map[string]http.HandlerFunc{
		"POST /repos/o/r/diffpatch": status(422, `{"message":"patch does not apply"}`),
	})
	got := te.fail(map[string]any{"action": "patch", "diff": "x", "message": "m", "branch": "dev"})
	contains(t, got, "422", "hint: file changed since read; re-read the file and rebuild the diff")
}

func TestFileArchive(t *testing.T) {
	te := newTool(t, registerFile, map[string]http.HandlerFunc{
		"GET /repos/o/r/archive/main.zip":       text("ZIPDATA"),
		"GET /repos/o/r/archive/rel/1.0.tar.gz": text("TARDATA"),
	})
	got := te.ok(map[string]any{"action": "archive"})
	contains(t, got, "ok: saved", "r-main.zip", "(7B)")
	data, err := os.ReadFile(filepath.Join(te.env.Cfg.SaveDir, "r-main.zip"))
	if err != nil || string(data) != "ZIPDATA" {
		t.Fatalf("archive file: %q %v", data, err)
	}
	// a ref with a slash keeps the slash in the route (wildcard) and is flattened in the file name
	got = te.ok(map[string]any{"action": "archive", "ref": "rel/1.0", "format": "tar.gz"})
	contains(t, got, "r-rel-1.0.tar.gz")
	contains(t, te.fail(map[string]any{"action": "archive"}), "exists")
	contains(t, te.fail(map[string]any{"action": "archive", "format": "rar"}), "valid: zip,tar.gz,bundle")
	contains(t, te.fail(map[string]any{"action": "archive", "save_to": "../x.zip"}), "error:")
}
