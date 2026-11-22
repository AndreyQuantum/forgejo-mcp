package settings

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
	"github.com/AndreyQuantum/forgejo-mcp/internal/endpoints"
	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// reqRec is one request seen by the fake server.
type reqRec struct {
	Method, Path, Query, Body string
}

type reply struct {
	Status int
	Body   string
	Total  string // X-Total-Count
}

// fake is a recording fake Forgejo keyed by "METHOD /api/v1/path".
type fake struct {
	mu     sync.Mutex
	routes map[string]reply
	reqs   []reqRec
}

func newFake(routes map[string]reply) *fake { return &fake{routes: routes} }

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.reqs = append(f.reqs, reqRec{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, string(b)})
	f.mu.Unlock()
	rep, ok := f.routes[r.Method+" "+r.URL.EscapedPath()]
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"not found"}`))
		return
	}
	if rep.Status == 0 {
		rep.Status = http.StatusOK
	}
	if rep.Total != "" {
		w.Header().Set("X-Total-Count", rep.Total)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(rep.Status)
	if rep.Body != "" {
		_, _ = w.Write([]byte(rep.Body))
	}
}

// writes returns the non-GET requests.
func (f *fake) writes() []reqRec {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []reqRec
	for _, r := range f.reqs {
		if r.Method != http.MethodGet {
			out = append(out, r)
		}
	}
	return out
}

func (f *fake) last() reqRec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

func spec(t testing.TB, name string) tools.Spec {
	t.Helper()
	r := tools.NewRegistry()
	Register(r)
	for _, s := range r.Specs() {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("tool %s not registered", name)
	return tools.Spec{}
}

func run(t testing.TB, f *fake, tool string, args map[string]any, opts ...testutil.EnvOption) (string, bool) {
	t.Helper()
	opts = append([]testutil.EnvOption{testutil.WithRepo("o/r"), testutil.WithToken("tok-secret-1234567890")}, opts...)
	env := testutil.NewEnv(t, f, opts...)
	return testutil.CallTool(t, env, spec(t, tool), args)
}

func mustJSON(t testing.TB, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("bad JSON %q: %v", s, err)
	}
	return m
}

const hookJSON = `{"id":2,"type":"forgejo","active":true,"events":["push","pull_request"],"branch_filter":"main",
"authorization_header":"Bearer SECRET-HEADER","config":{"url":"https://ci.example.com/hook","content_type":"json"},
"url":"https://ci.example.com/hook","content_type":"json","created_at":"2026-11-20T10:00:00Z","updated_at":"2026-11-21T11:30:00Z"}`

// ---- framework-level checks ---------------------------------------------

func TestSpecs(t *testing.T) {
	for _, name := range []string{"hook", "access"} {
		s := spec(t, name)
		if s.Toolset != "settings" || len(s.Description) > 160 || s.Single {
			t.Errorf("%s: bad spec %+v", name, s)
		}
		sc, err := tools.BuildInputSchema(s, s.Actions)
		if err != nil {
			t.Fatalf("%s schema: %v", name, err)
		}
		raw, _ := json.Marshal(sc)
		if len(raw) > tools.BudgetToolBytes {
			t.Errorf("%s schema is %d bytes", name, len(raw))
		}
		for _, a := range s.Actions {
			if len(a.Endpoints) == 0 || a.Summary == "" {
				t.Errorf("%s.%s: missing endpoints or summary", name, a.Name)
			}
			for _, ep := range a.Endpoints {
				sp := strings.SplitN(ep, " ", 2)
				if endpoints.Lookup(sp[0], sp[1]) == nil {
					t.Errorf("%s.%s: endpoint %q not in the index", name, a.Name, ep)
				}
			}
		}
	}
}

func TestReadOnlyHidesWrites(t *testing.T) {
	r := tools.NewRegistry()
	Register(r)
	cfg := config.Defaults()
	cfg.ReadOnly = true
	if err := r.Build(cfg); err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, s := range r.Enabled() {
		for _, a := range s.Actions {
			got[s.Name] = append(got[s.Name], a.Name)
		}
	}
	if strings.Join(got["hook"], ",") != "list,get" || strings.Join(got["access"], ",") != "list,permission" {
		t.Errorf("read-only actions = %v", got)
	}
}

func TestHelpMentionsRequired(t *testing.T) {
	s := spec(t, "hook")
	h := tools.HelpText(s, s.Actions, "")
	if !strings.Contains(h, "edit [W]") || !strings.Contains(h, "get [R] ") {
		t.Errorf("help:\n%s", h)
	}
}

// ---- hook ---------------------------------------------------------------

func TestHookListScopes(t *testing.T) {
	for _, tc := range []struct {
		args map[string]any
		path string
	}{
		{map[string]any{"action": "list"}, "/api/v1/repos/o/r/hooks"},
		{map[string]any{"action": "list", "scope": "org", "org": "acme"}, "/api/v1/orgs/acme/hooks"},
		{map[string]any{"action": "list", "scope": "user"}, "/api/v1/user/hooks"},
		{map[string]any{"action": "list", "scope": "admin"}, "/api/v1/admin/hooks"},
	} {
		f := newFake(map[string]reply{"GET " + tc.path: {Body: "[" + hookJSON + "]", Total: "1"}})
		text, isErr := run(t, f, "hook", tc.args)
		if isErr {
			t.Fatalf("%v: %s", tc.args, text)
		}
		last := f.last()
		if last.Path != tc.path || !strings.Contains(last.Query, "limit=") || !strings.Contains(last.Query, "page=1") {
			t.Errorf("%v: request %+v", tc.args, last)
		}
	}
}

func TestHookListInvalidRepo(t *testing.T) {
	f := newFake(nil)
	text, isErr := run(t, f, "hook", map[string]any{"action": "list", "scope": "repo", "repo": "x/y z"})
	if !isErr {
		t.Errorf("invalid repo accepted: %q", text)
	}
	f.mu.Lock()
	n := len(f.reqs)
	f.mu.Unlock()
	if n != 0 {
		t.Errorf("invalid repo reached the server: %d requests", n)
	}
}

func TestHookURLRedaction(t *testing.T) {
	for _, tc := range []struct{ typ, in, want string }{
		{"forgejo", "https://user:pw@ci.example.com/hook?token=abc", "https://***@ci.example.com/hook?***"},
		{"forgejo", "https://ci.example.com/hook", "https://ci.example.com/hook"},
		{"telegram", "https://api.telegram.org/bot123:AAA/sendMessage?chat_id=1", "https://api.telegram.org/***"},
		{"discord", "https://discord.com/api/webhooks/1/tok", "https://discord.com/***"},
	} {
		if got := safeHookURL(tc.typ, tc.in); got != tc.want {
			t.Errorf("safeHookURL(%s, %s) = %s, want %s", tc.typ, tc.in, got, tc.want)
		}
	}
	h := `{"id":4,"type":"forgejo","active":true,"events":["push"],"config":{"url":"https://u:pw@h.test/x"}}`
	f := newFake(map[string]reply{"GET /api/v1/repos/o/r/hooks/4": {Body: h}})
	text, _ := run(t, f, "hook", map[string]any{"action": "get", "id": 4})
	if strings.Contains(text, "pw") {
		t.Errorf("userinfo leaked:\n%s", text)
	}
}

func TestHookListGolden(t *testing.T) {
	second := `{"id":3,"type":"slack","active":false,"events":["issues"],"config":{"url":"https://hooks.slack.test/x","content_type":"form"}}`
	f := newFake(map[string]reply{"GET /api/v1/repos/o/r/hooks": {Body: "[" + hookJSON + "," + second + "]", Total: "2"}})
	text, isErr := run(t, f, "hook", map[string]any{"action": "list"})
	if isErr {
		t.Fatal(text)
	}
	if strings.Contains(text, "SECRET") {
		t.Errorf("secret leaked:\n%s", text)
	}
	testutil.Golden(t, "hook_list", text)
}

func TestHookListEmpty(t *testing.T) {
	f := newFake(map[string]reply{"GET /api/v1/orgs/acme/hooks": {Body: "[]", Total: "0"}})
	text, _ := run(t, f, "hook", map[string]any{"action": "list", "scope": "org", "org": "acme"})
	if text != "# hooks 0/0" {
		t.Errorf("got %q", text)
	}
}

func TestHookGetGolden(t *testing.T) {
	f := newFake(map[string]reply{"GET /api/v1/repos/o/r/hooks/2": {Body: hookJSON}})
	text, isErr := run(t, f, "hook", map[string]any{"action": "get", "id": 2})
	if isErr {
		t.Fatal(text)
	}
	if strings.Contains(text, "SECRET") {
		t.Errorf("authorization header leaked:\n%s", text)
	}
	testutil.Golden(t, "hook_get", text)
}

func TestHookCreateBodyDefaults(t *testing.T) {
	f := newFake(map[string]reply{"POST /api/v1/repos/o/r/hooks": {Status: 201, Body: hookJSON}})
	text, isErr := run(t, f, "hook", map[string]any{
		"action": "create", "url": "https://ci.example.com/hook", "secret": "s3cret",
	})
	if isErr {
		t.Fatal(text)
	}
	w := f.writes()
	if len(w) != 1 {
		t.Fatalf("writes: %+v", w)
	}
	got := mustJSON(t, w[0].Body)
	want := `{"type":"forgejo","active":true,"events":["push"],"config":{"url":"https://ci.example.com/hook","content_type":"json","secret":"s3cret"}}`
	if !jsonEqual(t, got, want) {
		t.Errorf("body = %s", w[0].Body)
	}
	if strings.Contains(text, "s3cret") {
		t.Errorf("secret echoed: %s", text)
	}
	testutil.Golden(t, "hook_create", text)
}

func TestHookCreateFull(t *testing.T) {
	f := newFake(map[string]reply{"POST /api/v1/orgs/acme/hooks": {Status: 201, Body: `{"id":9}`}})
	text, isErr := run(t, f, "hook", map[string]any{
		"action": "create", "scope": "org", "org": "acme", "type": "gitea", "url": "https://x.test/h",
		"content_type": "form", "events": []string{"issues", "release"}, "branch_filter": "release/*",
		"authorization_header": "Bearer abc", "active": false,
	})
	if isErr {
		t.Fatal(text)
	}
	got := mustJSON(t, f.writes()[0].Body)
	want := `{"type":"gitea","active":false,"events":["issues","release"],"branch_filter":"release/*",` +
		`"authorization_header":"Bearer abc","config":{"url":"https://x.test/h","content_type":"form"}}`
	if !jsonEqual(t, got, want) {
		t.Errorf("body = %s", f.writes()[0].Body)
	}
	if strings.Contains(text, "Bearer") {
		t.Errorf("header echoed: %s", text)
	}
	if !strings.HasPrefix(text, "ok: created hook 9 (org acme)") || !strings.HasSuffix(text, "(inactive)") {
		t.Errorf("text = %q", text)
	}
}

func TestHookCreateAdminIsSystemHook(t *testing.T) {
	f := newFake(map[string]reply{"POST /api/v1/admin/hooks": {Status: 201, Body: `{"id":1}`}})
	if text, isErr := run(t, f, "hook", map[string]any{"action": "create", "scope": "admin", "url": "http://x.test/h"}); isErr {
		t.Fatal(text)
	}
	want := `{"type":"forgejo","active":true,"events":["push"],"config":{"url":"http://x.test/h","content_type":"json","is_system_webhook":"true"}}`
	if !jsonEqual(t, mustJSON(t, f.writes()[0].Body), want) {
		t.Errorf("body = %s", f.writes()[0].Body)
	}
}

func TestHookCreateValidation(t *testing.T) {
	f := newFake(nil)
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"action": "create"}, "missing param url for hook.create"},
		{map[string]any{"action": "create", "url": "http://x", "type": "bogus"}, `invalid: type "bogus"`},
		{map[string]any{"action": "create", "url": "http://x", "content_type": "xml"}, `invalid: content_type "xml"`},
		{map[string]any{"action": "create", "url": "http://x", "scope": "org"}, "missing param org"},
		{map[string]any{"action": "create", "url": "http://x", "scope": "team"}, `invalid: scope "team"`},
	} {
		text, isErr := run(t, f, "hook", tc.args)
		if !isErr || !strings.Contains(text, tc.want) {
			t.Errorf("%v: got %q (err %v), want %q", tc.args, text, isErr, tc.want)
		}
	}
	if len(f.writes()) != 0 {
		t.Errorf("validation errors must not reach the server: %+v", f.writes())
	}
}

func TestHookEditReadModifyWrite(t *testing.T) {
	f := newFake(map[string]reply{
		"GET /api/v1/repos/o/r/hooks/2":   {Body: hookJSON},
		"PATCH /api/v1/repos/o/r/hooks/2": {Body: hookJSON},
	})
	text, isErr := run(t, f, "hook", map[string]any{"action": "edit", "id": 2, "url": "https://new.example.com/h"})
	if isErr {
		t.Fatal(text)
	}
	f.mu.Lock()
	order := []string{f.reqs[0].Method, f.reqs[1].Method}
	f.mu.Unlock()
	if order[0] != "GET" || order[1] != "PATCH" {
		t.Errorf("order = %v", order)
	}
	got := mustJSON(t, f.writes()[0].Body)
	// Everything not edited is sent back unchanged: events, filter, header, active, content type.
	want := `{"events":["push","pull_request_only"],"branch_filter":"main","authorization_header":"Bearer SECRET-HEADER",` +
		`"active":true,"config":{"url":"https://new.example.com/h","content_type":"json"}}`
	if !jsonEqual(t, got, want) {
		t.Errorf("body = %s", f.writes()[0].Body)
	}
	if strings.Contains(text, "SECRET") {
		t.Errorf("header echoed: %s", text)
	}
	if text != "ok: edited hook 2 (repo o/r): set url" {
		t.Errorf("text = %q", text)
	}
}

func TestHookEditNormalizesReviewEvents(t *testing.T) {
	cur := `{"id":2,"type":"forgejo","active":true,"events":["pull_request_review_approved","pull_request_review_rejected","pull_request_review_comment","issues"],` +
		`"config":{"url":"https://ci.example.com/hook","content_type":"json"}}`
	f := newFake(map[string]reply{
		"GET /api/v1/repos/o/r/hooks/2":   {Body: cur},
		"PATCH /api/v1/repos/o/r/hooks/2": {Body: cur},
	})
	text, isErr := run(t, f, "hook", map[string]any{"action": "edit", "id": 2, "url": "https://new.example.com/h"})
	if isErr || text != "ok: edited hook 2 (repo o/r): set url" {
		t.Fatalf("got %q", text)
	}
	want := `{"events":["pull_request_review","issues_only"],"branch_filter":"","authorization_header":"","active":true,` +
		`"config":{"url":"https://new.example.com/h","content_type":"json"}}`
	if !jsonEqual(t, mustJSON(t, f.writes()[0].Body), want) {
		t.Errorf("body = %s", f.writes()[0].Body)
	}
}

func TestHookEditEventMismatchNotes(t *testing.T) {
	cur := `{"id":2,"type":"forgejo","active":true,"events":["push","package"],"config":{"url":"https://x.test/h"}}`
	f := newFake(map[string]reply{
		"GET /api/v1/repos/o/r/hooks/2":   {Body: cur},
		"PATCH /api/v1/repos/o/r/hooks/2": {Body: `{"id":2,"events":["push","package"]}`},
	})
	text, isErr := run(t, f, "hook", map[string]any{"action": "edit", "id": 2, "events": []string{"push", "pull_request_review_approved"}})
	if isErr {
		t.Fatal(text)
	}
	if !strings.Contains(text, "note: package/action_run_*/workflow_* events cannot be changed by edit") ||
		!strings.Contains(text, "note: server did not apply events: pull_request_review") {
		t.Errorf("text = %q", text)
	}
}

func TestHookEditAdminScope(t *testing.T) {
	f := newFake(map[string]reply{
		"GET /api/v1/admin/hooks/7":   {Body: hookJSON},
		"PATCH /api/v1/admin/hooks/7": {Body: hookJSON},
	})
	text, isErr := run(t, f, "hook", map[string]any{"action": "edit", "scope": "admin", "id": 7, "active": false})
	if isErr || text != "ok: edited hook 7 (admin): set active" {
		t.Fatalf("got %q", text)
	}
	f.mu.Lock()
	m0, m1 := f.reqs[0].Method, f.reqs[1].Method
	f.mu.Unlock()
	if m0 != "GET" || m1 != "PATCH" || !strings.Contains(f.writes()[0].Body, `"active":false`) {
		t.Errorf("reqs %s %s body %s", m0, m1, f.writes()[0].Body)
	}
}

func TestHookEditRejectsEmptyAuthorizationHeader(t *testing.T) {
	f := newFake(map[string]reply{
		"GET /api/v1/repos/o/r/hooks/2":   {Body: hookJSON},
		"PATCH /api/v1/repos/o/r/hooks/2": {Body: hookJSON},
	})
	text, isErr := run(t, f, "hook", map[string]any{"action": "edit", "id": 2, "authorization_header": ""})
	if !isErr || !strings.Contains(text, "cannot be cleared via API") || len(f.writes()) != 0 {
		t.Fatalf("got %q (writes %d)", text, len(f.writes()))
	}
}

func TestHookCreateSlackRejected(t *testing.T) {
	f := newFake(nil)
	text, isErr := run(t, f, "hook", map[string]any{"action": "create", "type": "slack", "url": "https://hooks.slack.test/x"})
	if !isErr || !strings.Contains(text, "config.channel") || len(f.writes()) != 0 {
		t.Errorf("got %q", text)
	}
}

func TestHookEditFields(t *testing.T) {
	f := newFake(map[string]reply{
		"GET /api/v1/user/hooks/5":   {Body: hookJSON},
		"PATCH /api/v1/user/hooks/5": {Body: hookJSON},
	})
	text, isErr := run(t, f, "hook", map[string]any{
		"action": "edit", "scope": "user", "id": 5, "events": []string{"push"}, "active": false,
		"branch_filter": "", "authorization_header": "Bearer new", "content_type": "form",
	})
	if isErr {
		t.Fatal(text)
	}
	got := mustJSON(t, f.writes()[0].Body)
	want := `{"events":["push"],"branch_filter":"","authorization_header":"Bearer new","active":false,` +
		`"config":{"url":"https://ci.example.com/hook","content_type":"form"}}`
	if !jsonEqual(t, got, want) {
		t.Errorf("body = %s", f.writes()[0].Body)
	}
	if !strings.Contains(text, "set content_type,events,branch_filter,authorization_header,active") {
		t.Errorf("text = %q", text)
	}
}

func TestHookEditRejects(t *testing.T) {
	f := newFake(nil)
	text, isErr := run(t, f, "hook", map[string]any{"action": "edit", "id": 2, "secret": "new"})
	if !isErr || text != "error: invalid: hook secret cannot be changed via API; delete and recreate the hook" {
		t.Errorf("secret: %q", text)
	}
	text, isErr = run(t, f, "hook", map[string]any{"action": "edit", "id": 2})
	if !isErr || !strings.Contains(text, "nothing to change") {
		t.Errorf("empty edit: %q", text)
	}
	text, isErr = run(t, f, "hook", map[string]any{"action": "edit", "id": 2, "url": ""})
	if !isErr || !strings.Contains(text, "url must not be empty") {
		t.Errorf("empty url: %q", text)
	}
	f.mu.Lock()
	n := len(f.reqs)
	f.mu.Unlock()
	if n != 0 {
		t.Errorf("local rejections must not touch the server, saw %d requests", n)
	}
}

func TestHookEditMissingHook(t *testing.T) {
	f := newFake(nil)
	text, isErr := run(t, f, "hook", map[string]any{"action": "edit", "id": 99, "active": true})
	if !isErr || !strings.HasPrefix(text, "error: 404") {
		t.Errorf("got %q", text)
	}
	if len(f.writes()) != 0 {
		t.Error("PATCH sent for a missing hook")
	}
}

func TestHookDeleteAndTest(t *testing.T) {
	f := newFake(map[string]reply{
		"DELETE /api/v1/admin/hooks/4":         {Status: 204},
		"POST /api/v1/repos/o/r/hooks/2/tests": {Status: 204},
	})
	text, isErr := run(t, f, "hook", map[string]any{"action": "delete", "scope": "admin", "id": 4})
	if isErr || text != "ok: deleted hook 4 (admin)" {
		t.Errorf("delete: %q", text)
	}
	text, isErr = run(t, f, "hook", map[string]any{"action": "test", "id": 2, "ref": "refs/heads/dev"})
	if isErr || text != "ok: test queued (result is not visible via API)" {
		t.Errorf("test: %q", text)
	}
	if r := f.last(); r.Query != "ref=refs%2Fheads%2Fdev" {
		t.Errorf("test query = %q", r.Query)
	}
	text, isErr = run(t, f, "hook", map[string]any{"action": "test", "id": 2})
	if isErr || f.last().Query != "" {
		t.Errorf("test without ref: %q query %q", text, f.last().Query)
	}
	text, isErr = run(t, f, "hook", map[string]any{"action": "test", "id": 2, "scope": "org", "org": "a"})
	if !isErr || !strings.Contains(text, "scope=repo only") {
		t.Errorf("test org: %q", text)
	}
}

func TestHookMissingID(t *testing.T) {
	f := newFake(nil)
	text, isErr := run(t, f, "hook", map[string]any{"action": "get"})
	if !isErr || !strings.Contains(text, "missing param id for hook.get") {
		t.Errorf("got %q", text)
	}
}

func TestHookForbiddenHint(t *testing.T) {
	f := newFake(map[string]reply{"GET /api/v1/admin/hooks": {Status: 403, Body: `{"message":"forbidden"}`}})
	text, isErr := run(t, f, "hook", map[string]any{"action": "list", "scope": "admin"})
	if !isErr || !strings.Contains(text, "hint: needs: repo admin") {
		t.Errorf("got %q", text)
	}
}

// ---- access -------------------------------------------------------------

func TestAccessListCollaborators(t *testing.T) {
	f := newFake(map[string]reply{"GET /api/v1/repos/o/r/collaborators": {Body: `[{"login":"alice","id":1},{"login":"bob"}]`, Total: "2"}})
	text, isErr := run(t, f, "access", map[string]any{"action": "list", "kind": "collaborator"})
	if isErr {
		t.Fatal(text)
	}
	testutil.Golden(t, "access_list_collaborators", text)
}

func TestAccessListKeys(t *testing.T) {
	body := `[{"id":4,"key_id":77,"title":"deploy","fingerprint":"SHA256:abcd","read_only":true,"key":"ssh-ed25519 AAAA"},` +
		`{"id":5,"title":"rw key","fingerprint":"SHA256:efgh","read_only":false}]`
	f := newFake(map[string]reply{"GET /api/v1/repos/o/r/keys": {Body: body, Total: "2"}})
	text, isErr := run(t, f, "access", map[string]any{"action": "list", "kind": "key"})
	if isErr {
		t.Fatal(text)
	}
	if strings.Contains(text, "AAAA") || strings.Contains(text, "77") {
		t.Errorf("key material or key_id leaked:\n%s", text)
	}
	testutil.Golden(t, "access_list_keys", text)
}

func TestAccessListPaging(t *testing.T) {
	f := newFake(map[string]reply{"GET /api/v1/repos/o/r/collaborators": {Body: `[{"login":"a"},{"login":"b"}]`, Total: "5"}})
	text, _ := run(t, f, "access", map[string]any{"action": "list", "kind": "collaborator", "limit": 2, "page": 2})
	if !strings.HasPrefix(text, "# collaborators 2/5 page 2/3 next: page=3") {
		t.Errorf("header: %q", text)
	}
	if q := f.last().Query; !strings.Contains(q, "limit=2") || !strings.Contains(q, "page=2") {
		t.Errorf("query = %q", q)
	}
}

func TestAccessPermission(t *testing.T) {
	f := newFake(map[string]reply{
		"GET /api/v1/repos/o/r/collaborators/alice/permission": {Body: `{"permission":"write","role_name":"write","user":{"login":"alice"}}`},
	})
	text, isErr := run(t, f, "access", map[string]any{"action": "permission", "kind": "collaborator", "user": "alice"})
	if isErr || text != "alice: write" {
		t.Errorf("got %q", text)
	}
	f2 := newFake(map[string]reply{
		"GET /api/v1/repos/o/r/collaborators/bob/permission": {Body: `{"permission":"read","role_name":"custom"}`},
	})
	if text, _ := run(t, f2, "access", map[string]any{"action": "permission", "kind": "collaborator", "user": " bob "}); text != "bob: read (custom)" {
		t.Errorf("role: %q", text)
	}
	text, isErr = run(t, f, "access", map[string]any{"action": "permission", "kind": "collaborator", "user": "a/b"})
	if !isErr || !strings.Contains(text, `invalid: user "a/b"`) {
		t.Errorf("slash user: %q", text)
	}
	text, isErr = run(t, f, "access", map[string]any{"action": "permission", "kind": "key", "user": "alice"})
	if !isErr || !strings.Contains(text, "kind=collaborator only") {
		t.Errorf("key permission: %q", text)
	}
}

func TestAccessAddCollaborator(t *testing.T) {
	f := newFake(map[string]reply{"PUT /api/v1/repos/o/r/collaborators/alice": {Status: 204}})
	text, isErr := run(t, f, "access", map[string]any{"action": "add", "kind": "collaborator", "user": "alice"})
	if isErr || text != "ok: added collaborator alice (write) to o/r" {
		t.Errorf("got %q", text)
	}
	if !jsonEqual(t, mustJSON(t, f.writes()[0].Body), `{"permission":"write"}`) {
		t.Errorf("body = %s", f.writes()[0].Body)
	}
	text, _ = run(t, f, "access", map[string]any{"action": "add", "kind": "collaborator", "user": "alice", "permission": "admin"})
	if text != "ok: added collaborator alice (admin) to o/r" || f.writes()[1].Body != `{"permission":"admin"}` {
		t.Errorf("admin: %q %s", text, f.writes()[1].Body)
	}
	text, isErr = run(t, f, "access", map[string]any{"action": "add", "kind": "collaborator", "user": "alice", "permission": "owner"})
	if !isErr || !strings.Contains(text, `invalid: permission "owner"`) {
		t.Errorf("bad permission: %q", text)
	}
	text, isErr = run(t, f, "access", map[string]any{"action": "add", "kind": "collaborator"})
	if !isErr || !strings.Contains(text, "missing param user") {
		t.Errorf("missing user: %q", text)
	}
}

func TestAccessAddKeyDefaultsReadOnly(t *testing.T) {
	f := newFake(map[string]reply{"POST /api/v1/repos/o/r/keys": {Status: 201, Body: `{"id":8,"title":"ci","read_only":true}`}})
	text, isErr := run(t, f, "access", map[string]any{"action": "add", "kind": "key", "title": "ci", "key": " ssh-ed25519 AAAA ci\n"})
	if isErr || text != "ok: added deploy key 8 ci (ro) to o/r" {
		t.Errorf("got %q", text)
	}
	if !jsonEqual(t, mustJSON(t, f.writes()[0].Body), `{"title":"ci","key":"ssh-ed25519 AAAA ci","read_only":true}`) {
		t.Errorf("body = %s", f.writes()[0].Body)
	}
	f = newFake(map[string]reply{"POST /api/v1/repos/o/r/keys": {Status: 201, Body: `{"id":9}`}})
	text, _ = run(t, f, "access", map[string]any{"action": "add", "kind": "key", "title": "ci", "key": "ssh-ed25519 AAAA", "read_only": false})
	if text != "ok: added deploy key 9 ci (read-write) to o/r" || !strings.Contains(f.writes()[0].Body, `"read_only":false`) {
		t.Errorf("rw: %q %s", text, f.writes()[0].Body)
	}
	text, isErr = run(t, f, "access", map[string]any{"action": "add", "kind": "key", "title": "ci"})
	if !isErr || !strings.Contains(text, "missing param key for access.add kind=key") {
		t.Errorf("missing key: %q", text)
	}
}

func TestAccessRemove(t *testing.T) {
	f := newFake(map[string]reply{
		"DELETE /api/v1/repos/o/r/collaborators/alice": {Status: 204},
		"DELETE /api/v1/repos/o/r/keys/8":              {Status: 204},
	})
	text, isErr := run(t, f, "access", map[string]any{"action": "remove", "kind": "collaborator", "user": "alice"})
	if isErr || text != "ok: removed collaborator alice from o/r" {
		t.Errorf("collab: %q", text)
	}
	text, isErr = run(t, f, "access", map[string]any{"action": "remove", "kind": "key", "id": 8})
	if isErr || text != "ok: removed deploy key 8 from o/r" {
		t.Errorf("key: %q", text)
	}
	text, isErr = run(t, f, "access", map[string]any{"action": "remove", "kind": "key"})
	if !isErr || !strings.Contains(text, "missing param id") {
		t.Errorf("missing id: %q", text)
	}
}

func TestAccessKindValidation(t *testing.T) {
	f := newFake(nil)
	text, isErr := run(t, f, "access", map[string]any{"action": "list"})
	if !isErr || !strings.Contains(text, "missing param kind for access.list") {
		t.Errorf("missing kind: %q", text)
	}
	text, isErr = run(t, f, "access", map[string]any{"action": "list", "kind": "team"})
	if !isErr || !strings.Contains(text, `invalid: kind "team"`) {
		t.Errorf("bad kind: %q", text)
	}
}

func TestAccessEscapesUser(t *testing.T) {
	f := newFake(map[string]reply{"DELETE /api/v1/repos/o/r/collaborators/a%20b": {Status: 204}})
	text, isErr := run(t, f, "access", map[string]any{"action": "remove", "kind": "collaborator", "user": "a b"})
	if isErr {
		t.Errorf("got %q", text)
	}
	text, isErr = run(t, f, "access", map[string]any{"action": "remove", "kind": "collaborator", "user": "../x"})
	if !isErr {
		t.Errorf("path traversal accepted: %q", text)
	}
}

// jsonEqual compares got with a JSON literal.
func jsonEqual(t testing.TB, got map[string]any, want string) bool {
	t.Helper()
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(mustJSON(t, want))
	return string(a) == string(b)
}
