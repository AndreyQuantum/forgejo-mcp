package content

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

const (
	sha1Full = "8a1b2c3d4e5f60718293a4b5c6d7e8f901234567"
	sha2Full = "11223344556677889900aabbccddeeff00112233"
)

func commitJSON(sha, msg, name, date string, files string) string {
	return `{"sha":"` + sha + `","html_url":"https://forgejo.example.com/o/r/commit/` + sha + `",
"commit":{"message":"` + msg + `","author":{"name":"` + name + `","date":"` + date + `"}},
"author":{"login":"` + name + `"},"parents":[{"sha":"` + sha2Full + `"}],"files":` + files + `}`
}

func TestCommitList(t *testing.T) {
	list := `[` + commitJSON(sha1Full, `Fix login race\n\nlong body`, "alice", "2026-11-22T10:15:00Z", "null") + `,` +
		commitJSON(sha2Full, "Initial commit", "bob", "2026-11-20T08:01:00Z", "null") + `]`
	te := newTool(t, registerCommit, map[string]http.HandlerFunc{
		"GET /repos/o/r/commits": jsTotal("2", list),
	})
	got := te.ok(map[string]any{"action": "list", "ref": "refs/heads/dev", "path": "src/a.go", "not": "main", "limit": 100})
	testutil.Golden(t, "commit_list", got)
	q := te.fake.only(t, "GET", "/repos/o/r/commits").Query
	// the list is requested without per-commit diff work, and the page size
	// is clamped to the server's commit range size
	contains(t, q, "stat=false", "files=false", "verification=false", "sha=dev", "path=src%2Fa.go", "not=main", "limit=50")
	lacks(t, got, "+", "long body")

	got = te.ok(map[string]any{"action": "list", "verbose": true})
	contains(t, got, sha1Full)
}

func TestCommitListPathWithoutCommits(t *testing.T) {
	te := newTool(t, registerCommit, map[string]http.HandlerFunc{
		"GET /repos/o/r/commits": status(404, `{"message":"FileCommitsCount"}`),
	})
	got := te.ok(map[string]any{"action": "list", "path": "nope.txt"})
	contains(t, got, "# commits 0/0")
}

func TestCommitListEmptyRepo(t *testing.T) {
	te := newTool(t, registerCommit, map[string]http.HandlerFunc{
		"GET /repos/o/r/commits": status(409, `{"message":"Git Repository is empty."}`),
	})
	got := te.fail(map[string]any{"action": "list"})
	if got != "error: repo is empty" {
		t.Errorf("got %q", got)
	}
}

func TestCommitGet(t *testing.T) {
	c := `{"sha":"` + sha1Full + `","commit":{"message":"Fix login race\n\nThe lock was taken twice.","author":{"name":"Alice A","date":"2026-11-22T10:15:00Z"}},
"author":{"login":"alice"},"parents":[{"sha":"` + sha2Full + `"}],
"files":[{"filename":"src/a.go","status":"modified"},{"filename":"docs/new.md","status":"added"},{"filename":"old.txt","status":"removed"}],
"stats":{"additions":12,"deletions":3,"total":15}}`
	te := newTool(t, registerCommit, map[string]http.HandlerFunc{
		"GET /repos/o/r/git/commits/" + sha1Full: js(c),
	})
	got := te.ok(map[string]any{"action": "get", "sha": sha1Full})
	testutil.Golden(t, "commit_get", got)
	q := te.fake.only(t, "GET", "/repos/o/r/git/commits/"+sha1Full).Query
	contains(t, q, "files=true", "stat=false", "verification=false")
	lacks(t, got, "+12")

	got = te.ok(map[string]any{"action": "get", "sha": sha1Full, "include": []string{"stat"}})
	contains(t, got, "stats: +12 -3 files:3")
	if !strings.Contains(te.fake.find("GET", "/repos/o/r/git/commits/"+sha1Full)[1].Query, "stat=true") {
		t.Errorf("include=stat must request stat=true")
	}
	contains(t, te.fail(map[string]any{"action": "get", "sha": sha1Full, "include": []string{"bogus"}}), "valid: stat,patch")
	contains(t, te.fail(map[string]any{"action": "get"}), "pass sha")
}

const patchText = "diff --git a/src/a.go b/src/a.go\n--- a/src/a.go\n+++ b/src/a.go\n@@ -1,2 +1,2 @@\n-old\n+new\n keep\n" +
	"diff --git a/docs/new.md b/docs/new.md\nnew file mode 100644\n--- /dev/null\n+++ b/docs/new.md\n@@ -0,0 +1 @@\n+# hi\n"

func TestCommitGetPatch(t *testing.T) {
	c := commitJSON(sha1Full, "m", "alice", "2026-11-22T10:15:00Z", `[{"filename":"src/a.go","status":"modified"}]`)
	te := newTool(t, registerCommit, map[string]http.HandlerFunc{
		"GET /repos/o/r/git/commits/" + sha1Full:           js(c),
		"GET /repos/o/r/git/commits/" + sha1Full + ".diff": text(patchText),
	})
	got := te.ok(map[string]any{"action": "get", "sha": sha1Full, "include": []string{"patch"}})
	testutil.Golden(t, "commit_get_patch", got)

	got = te.ok(map[string]any{"action": "get", "sha": sha1Full, "include": []string{"patch"}, "paths": []string{"docs/*"}})
	contains(t, got, "docs/new.md", "+# hi")
	lacks(t, got, "src/a.go (+")

	got = te.ok(map[string]any{"action": "get", "sha": sha1Full, "include": []string{"patch"}, "paths": []string{"nothing/**"}})
	contains(t, got, "no file of the 2 changed files matches paths")
}

func TestCommitGetPatchCap(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 6; i++ {
		fmt.Fprintf(&sb, "diff --git a/f%d.txt b/f%d.txt\n--- a/f%d.txt\n+++ b/f%d.txt\n@@ -1 +1 @@\n", i, i, i, i)
		for j := 0; j < 40; j++ {
			fmt.Fprintf(&sb, "+%s\n", strings.Repeat("x", 60))
		}
	}
	te := newTool(t, registerCommit, map[string]http.HandlerFunc{
		"GET /repos/o/r/git/commits/abc":      js(commitJSON("abc", "m", "a", "2026-11-22T10:15:00Z", "null")),
		"GET /repos/o/r/git/commits/abc.diff": text(sb.String()),
	})
	got := te.ok(map[string]any{"action": "get", "sha": "abc", "include": []string{"patch"}, "max_chars": 3000})
	contains(t, got, "[truncated:", "files shown", "omitted:")
}

func TestCommitCompare(t *testing.T) {
	cmp := `{"total_commits":3,"commits":[` +
		commitJSON(sha1Full, "Third", "alice", "2026-11-22T10:15:00Z", "null") + `,` +
		commitJSON(sha2Full, "Second", "bob", "2026-11-21T09:00:00Z", "null") + `,` +
		commitJSON("0000111122223333444455556666777788889999", "First", "bob", "2026-11-20T08:00:00Z", "null") + `],
"files":[{"filename":"a.go","status":"modified"},{"filename":"tmp.txt","status":"removed"},{"filename":"gone.txt","status":"removed"},
{"filename":"b.go","status":"modified"},
{"filename":"a.go","status":"added"},{"filename":"b.go","status":"modified"},{"filename":"tmp.txt","status":"added"},{"filename":"gone.txt","status":"modified"}]}`
	te := newTool(t, registerCommit, map[string]http.HandlerFunc{
		"GET /repos/o/r/compare/main...feature/x": js(cmp),
		"GET /repos/o/r/compare/main...fork:dev":  js(`{"total_commits":0,"commits":[],"files":[]}`),
	})
	got := te.ok(map[string]any{"action": "compare", "base": "main", "head": "feature/x", "limit": 2})
	testutil.Golden(t, "commit_compare", got)
	contains(t, te.fake.only(t, "GET", "/repos/o/r/compare/main...feature/x").Query, "files=true", "verification=false")

	got = te.ok(map[string]any{"action": "compare", "base": "main", "head": "fork:dev"})
	contains(t, got, "total_commits: 0", "# commits 0/0")
}

func TestCommitPullsOf(t *testing.T) {
	te := newTool(t, registerCommit, map[string]http.HandlerFunc{
		"GET /repos/o/r/commits/" + sha1Full + "/pull": js(`{"number":42,"title":"Fix login race","state":"closed","merged":true,
"user":{"login":"alice"},"base":{"ref":"main"},"head":{"label":"fix/login","ref":"fix/login"},"updated_at":"2026-11-22T10:15:00Z"}`),
		"GET /repos/o/r/commits/" + sha2Full + "/pull": status(404, `{"message":"PullRequestNotExist"}`),
		"GET /repos/o/r/git/commits/nomerge":           js(commitJSON(sha2Full, "m", "bob", "2026-11-20T08:01:00Z", "null")),
		"GET /repos/o/r/git/commits/8a1b2c3d":          js(commitJSON(sha1Full, "m", "bob", "2026-11-20T08:01:00Z", "null")),
	})
	// a short sha is resolved to the full one first
	short := te.ok(map[string]any{"action": "pulls_of", "sha": "8a1b2c3d"})
	contains(t, short, "#42 merged")

	got := te.ok(map[string]any{"action": "pulls_of", "sha": sha1Full})
	if want := "#42 merged | Fix login race | by alice | fix/login->main | upd 2026-11-22 10:15"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	got = te.ok(map[string]any{"action": "pulls_of", "sha": "nomerge"})
	if got != "no merged PR has this merge commit" {
		t.Errorf("got %q", got)
	}
}

func TestMergeStatuses(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{"added", "modified"}, "A"},
		{[]string{"modified", "modified"}, "M"},
		{[]string{"added", "removed"}, ""},
		{[]string{"modified", "removed"}, "D"},
		{[]string{"modified", "renamed"}, "R"},
		{[]string{"added", "modified", "removed"}, ""},
	} {
		if got := mergeStatuses(c.in); got != c.want {
			t.Errorf("mergeStatuses(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGlobMatcher(t *testing.T) {
	for _, c := range []struct {
		glob, path string
		want       bool
	}{
		{"*.go", "src/a.go", true},
		{"*.go", "a.go", true},
		{"*.go", "a.txt", false},
		{"src/*.go", "src/a.go", true},
		{"src/*.go", "src/pkg/a.go", false},
		{"src/**", "src/pkg/a.go", true},
		{"**/*.md", "docs/a/b.md", true},
		{"**/*.md", "b.md", true},
		{"a?.txt", "ab.txt", true},
		{"src/**/x.go", "src/abcx.go", false},
		{"src/**/x.go", "src/a/b/x.go", true},
		{"src/**/x.go", "src/x.go", true},
		{"**/foo", "barfoo", false},
		{"**/foo", "a/foo", true},
	} {
		m, err := globMatcher(c.glob)
		if err != nil {
			t.Fatal(err)
		}
		if got := m(c.path); got != c.want {
			t.Errorf("glob %q on %q = %v, want %v", c.glob, c.path, got, c.want)
		}
	}
}
