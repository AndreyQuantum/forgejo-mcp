package packages

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

type reqRec struct{ Method, Path, Query, Body string }

type route struct {
	status int
	body   string
	total  string
}

// fakeAPI answers "METHOD /path" routes and records every call.
type fakeAPI struct {
	mu     sync.Mutex
	routes map[string]route
	reqs   []reqRec
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v1")
	if path == "/settings/api" || path == "/version" {
		http.NotFound(w, r)
		return
	}
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.reqs = append(f.reqs, reqRec{r.Method, path, r.URL.Query().Encode(), string(b)})
	rt, ok := f.routes[r.Method+" "+path]
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"not found"}`))
		return
	}
	if rt.total != "" {
		w.Header().Set("X-Total-Count", rt.total)
	}
	if rt.status == 0 {
		rt.status = http.StatusOK
	}
	w.WriteHeader(rt.status)
	_, _ = w.Write([]byte(rt.body))
}

func (f *fakeAPI) last() reqRec {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) == 0 {
		return reqRec{}
	}
	return f.reqs[len(f.reqs)-1]
}

func (f *fakeAPI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func spec(t *testing.T) tools.Spec {
	t.Helper()
	r := tools.NewRegistry()
	registerPackage(r)
	return r.Specs()[0]
}

const listBody = `[
 {"id":1,"type":"npm","name":"foo","version":"1.0.2","created_at":"2026-11-20T09:30:00Z","owner":{"login":"alice"},"repository":{"full_name":"alice/foo"},"html_url":"https://h/alice/-/packages/npm/foo/1.0.2"},
 {"id":2,"type":"generic","name":"blob","version":"0.1","created_at":"2026-11-21T10:00:00Z","owner":{"login":"alice"}}
]`

const oneBody = `{"id":1,"type":"npm","name":"foo","version":"1.0.2","created_at":"2026-11-20T09:30:00Z",
 "owner":{"login":"alice"},"creator":{"login":"bob"},"repository":{"full_name":"alice/foo"},
 "html_url":"https://h/alice/-/packages/npm/foo/1.0.2","unused":"x"}`

const filesBody = `[
 {"id":7,"Size":1234,"name":"foo-1.0.2.tgz","md5":"m","sha1":"s","sha256":"0123456789abcdef0123456789abcdef","sha512":"z"},
 {"id":8,"Size":12,"name":"package.json","sha256":""},
 {"id":9,"Size":3000000,"name":"big.bin","sha256":"fedcba9876543210"}
]`

func TestPackageActions(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]any
		routes  map[string]route
		wantReq reqRec
		golden  string
		isErr   bool
	}{
		{
			name:    "list",
			args:    map[string]any{"action": "list", "owner": "alice", "type": "npm", "q": "fo", "limit": 2},
			routes:  map[string]route{"GET /packages/alice": {body: listBody, total: "5"}},
			wantReq: reqRec{"GET", "/packages/alice", "limit=2&page=1&q=fo&type=npm", ""},
			golden:  "list",
		},
		{
			name:    "list empty",
			args:    map[string]any{"action": "list", "owner": "alice"},
			routes:  map[string]route{"GET /packages/alice": {body: `[]`, total: "0"}},
			wantReq: reqRec{"GET", "/packages/alice", "limit=30&page=1", ""},
			golden:  "list_empty",
		},
		{
			name:    "get",
			args:    map[string]any{"action": "get", "owner": "alice", "type": "npm", "name": "foo", "version": "1.0.2"},
			routes:  map[string]route{"GET /packages/alice/npm/foo/1.0.2": {body: oneBody}},
			wantReq: reqRec{"GET", "/packages/alice/npm/foo/1.0.2", "", ""},
			golden:  "get",
		},
		{
			name:    "get escapes name and version",
			args:    map[string]any{"action": "get", "owner": "alice", "type": "container", "name": "img/sub", "version": "sha256:ab"},
			routes:  map[string]route{"GET /packages/alice/container/img%2Fsub/sha256:ab": {body: oneBody}},
			wantReq: reqRec{"GET", "/packages/alice/container/img%2Fsub/sha256:ab", "", ""},
			golden:  "get",
		},
		{
			name:    "files",
			args:    map[string]any{"action": "files", "owner": "alice", "type": "npm", "name": "foo", "version": "1.0.2", "limit": 2},
			routes:  map[string]route{"GET /packages/alice/npm/foo/1.0.2/files": {body: filesBody}},
			wantReq: reqRec{"GET", "/packages/alice/npm/foo/1.0.2/files", "", ""},
			golden:  "files",
		},
		{
			name:    "files page 2",
			args:    map[string]any{"action": "files", "owner": "alice", "type": "npm", "name": "foo", "version": "1.0.2", "limit": 2, "page": 2},
			routes:  map[string]route{"GET /packages/alice/npm/foo/1.0.2/files": {body: filesBody}},
			wantReq: reqRec{"GET", "/packages/alice/npm/foo/1.0.2/files", "", ""},
			golden:  "files_page2",
		},
		{
			name:    "delete",
			args:    map[string]any{"action": "delete", "owner": "alice", "type": "npm", "name": "foo", "version": "1.0.2", "confirm": "alice/npm/foo/1.0.2"},
			routes:  map[string]route{"DELETE /packages/alice/npm/foo/1.0.2": {status: 204}},
			wantReq: reqRec{"DELETE", "/packages/alice/npm/foo/1.0.2", "", ""},
			golden:  "delete",
		},
		{
			name:    "link",
			args:    map[string]any{"action": "link", "owner": "alice", "type": "npm", "name": "foo", "repo": "foo"},
			routes:  map[string]route{"POST /packages/alice/npm/foo/-/link/foo": {status: 201}},
			wantReq: reqRec{"POST", "/packages/alice/npm/foo/-/link/foo", "", ""},
			golden:  "link",
		},
		{
			name:    "link with owner prefix",
			args:    map[string]any{"action": "link", "owner": "alice", "type": "npm", "name": "foo", "repo": "alice/foo"},
			routes:  map[string]route{"POST /packages/alice/npm/foo/-/link/foo": {status: 201}},
			wantReq: reqRec{"POST", "/packages/alice/npm/foo/-/link/foo", "", ""},
			golden:  "link",
		},
		{
			name:    "unlink",
			args:    map[string]any{"action": "unlink", "owner": "alice", "type": "npm", "name": "foo"},
			routes:  map[string]route{"POST /packages/alice/npm/foo/-/unlink": {status: 201}},
			wantReq: reqRec{"POST", "/packages/alice/npm/foo/-/unlink", "", ""},
			golden:  "unlink",
		},
		{
			name:    "default owner from default repo",
			args:    map[string]any{"action": "list"},
			routes:  map[string]route{"GET /packages/alice": {body: `[]`, total: "0"}},
			wantReq: reqRec{"GET", "/packages/alice", "limit=30&page=1", ""},
			golden:  "list_empty",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAPI{routes: tc.routes}
			env := testutil.NewEnv(t, f, testutil.WithToken("tkn-0123456789"), testutil.WithRepo("alice/foo"))
			got, isErr := testutil.CallTool(t, env, spec(t), tc.args)
			if isErr != tc.isErr {
				t.Fatalf("isErr = %v, want %v: %s", isErr, tc.isErr, got)
			}
			if r := f.last(); r != tc.wantReq {
				t.Errorf("request = %+v, want %+v", r, tc.wantReq)
			}
			testutil.Golden(t, tc.golden, got)
		})
	}
}

func TestPackageDeleteNeedsExactConfirm(t *testing.T) {
	f := &fakeAPI{routes: map[string]route{"DELETE /packages/alice/npm/foo/1.0.2": {status: 204}}}
	env := testutil.NewEnv(t, f, testutil.WithToken("tkn-0123456789"))
	base := map[string]any{"action": "delete", "owner": "alice", "type": "npm", "name": "foo", "version": "1.0.2"}

	got, isErr := testutil.CallTool(t, env, spec(t), base)
	if !isErr || !strings.Contains(got, "missing param confirm") {
		t.Fatalf("no confirm: %q (isErr %v)", got, isErr)
	}
	base["confirm"] = "alice/npm/foo"
	got, isErr = testutil.CallTool(t, env, spec(t), base)
	if !isErr || !strings.Contains(got, `confirm="alice/npm/foo/1.0.2"`) {
		t.Fatalf("wrong confirm: %q (isErr %v)", got, isErr)
	}
	if n := f.count(); n != 0 {
		t.Fatalf("server was called %d times without a valid confirm", n)
	}
}

func TestPackageValidation(t *testing.T) {
	f := &fakeAPI{}
	env := testutil.NewEnv(t, f)
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"bad type", map[string]any{"action": "get", "owner": "alice", "type": "zip", "name": "n", "version": "1"}, `type "zip"; valid:`},
		{"missing version", map[string]any{"action": "get", "owner": "alice", "type": "npm", "name": "n"}, "missing param version"},
		{"missing repo", map[string]any{"action": "link", "owner": "alice", "type": "npm", "name": "n"}, "missing param repo"},
		{"foreign repo", map[string]any{"action": "link", "owner": "alice", "type": "npm", "name": "n", "repo": "bob/x"}, "must belong to alice"},
		{"dotdot name", map[string]any{"action": "get", "owner": "alice", "type": "npm", "name": "..", "version": "1"}, `invalid: name ".."`},
		{"dotdot version", map[string]any{"action": "files", "owner": "alice", "type": "npm", "name": "n", "version": ".."}, `invalid: version ".."`},
		{"bad owner", map[string]any{"action": "list", "owner": "a b"}, `invalid: owner "a b"`},
		{"dot repo", map[string]any{"action": "link", "owner": "alice", "type": "npm", "name": "n", "repo": ".."}, "invalid: repo"},
		{"no owner", map[string]any{"action": "list"}, "missing param owner"},
		{"unknown action", map[string]any{"action": "push"}, "unknown action push; valid: list,get,files,delete,link,unlink,help"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, isErr := testutil.CallTool(t, env, spec(t), tc.args)
			if !isErr || !strings.Contains(got, tc.want) {
				t.Fatalf("got %q (isErr %v), want error containing %q", got, isErr, tc.want)
			}
		})
	}
	if n := f.count(); n != 0 {
		t.Fatalf("validation errors must not reach the server (%d calls)", n)
	}
}

func TestPackageDefaultOwnerFromMe(t *testing.T) {
	f := &fakeAPI{routes: map[string]route{
		"GET /user":           {body: `{"id":3,"login":"carol"}`},
		"GET /packages/carol": {body: `[]`, total: "0"},
	}}
	env := testutil.NewEnv(t, f, testutil.WithToken("tkn-0123456789"))
	got, isErr := testutil.CallTool(t, env, spec(t), map[string]any{"action": "list"})
	if isErr {
		t.Fatalf("unexpected error: %s", got)
	}
	if r := f.last(); r.Path != "/packages/carol" {
		t.Fatalf("last request = %+v", r)
	}
}

func TestPackageErrors(t *testing.T) {
	f := &fakeAPI{routes: map[string]route{
		"GET /packages/alice/npm/foo/9":        {status: 404, body: `{"message":"GetPackageByNameAndVersion","errors":["package does not exist"]}`},
		"DELETE /packages/alice/npm/foo/1.0.2": {status: 403, body: `{"message":"forbidden"}`},
	}}
	env := testutil.NewEnv(t, f, testutil.WithToken("tkn-0123456789"))

	got, isErr := testutil.CallTool(t, env, spec(t), map[string]any{"action": "get", "owner": "alice", "type": "npm", "name": "foo", "version": "9"})
	if !isErr || !strings.Contains(got, "404 not found: package does not exist") {
		t.Fatalf("404: %q", got)
	}
	got, isErr = testutil.CallTool(t, env, spec(t), map[string]any{"action": "delete", "owner": "alice", "type": "npm", "name": "foo", "version": "1.0.2", "confirm": "alice/npm/foo/1.0.2"})
	if !isErr || !strings.Contains(got, "403") || !strings.Contains(got, "needs: package owner") {
		t.Fatalf("403: %q", got)
	}
}

func TestPackageReadOnlyHidesWrites(t *testing.T) {
	f := &fakeAPI{}
	env := testutil.NewEnv(t, f, testutil.WithToken("tkn-0123456789"))
	env.Cfg.ReadOnly = true
	got, isErr := testutil.CallTool(t, env, spec(t), map[string]any{"action": "delete", "owner": "a", "type": "npm", "name": "n", "version": "1", "confirm": "a/npm/n/1"})
	if !isErr || !strings.Contains(got, "is disabled (read-only mode)") {
		t.Fatalf("read-only delete: %q (isErr %v)", got, isErr)
	}
	if f.count() != 0 {
		t.Fatal("server was called in read-only mode")
	}
}
