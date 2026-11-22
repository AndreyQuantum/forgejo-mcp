package runners

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/endpoints"
	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// seen records the requests that reached the fake server (without the
// /version and /settings/api probes).
type seen struct {
	mu   sync.Mutex
	reqs []seenReq
}

type seenReq struct {
	Method string
	Path   string
	Query  url.Values
	Body   string
}

func (s *seen) last(t testing.TB) seenReq {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.reqs) == 0 {
		t.Fatal("no request reached the server")
	}
	return s.reqs[len(s.reqs)-1]
}

func (s *seen) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

// fake serves /version, /settings/api and routes to h, recording requests.
func fake(h func(w http.ResponseWriter, r *http.Request)) (http.Handler, *seen) {
	s := &seen{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/version", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"17.0.2+gitea-1.22.0"}`))
	})
	mux.HandleFunc("/api/v1/settings/api", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"max_response_items":50,"default_paging_num":30}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, seenReq{r.Method, r.URL.EscapedPath(), r.URL.Query(), string(b)})
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		h(w, r)
	})
	return mux, s
}

func reply(status int, body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func spec(t testing.TB) tools.Spec {
	t.Helper()
	r := tools.NewRegistry()
	Register(r)
	for _, s := range r.Specs() {
		if s.Name == "runner" {
			return s
		}
	}
	t.Fatal("runner spec not registered")
	return tools.Spec{}
}

func run(t testing.TB, h func(http.ResponseWriter, *http.Request), args map[string]any, opts ...testutil.EnvOption) (string, bool, *seen) {
	t.Helper()
	handler, s := fake(h)
	opts = append([]testutil.EnvOption{testutil.WithToken("tok-secret-1234567890"), testutil.WithRepo("o/r")}, opts...)
	env := testutil.NewEnv(t, handler, opts...)
	text, isErr := testutil.CallTool(t, env, spec(t), args)
	return text, isErr, s
}

const runnersList = `[
 {"id":7,"uuid":"3f2b0000-0000-4000-8000-000000000001","name":"build-1","status":"idle","labels":["docker:docker://node:20","ubuntu-latest"],"version":"v6.3.1","repo_id":12,"owner_id":0,"ephemeral":true},
 {"id":8,"uuid":"3f2b0000-0000-4000-8000-000000000002","name":"org-1","status":"active","labels":["docker"],"version":"6.3.0","repo_id":0,"owner_id":4,"description":"org runner"},
 {"id":9,"uuid":"3f2b0000-0000-4000-8000-000000000003","name":"glob-1","status":"offline","labels":[],"version":"","repo_id":0,"owner_id":0}
]`

func TestSpecShape(t *testing.T) {
	s := spec(t)
	if s.Name != "runner" || s.Toolset != "runners" || len(s.Description) > 160 || s.RepoScoped || s.Single {
		t.Fatalf("bad spec: %+v", s)
	}
	want := map[string]tools.Mode{"list": tools.Read, "get": tools.Read, "register": tools.Write, "delete": tools.Write, "queue": tools.Read, "reg_token": tools.Write}
	if len(s.Actions) != len(want) {
		t.Fatalf("actions = %d", len(s.Actions))
	}
	for _, a := range s.Actions {
		if m, ok := want[a.Name]; !ok || m != a.Mode {
			t.Errorf("action %s mode %v", a.Name, a.Mode)
		}
		if len(a.Endpoints) != 4 || a.Summary == "" {
			t.Errorf("action %s: endpoints/summary missing", a.Name)
		}
		for _, ep := range a.Endpoints {
			sp := strings.SplitN(ep, " ", 2)
			if endpoints.Lookup(sp[0], sp[1]) == nil {
				t.Errorf("%s.%s: endpoint %q not in index", s.Name, a.Name, ep)
			}
		}
	}
	sc, err := tools.BuildInputSchema(s, s.Actions)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(sc)
	if len(raw) > tools.BudgetToolBytes {
		t.Errorf("schema is %d bytes", len(raw))
	}
	r := tools.NewRegistry()
	Register(r)
	cfg := config.Defaults()
	cfg.ReadOnly = true
	if err := r.Build(cfg); err != nil {
		t.Fatal(err)
	}
	en := r.Enabled()
	if len(en) != 1 || len(en[0].Actions) != 3 {
		t.Errorf("read-only: want only list,get,queue; got %+v", en)
	}
}

func TestListRepoScope(t *testing.T) {
	h := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Total-Count", "3")
		_, _ = w.Write([]byte(runnersList))
	}
	out, isErr, s := run(t, h, map[string]any{"action": "list"})
	if isErr {
		t.Fatal(out)
	}
	req := s.last(t)
	if req.Method != "GET" || req.Path != "/api/v1/repos/o/r/actions/runners" {
		t.Errorf("request = %+v", req)
	}
	if req.Query.Get("visible") != "true" || req.Query.Get("limit") == "" || req.Query.Get("page") != "1" {
		t.Errorf("query = %v", req.Query)
	}
	testutil.Golden(t, "list_repo", out)
}

func TestListFiltersAndVisibleFalse(t *testing.T) {
	h := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Total-Count", "3")
		_, _ = w.Write([]byte(runnersList))
	}
	out, isErr, s := run(t, h, map[string]any{"action": "list", "scope": "org", "org": "acme", "visible": false, "status": "online", "label": "docker"})
	if isErr {
		t.Fatal(out)
	}
	req := s.last(t)
	if req.Path != "/api/v1/orgs/acme/actions/runners" || req.Query.Get("visible") != "false" {
		t.Errorf("request = %+v", req)
	}
	testutil.Golden(t, "list_filtered", out)

	out, _, _ = run(t, h, map[string]any{"action": "list", "scope": "admin", "status": "offline"})
	if !strings.Contains(out, "runner 9 glob-1 offline | global") || strings.Contains(out, "runner 7") {
		t.Errorf("offline filter wrong:\n%s", out)
	}
	out, _, _ = run(t, h, map[string]any{"action": "list", "scope": "user", "status": "idle"})
	if strings.Count(out, "\nrunner ") != 1 || !strings.Contains(out, "runner 7 build-1 idle") {
		t.Errorf("idle filter wrong:\n%s", out)
	}
}

func TestListPaging(t *testing.T) {
	h := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Total-Count", "45")
		_, _ = w.Write([]byte(`[{"id":1,"name":"a","status":"idle"}]`))
	}
	out, _, s := run(t, h, map[string]any{"action": "list", "scope": "user", "page": 2, "limit": 20})
	req := s.last(t)
	if req.Path != "/api/v1/user/actions/runners" || req.Query.Get("page") != "2" || req.Query.Get("limit") != "20" {
		t.Errorf("request = %+v", req)
	}
	if !strings.HasPrefix(out, "# runners 1/45 page 2/3 next: page=3 | visible=true") {
		t.Errorf("header = %q", strings.SplitN(out, "\n", 2)[0])
	}
	out, _, _ = run(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Total-Count", "0")
		_, _ = w.Write([]byte(`[]`))
	}, map[string]any{"action": "list"})
	if out != "# runners 0/0 | visible=true" {
		t.Errorf("empty = %q", out)
	}
}

func TestGet(t *testing.T) {
	h := func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":7,"uuid":"3f2b0000-0000-4000-8000-000000000001","name":"build-1","status":"idle","labels":["docker:docker://node:20"],"version":"v6.3.1","repo_id":12,"description":"main builder","ephemeral":true}`))
	}
	out, isErr, s := run(t, h, map[string]any{"action": "get", "id": 7, "repo": "acme/proj"})
	if isErr {
		t.Fatal(out)
	}
	if req := s.last(t); req.Method != "GET" || req.Path != "/api/v1/repos/acme/proj/actions/runners/7" {
		t.Errorf("request = %+v", req)
	}
	testutil.Golden(t, "get", out)

	out, isErr, _ = run(t, reply(404, `{"message":"GetRunnerNotFound"}`), map[string]any{"action": "get", "id": 99, "scope": "admin"})
	if !isErr || !strings.HasPrefix(out, "error: 404") {
		t.Errorf("404 = %q", out)
	}
	out, isErr, _ = run(t, h, map[string]any{"action": "get"})
	if !isErr || !strings.Contains(out, "missing param id") {
		t.Errorf("missing id = %q", out)
	}
}

func TestRegister(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef01234567"
	h := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":7,"uuid":"3f2b0000-0000-4000-8000-000000000001","token":"` + token + `"}`))
	}
	// The debug loggers write to os.Stderr as of their construction, so swap
	// it for a pipe before the env (and its client) is built.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origErr := os.Stderr
	os.Stderr = pw
	captured := make(chan string)
	go func() {
		b, _ := io.ReadAll(pr)
		captured <- string(b)
	}()
	restored := false
	restore := func() {
		if !restored {
			restored = true
			os.Stderr = origErr
		}
	}
	defer restore()
	handler, seenReqs := fake(h)
	env := testutil.NewEnv(t, handler, testutil.WithToken("tok-secret-1234567890"), testutil.WithRepo("o/r"),
		testutil.WithConfig(func(c *config.Config) { c.Debug = true }))
	out, isErr := testutil.CallTool(t, env, spec(t), map[string]any{
		"action": "register", "name": "ci-1", "description": "d", "ephemeral": true, "labels": "docker:docker://node:20, host",
	})
	if isErr {
		t.Fatal(out)
	}
	req := seenReqs.last(t)
	if req.Method != "POST" || req.Path != "/api/v1/repos/o/r/actions/runners" {
		t.Errorf("request = %+v", req)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		t.Fatal(err)
	}
	if body["name"] != "ci-1" || body["description"] != "d" || body["ephemeral"] != true || len(body) != 3 {
		t.Errorf("body = %v", body)
	}
	restore()
	_ = pw.Close()
	logs := <-captured
	if !strings.Contains(logs, "http POST") {
		t.Errorf("no debug output captured, the check proves nothing: %q", logs)
	}
	if strings.Contains(logs, token) {
		t.Errorf("token leaked into the log: %s", logs)
	}
	if strings.Count(out, "token: ") != 1 {
		t.Errorf("token line must appear once:\n%s", out)
	}
	line, ok := ExtractRunnerFile(out)
	if !ok {
		t.Fatalf("no .runner line:\n%s", out)
	}
	var rf struct {
		ID      int64    `json:"id"`
		UUID    string   `json:"uuid"`
		Name    string   `json:"name"`
		Token   string   `json:"token"`
		Address string   `json:"address"`
		Labels  []string `json:"labels"`
	}
	if err := json.Unmarshal([]byte(line), &rf); err != nil {
		t.Fatalf(".runner is not JSON: %v: %s", err, line)
	}
	if rf.ID != 7 || rf.Name != "ci-1" || rf.Token != token || rf.Address != env.Client.BaseURL() ||
		len(rf.Labels) != 2 || rf.Labels[0] != "docker:docker://node:20" || rf.Labels[1] != "host" {
		t.Errorf("runner file = %+v", rf)
	}
	if line != RenderRunnerFile(7, rf.UUID, "ci-1", token, rf.Address, rf.Labels) {
		t.Errorf("tool output differs from RenderRunnerFile")
	}
	if strings.Contains(out, "pass labels=") {
		t.Errorf("labels were given, no hint expected")
	}
	golden := strings.ReplaceAll(out, env.Client.BaseURL(), "https://forgejo.example.com")
	testutil.Golden(t, "register", golden)

	// without labels: key omitted + hint, org scope text
	out, isErr = testutil.CallTool(t, env, spec(t), map[string]any{"action": "register", "name": "x", "scope": "org", "org": "acme"})
	if isErr {
		t.Fatal(out)
	}
	if req := seenReqs.last(t); req.Path != "/api/v1/orgs/acme/actions/runners" {
		t.Errorf("path = %s", req.Path)
	}
	line, _ = ExtractRunnerFile(out)
	if strings.Contains(line, "labels") || !strings.Contains(out, "note: pass labels=a,b to include labels in the snippet") ||
		!strings.Contains(out, `(org scope)`) {
		t.Errorf("no-label output wrong:\n%s", out)
	}
}

func TestRegisterErrorsAndHints(t *testing.T) {
	out, isErr, _ := run(t, reply(403, `{"message":""}`), map[string]any{"action": "register", "name": "x", "scope": "admin"})
	if !isErr || !strings.Contains(out, "needs: site admin") {
		t.Errorf("403 admin = %q", out)
	}
	out, isErr, _ = run(t, reply(403, `{"message":""}`), map[string]any{"action": "list"})
	if !isErr || !strings.Contains(out, "needs: repo owner") {
		t.Errorf("403 repo = %q", out)
	}
	out, isErr, _ = run(t, reply(201, `{}`), map[string]any{"action": "register", "scope": "repo"})
	if !isErr || !strings.Contains(out, "missing param name") {
		t.Errorf("missing name = %q", out)
	}
}

func TestDelete(t *testing.T) {
	out, isErr, s := run(t, reply(204, ``), map[string]any{"action": "delete", "id": 7, "scope": "user"})
	if isErr {
		t.Fatal(out)
	}
	if req := s.last(t); req.Method != "DELETE" || req.Path != "/api/v1/user/actions/runners/7" {
		t.Errorf("request = %+v", req)
	}
	if out != "ok: deleted runner 7 (user scope)" {
		t.Errorf("out = %q", out)
	}
	out, isErr, _ = run(t, reply(404, `{"message":"No permission to delete this runner"}`), map[string]any{"action": "delete", "id": 7})
	if !isErr || !strings.Contains(out, "No permission to delete this runner") || !strings.Contains(out, "hint: the runner belongs to another level") {
		t.Errorf("404 = %q", out)
	}
}

func TestQueue(t *testing.T) {
	h := func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
 {"id":5678,"name":"build","status":"waiting","runs_on":["docker"],"repo_id":12},
 {"id":5679,"name":"test","status":"waiting","runs_on":["docker"],"repo_id":12},
 {"id":5680,"name":"lint","status":"running","runs_on":["ubuntu"],"repo_id":13},
 {"id":5681,"name":"misc","status":"waiting","runs_on":[]}
]`))
	}
	out, isErr, s := run(t, h, map[string]any{"action": "queue", "labels": "docker,ubuntu", "scope": "admin"})
	if isErr {
		t.Fatal(out)
	}
	req := s.last(t)
	if req.Path != "/api/v1/admin/actions/runners/jobs" || req.Query.Get("labels") != "docker,ubuntu" {
		t.Errorf("request = %+v", req)
	}
	testutil.Golden(t, "queue", out)

	_, _, s = run(t, h, map[string]any{"action": "queue"})
	if q := s.last(t).Query; q.Has("labels") {
		t.Errorf("labels must be omitted: %v", q)
	}
	out, _, _ = run(t, reply(200, `[]`), map[string]any{"action": "queue"})
	if out != "waiting: 0, running: 0" {
		t.Errorf("empty queue = %q", out)
	}
}

func TestQueueCapsJobLines(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < 25; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"id":` + string(rune('0'+i%10)) + `,"name":"j","status":"waiting","runs_on":["docker"]}`)
	}
	sb.WriteString("]")
	out, _, _ := run(t, reply(200, sb.String()), map[string]any{"action": "queue"})
	if strings.Count(out, "\njob ") != 20 || !strings.Contains(out, "[truncated: 20 of 25 jobs") || !strings.HasPrefix(out, "waiting: 25, running: 0 | by runs-on: docker 25") {
		t.Errorf("cap wrong:\n%s", out)
	}
}

func TestRegToken(t *testing.T) {
	out, isErr, s := run(t, reply(200, `{"token":"REGTOKEN123"}`), map[string]any{"action": "reg_token", "scope": "org", "org": "acme"})
	if isErr {
		t.Fatal(out)
	}
	if req := s.last(t); req.Method != "GET" || req.Path != "/api/v1/orgs/acme/actions/runners/registration-token" {
		t.Errorf("request = %+v", req)
	}
	if out != "token: REGTOKEN123\nnote: deprecated; prefer register" {
		t.Errorf("out = %q", out)
	}
}

func TestScopeValidation(t *testing.T) {
	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"action": "list", "scope": "team"}, "invalid: scope"},
		{map[string]any{"action": "list", "scope": "org"}, "missing param org"},
		{map[string]any{"action": "list", "scope": "org", "org": "a/b"}, "invalid: org"},
		{map[string]any{"action": "list", "status": "weird"}, "invalid: status"},
		{map[string]any{"action": "list", "repo": "bad name/x"}, "error:"},
	}
	for _, c := range cases {
		out, isErr, s := run(t, reply(200, `[]`), c.args)
		if !isErr || !strings.Contains(out, c.want) {
			t.Errorf("%v -> %q, want %q", c.args, out, c.want)
		}
		if s.count() != 0 {
			t.Errorf("%v reached the server", c.args)
		}
	}
}

func TestRepoPathEscaping(t *testing.T) {
	_, _, s := run(t, reply(200, `[]`), map[string]any{"action": "list", "repo": "my-org/re.po_1"})
	if p := s.last(t).Path; p != "/api/v1/repos/my-org/re.po_1/actions/runners" {
		t.Errorf("path = %s", p)
	}
}

func TestRenderRunnerFile(t *testing.T) {
	got := RenderRunnerFile(3, "u-1", "r&1", "tok", "http://h:3000", []string{"a:b://c", "d"})
	want := `{"id":3,"uuid":"u-1","name":"r&1","token":"tok","address":"http://h:3000","labels":["a:b://c","d"]}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if g := RenderRunnerFile(3, "u", "n", "t", "a", nil); strings.Contains(g, "labels") {
		t.Errorf("labels key must be omitted: %s", g)
	}
	if _, ok := ExtractRunnerFile("no such line"); ok {
		t.Error("unexpected match")
	}
}

func TestQueueLabelsNormalised(t *testing.T) {
	for _, labels := range []any{"docker, ubuntu", " docker ,, ubuntu ", []any{"docker", " ubuntu"}} {
		_, isErr, s := run(t, reply(200, `[]`), map[string]any{"action": "queue", "labels": labels})
		if isErr {
			t.Fatalf("labels %v failed", labels)
		}
		if got := s.last(t).Query.Get("labels"); got != "docker,ubuntu" {
			t.Errorf("labels %v sent as %q", labels, got)
		}
	}
}

func TestQueueNullBody(t *testing.T) {
	out, isErr, _ := run(t, reply(200, `null`), map[string]any{"action": "queue"})
	if isErr || out != "waiting: 0, running: 0" {
		t.Errorf("null queue = %q (err %v)", out, isErr)
	}
}

func TestScopeDefaultsAndIgnoredNotes(t *testing.T) {
	out, isErr, s := run(t, reply(200, `[]`), map[string]any{"action": "list", "org": "acme"})
	if isErr || s.last(t).Path != "/api/v1/orgs/acme/actions/runners" {
		t.Errorf("org without scope: %q path %s", out, s.last(t).Path)
	}
	out, _, s = run(t, reply(200, `[]`), map[string]any{"action": "list", "scope": "admin", "repo": "a/b", "org": "x"})
	if s.last(t).Path != "/api/v1/admin/actions/runners" || !strings.HasSuffix(out, "note: ignored params: repo,org (scope=admin)") {
		t.Errorf("admin with repo+org: %q", out)
	}
	out, _, _ = run(t, reply(200, `[]`), map[string]any{"action": "list", "scope": "org", "org": "x", "repo": "a/b"})
	if !strings.HasSuffix(out, "note: ignored params: repo (scope=org)") {
		t.Errorf("org with repo: %q", out)
	}
}

func TestListFilterFetchesMorePages(t *testing.T) {
	h := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Total-Count", "2")
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`[{"id":2,"name":"on","status":"idle"}]`))
			return
		}
		_, _ = w.Write([]byte(`[{"id":1,"name":"off","status":"offline"}]`))
	}
	out, isErr, s := run(t, h, map[string]any{"action": "list", "status": "online", "limit": 1})
	if isErr {
		t.Fatal(out)
	}
	if s.count() != 2 || s.last(t).Query.Get("page") != "2" {
		t.Errorf("requests = %d, last %+v", s.count(), s.last(t))
	}
	if !strings.Contains(out, "runner 2 on idle") || strings.Contains(out, "runner 1") || !strings.Contains(out, "1 of 2 scanned") {
		t.Errorf("out:\n%s", out)
	}
}

func TestRunnerNameSanitised(t *testing.T) {
	body := `[{"id":3,"name":"evil\nrunner 99 x | y","status":"idle"}]`
	out, _, _ := run(t, reply(200, body), map[string]any{"action": "list"})
	if strings.Count(out, "\nrunner ") != 1 || strings.Contains(out, "|  y") || !strings.Contains(out, "runner 3 evil runner 99 x / y idle") {
		t.Errorf("list:\n%s", out)
	}
	out, _, _ = run(t, reply(200, body[1:len(body)-1]), map[string]any{"action": "get", "id": 3})
	if !strings.Contains(out, "name: evil runner 99 x / y") {
		t.Errorf("get:\n%s", out)
	}
}
