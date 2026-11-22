package meta

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/endpoints"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

func TestSpecsAndSchemas(t *testing.T) {
	reg := newRegistry(t, config.Defaults(), false)
	names := map[string]tools.Spec{}
	for _, s := range reg.Enabled() {
		names[s.Name] = s
	}
	for _, n := range []string{"forgejo_info", "forgejo_api", "forgejo_endpoints"} {
		s, ok := names[n]
		if !ok {
			t.Fatalf("tool %s not registered", n)
		}
		if !s.Single || s.Toolset != "meta" || len(s.Description) > 160 {
			t.Errorf("%s: bad spec %+v", n, s)
		}
		sc, err := tools.BuildInputSchema(s, s.Actions)
		if err != nil {
			t.Fatalf("%s schema: %v", n, err)
		}
		raw, _ := json.Marshal(sc)
		if len(raw) > tools.BudgetToolBytes {
			t.Errorf("%s schema is %d bytes", n, len(raw))
		}
		if _, hasAction := sc.Properties["action"]; hasAction {
			t.Errorf("%s: Single tool exposes an action property", n)
		}
	}
	sc, _ := tools.BuildInputSchema(names["forgejo_api"], names["forgejo_api"].Actions)
	if len(sc.Required) != 2 {
		t.Errorf("forgejo_api required = %v", sc.Required)
	}
	// read-only mode keeps forgejo_api registered (ModeFor) and info/endpoints.
	cfg := config.Defaults()
	cfg.ReadOnly = true
	ro := newRegistry(t, cfg, false)
	if len(ro.Enabled()) != 3 {
		t.Errorf("read-only registry has %d meta tools, want 3", len(ro.Enabled()))
	}
}

func TestEndpointsExistInIndex(t *testing.T) {
	reg := newRegistry(t, config.Defaults(), false)
	for _, s := range reg.Specs() {
		for _, a := range s.Actions {
			for _, ep := range a.Endpoints {
				sp := strings.SplitN(ep, " ", 2)
				if endpoints.Lookup(sp[0], sp[1]) == nil {
					t.Errorf("%s.%s: endpoint %q is not in the index", s.Name, a.Name, ep)
				}
			}
		}
	}
}

func TestAPIGetPickAndNoise(t *testing.T) {
	var gotQuery url.Values
	var gotAuth string
	h := baseMux(map[string]http.HandlerFunc{
		"/api/v1/repos/o/r/pulls": func(w http.ResponseWriter, r *http.Request) {
			gotQuery, gotAuth = r.URL.Query(), r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Total-Count", "134")
			_, _ = w.Write([]byte(`[{"number":1,"title":"a","user":{"login":"alice","avatar_url":"x"},"labels":[{"name":"bug"},{"name":"ui"}],"body":"","url":"u","html_url":"h"},
				{"number":2,"title":"b","user":{"login":"bob"},"labels":[]}]`))
		},
	})
	env, reg := envFor(t, h, nil)

	// default: noise stripped, header with list counts
	out, isErr := callReg(t, env, reg, "forgejo_api", map[string]any{
		"method": "GET", "path": "/repos/o/r/pulls", "query": map[string]any{"state": "open", "labels": []any{1, 2}, "page": 2, "limit": 2},
	})
	if isErr {
		t.Fatalf("error: %s", out)
	}
	if !strings.HasPrefix(out, "# 200 GET /repos/o/r/pulls 2/134 items page 2 (more: page=3)\n") {
		t.Errorf("header wrong:\n%s", out)
	}
	if strings.Contains(out, "avatar_url") || strings.Contains(out, `"body"`) || !strings.Contains(out, `"user":"alice"`) || !strings.Contains(out, `"html_url":"h"`) {
		t.Errorf("noise stripping wrong:\n%s", out)
	}
	if gotAuth != "token tok-secret-1234567890" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotQuery.Get("state") != "open" || strings.Join(gotQuery["labels"], ",") != "1,2" || gotQuery.Get("page") != "2" {
		t.Errorf("query = %v", gotQuery)
	}

	// pick
	out, _ = callReg(t, env, reg, "forgejo_api", map[string]any{
		"method": "GET", "path": "/repos/o/r/pulls", "pick": []any{"number", "user.login:by", "labels[].name:labels"},
	})
	body := out[strings.Index(out, "\n")+1:]
	lines := strings.Split(body, "\n")
	if len(lines) != 2 || lines[0] != `{"number":1,"by":"alice","labels":["bug","ui"]}` || !strings.HasPrefix(lines[1], `{"number":2,"by":"bob"`) {
		t.Errorf("pick output:\n%s", out)
	}

	// raw keeps everything
	out, _ = callReg(t, env, reg, "forgejo_api", map[string]any{"method": "GET", "path": "/repos/o/r/pulls", "raw": true})
	if !strings.Contains(out, "avatar_url") || !strings.Contains(out, `"body":""`) {
		t.Errorf("raw output:\n%s", out)
	}

	// bad pick
	out, isErr = callReg(t, env, reg, "forgejo_api", map[string]any{"method": "GET", "path": "/repos/o/r/pulls", "pick": []any{"a["}})
	if !isErr || !strings.HasPrefix(out, "error: pick:") {
		t.Errorf("bad pick: %v %q", isErr, out)
	}
}

func TestAPIWrapperUnwrapAndAllPages(t *testing.T) {
	var mu sync.Mutex
	var pages []string
	h := baseMux(map[string]http.HandlerFunc{
		"/api/v1/repos/o/r/actions/runs": func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			pages = append(pages, r.URL.Query().Get("page")+"/"+r.URL.Query().Get("limit"))
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Query().Get("page") {
			case "1":
				_, _ = w.Write([]byte(`{"workflow_runs":[{"id":1},{"id":2}],"total_count":5}`))
			case "2":
				_, _ = w.Write([]byte(`{"workflow_runs":[{"id":3},{"id":4}],"total_count":5}`))
			default:
				_, _ = w.Write([]byte(`{"workflow_runs":[{"id":5}],"total_count":5}`))
			}
		},
		"/api/v1/settings/api": jsonReply(`{"max_response_items":2,"default_paging_num":2}`),
	})
	env, reg := envFor(t, h, nil)

	// single page unwraps and moves the total into the header
	out, isErr := callReg(t, env, reg, "forgejo_api", map[string]any{"method": "GET", "path": "/repos/o/r/actions/runs", "query": map[string]any{"limit": 2, "page": 1}})
	if isErr || !strings.HasPrefix(out, "# 200 GET /repos/o/r/actions/runs 2/5 items page 1 (more: page=2)") || !strings.HasSuffix(out, `[{"id":1},{"id":2}]`) {
		t.Errorf("single page:\n%s", out)
	}

	// all pages: end
	pages = nil
	out, isErr = callReg(t, env, reg, "forgejo_api", map[string]any{"method": "GET", "path": "/repos/o/r/actions/runs", "all_pages": true})
	if isErr {
		t.Fatalf("error: %s", out)
	}
	if !strings.Contains(out, "pages: 3/5 fetched, 5 items, stopped: end") || !strings.HasSuffix(out, `[{"id":1},{"id":2},{"id":3},{"id":4},{"id":5}]`) {
		t.Errorf("all pages:\n%s", out)
	}
	if strings.Join(pages, ",") != "1/2,2/2,3/2" {
		t.Errorf("pages requested = %v", pages)
	}

	// all pages: max_pages
	out, _ = callReg(t, env, reg, "forgejo_api", map[string]any{"method": "GET", "path": "/repos/o/r/actions/runs", "all_pages": true, "max_pages": 2})
	if !strings.Contains(out, "pages: 2/2 fetched, 4 items, stopped: max_pages") {
		t.Errorf("max_pages:\n%s", out)
	}
	// all pages: chars
	out, _ = callReg(t, env, reg, "forgejo_api", map[string]any{"method": "GET", "path": "/repos/o/r/actions/runs", "all_pages": true, "max_chars": 200, "pick": []any{"id"}})
	_ = out
}

func TestAPIAllPagesNotAList(t *testing.T) {
	h := baseMux(map[string]http.HandlerFunc{"/api/v1/repos/o/r": jsonReply(`{"name":"r"}`)})
	env, reg := envFor(t, h, nil)
	out, isErr := callReg(t, env, reg, "forgejo_api", map[string]any{"method": "GET", "path": "/repos/o/r", "all_pages": true})
	if !isErr || out != "error: all_pages needs a list response" {
		t.Errorf("got %v %q", isErr, out)
	}
	out, isErr = callReg(t, env, reg, "forgejo_api", map[string]any{"method": "POST", "path": "/repos/o/r/labels", "all_pages": true})
	if !isErr || !strings.Contains(out, "all_pages needs method GET") {
		t.Errorf("got %v %q", isErr, out)
	}
}

func TestAPIBodiesAndEmptyResponses(t *testing.T) {
	type seen struct{ method, path, ct, body string }
	var got []seen
	h := baseMux(map[string]http.HandlerFunc{
		"/api/v1/": func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			got = append(got, seen{r.Method, r.URL.EscapedPath(), r.Header.Get("Content-Type"), string(b)})
			switch r.URL.Path {
			case "/api/v1/repos/o/r/topics/x":
				w.WriteHeader(http.StatusNoContent)
			case "/api/v1/markdown/raw":
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte("<p>hi</p>"))
			default:
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":7,"name":"n"}`))
			}
		},
	})
	env, reg := envFor(t, h, nil)

	out, isErr := callReg(t, env, reg, "forgejo_api", map[string]any{"method": "PUT", "path": "/repos/o/r/topics/x"})
	if isErr || out != "ok (204)" {
		t.Errorf("204: %v %q", isErr, out)
	}
	out, isErr = callReg(t, env, reg, "forgejo_api", map[string]any{"method": "POST", "path": "/repos/o/r/labels", "body": map[string]any{"name": "bug", "color": "#ff0000", "n": 12345678901}})
	if isErr || !strings.HasPrefix(out, "# 201 POST /repos/o/r/labels") || !strings.HasSuffix(out, `{"id":7,"name":"n"}`) {
		t.Errorf("201: %v %q", isErr, out)
	}
	last := got[len(got)-1]
	if last.ct != "application/json" || !strings.Contains(last.body, `"n":12345678901`) || !strings.Contains(last.body, `"name":"bug"`) {
		t.Errorf("json body request = %+v", last)
	}
	out, isErr = callReg(t, env, reg, "forgejo_api", map[string]any{"method": "POST", "path": "/markdown/raw", "body": "**hi**"})
	if isErr || !strings.HasSuffix(out, "\n<p>hi</p>") {
		t.Errorf("text: %v %q", isErr, out)
	}
	last = got[len(got)-1]
	if last.ct != "text/plain" || last.body != "**hi**" {
		t.Errorf("raw body request = %+v", last)
	}
}

func TestAPIPathEscapingKeepsPercent(t *testing.T) {
	var escaped string
	h := baseMux(map[string]http.HandlerFunc{
		"/api/v1/repos/o/r/contents/": func(w http.ResponseWriter, r *http.Request) {
			escaped = r.URL.EscapedPath()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"a b.txt"}`))
		},
	})
	env, reg := envFor(t, h, nil)
	for _, in := range []string{"/repos/o/r/contents/dir/a%20b.txt", "/repos/o/r/contents/dir/a b.txt", "//repos/o/r/contents/dir/a%20b.txt/"} {
		escaped = ""
		out, isErr := callReg(t, env, reg, "forgejo_api", map[string]any{"method": "GET", "path": in})
		if isErr {
			t.Fatalf("%q: %s", in, out)
		}
		if escaped != "/api/v1/repos/o/r/contents/dir/a%20b.txt" {
			t.Errorf("%q -> %q", in, escaped)
		}
	}
}

func TestAPIForgejoPrefix(t *testing.T) {
	var path string
	h := baseMux(map[string]http.HandlerFunc{
		"/api/forgejo/v1/": func(w http.ResponseWriter, r *http.Request) {
			path = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		},
	})
	env, reg := envFor(t, h, nil)
	out, isErr := callReg(t, env, reg, "forgejo_api", map[string]any{"method": "GET", "path": "/forgejo/v1/something"})
	if isErr || path != "/api/forgejo/v1/something" || !strings.Contains(out, "GET /forgejo/v1/something") {
		t.Errorf("%v %q path=%q", isErr, out, path)
	}
}

func TestAPIBinaryAndSave(t *testing.T) {
	zip := []byte("PK\x03\x04\x00binary")
	h := baseMux(map[string]http.HandlerFunc{
		"/api/v1/repos/o/r/archive/main.zip": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(zip)
		},
		"/api/v1/repos/o/r/missing": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"message":"The target couldn't be found."}`))
		},
	})
	env, reg := envFor(t, h, nil)
	out, isErr := callReg(t, env, reg, "forgejo_api", map[string]any{"method": "GET", "path": "/repos/o/r/archive/main.zip"})
	if isErr || out != "binary application/zip 11 bytes; pass save_to=<path>" {
		t.Errorf("binary: %v %q", isErr, out)
	}

	if !fileRootsWork(env) {
		t.Skip("shared.FileRoots (WP6) not implemented yet")
	}
	out, isErr = callReg(t, env, reg, "forgejo_api", map[string]any{"method": "GET", "path": "/repos/o/r/archive/main.zip", "save_to": "a.zip"})
	if isErr || !strings.HasPrefix(out, "ok: saved ") || !strings.HasSuffix(out, "(11B)") {
		t.Errorf("save: %v %q", isErr, out)
	}
	b, err := os.ReadFile(filepath.Join(env.Cfg.SaveDir, "a.zip"))
	if err != nil || string(b) != string(zip) {
		t.Errorf("saved file: %v %q", err, b)
	}
	// existing file is never overwritten
	out, isErr = callReg(t, env, reg, "forgejo_api", map[string]any{"method": "GET", "path": "/repos/o/r/archive/main.zip", "save_to": "a.zip"})
	if !isErr || !strings.Contains(out, "exists") {
		t.Errorf("overwrite: %v %q", isErr, out)
	}
	// a failed request leaves no file behind
	out, isErr = callReg(t, env, reg, "forgejo_api", map[string]any{"method": "GET", "path": "/repos/o/r/missing", "save_to": "b.zip"})
	if !isErr || !strings.Contains(out, "404") {
		t.Errorf("404 save: %v %q", isErr, out)
	}
	if _, err := os.Stat(filepath.Join(env.Cfg.SaveDir, "b.zip")); err == nil {
		t.Error("b.zip was created despite the failed request")
	}
	out, isErr = callReg(t, env, reg, "forgejo_api", map[string]any{"method": "GET", "path": "/repos/o/r/archive/main.zip", "save_to": "../x.zip"})
	if !isErr {
		t.Errorf("path escape accepted: %q", out)
	}
	out, isErr = callReg(t, env, reg, "forgejo_api", map[string]any{"method": "POST", "path": "/repos/o/r/labels", "save_to": "c"})
	if !isErr || !strings.Contains(out, "save_to needs method GET") {
		t.Errorf("save_to POST: %v %q", isErr, out)
	}
}

func TestAPIErrors(t *testing.T) {
	h := baseMux(map[string]http.HandlerFunc{
		"/api/v1/repos/o/r/missing": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"message":"The target couldn't be found."}`))
		},
		"/api/v1/forgejo-only": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) },
		"/api/v1/repos/o/r/actions/runs/1/jobs": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"message":"token does not have at least one of required scope(s): [read:repository write:repository]"}`))
		},
	})
	env, reg := envFor(t, h, nil)
	out, isErr := callReg(t, env, reg, "forgejo_api", map[string]any{"method": "GET", "path": "/repos/o/r/missing"})
	if !isErr || !strings.HasPrefix(out, "error: 404 not found") {
		t.Errorf("404: %v %q", isErr, out)
	}
	// unknown path: version hint
	out, isErr = callReg(t, env, reg, "forgejo_api", map[string]any{"method": "GET", "path": "/forgejo-only"})
	if !isErr || !strings.Contains(out, "hint: endpoint may not exist on this server (version 17.0.2+gitea-1.22.0)") {
		t.Errorf("unknown 404: %v %q", isErr, out)
	}
	out, isErr = callReg(t, env, reg, "forgejo_api", map[string]any{"method": "GET", "path": "/repos/o/r/actions/runs/1/jobs"})
	if !isErr || !strings.Contains(out, "403") || !strings.Contains(out, "scope") {
		t.Errorf("403: %v %q", isErr, out)
	}
	if strings.Contains(out, "tok-secret") {
		t.Errorf("token leaked: %q", out)
	}
	out, isErr = callReg(t, env, reg, "forgejo_api", map[string]any{"method": "FETCH", "path": "/version"})
	if !isErr || !strings.Contains(out, "invalid: method") {
		t.Errorf("method: %v %q", isErr, out)
	}
	out, isErr = callReg(t, env, reg, "forgejo_api", map[string]any{"path": "/version"})
	if !isErr || !strings.Contains(out, "missing param method") {
		t.Errorf("missing: %v %q", isErr, out)
	}
}

func TestAPIPolicyThroughTool(t *testing.T) {
	called := false
	h := baseMux(map[string]http.HandlerFunc{
		"/api/v1/repos/o/r": func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"r"}`))
		},
	})
	// deny repo.get: the forgejo_api GET is refused before any request.
	env, reg := envFor(t, h, func(c *config.Config) { c.Deny = []string{"repo.get"} })
	out, isErr := callReg(t, env, reg, "forgejo_api", map[string]any{"method": "GET", "path": "//repos/o/r/"})
	if !isErr || !strings.Contains(out, "blocked by FORGEJO_DENY (repo.get)") || called {
		t.Errorf("deny: %v %q called=%v", isErr, out, called)
	}
	// read-only: GET works, POST refused, DELETE refused
	env, reg = envFor(t, h, func(c *config.Config) { c.ReadOnly = true })
	out, isErr = callReg(t, env, reg, "forgejo_api", map[string]any{"method": "GET", "path": "/repos/o/r"})
	if isErr || !called {
		t.Errorf("ro get: %v %q", isErr, out)
	}
	out, isErr = callReg(t, env, reg, "forgejo_api", map[string]any{"method": "POST", "path": "/repos/o/r/labels", "body": map[string]any{}})
	if !isErr || !strings.Contains(out, "read-only") {
		t.Errorf("ro post: %v %q", isErr, out)
	}
	// destructive confirm
	env, reg = envFor(t, h, nil)
	out, isErr = callReg(t, env, reg, "forgejo_api", map[string]any{"method": "DELETE", "path": "/repos/o/r"})
	if !isErr || !strings.Contains(out, `destructive: pass confirm="/repos/o/r"`) {
		t.Errorf("destructive: %v %q", isErr, out)
	}
}

func TestInfo(t *testing.T) {
	h := baseMux(map[string]http.HandlerFunc{
		"/api/v1/user":                   jsonReply(`{"id":4,"login":"alice","is_admin":true}`),
		"/api/v1/settings/repository":    jsonReply(`{"stars_disabled":true,"migrations_disabled":true,"lfs_disabled":false}`),
		"/api/v1/users/alice/tokens":     jsonReply(`[{"token_last_eight":"34567890","scopes":["write:repository","read:user"]},{"token_last_eight":"deadbeef","scopes":["all"]}]`),
		"/api/v1/user/quota":             func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) },
		"/api/v1/repos/o/r/actions/runs": jsonReply(`{"workflow_runs":[],"total_count":0}`),
	})
	env, reg := envFor(t, h, nil, testutilRepo("o/r"))
	out, isErr := callReg(t, env, reg, "forgejo_info", map[string]any{})
	if isErr {
		t.Fatalf("error: %s", out)
	}
	lines := strings.Split(out, "\n")
	if !strings.HasPrefix(lines[0], "forgejo 17.0.2 (limits: default 30, max 50) http://") {
		t.Errorf("line 1: %q", lines[0])
	}
	want := []string{
		"me: alice (id 4, admin)",
		"repo: o/r (env)",
		"toolsets: repos,pulls,issues,actions,runners,settings,users,packages | read-only: no | ",
		"disabled on server: migrations, stars",
	}
	for i, w := range want {
		if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], w) {
			t.Errorf("line %d = %q, want prefix %q\nfull:\n%s", i+2, safeLine(lines, i+1), w, out)
		}
	}
	if strings.Contains(out, "token scopes") || strings.Contains(out, "quota") {
		t.Errorf("non-verbose output has verbose lines:\n%s", out)
	}

	out, _ = callReg(t, env, reg, "forgejo_info", map[string]any{"verbose": true})
	for _, w := range []string{"me: alice (id 4, admin) | token scopes: read:user,write:repository", "quota: disabled", "actions: enabled"} {
		if !strings.Contains(out, w) {
			t.Errorf("verbose output lacks %q:\n%s", w, out)
		}
	}
	if strings.Contains(out, "tok-secret") {
		t.Errorf("token leaked:\n%s", out)
	}
}

func TestInfoAnonymousAndOldServer(t *testing.T) {
	h := http.NewServeMux()
	h.HandleFunc("/api/v1/version", jsonReply(`{"version":"14.0.1"}`))
	h.HandleFunc("/api/v1/settings/api", jsonReply(settingsAPI))
	h.HandleFunc("/api/v1/settings/repository", jsonReply(`{}`))
	env := newEnvNoToken(t, h)
	reg := newRegistry(t, env.Cfg, false)
	out, isErr := callReg(t, env, reg, "forgejo_info", map[string]any{})
	if isErr {
		t.Fatalf("error: %s", out)
	}
	for _, w := range []string{"me: anonymous", "repo: none", "warn: server major 14 < 15, some actions unsupported", "warn: no token"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
}

func TestEndpointsTool(t *testing.T) {
	env, reg := envFor(t, baseMux(nil), nil)
	out, isErr := callReg(t, env, reg, "forgejo_endpoints", map[string]any{"q": "list pull requests", "method": "GET"})
	if isErr {
		t.Fatalf("error: %s", out)
	}
	lines := strings.Split(out, "\n")
	if !strings.HasPrefix(lines[0], "# endpoints ") || !strings.Contains(lines[0], `(q="list pull requests")`) {
		t.Errorf("header: %q", lines[0])
	}
	if !strings.Contains(out, "GET /repos/{owner}/{repo}/pulls | repoListPullRequests | ") || !strings.Contains(out, "| tool: -") {
		t.Errorf("rows:\n%s", out)
	}
	// covered_by wiring: the fake `repo` tool covers GET /repos/{owner}/{repo}.
	out, _ = callReg(t, env, reg, "forgejo_endpoints", map[string]any{"q": "repoGet", "method": "GET", "limit": 50})
	if !strings.Contains(out, "GET /repos/{owner}/{repo} | repoGet | ") || !strings.Contains(out, "| tool: repo.get") {
		t.Errorf("covered_by missing:\n%s", out)
	}
	// few results get an inline parameter line
	out, _ = callReg(t, env, reg, "forgejo_endpoints", map[string]any{"q": "getRepoRunners"})
	if !strings.Contains(out, "\n  query: ") {
		t.Errorf("no inline params:\n%s", out)
	}
	// detail adds scope and body fields
	out, _ = callReg(t, env, reg, "forgejo_endpoints", map[string]any{"q": "issueCreateLabel", "detail": true})
	if !strings.Contains(out, "scope: write:issue") || !strings.Contains(out, "body: ") || !strings.Contains(out, "name*") {
		t.Errorf("detail:\n%s", out)
	}
	// tag filter + paging
	out, _ = callReg(t, env, reg, "forgejo_endpoints", map[string]any{"tag": "settings"})
	if !strings.HasPrefix(out, "# endpoints 4/4 page 1/1") {
		t.Errorf("tag header: %q", strings.SplitN(out, "\n", 2)[0])
	}
	out, _ = callReg(t, env, reg, "forgejo_endpoints", map[string]any{"q": "repo", "limit": 10, "page": 2})
	if !strings.Contains(strings.SplitN(out, "\n", 2)[0], "page 2/") || !strings.Contains(strings.SplitN(out, "\n", 2)[0], "next: page=3") {
		t.Errorf("paging header: %q", strings.SplitN(out, "\n", 2)[0])
	}
	out, _ = callReg(t, env, reg, "forgejo_endpoints", map[string]any{"q": "zzzznothing"})
	if !strings.Contains(out, "no matching endpoint") {
		t.Errorf("empty:\n%s", out)
	}
}

func safeLine(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return "<missing>"
}

func TestAPIAllPagesStopsOnChars(t *testing.T) {
	long := strings.Repeat("x", 300)
	h := baseMux(map[string]http.HandlerFunc{
		"/api/v1/repos/o/r/issues": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"title":"` + long + `"},{"title":"` + long + `"}]`))
		},
		"/api/v1/settings/api": jsonReply(`{"max_response_items":2,"default_paging_num":2}`),
	})
	env, reg := envFor(t, h, nil)
	out, isErr := callReg(t, env, reg, "forgejo_api", map[string]any{"method": "GET", "path": "/repos/o/r/issues", "all_pages": true, "max_chars": 300})
	if isErr || !strings.Contains(out, "pages: 1/5 fetched, 2 items, stopped: chars") || !strings.Contains(out, "[truncated:") {
		t.Errorf("chars stop:\n%.400s", out)
	}
}

func TestMaskSecrets(t *testing.T) {
	v := map[string]any{"id": 1.0, "authorization_header": "Bearer x", "list": []any{map[string]any{"secret": "s", "url": "u"}}, "empty": map[string]any{"authorization_header": ""}}
	maskSecrets(v)
	b, _ := json.Marshal(v)
	if s := string(b); strings.Contains(s, "Bearer x") || strings.Contains(s, `"s"`) || !strings.Contains(s, "(hidden)") || !strings.Contains(s, `"authorization_header":""`) {
		t.Errorf("masked = %s", s)
	}
}
