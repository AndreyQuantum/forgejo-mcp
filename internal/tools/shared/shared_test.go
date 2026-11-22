package shared

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/gitremote"
)

func newClient(t *testing.T, h http.Handler, tok string) *forgejo.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return forgejo.New(forgejo.Config{BaseURL: srv.URL, Token: tok, HTTPClient: srv.Client()})
}

func TestParseOwnerRepo(t *testing.T) {
	for _, bad := range []string{"", "a", "a/b/c", "a/", "/b", "a b/c", "../x", "a/..", "a/b?x"} {
		if _, _, err := ParseOwnerRepo(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	o, n, err := ParseOwnerRepo("my-org/re.po_1")
	if err != nil || o != "my-org" || n != "re.po_1" {
		t.Fatalf("got %q %q %v", o, n, err)
	}
}

func TestNormalizeRef(t *testing.T) {
	for in, want := range map[string]string{
		" refs/heads/main ": "main", "refs/tags/v1": "v1", "main": "main", "feature/x": "feature/x",
	} {
		if got := NormalizeRef(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestFullRunRef(t *testing.T) {
	for in, want := range map[string]string{
		"main": "refs/heads/main", "pr:12": "refs/pull/12/head", "refs/tags/v1": "refs/tags/v1",
	} {
		got, err := FullRunRef(in)
		if err != nil || got != want {
			t.Errorf("%q -> %q %v, want %q", in, got, err, want)
		}
	}
	if _, err := FullRunRef("#12"); err == nil || !strings.Contains(err.Error(), "pr=12") {
		t.Errorf("#12: %v", err)
	}
}

func TestAssetPath(t *testing.T) {
	cases := []struct {
		url, sub, want string
		ok             bool
	}{
		{"https://forgejo.example.com/o/r/releases/download/v1/a%20b.zip", "", "/o/r/releases/download/v1/a%20b.zip", true},
		{"https://other.example.org/sub/o/r/attachments/uuid", "/sub", "/o/r/attachments/uuid", true},
		{"https://h/o/r/x?token=1", "", "/o/r/x", true},
		{"https://h/api/v1/admin/users", "", "", false},
		{"https://h/o/../x", "", "", false},
		{"https://h/", "", "", false},
		{"ftp://h/o/r", "", "", false},
		{"file:///etc/passwd", "", "", false},
	}
	for _, c := range cases {
		got, ok := AssetPath(c.url, c.sub)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: got %q %v", c.url, got, ok)
		}
	}
	if got := RootSubPath("https://h/sub/o/r", "o", "r"); got != "/sub" {
		t.Errorf("sub = %q", got)
	}
	if got := RootSubPath("https://h/sub/o/r", "O", "R"); got != "/sub" {
		t.Errorf("case-insensitive sub = %q", got)
	}
	if got := RootSubPath("https://h/o/r", "o", "r"); got != "" {
		t.Errorf("sub = %q", got)
	}
}

func TestRepoMetaCacheIsPerToken(t *testing.T) {
	var hits atomic.Int32
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/o/r" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		fmt.Fprintf(w, `{"full_name":"o/r","default_branch":"main","private":%v}`, strings.Contains(r.Header.Get("Authorization"), "secret"))
	})
	c1 := newClient(t, h, "secret")
	for range 3 {
		m, err := RepoMeta(context.Background(), c1, "o", "r")
		if err != nil || m.DefaultBranch != "main" || !m.Private {
			t.Fatalf("%v %+v", err, m)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
	// another token on the same cache must not see the first token's entry
	c2 := c1.WithToken("other")
	m, err := RepoMeta(context.Background(), c2, "o", "r")
	if err != nil || m.Private || hits.Load() != 2 {
		t.Fatalf("shared cache across tokens: %v %+v hits=%d", err, m, hits.Load())
	}
	InvalidateRepo(c1, "o", "r")
	if _, err := RepoMeta(context.Background(), c1, "o", "r"); err != nil || hits.Load() != 3 {
		t.Fatalf("invalidate: %v hits=%d", err, hits.Load())
	}
	if _, err := RepoMeta(context.Background(), c1, "o", "../x"); err == nil {
		t.Fatal("bad repo accepted")
	}
}

func TestResolveLabelsAndMilestones(t *testing.T) {
	var labelHits atomic.Int32
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/settings/api":
			fmt.Fprint(w, `{"default_paging_num":30,"max_response_items":50}`)
		case "/api/v1/repos/org/r/labels":
			labelHits.Add(1)
			w.Header().Set("X-Total-Count", "2")
			fmt.Fprint(w, `[{"id":1,"name":"bug"},{"id":2,"name":"Enhancement"}]`)
		case "/api/v1/orgs/org/labels":
			w.Header().Set("X-Total-Count", "1")
			fmt.Fprint(w, `[{"id":9,"name":"org:p1"}]`)
		case "/api/v1/repos/usr/r/labels":
			fmt.Fprint(w, `[{"id":5,"name":"x"}]`)
		case "/api/v1/orgs/usr/labels":
			w.WriteHeader(404)
			fmt.Fprint(w, `{"message":"GetOrgByName"}`)
		case "/api/v1/repos/org/r/milestones":
			if r.URL.Query().Get("state") != "all" {
				t.Errorf("state = %q", r.URL.Query().Get("state"))
			}
			fmt.Fprint(w, `[{"id":7,"title":"v1.0"},{"id":8,"title":"v2.0"}]`)
		default:
			http.NotFound(w, r)
		}
	})
	c := newClient(t, h, "t")
	ctx := context.Background()
	ids, err := ResolveLabels(ctx, c, "org", "r", []string{"bug", "enhancement", "org:p1"})
	if err != nil || len(ids) != 3 || ids[0] != 1 || ids[1] != 2 || ids[2] != 9 {
		t.Fatalf("ids = %v err = %v", ids, err)
	}
	_, err = ResolveLabels(ctx, c, "org", "r", []string{"nope"})
	if err == nil || err.Error() != `unknown label "nope"; known: bug,Enhancement,org:p1` {
		t.Fatalf("err = %v", err)
	}
	if labelHits.Load() != 1 {
		t.Fatalf("label list fetched %d times, want 1 (cache)", labelHits.Load())
	}
	InvalidateLabels(c, "org", "r")
	if _, err := ResolveLabels(ctx, c, "org", "r", []string{"bug"}); err != nil || labelHits.Load() != 2 {
		t.Fatalf("after invalidate: %v hits=%d", err, labelHits.Load())
	}
	if ids, err := ResolveLabels(ctx, c, "usr", "r", []string{"x"}); err != nil || ids[0] != 5 {
		t.Fatalf("user repo: %v %v", ids, err)
	}
	if ids, err := ResolveLabels(ctx, c, "org", "r", nil); err != nil || ids == nil || len(ids) != 0 {
		t.Fatalf("empty: %v %v", ids, err)
	}

	for in, want := range map[string]int64{"v1.0": 7, "8": 8, "V2.0": 8} {
		got, err := ResolveMilestone(ctx, c, "org", "r", in)
		if err != nil || got != want {
			t.Errorf("milestone %q = %d %v", in, got, err)
		}
	}
	if _, err := ResolveMilestone(ctx, c, "org", "r", "zzz"); err == nil || !strings.Contains(err.Error(), "known: v1.0,v2.0") {
		t.Errorf("err = %v", err)
	}
}

func TestResolveTarget(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".git/config", "[remote \"origin\"]\n\turl = https://forgejo.example.com/o/r.git\n")
	var prJSON atomic.Value
	prJSON.Store(`[]`)
	var branchMissing atomic.Bool
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/settings/api":
			fmt.Fprint(w, `{"default_paging_num":30,"max_response_items":50}`)
		case "/api/v1/repos/o/r", "/api/v1/repos/o/other":
			fmt.Fprint(w, `{"full_name":"o/r","default_branch":"main"}`)
		case "/api/v1/repos/o/r/branches/fix/login":
			if branchMissing.Load() {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, `{"name":"fix/login"}`)
		case "/api/v1/repos/o/r/pulls":
			if r.URL.Query().Get("head") != "fix/login" || r.URL.Query().Get("state") != "open" {
				t.Errorf("query = %v", r.URL.Query())
			}
			fmt.Fprint(w, prJSON.Load().(string))
		default:
			http.NotFound(w, r)
		}
	})
	c := newClient(t, h, "t")
	det := gitremote.NewDetector(gitremote.Options{InstanceURL: c.BaseURL(), Dir: dir})
	_ = det
	// InstanceURL host is 127.0.0.1:port, so the remote must match it
	write(".git/config", fmt.Sprintf("[remote \"origin\"]\n\turl = %s/o/r.git\n", c.BaseURL()))
	det = gitremote.NewDetector(gitremote.Options{InstanceURL: c.BaseURL(), Dir: dir})
	ctx := context.Background()

	// default branch checked out
	write(".git/HEAD", "ref: refs/heads/main\n")
	tg, err := ResolveTarget(ctx, c, det, "o", "r")
	if err != nil || tg.Kind != "branch" || tg.Branch != "main" || tg.Label != "target: branch main (default)" {
		t.Fatalf("default: %+v %v", tg, err)
	}
	// feature branch without PR
	write(".git/HEAD", "ref: refs/heads/fix/login\n")
	tg, err = ResolveTarget(ctx, c, det, "o", "r")
	if err != nil || tg.Kind != "branch" || tg.Branch != "fix/login" {
		t.Fatalf("branch: %+v %v", tg, err)
	}
	// feature branch with PR
	prJSON.Store(`[{"number":41,"head":{"ref":"fix/login","repo":{"full_name":"fork/r"}}},{"number":42,"head":{"ref":"fix/login","repo":{"full_name":"o/r"}}}]`)
	tg, err = ResolveTarget(ctx, c, det, "o", "r")
	if err != nil || tg.Kind != "pr" || tg.PR != 42 || tg.Label != "target: pr #42 (branch fix/login)" {
		t.Fatalf("pr: %+v %v", tg, err)
	}
	// local, unpushed branch: default branch
	branchMissing.Store(true)
	tg, err = ResolveTarget(ctx, c, det, "o", "r")
	if err != nil || tg.Branch != "main" || tg.Label != "target: branch main (default)" {
		t.Fatalf("unpushed: %+v %v", tg, err)
	}
	branchMissing.Store(false)
	// different repo than git: default branch
	tg, err = ResolveTarget(ctx, c, det, "o", "other")
	if err != nil || tg.Kind != "branch" || tg.Branch != "main" {
		t.Fatalf("other repo: %+v %v", tg, err)
	}
	// detached HEAD
	write(".git/HEAD", "0123456789012345678901234567890123456789\n")
	tg, err = ResolveTarget(ctx, c, det, "o", "r")
	if err != nil || tg.Branch != "main" {
		t.Fatalf("detached: %+v %v", tg, err)
	}
	// no detector
	if tg, err = ResolveTarget(ctx, c, nil, "o", "r"); err != nil || tg.Branch != "main" {
		t.Fatalf("nil det: %+v %v", tg, err)
	}
}

func TestResolveRun(t *testing.T) {
	var lastQuery atomic.Value
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/o/r":
			fmt.Fprint(w, `{"default_branch":"trunk"}`)
		case "/api/v1/repos/o/r/actions/runs/55":
			fmt.Fprint(w, `{"id":55,"index_in_repo":9,"commit_sha":"aaa","status":"success"}`)
		case "/api/v1/repos/o/r/git/commits/abcd":
			fmt.Fprint(w, `{"sha":"abcd000000000000000000000000000000000000"}`)
		case "/api/v1/repos/o/r/actions/runs":
			q := r.URL.Query()
			lastQuery.Store(q)
			switch {
			case q.Get("run_number") == "9":
				fmt.Fprint(w, `{"workflow_runs":[{"id":55,"index_in_repo":9,"status":"failure"}],"total_count":1}`)
			case q.Get("ref") == "refs/pull/12/head":
				fmt.Fprint(w, `{"workflow_runs":[{"id":70,"index_in_repo":12,"status":"running"},{"id":71,"index_in_repo":13,"status":"waiting"}]}`)
			case q.Get("ref") == "refs/heads/trunk":
				fmt.Fprint(w, `{"workflow_runs":[{"id":80,"index_in_repo":20,"status":"success","commit_sha":"d"}]}`)
			case q.Get("ref") == "refs/tags/v1":
				fmt.Fprint(w, `{"workflow_runs":[{"id":90,"index_in_repo":30,"status":"success"}]}`)
			case q.Get("head_sha") == "abcd000000000000000000000000000000000000":
				fmt.Fprint(w, `{"workflow_runs":[{"id":95,"index_in_repo":31,"status":"success"}]}`)
			default:
				fmt.Fprint(w, `{"workflow_runs":[],"total_count":0}`)
			}
		default:
			http.NotFound(w, r)
		}
	})
	c := newClient(t, h, "t")
	ctx := context.Background()
	tests := []struct {
		name string
		sel  RunSelector
		want int64
	}{
		{"id", RunSelector{Run: 55}, 55},
		{"number", RunSelector{Number: 9}, 55},
		{"pr newest", RunSelector{PR: 12}, 71},
		{"default branch", RunSelector{}, 80},
		{"tag fallback", RunSelector{Ref: "v1"}, 90},
		{"short sha", RunSelector{SHA: "abcd"}, 95},
		{"pr: ref", RunSelector{Ref: "pr:12"}, 71},
	}
	for _, tc := range tests {
		got, err := ResolveRun(ctx, c, "o", "r", tc.sel)
		if err != nil || got.ID != tc.want {
			t.Errorf("%s: %+v %v", tc.name, got, err)
		}
	}
	if got, _ := ResolveRun(ctx, c, "o", "r", RunSelector{Number: 9}); got.Number != 9 || got.Status != "failure" {
		t.Errorf("number run = %+v", got)
	}
	for name, sel := range map[string]RunSelector{
		"two": {Run: 1, PR: 2}, "none found": {Ref: "ghost"}, "hash": {Ref: "#12"}, "badsha": {SHA: "zz"},
	} {
		if _, err := ResolveRun(ctx, c, "o", "r", sel); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestAllReviewsPagesUntilEmpty(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/settings/api":
			fmt.Fprint(w, `{"default_paging_num":2,"max_response_items":2}`)
		case "/api/v1/repos/o/r/pulls/5/reviews":
			if r.URL.Query().Get("limit") != "2" {
				t.Errorf("limit = %q", r.URL.Query().Get("limit"))
			}
			switch r.URL.Query().Get("page") {
			case "1":
				fmt.Fprint(w, `[{"id":1,"state":"APPROVED","user":{"login":"ann"},"stale":true}]`) // short page
			case "2":
				fmt.Fprint(w, `[{"id":2,"state":"REQUEST_CHANGES","user":{"login":"bob"},"official":true},{"id":3,"state":"COMMENT","user":{"login":"ann"}}]`)
			default:
				fmt.Fprint(w, `[]`)
			}
		default:
			http.NotFound(w, r)
		}
	})
	c := newClient(t, h, "t")
	rs, err := AllReviews(context.Background(), c, "o", "r", 5)
	if err != nil || len(rs) != 3 {
		t.Fatalf("%v %+v", err, rs)
	}
	if rs[0].User != "ann" || !rs[0].Stale || rs[1].User != "bob" || !rs[1].Official || rs[2].ID != 3 {
		t.Fatalf("%+v", rs)
	}
}
