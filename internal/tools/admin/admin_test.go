package admin

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/clock"
	"github.com/AndreyQuantum/forgejo-mcp/internal/config"
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
	block  chan struct{} // when set, POST /admin/cron/* waits for it
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
	block := f.block
	f.mu.Unlock()
	if block != nil && strings.HasPrefix(path, "/admin/cron/") {
		<-block
	}
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

func (f *fakeAPI) all() []reqRec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]reqRec(nil), f.reqs...)
}

func spec(t testing.TB) tools.Spec {
	t.Helper()
	r := tools.NewRegistry()
	registerAdmin(r)
	return r.Specs()[0]
}

// newEnv builds an Env with the admin toolset on.
func newEnv(t testing.TB, f http.Handler) *tools.Env {
	t.Helper()
	return testutil.NewEnv(t, f, testutil.WithToken("tkn-0123456789"), testutil.WithConfig(func(c *config.Config) {
		c.Toolsets = "admin"
	}))
}

const usersBody = `[
 {"id":1,"login":"root","full_name":"Root","email":"root@example.com","is_admin":true,"active":true,"login_name":"root","source_id":0,"created":"2026-01-02T03:04:05Z","avatar_url":"x"},
 {"id":2,"login":"bob","full_name":"","email":"bob@example.com","is_admin":false,"active":false,"restricted":true,"prohibit_login":true}
]`

const quotaBody = `{"used":{"size":{"repos":{"public":1500,"private":20},"git":{"LFS":0},
 "assets":{"attachments":{"issues":10,"releases":2000000},"artifacts":5,"packages":{"all":3000}}}},
 "groups":[{"name":"default","rules":[{"name":"all","limit":1000000000,"subjects":["size:all"]},{"name":"nolimit","limit":-1,"subjects":["size:repos:all","size:git:lfs"]}]}]}`

func TestAdminActions(t *testing.T) {
	cases := []struct {
		name   string
		args   map[string]any
		routes map[string]route
		want   []reqRec
		golden string
		isErr  bool
	}{
		{
			name:   "users_list",
			args:   map[string]any{"action": "users_list", "limit": 2, "login_name": "x", "source_id": 0},
			routes: map[string]route{"GET /admin/users": {body: usersBody, total: "3"}},
			want:   []reqRec{{"GET", "/admin/users", "limit=2&login_name=x&page=1&source_id=0", ""}},
			golden: "users_list",
		},
		{
			name:   "users_create",
			args:   map[string]any{"action": "users_create", "user": "alice", "email": "a@x.org", "password": "S3cret-pw!", "full_name": "Alice", "must_change_password": false},
			routes: map[string]route{"POST /admin/users": {status: 201, body: `{"id":5,"login":"alice"}`}},
			want: []reqRec{{
				"POST", "/admin/users", "",
				`{"email":"a@x.org","full_name":"Alice","must_change_password":false,"password":"S3cret-pw!","username":"alice"}`,
			}},
			golden: "users_create",
		},
		{
			name: "users_create admin follow-up",
			args: map[string]any{"action": "users_create", "user": "alice", "email": "a@x.org", "password": "S3cret-pw!", "admin": true, "visibility": "private"},
			routes: map[string]route{
				"POST /admin/users":        {status: 201, body: `{"id":5,"login":"alice"}`},
				"PATCH /admin/users/alice": {body: `{"id":5}`},
			},
			want: []reqRec{
				{"POST", "/admin/users", "", `{"email":"a@x.org","password":"S3cret-pw!","username":"alice","visibility":"private"}`},
				{"PATCH", "/admin/users/alice", "", `{"admin":true}`},
			},
			golden: "users_create_admin",
		},
		{
			name:   "users_create external",
			args:   map[string]any{"action": "users_create", "user": "ext", "email": "e@x.org", "login_name": "ext1", "source_id": 4},
			routes: map[string]route{"POST /admin/users": {status: 201, body: `{"id":6,"login":"ext"}`}},
			want:   []reqRec{{"POST", "/admin/users", "", `{"email":"e@x.org","login_name":"ext1","source_id":4,"username":"ext"}`}},
			golden: "users_create_ext",
		},
		{
			name:   "users_create without password",
			args:   map[string]any{"action": "users_create", "user": "alice", "email": "a@x.org"},
			golden: "users_create_nopw",
			isErr:  true,
		},
		{
			name:   "users_edit",
			args:   map[string]any{"action": "users_edit", "user": "alice", "email": "n@x.org", "admin": true, "active": false, "password": "N3w-secret!"},
			routes: map[string]route{"PATCH /admin/users/alice": {body: `{"id":5}`}},
			want:   []reqRec{{"PATCH", "/admin/users/alice", "", `{"active":false,"admin":true,"email":"n@x.org","password":"N3w-secret!"}`}},
			golden: "users_edit",
		},
		{
			name: "users_edit login_name fills source_id",
			args: map[string]any{"action": "users_edit", "user": "alice", "login_name": "ldap-alice"},
			routes: map[string]route{
				"GET /users/alice":         {body: `{"id":5,"login":"alice","login_name":"old","source_id":3}`},
				"PATCH /admin/users/alice": {body: `{"id":5}`},
			},
			want: []reqRec{
				{"GET", "/users/alice", "", ""},
				{"PATCH", "/admin/users/alice", "", `{"login_name":"ldap-alice","source_id":3}`},
			},
			golden: "users_edit_login",
		},
		{
			name: "users_edit source_id fills login_name",
			args: map[string]any{"action": "users_edit", "user": "alice", "source_id": 0},
			routes: map[string]route{
				"GET /users/alice":         {body: `{"id":5,"login":"alice","login_name":"alice","source_id":3}`},
				"PATCH /admin/users/alice": {body: `{"id":5}`},
			},
			want: []reqRec{
				{"GET", "/users/alice", "", ""},
				{"PATCH", "/admin/users/alice", "", `{"login_name":"alice","source_id":0}`},
			},
			golden: "users_edit_source",
		},
		{
			name:   "users_edit nothing",
			args:   map[string]any{"action": "users_edit", "user": "alice"},
			golden: "users_edit_nothing",
			isErr:  true,
		},
		{
			name:   "users_delete purge",
			args:   map[string]any{"action": "users_delete", "user": "bob", "confirm": "bob", "purge": true},
			routes: map[string]route{"DELETE /admin/users/bob": {status: 204}},
			want:   []reqRec{{"DELETE", "/admin/users/bob", "purge=true", ""}},
			golden: "users_delete",
		},
		{
			name:   "users_delete wrong confirm",
			args:   map[string]any{"action": "users_delete", "user": "bob", "confirm": "alice"},
			golden: "users_delete_wrong",
			isErr:  true,
		},
		{
			name:   "users_rename",
			args:   map[string]any{"action": "users_rename", "user": "bob", "new_name": "robert"},
			routes: map[string]route{"POST /admin/users/bob/rename": {status: 204}},
			want:   []reqRec{{"POST", "/admin/users/bob/rename", "", `{"new_username":"robert"}`}},
			golden: "users_rename",
		},
		{
			name: "orgs_list",
			args: map[string]any{"action": "orgs_list"},
			routes: map[string]route{"GET /admin/orgs": {total: "2", body: `[
 {"id":1,"name":"acme","visibility":"public","description":"Acme\nCorp tools","full_name":"Acme"},
 {"id":2,"name":"secret","visibility":"private"}]`}},
			want:   []reqRec{{"GET", "/admin/orgs", "limit=30&page=1", ""}},
			golden: "orgs_list",
		},
		{
			name: "emails_list",
			args: map[string]any{"action": "emails_list"},
			routes: map[string]route{"GET /admin/emails": {total: "2", body: `[
 {"email":"root@example.com","verified":true,"primary":true,"user_id":1,"username":"root"},
 {"email":"x@example.com","verified":false,"primary":false,"user_id":2,"username":"bob"}]`}},
			want:   []reqRec{{"GET", "/admin/emails", "limit=30&page=1", ""}},
			golden: "emails_list",
		},
		{
			name: "emails_search",
			args: map[string]any{"action": "emails_search", "q": "root"},
			routes: map[string]route{"GET /admin/emails/search": {total: "1", body: `[
 {"email":"root@example.com","verified":true,"primary":true,"user_id":1,"username":"root"}]`}},
			want:   []reqRec{{"GET", "/admin/emails/search", "limit=30&page=1&q=root", ""}},
			golden: "emails_search",
		},
		{
			name: "cron_list",
			args: map[string]any{"action": "cron_list"},
			routes: map[string]route{"GET /admin/cron": {total: "2", body: `[
 {"name":"update_mirrors","schedule":"@every 10m","next":"2026-11-22T10:10:00Z","prev":"0001-01-01T00:00:00Z","exec_times":4},
 {"name":"cleanup","schedule":"@midnight","next":"0001-01-01T00:00:00Z","exec_times":0}]`}},
			want:   []reqRec{{"GET", "/admin/cron", "limit=30&page=1", ""}},
			golden: "cron_list",
		},
		{
			name:   "cron_run quick",
			args:   map[string]any{"action": "cron_run", "task": "update_mirrors"},
			routes: map[string]route{"POST /admin/cron/update_mirrors": {status: 204}},
			want:   []reqRec{{"POST", "/admin/cron/update_mirrors", "", ""}},
			golden: "cron_run",
		},
		{
			name:   "unadopted_list",
			args:   map[string]any{"action": "unadopted_list", "pattern": "demo", "limit": 5},
			routes: map[string]route{"GET /admin/unadopted": {total: "2", body: `["alice/demo","bob/demo2"]`}},
			want:   []reqRec{{"GET", "/admin/unadopted", "limit=5&page=1&query=demo", ""}},
			golden: "unadopted_list",
		},
		{
			name:   "unadopted_adopt",
			args:   map[string]any{"action": "unadopted_adopt", "owner": "alice", "repo": "demo"},
			routes: map[string]route{"POST /admin/unadopted/alice/demo": {status: 204}},
			want:   []reqRec{{"POST", "/admin/unadopted/alice/demo", "", ""}},
			golden: "unadopted_adopt",
		},
		{
			name:   "unadopted_adopt owner/repo form",
			args:   map[string]any{"action": "unadopted_adopt", "owner": "alice", "repo": "alice/demo"},
			routes: map[string]route{"POST /admin/unadopted/alice/demo": {status: 204}},
			want:   []reqRec{{"POST", "/admin/unadopted/alice/demo", "", ""}},
			golden: "unadopted_adopt",
		},
		{
			name:   "unadopted_delete",
			args:   map[string]any{"action": "unadopted_delete", "owner": "alice", "repo": "demo", "confirm": "alice/demo"},
			routes: map[string]route{"DELETE /admin/unadopted/alice/demo": {status: 204}},
			want:   []reqRec{{"DELETE", "/admin/unadopted/alice/demo", "", ""}},
			golden: "unadopted_delete",
		},
		{
			name:   "quota_info me",
			args:   map[string]any{"action": "quota_info"},
			routes: map[string]route{"GET /user/quota": {body: quotaBody}},
			want:   []reqRec{{"GET", "/user/quota", "", ""}},
			golden: "quota_info",
		},
		{
			name:   "quota_info user",
			args:   map[string]any{"action": "quota_info", "user": "bob"},
			routes: map[string]route{"GET /admin/users/bob/quota": {body: quotaBody}},
			want:   []reqRec{{"GET", "/admin/users/bob/quota", "", ""}},
			golden: "quota_info_user",
		},
		{
			name:   "quota_info org",
			args:   map[string]any{"action": "quota_info", "org": "acme"},
			routes: map[string]route{"GET /orgs/acme/quota": {body: `{"used":{"size":{}},"groups":[]}`}},
			want:   []reqRec{{"GET", "/orgs/acme/quota", "", ""}},
			golden: "quota_info_org",
		},
		{
			name: "quota_groups",
			args: map[string]any{"action": "quota_groups"},
			routes: map[string]route{"GET /admin/quota/groups": {body: `[
 {"name":"default","rules":[{"name":"all","limit":1000000000,"subjects":["size:all"]}]},
 {"name":"empty","rules":[]}]`}},
			want:   []reqRec{{"GET", "/admin/quota/groups", "", ""}},
			golden: "quota_groups",
		},
		{
			name: "quota_groups one",
			args: map[string]any{"action": "quota_groups", "group": "default"},
			routes: map[string]route{"GET /admin/quota/groups/default": {body: `
 {"name":"default","rules":[{"name":"all","limit":1000000000,"subjects":["size:all"]}]}`}},
			want:   []reqRec{{"GET", "/admin/quota/groups/default", "", ""}},
			golden: "quota_group_one",
		},
		{
			name: "quota_rules",
			args: map[string]any{"action": "quota_rules"},
			routes: map[string]route{"GET /admin/quota/rules": {body: `[
 {"name":"all","limit":1000000000,"subjects":["size:all"]},{"name":"nolimit","limit":-1,"subjects":["size:repos:all"]}]`}},
			want:   []reqRec{{"GET", "/admin/quota/rules", "", ""}},
			golden: "quota_rules",
		},
		{
			name:   "quota_rules one",
			args:   map[string]any{"action": "quota_rules", "rule": "all"},
			routes: map[string]route{"GET /admin/quota/rules/all": {body: `{"name":"all","limit":1000000000,"subjects":["size:all"]}`}},
			want:   []reqRec{{"GET", "/admin/quota/rules/all", "", ""}},
			golden: "quota_rule_one",
		},
		{
			name: "quota_group_set creates and adds rule",
			args: map[string]any{"action": "quota_group_set", "group": "team", "rule": "all"},
			routes: map[string]route{
				"GET /user/quota":                        {body: `{}`},
				"POST /admin/quota/groups":               {status: 201, body: `{"name":"team","rules":[]}`},
				"PUT /admin/quota/groups/team/rules/all": {status: 204},
			},
			want: []reqRec{
				{"GET", "/admin/quota/groups/team", "", ""},
				{"GET", "/user/quota", "", ""},
				{"POST", "/admin/quota/groups", "", `{"name":"team"}`},
				{"PUT", "/admin/quota/groups/team/rules/all", "", ""},
			},
			golden: "quota_group_set",
		},
		{
			name: "quota_group_set existing group",
			args: map[string]any{"action": "quota_group_set", "group": "team"},
			routes: map[string]route{
				"GET /admin/quota/groups/team": {body: `{"name":"team","rules":[]}`},
			},
			want:   []reqRec{{"GET", "/admin/quota/groups/team", "", ""}},
			golden: "quota_group_set_exists",
		},
		{
			name:   "quota_group_delete",
			args:   map[string]any{"action": "quota_group_delete", "group": "team"},
			routes: map[string]route{"DELETE /admin/quota/groups/team": {status: 204}},
			want:   []reqRec{{"DELETE", "/admin/quota/groups/team", "", ""}},
			golden: "quota_group_delete",
		},
		{
			name:   "quota_group_delete rule only",
			args:   map[string]any{"action": "quota_group_delete", "group": "team", "rule": "all"},
			routes: map[string]route{"DELETE /admin/quota/groups/team/rules/all": {status: 201}},
			want:   []reqRec{{"DELETE", "/admin/quota/groups/team/rules/all", "", ""}},
			golden: "quota_group_delete_rule",
		},
		{
			name: "quota_rule_set creates",
			args: map[string]any{"action": "quota_rule_set", "rule": "small", "limit_bytes": 5000000, "subjects": []string{"size:repos:all"}},
			routes: map[string]route{
				"GET /user/quota":         {body: `{}`},
				"POST /admin/quota/rules": {status: 201, body: `{"name":"small"}`},
			},
			want: []reqRec{
				{"GET", "/admin/quota/rules/small", "", ""},
				{"GET", "/user/quota", "", ""},
				{"POST", "/admin/quota/rules", "", `{"limit":5000000,"name":"small","subjects":["size:repos:all"]}`},
			},
			golden: "quota_rule_set_create",
		},
		{
			name: "quota_rule_set updates",
			args: map[string]any{"action": "quota_rule_set", "rule": "small", "limit_bytes": -1},
			routes: map[string]route{
				"GET /admin/quota/rules/small":   {body: `{"name":"small","limit":1,"subjects":["size:all"]}`},
				"PATCH /admin/quota/rules/small": {body: `{"name":"small"}`},
			},
			want: []reqRec{
				{"GET", "/admin/quota/rules/small", "", ""},
				{"PATCH", "/admin/quota/rules/small", "", `{"limit":-1}`},
			},
			golden: "quota_rule_set_update",
		},
		{
			name: "quota_rule_set create needs limit and subjects",
			args: map[string]any{"action": "quota_rule_set", "rule": "small"},
			routes: map[string]route{
				"GET /user/quota": {body: `{}`},
			},
			want: []reqRec{
				{"GET", "/admin/quota/rules/small", "", ""},
				{"GET", "/user/quota", "", ""},
			},
			golden: "quota_rule_set_invalid",
			isErr:  true,
		},
		{
			name:   "quota_rule_delete",
			args:   map[string]any{"action": "quota_rule_delete", "rule": "small"},
			routes: map[string]route{"DELETE /admin/quota/rules/small": {status: 204}},
			want:   []reqRec{{"DELETE", "/admin/quota/rules/small", "", ""}},
			golden: "quota_rule_delete",
		},
		{
			name:   "quota_user_groups_set",
			args:   map[string]any{"action": "quota_user_groups_set", "user": "bob", "groups": []string{"default", "team"}},
			routes: map[string]route{"POST /admin/users/bob/quota/groups": {status: 204}},
			want:   []reqRec{{"POST", "/admin/users/bob/quota/groups", "", `{"groups":["default","team"]}`}},
			golden: "quota_user_groups_set",
		},
		{
			name:   "quota_user_groups_set clears",
			args:   map[string]any{"action": "quota_user_groups_set", "user": "bob", "groups": []string{}},
			routes: map[string]route{"POST /admin/users/bob/quota/groups": {status: 204}},
			want:   []reqRec{{"POST", "/admin/users/bob/quota/groups", "", `{"groups":[]}`}},
			golden: "quota_user_groups_clear",
		},
		{
			name:   "quota_user_groups_set needs groups",
			args:   map[string]any{"action": "quota_user_groups_set", "user": "bob"},
			golden: "quota_user_groups_missing",
			isErr:  true,
		},
		{
			name:   "quota_check ok",
			args:   map[string]any{"action": "quota_check", "subject": "size:repos:all"},
			routes: map[string]route{"GET /user/quota/check": {body: `true`}},
			want:   []reqRec{{"GET", "/user/quota/check", "subject=size%3Arepos%3Aall", ""}},
			golden: "quota_check_ok",
		},
		{
			name:   "quota_check org over",
			args:   map[string]any{"action": "quota_check", "org": "acme", "subject": "size:all"},
			routes: map[string]route{"GET /orgs/acme/quota/check": {body: `false`}},
			want:   []reqRec{{"GET", "/orgs/acme/quota/check", "subject=size%3Aall", ""}},
			golden: "quota_check_over",
		},
		{
			name:   "quota disabled",
			args:   map[string]any{"action": "quota_rules"},
			want:   []reqRec{{"GET", "/admin/quota/rules", "", ""}, {"GET", "/user/quota", "", ""}},
			golden: "quota_disabled",
			isErr:  true,
		},
		{
			name: "quota 404 on enabled server keeps the original error",
			args: map[string]any{"action": "quota_groups", "group": "nope"},
			routes: map[string]route{
				"GET /user/quota": {body: `{}`},
			},
			want:   []reqRec{{"GET", "/admin/quota/groups/nope", "", ""}, {"GET", "/user/quota", "", ""}},
			golden: "quota_group_404",
			isErr:  true,
		},
		{
			name:   "403 hint",
			args:   map[string]any{"action": "orgs_list"},
			routes: map[string]route{"GET /admin/orgs": {status: 403, body: `{"message":"You must be a site admin"}`}},
			want:   []reqRec{{"GET", "/admin/orgs", "limit=30&page=1", ""}},
			golden: "forbidden",
			isErr:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAPI{routes: tc.routes}
			env := newEnv(t, f)
			got, isErr := testutil.CallTool(t, env, spec(t), tc.args)
			if isErr != tc.isErr {
				t.Fatalf("isErr = %v, want %v: %s", isErr, tc.isErr, got)
			}
			if reqs := f.all(); !reflect.DeepEqual(reqs, append([]reqRec(nil), tc.want...)) && !(len(reqs) == 0 && len(tc.want) == 0) {
				t.Errorf("requests:\n got %+v\nwant %+v", reqs, tc.want)
			}
			for _, secret := range []string{"S3cret-pw!", "N3w-secret!"} {
				if strings.Contains(got, secret) {
					t.Errorf("output contains a password: %q", got)
				}
			}
			testutil.Golden(t, tc.golden, got)
		})
	}
}

func TestAdminDeleteConfirm(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"users_delete without confirm", map[string]any{"action": "users_delete", "user": "bob"}, "missing param confirm"},
		{"users_delete wrong confirm", map[string]any{"action": "users_delete", "user": "bob", "confirm": "BOB"}, `confirm="bob"`},
		{"unadopted_delete wrong confirm", map[string]any{"action": "unadopted_delete", "owner": "a", "repo": "b", "confirm": "b"}, `confirm="a/b"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAPI{}
			got, isErr := testutil.CallTool(t, newEnv(t, f), spec(t), tc.args)
			if !isErr || !strings.Contains(got, tc.want) {
				t.Fatalf("got %q (isErr %v), want error containing %q", got, isErr, tc.want)
			}
			if n := len(f.all()); n != 0 {
				t.Fatalf("server called %d times without a valid confirm", n)
			}
		})
	}
}

func TestAdminQuotaDisabledMessage(t *testing.T) {
	// Every quota action maps a 404 on a server without quota routes.
	for _, args := range []map[string]any{
		{"action": "quota_info"},
		{"action": "quota_groups"},
		{"action": "quota_rules"},
		{"action": "quota_group_set", "group": "g"},
		{"action": "quota_group_delete", "group": "g"},
		{"action": "quota_rule_set", "rule": "r", "limit_bytes": 1, "subjects": []string{"size:all"}},
		{"action": "quota_rule_delete", "rule": "r"},
		{"action": "quota_user_groups_set", "user": "u", "groups": []string{"g"}},
		{"action": "quota_check", "subject": "size:all"},
	} {
		got, isErr := testutil.CallTool(t, newEnv(t, &fakeAPI{}), spec(t), args)
		if !isErr || got != "error: quota is disabled on this server" {
			t.Errorf("%v: got %q (isErr %v)", args["action"], got, isErr)
		}
	}
}

func TestAdminEndpointsAndModes(t *testing.T) {
	s := spec(t)
	if len(s.Actions) != 22 {
		t.Fatalf("actions = %d, want 22", len(s.Actions))
	}
	wantMode := map[string]tools.Mode{
		"users_list": tools.Read, "users_create": tools.Write, "users_edit": tools.Write,
		"users_delete": tools.Destructive, "users_rename": tools.Write, "orgs_list": tools.Read,
		"emails_list": tools.Read, "emails_search": tools.Read, "cron_list": tools.Read, "cron_run": tools.Write,
		"unadopted_list": tools.Read, "unadopted_adopt": tools.Write, "unadopted_delete": tools.Destructive,
		"quota_info": tools.Read, "quota_groups": tools.Read, "quota_group_set": tools.Write,
		"quota_group_delete": tools.Write, "quota_rules": tools.Read, "quota_rule_set": tools.Write,
		"quota_rule_delete": tools.Write, "quota_user_groups_set": tools.Write, "quota_check": tools.Read,
	}
	for _, a := range s.Actions {
		if m, ok := wantMode[a.Name]; !ok || m != a.Mode {
			t.Errorf("action %s: mode %v, want %v (known %v)", a.Name, a.Mode, m, ok)
		}
		if len(a.Endpoints) == 0 || a.Summary == "" || a.Needs == "" {
			t.Errorf("action %s lacks endpoints, summary or needs", a.Name)
		}
	}
}

func TestAdminHiddenByDefaultAndInReadOnly(t *testing.T) {
	r := tools.NewRegistry()
	registerAdmin(r)
	cfg := config.Defaults()
	if err := r.Build(cfg); err != nil {
		t.Fatal(err)
	}
	if got := r.EnabledToolsets(); got["admin"] {
		t.Fatal("admin toolset is on by default")
	}
	// In read-only mode the write actions are refused.
	env := newEnv(t, &fakeAPI{})
	env.Cfg.ReadOnly = true
	got, isErr := testutil.CallTool(t, env, spec(t), map[string]any{"action": "users_create", "user": "a", "email": "a@x.org", "password": "x"})
	if !isErr || !strings.Contains(got, "is disabled (read-only mode)") {
		t.Fatalf("read-only create: %q (isErr %v)", got, isErr)
	}
}

func TestCronRunStillRunning(t *testing.T) {
	f := &fakeAPI{
		routes: map[string]route{"POST /admin/cron/update_mirrors": {status: 204}},
		block:  make(chan struct{}),
	}
	env := newEnv(t, f)
	fc := env.Clock.(*clock.Fake)
	t.Cleanup(func() { close(f.block) })

	type out struct {
		text  string
		isErr bool
	}
	done := make(chan out, 1)
	go func() {
		txt, isErr := testutil.CallTool(t, env, spec(t), map[string]any{"action": "cron_run", "task": "update_mirrors"})
		done <- out{txt, isErr}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !fc.BlockUntil(ctx, 2) {
		t.Fatal("cron_run never parked on its timers")
	}
	fc.Advance(cronProgress) // a progress tick, still waiting
	if !fc.BlockUntil(ctx, 2) {
		t.Fatal("cron_run did not re-arm its progress timer")
	}
	select {
	case o := <-done:
		t.Fatalf("returned before FORGEJO_WAIT_MAX: %q", o.text)
	default:
	}
	fc.Advance(env.Cfg.WaitMax) // past the deadline
	select {
	case o := <-done:
		if o.isErr || !strings.HasPrefix(o.text, "started: cron task update_mirrors; still running on the server; check with cron_list") {
			t.Fatalf("got %q (isErr %v)", o.text, o.isErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cron_run did not return after the wait deadline")
	}
}

func TestCronRunError(t *testing.T) {
	f := &fakeAPI{routes: map[string]route{}}
	got, isErr := testutil.CallTool(t, newEnv(t, f), spec(t), map[string]any{"action": "cron_run", "task": "nope"})
	if !isErr || !strings.Contains(got, "404") {
		t.Fatalf("got %q (isErr %v)", got, isErr)
	}
}

func TestUnadoptedOwnerPrefixConflict(t *testing.T) {
	f := &fakeAPI{routes: map[string]route{}}
	env := newEnv(t, f)
	got, isErr := testutil.CallTool(t, env, spec(t), map[string]any{"action": "unadopted_adopt", "owner": "a", "repo": "b/c"})
	if !isErr || !strings.Contains(got, "invalid: repo b/c does not belong to owner a") {
		t.Fatalf("got %q (isErr %v)", got, isErr)
	}
	if n := len(f.all()); n != 0 {
		t.Fatalf("conflicting owner reached the server (%d calls)", n)
	}
}

func TestUsersListSort(t *testing.T) {
	f := &fakeAPI{routes: map[string]route{"GET /admin/users": {body: usersBody, total: "2"}}}
	got, isErr := testutil.CallTool(t, newEnv(t, f), spec(t), map[string]any{"action": "users_list", "sort": "newest"})
	if isErr || !strings.HasPrefix(got, "# users ") {
		t.Fatalf("got %q (isErr %v)", got, isErr)
	}
	if reqs := f.all(); len(reqs) != 1 || !strings.Contains(reqs[0].Query, "sort=newest") {
		t.Fatalf("requests %+v", reqs)
	}
	if got, isErr = testutil.CallTool(t, newEnv(t, f), spec(t), map[string]any{"action": "users_list", "sort": "bogus"}); !isErr || !strings.Contains(got, "valid:") {
		t.Fatalf("bad sort: %q (isErr %v)", got, isErr)
	}
}

func TestAdminDoesNotShadowUserReads(t *testing.T) {
	r := tools.NewRegistry()
	registerAdmin(r)
	if err := r.Build(config.Defaults()); err != nil {
		t.Fatal(err)
	}
	if owner, ok := r.DeniedEndpoints()["GET /users/{username}"]; ok {
		t.Errorf("GET /users/{username} is denied via %s", owner)
	}
	// The quota GETs belong to the read actions only, never to a write action.
	for _, ep := range []string{"GET /users/{username}", "GET /admin/quota/groups/{quotagroup}", "GET /admin/quota/rules/{quotarule}"} {
		for _, g := range r.SideEffectGets() {
			if g == ep {
				t.Errorf("%s is listed as a side-effect GET", ep)
			}
		}
	}
}
