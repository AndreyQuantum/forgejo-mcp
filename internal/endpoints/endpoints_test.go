package endpoints

import (
	"regexp"
	"strings"
	"testing"
)

func TestIndexComplete(t *testing.T) {
	ix, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(ix.Ops) != 512 || ix.N != 512 {
		t.Fatalf("ops = %d, n = %d, want 512", len(ix.Ops), ix.N)
	}
	seen := map[string]bool{}
	for _, op := range ix.Ops {
		if op.M == "" || op.P == "" || op.ID == "" || op.T == "" {
			t.Errorf("incomplete op %+v", op)
		}
		if seen[op.Key()] {
			t.Errorf("duplicate %s", op.Key())
		}
		seen[op.Key()] = true
	}
}

func TestIndexHeader(t *testing.T) {
	ix, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(ix.V) {
		t.Errorf("v = %q is not semver", ix.V)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(ix.SwaggerSHA256) {
		t.Errorf("swagger_sha256 = %q", ix.SwaggerSHA256)
	}
	if v, n, _ := Header(); v != ix.V || n != 512 {
		t.Errorf("Header() = %s %d", v, n)
	}
}

func TestLookup(t *testing.T) {
	if op := Lookup("get", "/repos/{owner}/{repo}/pulls"); op == nil || op.ID != "repoListPullRequests" {
		t.Errorf("lookup = %+v", op)
	}
	if Lookup("GET", "/nope") != nil {
		t.Error("unknown template found")
	}
}

func TestMatch(t *testing.T) {
	cases := []struct{ method, path, want string }{
		{"GET", "/repos/o/r/pulls", "GET /repos/{owner}/{repo}/pulls"},
		{"GET", "/repos/o/r/pulls/42", "GET /repos/{owner}/{repo}/pulls/{index}"},
		{"GET", "/repos/o/r/pulls/42.diff", "GET /repos/{owner}/{repo}/pulls/{index}.{diffType}"},
		{"GET", "/repos/o/r/pulls/main/feat", "GET /repos/{owner}/{repo}/pulls/{base}/{head}"},
		{"GET", "/repos/o/r/pulls/main/feat/x", "GET /repos/{owner}/{repo}/pulls/{base}/{head}"},
		{"GET", "/repos/o/r/contents/a/b/c.go", "GET /repos/{owner}/{repo}/contents/{filepath}"},
		{"GET", "/repos/o/r/contents", "GET /repos/{owner}/{repo}/contents"},
		{"DELETE", "/repos/o/r/branches/release/1.x", "DELETE /repos/{owner}/{repo}/branches/{branch}"},
		{"GET", "/repos/o/r/branches", "GET /repos/{owner}/{repo}/branches"},
		{"GET", "/repos/search", "GET /repos/search"},
		{"GET", "/repos/o/r", "GET /repos/{owner}/{repo}"},
		{"GET", "/repos/o/r/compare/main...feat", "GET /repos/{owner}/{repo}/compare/{basehead}"},
		{"GET", "/admin/runners/registration-token", "GET /admin/runners/registration-token"},
		{"POST", "/repos/o/r/pulls", "POST /repos/{owner}/{repo}/pulls"},
		{"PATCH", "/repos/o/r/pulls", ""},
		{"GET", "/nothing/here", ""},
		{"GET", "/repos//r/pulls", ""},
		{"GET", "/repos/o/r/pulls/", "GET /repos/{owner}/{repo}/pulls"},
	}
	for _, c := range cases {
		got := ""
		if op := Match(c.method, c.path); op != nil {
			got = op.Key()
		}
		if got != c.want {
			t.Errorf("Match(%s %s) = %q, want %q", c.method, c.path, got, c.want)
		}
	}
}

func TestMatchEveryTemplate(t *testing.T) {
	// every template, instantiated with simple values, matches an operation
	// that is at least as specific.
	re := regexp.MustCompile(`\{[^}]+\}`)
	for _, op := range All() {
		p := re.ReplaceAllString(op.P, "x")
		got := Match(op.M, p)
		if got == nil {
			t.Errorf("%s: no match for %s", op.Key(), p)
			continue
		}
		if got.Key() != op.Key() && strings.Count(got.P, "{") > strings.Count(op.P, "{") {
			t.Errorf("%s: matched less specific %s", op.Key(), got.Key())
		}
	}
}

func TestSearch(t *testing.T) {
	ops, total := Search("actions runners", "", "", 15, 1)
	if total == 0 || len(ops) == 0 {
		t.Fatal("no results for 'actions runners'")
	}
	for _, op := range ops {
		if !strings.Contains(strings.ToLower(op.P+op.ID+op.S), "runner") {
			t.Errorf("unexpected hit %s", op.Key())
		}
	}
	// operationId hits sort first.
	ops, _ = Search("ListPullRequests", "", "get", 5, 1)
	if len(ops) == 0 || ops[0].ID != "repoListPullRequests" {
		t.Errorf("top hit = %+v", ops)
	}
	ops, total = Search("", "settings", "", 50, 1)
	if total != 4 || len(ops) != 4 {
		t.Errorf("tag filter: total %d, n %d", total, len(ops))
	}
	_, total = Search("zzzznotthere", "", "", 15, 1)
	if total != 0 {
		t.Errorf("total = %d", total)
	}
	a, tot := Search("repo", "", "GET", 10, 1)
	b, _ := Search("repo", "", "GET", 10, 2)
	if tot < 20 || len(a) != 10 || len(b) != 10 || a[0].Key() == b[0].Key() {
		t.Errorf("paging broken: %d %d %d", tot, len(a), len(b))
	}
	if ops, _ := Search("repo", "", "GET", 10, 9999); ops != nil {
		t.Error("page beyond end should be empty")
	}
	// parameter names count: "visible" is a query param of runner lists.
	if _, n := Search("visible", "", "", 15, 1); n == 0 {
		t.Error("param name search failed")
	}
}

func TestWildFlag(t *testing.T) {
	for _, k := range []string{
		"GET /repos/{owner}/{repo}/contents/{filepath}",
		"DELETE /repos/{owner}/{repo}/branches/{branch}",
		"GET /repos/{owner}/{repo}/tags/{tag}",
		"GET /repos/{owner}/{repo}/compare/{basehead}",
	} {
		sp := strings.SplitN(k, " ", 2)
		if op := Lookup(sp[0], sp[1]); op == nil || op.W != 1 {
			t.Errorf("%s should be wild", k)
		}
	}
	for _, k := range []string{
		"GET /repos/{owner}/{repo}/releases/tags/{tag}",
		"GET /repos/{owner}/{repo}/branch_protections/{name}",
		"GET /repos/{owner}/{repo}/pulls/{index}",
	} {
		sp := strings.SplitN(k, " ", 2)
		if op := Lookup(sp[0], sp[1]); op == nil || op.W == 1 {
			t.Errorf("%s should not be wild", k)
		}
	}
}

func TestScopeAssigned(t *testing.T) {
	cnt := map[string]int{}
	for _, op := range All() {
		cnt[op.SC]++
	}
	for _, sc := range []string{"repository", "issue", "user", "organization", "admin", "package", "notification"} {
		if cnt[sc] == 0 {
			t.Errorf("no operation with scope %q: %v", sc, cnt)
		}
	}
	if op := Lookup("GET", "/version"); op == nil || op.SC != "" {
		t.Errorf("/version scope = %+v", op)
	}
}

func TestCoverageReport(t *testing.T) {
	t.Logf("index: %d operations", len(All()))
}
