package integration

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// contentRepo creates an auto-initialised repository owned by the token user
// through the raw API (the repo tool belongs to another work package) and
// deletes it with t.Cleanup. It returns "owner/name".
func contentRepo(t *testing.T) string {
	t.Helper()
	skipUnlessIntegration(t)
	st, body := rawAPI(t, http.MethodGet, "/user", nil)
	if st != http.StatusOK {
		t.Fatalf("GET /user -> %d", st)
	}
	var me struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &me); err != nil || me.Login == "" {
		t.Fatalf("cannot read the token user: %v", err)
	}
	name := uniqueName("it")
	st, body = rawAPI(t, http.MethodPost, "/user/repos", map[string]any{
		"name": name, "auto_init": true, "default_branch": "main", "readme": "Default",
	})
	if st != http.StatusCreated {
		t.Fatalf("create repo -> %d: %s", st, body)
	}
	full := me.Login + "/" + name
	t.Cleanup(func() { rawAPI(t, http.MethodDelete, "/repos/"+full, nil) })
	return full
}

var reSHA8 = regexp.MustCompile(`@[0-9a-f]{8}\b`)

func TestContentFileRoundTrip(t *testing.T) {
	skipUnlessIntegration(t)
	repo := contentRepo(t)

	// write: create, then update (the blob sha is looked up by the tool)
	out := mustOK(t, "file", map[string]any{"action": "write", "repo": repo, "path": "docs/a.txt", "message": "add a", "content": "alpha\nbeta\ngamma\n"})
	if !strings.HasPrefix(out, "ok: created docs/a.txt on main @") || !reSHA8.MatchString(out) {
		t.Fatalf("write create: %s", out)
	}
	out = mustOK(t, "file", map[string]any{"action": "write", "repo": repo, "path": "docs/a.txt", "message": "edit a", "content": "alpha\nBETA\ngamma\n"})
	if !strings.HasPrefix(out, "ok: updated docs/a.txt on main @") {
		t.Fatalf("write update: %s", out)
	}

	// read: whole file, range with numbers, grep; a second read revalidates the ETag
	out = mustOK(t, "file", map[string]any{"action": "read", "repo": repo, "path": "docs/a.txt"})
	if !strings.Contains(out, "lines 1-3/3") || !strings.Contains(out, "BETA") {
		t.Fatalf("read: %s", out)
	}
	if again := mustOK(t, "file", map[string]any{"action": "read", "repo": repo, "path": "docs/a.txt"}); again != out {
		t.Fatalf("second read differs:\n%s\n---\n%s", out, again)
	}
	out = mustOK(t, "file", map[string]any{"action": "read", "repo": repo, "path": "docs/a.txt", "start_line": 2, "end_line": 2, "numbers": true})
	if !strings.Contains(out, "lines 2-2/3") || !strings.Contains(out, "2: BETA") {
		t.Fatalf("read range: %s", out)
	}
	out = mustOK(t, "file", map[string]any{"action": "read", "repo": repo, "path": "docs/a.txt", "grep": "^g"})
	if !strings.Contains(out, "matches 1 of 3 lines") || !strings.Contains(out, "3: gamma") {
		t.Fatalf("read grep: %s", out)
	}

	// ls and tree
	out = mustOK(t, "file", map[string]any{"action": "ls", "repo": repo})
	golden(t, "content_ls", out)
	if !strings.Contains(out, "d docs/") || !strings.Contains(out, "f README.md") {
		t.Fatalf("ls: %s", out)
	}
	if e := mustErr(t, "file", map[string]any{"action": "ls", "repo": repo, "path": "docs/a.txt"}); !strings.Contains(e, "use action=read") {
		t.Fatalf("ls of a file: %s", e)
	}
	if dir := mustOK(t, "file", map[string]any{"action": "read", "repo": repo, "path": "docs"}); !strings.Contains(dir, "f a.txt") {
		t.Fatalf("read of a directory must list it: %s", dir)
	}
	out = mustOK(t, "file", map[string]any{"action": "tree", "repo": repo})
	golden(t, "content_tree", out)
	out = mustOK(t, "file", map[string]any{"action": "tree", "repo": repo, "glob": "*.txt"})
	if !strings.Contains(out, "# paths 1/1") || !strings.Contains(out, "docs/a.txt") {
		t.Fatalf("tree glob: %s", out)
	}

	// commit: create + update + rename + delete in ONE commit
	mustOK(t, "file", map[string]any{"action": "write", "repo": repo, "path": "old.txt", "message": "seed", "content": "to be moved"})
	mustOK(t, "file", map[string]any{"action": "write", "repo": repo, "path": "gone.txt", "message": "seed", "content": "to be deleted"})
	out = mustOK(t, "file", map[string]any{
		"action": "commit", "repo": repo, "message": "bulk change",
		"ops": []map[string]any{
			{"op": "create", "path": "src/new.go", "content": "package src\n"},
			{"op": "update", "path": "docs/a.txt", "content": "alpha\nbeta\ngamma\ndelta\n"},
			{"op": "rename", "path": "moved.txt", "from_path": "old.txt"},
			{"op": "delete", "path": "gone.txt"},
		},
	})
	if !strings.HasPrefix(out, "ok: committed 4 files on main @") {
		t.Fatalf("commit: %s", out)
	}
	if got := mustOK(t, "file", map[string]any{"action": "read", "repo": repo, "path": "moved.txt"}); !strings.Contains(got, "to be moved") {
		t.Fatalf("a rename must keep the content: %s", got)
	}
	if e := mustErr(t, "file", map[string]any{"action": "read", "repo": repo, "path": "old.txt"}); !strings.Contains(e, "404") {
		t.Fatalf("renamed source still readable: %s", e)
	}
	if e := mustErr(t, "file", map[string]any{"action": "read", "repo": repo, "path": "gone.txt"}); !strings.Contains(e, "404") {
		t.Fatalf("deleted file still readable: %s", e)
	}

	// patch: one unified diff, always against an explicit branch
	diff := "diff --git a/docs/a.txt b/docs/a.txt\n--- a/docs/a.txt\n+++ b/docs/a.txt\n@@ -1,4 +1,4 @@\n alpha\n beta\n-gamma\n+GAMMA\n delta\n"
	out = mustOK(t, "file", map[string]any{"action": "patch", "repo": repo, "diff": diff, "message": "patch gamma"})
	if !strings.HasPrefix(out, "ok: applied patch @") {
		t.Fatalf("patch: %s", out)
	}
	if got := mustOK(t, "file", map[string]any{"action": "read", "repo": repo, "path": "docs/a.txt"}); !strings.Contains(got, "GAMMA") {
		t.Fatalf("patch not applied: %s", got)
	}
	if e := mustErr(t, "file", map[string]any{"action": "patch", "repo": repo, "diff": diff, "message": "again"}); !strings.Contains(e, "error:") {
		t.Fatalf("a patch that no longer applies must fail: %s", e)
	}

	// write on a new branch, delete with a stale sha
	out = mustOK(t, "file", map[string]any{"action": "write", "repo": repo, "path": "feature.txt", "message": "feat", "content": "f", "new_branch": "feature"})
	if !strings.Contains(out, "on feature @") {
		t.Fatalf("write new_branch: %s", out)
	}
	e := mustErr(t, "file", map[string]any{"action": "delete", "repo": repo, "path": "docs/a.txt", "message": "rm", "sha": "0000000000000000000000000000000000000000"})
	t.Logf("file delete with a wrong sha: %s", strings.ReplaceAll(e, "\n", " | "))
	if !strings.Contains(e, "re-read and retry") {
		t.Fatalf("stale sha delete must carry the retry hint: %s", e)
	}
	out = mustOK(t, "file", map[string]any{"action": "delete", "repo": repo, "path": "src/new.go", "message": "rm new.go"})
	if !strings.HasPrefix(out, "ok: deleted src/new.go on main @") {
		t.Fatalf("delete: %s", out)
	}

	// archive into the save dir
	name := uniqueName("fmcp") + ".zip"
	t.Cleanup(func() { os.Remove(filepath.Join(os.TempDir(), name)) })
	out = mustOK(t, "file", map[string]any{"action": "archive", "repo": repo, "ref": "main", "save_to": name})
	if !strings.HasPrefix(out, "ok: saved ") {
		t.Fatalf("archive: %s", out)
	}
	fi, err := os.Stat(filepath.Join(os.TempDir(), name))
	if err != nil || fi.Size() < 100 {
		t.Fatalf("archive file: %v", err)
	}
	if e := mustErr(t, "file", map[string]any{"action": "archive", "repo": repo, "save_to": name}); !strings.Contains(e, "exists") {
		t.Fatalf("archive must never overwrite: %s", e)
	}
}

func TestContentFileOnEmptyRepo(t *testing.T) {
	skipUnlessIntegration(t)
	st, body := rawAPI(t, http.MethodGet, "/user", nil)
	if st != http.StatusOK {
		t.Fatalf("GET /user -> %d", st)
	}
	var me struct {
		Login string `json:"login"`
	}
	_ = json.Unmarshal(body, &me)
	name := uniqueName("it")
	if st, b := rawAPI(t, http.MethodPost, "/user/repos", map[string]any{"name": name, "auto_init": false}); st != http.StatusCreated {
		t.Fatalf("create empty repo -> %d: %s", st, b)
	}
	repo := me.Login + "/" + name
	t.Cleanup(func() { rawAPI(t, http.MethodDelete, "/repos/"+repo, nil) })

	if e := mustErr(t, "commit", map[string]any{"action": "list", "repo": repo}); e != "error: repo is empty" {
		t.Fatalf("commit list on an empty repo: %s", e)
	}
	// the first commit goes through the contents API on an empty repo
	out := mustOK(t, "file", map[string]any{"action": "write", "repo": repo, "path": "README.md", "message": "first", "content": "# hi\n"})
	if !strings.HasPrefix(out, "ok: created README.md") {
		t.Fatalf("first write: %s", out)
	}
	if got := mustOK(t, "commit", map[string]any{"action": "list", "repo": repo}); !strings.Contains(got, "| first") {
		t.Fatalf("commit list: %s", got)
	}
}

func TestContentCommit(t *testing.T) {
	skipUnlessIntegration(t)
	repo := contentRepo(t)
	mustOK(t, "file", map[string]any{"action": "write", "repo": repo, "path": "a.txt", "message": "base file\n\nwith a body", "content": "one\n"})
	mustOK(t, "file", map[string]any{"action": "write", "repo": repo, "path": "a.txt", "message": "second change", "content": "one\ntwo\n", "new_branch": "feat"})
	mustOK(t, "file", map[string]any{"action": "write", "repo": repo, "path": "b.txt", "message": "third change", "content": "b\n", "branch": "feat"})

	out := mustOK(t, "commit", map[string]any{"action": "list", "repo": repo, "ref": "feat"})
	if !strings.Contains(out, "# commits 4/4") || !strings.Contains(out, "| third change") || !strings.Contains(out, "| Initial commit") {
		t.Fatalf("list feat: %s", out)
	}
	out = mustOK(t, "commit", map[string]any{"action": "list", "repo": repo, "ref": "feat", "not": "main"})
	if !strings.Contains(out, "# commits 2/2") {
		t.Fatalf("list feat not main: %s", out)
	}
	out = mustOK(t, "commit", map[string]any{"action": "list", "repo": repo, "path": "b.txt", "ref": "feat"})
	if !strings.Contains(out, "# commits 1/1") {
		t.Fatalf("list by path: %s", out)
	}

	// get: header + files, then stat and patch
	out = mustOK(t, "commit", map[string]any{"action": "get", "repo": repo, "sha": "feat"})
	if !strings.Contains(out, "files: 1") || !strings.Contains(out, "  A b.txt") || !strings.Contains(out, "third change") {
		t.Fatalf("get: %s", out)
	}
	if strings.Contains(out, "stats:") || strings.Contains(out, "patch:") {
		t.Fatalf("get without include must stay small: %s", out)
	}
	out = mustOK(t, "commit", map[string]any{"action": "get", "repo": repo, "sha": "feat", "include": []string{"stat", "patch"}})
	if !strings.Contains(out, "stats: +1 -0 files:1") || !strings.Contains(out, "=== A b.txt (+1 -0) ===") || !strings.Contains(out, "+b") {
		t.Fatalf("get stat+patch: %s", out)
	}
	out = mustOK(t, "commit", map[string]any{"action": "get", "repo": repo, "sha": "feat", "include": []string{"patch"}, "paths": []string{"zzz/*"}})
	if !strings.Contains(out, "matches paths") {
		t.Fatalf("get patch with a non-matching filter: %s", out)
	}

	// compare: files are aggregated per commit
	out = mustOK(t, "commit", map[string]any{"action": "compare", "repo": repo, "base": "main", "head": "feat"})
	if !strings.Contains(out, "total_commits: 2") || !strings.Contains(out, "  M a.txt") || !strings.Contains(out, "  A b.txt") || !strings.Contains(out, "not a net diff") {
		t.Fatalf("compare: %s", out)
	}

	// pulls_of: no PR contains this commit
	lst := mustOK(t, "commit", map[string]any{"action": "list", "repo": repo, "limit": 1})
	sm := regexp.MustCompile(`\n([0-9a-f]{8}) `).FindStringSubmatch(lst)
	if sm == nil {
		t.Fatalf("commit list: %s", lst)
	}
	sha := sm[1]
	out = mustOK(t, "commit", map[string]any{"action": "pulls_of", "repo": repo, "sha": sha})
	if out != "no merged PR has this merge commit" {
		t.Fatalf("pulls_of: %s", out)
	}
}

func TestContentRelease(t *testing.T) {
	skipUnlessIntegration(t)
	repo := contentRepo(t)

	// create: the tag does not exist yet, so the server creates it at the target
	out := mustOK(t, "release", map[string]any{"action": "create", "repo": repo, "tag": "v1.0", "title": "First", "body": "release notes", "target": "main"})
	if !strings.HasPrefix(out, "ok: created release ") || !strings.Contains(out, "v1.0") {
		t.Fatalf("create: %s", out)
	}
	e := mustErr(t, "release", map[string]any{"action": "create", "repo": repo, "tag": "v1.0"})
	t.Logf("duplicate release create: %s", strings.ReplaceAll(e, "\n", " | "))
	if !strings.Contains(e, "409") {
		t.Fatalf("a second release for the tag must conflict: %s", e)
	}

	out = mustOK(t, "release", map[string]any{"action": "list", "repo": repo})
	if !strings.Contains(out, "# releases 1/1") || !strings.Contains(out, "| v1.0 | First") {
		t.Fatalf("list: %s", out)
	}
	out = mustOK(t, "release", map[string]any{"action": "get", "repo": repo, "tag": "v1.0"})
	idm := regexp.MustCompile(`^release (\d+) v1\.0`).FindStringSubmatch(out)
	if idm == nil || !strings.Contains(out, "release notes") {
		t.Fatalf("get by tag: %s", out)
	}
	id, _ := strconv.Atoi(idm[1])
	if latest := mustOK(t, "release", map[string]any{"action": "get", "repo": repo, "tag": "latest"}); !strings.HasPrefix(latest, "release "+strconv.Itoa(id)+" v1.0") {
		t.Fatalf("get latest: %s", latest)
	}

	// edit: only the given fields change; an empty value is refused locally
	if e := mustErr(t, "release", map[string]any{"action": "edit", "repo": repo, "tag": "v1.0", "body": ""}); !strings.Contains(e, "cannot be emptied") {
		t.Fatalf("edit with empty body: %s", e)
	}
	out = mustOK(t, "release", map[string]any{"action": "edit", "repo": repo, "tag": "v1.0", "title": "First!", "prerelease": true})
	if !strings.Contains(out, "title,prerelease") {
		t.Fatalf("edit: %s", out)
	}
	if got := mustOK(t, "release", map[string]any{"action": "get", "repo": repo, "id": id}); !strings.Contains(got, "title: First!") || !strings.Contains(got, "flags: prerelease") {
		t.Fatalf("get after edit: %s", got)
	}

	// assets: upload a local file, list, rename, download, delete
	payload := []byte("release asset payload\n")
	fname := uniqueName("fmcp") + ".txt"
	t.Cleanup(func() { os.Remove(filepath.Join(os.TempDir(), fname)) })
	if err := os.WriteFile(filepath.Join(os.TempDir(), fname), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	out = mustOK(t, "release", map[string]any{"action": "asset_upload", "repo": repo, "tag": "v1.0", "file": fname, "name": "notes.txt"})
	am := regexp.MustCompile(`^ok: uploaded asset (\d+) notes\.txt`).FindStringSubmatch(out)
	if am == nil {
		t.Fatalf("asset_upload: %s", out)
	}
	assetID, _ := strconv.Atoi(am[1])
	emID := 0
	out = mustOK(t, "release", map[string]any{"action": "asset_list", "repo": repo, "id": id})
	if !strings.Contains(out, "asset "+strconv.Itoa(assetID)+" notes.txt 22B") {
		t.Fatalf("asset_list: %s", out)
	}
	out = mustOK(t, "release", map[string]any{"action": "asset_edit", "repo": repo, "id": id, "asset_id": assetID, "name": "renamed.txt"})
	if !strings.Contains(out, "renamed asset") {
		t.Fatalf("asset_edit: %s", out)
	}
	dl := uniqueName("fmcp") + "-dl.txt"
	t.Cleanup(func() { os.Remove(filepath.Join(os.TempDir(), dl)) })
	out = mustOK(t, "release", map[string]any{"action": "asset_download", "repo": repo, "id": id, "asset_id": assetID, "save_to": dl})
	if !strings.HasPrefix(out, "ok: saved ") {
		t.Fatalf("asset_download: %s", out)
	}
	got, err := os.ReadFile(filepath.Join(os.TempDir(), dl))
	if err != nil || string(got) != string(payload) {
		t.Fatalf("downloaded asset: %q %v", got, err)
	}

	// external link: multipart with an empty form (quirk A3); never downloaded
	out = mustOK(t, "release", map[string]any{"action": "asset_upload", "repo": repo, "id": id, "url": "https://example.com/files/tool.tgz"})
	em := regexp.MustCompile(`^ok: linked external asset (\d+) tool\.tgz`).FindStringSubmatch(out)
	if em != nil {
		emID, _ = strconv.Atoi(em[1])
	}
	if em == nil {
		t.Fatalf("external asset_upload: %s", out)
	}
	out = mustOK(t, "release", map[string]any{"action": "asset_download", "repo": repo, "id": id, "asset_id": emID})
	if !strings.HasPrefix(out, "external asset: ") {
		t.Fatalf("an external asset must not be downloaded: %s", out)
	}
	mustOK(t, "release", map[string]any{"action": "asset_delete", "repo": repo, "id": id, "asset_id": emID})
	mustOK(t, "release", map[string]any{"action": "asset_delete", "repo": repo, "id": id, "asset_id": assetID})
	if out := mustOK(t, "release", map[string]any{"action": "asset_list", "repo": repo, "id": id}); !strings.Contains(out, "# assets 0/0") {
		t.Fatalf("asset_list after delete: %s", out)
	}

	// delete the release; whether the git tag survives is item 5 of section 15
	out = mustOK(t, "release", map[string]any{"action": "delete", "repo": repo, "tag": "v1.0"})
	if !strings.Contains(out, "ok: deleted release v1.0") {
		t.Fatalf("delete: %s", out)
	}
	st, body := rawAPI(t, http.MethodGet, "/repos/"+repo+"/tags/v1.0", nil)
	t.Logf("[v] release delete: git tag v1.0 afterwards -> HTTP %d %.80s", st, body)
	if out := mustOK(t, "release", map[string]any{"action": "list", "repo": repo}); !strings.Contains(out, "# releases 0/0") {
		t.Fatalf("list after delete: %s", out)
	}
}

func TestContentReadOnly(t *testing.T) {
	skipUnlessIntegration(t)
	s := newSession(t, map[string]string{"FORGEJO_READ_ONLY": "1"})
	repo := contentRepo(t)
	out, isErr := s.call(t, "file", map[string]any{"action": "write", "repo": repo, "path": "x.txt", "message": "m", "content": "x"})
	if !isErr || !strings.Contains(out, "action write is disabled (read-only mode)") {
		t.Fatalf("write in read-only mode: %v %s", isErr, out)
	}
	if out, isErr := s.call(t, "file", map[string]any{"action": "ls", "repo": repo}); isErr || !strings.Contains(out, "README.md") {
		t.Fatalf("ls in read-only mode: %v %s", isErr, out)
	}
	if out, isErr := s.call(t, "release", map[string]any{"action": "asset_upload", "repo": repo, "id": 1, "file": "x.bin"}); !isErr || !strings.Contains(out, "disabled") {
		t.Fatalf("asset_upload in read-only mode: %v %s", isErr, out)
	}
}
