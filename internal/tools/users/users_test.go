package users

import (
	"reflect"
	"strings"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
)

// tcase is one fake-server test: the exact requests (minus /version and
// /settings/api) and the rendered output (golden) of one tool call.
type tcase struct {
	name   string // golden name
	tool   string
	args   map[string]any
	routes map[string]reply
	want   []string // requests, in order: "METHOD /path?sorted-query body"
	isErr  bool
}

const (
	userList = `[{"id":4,"login":"alice","full_name":"Alice A","is_admin":true,"active":true,"created":"2026-01-02T03:04:05Z"},{"id":5,"login":"bob","full_name":"","active":true,"created":"2026-02-03T04:05:06Z"}]`
	repoList = `[{"full_name":"o/a","description":"A repo with a description","private":false,"stars_count":12,"updated_at":"2026-11-20T10:15:00Z"},{"full_name":"o/b","private":true,"fork":true,"archived":true}]`
	orgList  = `[{"id":1,"name":"acme","username":"acme","full_name":"Acme Inc","visibility":"public","description":"We build things"},{"id":2,"name":"hid","visibility":"private"}]`
	teamJSON = `{"id":5,"name":"devs","description":"Developers","permission":"write","includes_all_repositories":false,"can_create_org_repo":true,"units_map":{"repo.code":"write","repo.issues":"read","repo.wiki":"none"},"organization":{"id":1,"name":"acme"}}`
)

func TestUserActions(t *testing.T) {
	cases := []tcase{
		{
			name: "user_me", tool: "user", args: map[string]any{"action": "me"},
			routes: map[string]reply{"GET /user": {body: meAdmin}},
			want:   []string{"GET /user"},
		},
		{
			name: "user_get", tool: "user", args: map[string]any{"action": "get", "user": "bob"},
			routes: map[string]reply{"GET /users/bob": {body: meUser}},
			want:   []string{"GET /users/bob"},
		},
		{
			name: "user_search", tool: "user", args: map[string]any{"action": "search", "q": "ali", "sort": "newest", "page": 2, "limit": 2},
			routes: map[string]reply{"GET /users/search": {body: `{"ok":true,"data":` + userList + `}`, total: "5"}},
			want:   []string{"GET /users/search?limit=2&page=2&q=ali&sort=newest"},
		},
		{
			name: "user_followers_me", tool: "user", args: map[string]any{"action": "followers"},
			routes: map[string]reply{"GET /user/followers": {body: userList, total: "2"}},
			want:   []string{"GET /user/followers?limit=20&page=1"},
		},
		{
			name: "user_followers_other", tool: "user", args: map[string]any{"action": "followers", "user": "bob"},
			routes: map[string]reply{"GET /users/bob/followers": {body: userList, total: "2"}},
			want:   []string{"GET /users/bob/followers?limit=20&page=1"},
		},
		{
			name: "user_following", tool: "user", args: map[string]any{"action": "following", "user": "bob"},
			routes: map[string]reply{"GET /users/bob/following": {body: `[]`, total: "0"}},
			want:   []string{"GET /users/bob/following?limit=20&page=1"},
		},
		{
			name: "user_starred", tool: "user", args: map[string]any{"action": "starred", "user": "bob"},
			routes: map[string]reply{"GET /users/bob/starred": {body: repoList, total: "2"}},
			want:   []string{"GET /users/bob/starred?limit=20&page=1"},
		},
		{
			name: "user_starred_me", tool: "user", args: map[string]any{"action": "starred"},
			routes: map[string]reply{"GET /user/starred": {body: repoList, total: "2"}},
			want:   []string{"GET /user/starred?limit=20&page=1"},
		},
		{
			name: "user_orgs", tool: "user", args: map[string]any{"action": "orgs"},
			routes: map[string]reply{"GET /user/orgs": {body: orgList, total: "2"}},
			want:   []string{"GET /user/orgs?limit=20&page=1"},
		},
		{
			name: "user_orgs_other", tool: "user", args: map[string]any{"action": "orgs", "user": "bob"},
			routes: map[string]reply{"GET /users/bob/orgs": {body: orgList, total: "2"}},
			want:   []string{"GET /users/bob/orgs?limit=20&page=1"},
		},
		{
			name: "user_keys_list", tool: "user", args: map[string]any{"action": "keys_list"},
			routes: map[string]reply{"GET /user/keys": {body: `[{"id":4,"title":"laptop","fingerprint":"SHA256:abc","read_only":true,"key":"ssh-ed25519 AAAA"},{"id":6,"title":"ci","fingerprint":"SHA256:def"}]`, total: "2"}},
			want:   []string{"GET /user/keys?limit=20&page=1"},
		},
		{
			name: "user_keys_add", tool: "user", args: map[string]any{"action": "keys_add", "title": "laptop", "key": " ssh-ed25519 AAAA ", "read_only": true},
			routes: map[string]reply{"POST /user/keys": {status: 201, body: `{"id":4,"title":"laptop","fingerprint":"SHA256:abc","read_only":true}`}},
			want:   []string{`POST /user/keys {"key":"ssh-ed25519 AAAA","read_only":true,"title":"laptop"}`},
		},
		{
			name: "user_keys_delete", tool: "user", args: map[string]any{"action": "keys_delete", "id": 4},
			routes: map[string]reply{"DELETE /user/keys/4": {status: 204}},
			want:   []string{"DELETE /user/keys/4"},
		},
		{
			name: "user_tokens_list", tool: "user", args: map[string]any{"action": "tokens_list"},
			routes: map[string]reply{
				"GET /user": {body: meAdmin},
				"GET /users/alice/tokens": {total: "2", body: `[{"id":3,"name":"ci","scopes":["read:repository","write:issue"],"token_last_eight":"a1b2c3d4","created_at":"2026-11-01T08:00:00Z","repositories":[{"id":1,"name":"r","owner":"o","full_name":"o/r"}]},
				{"id":4,"name":"all","scopes":["all"],"token_last_eight":"deadbeef","created_at":"2026-11-02T09:30:00Z","repositories":null}]`},
			},
			want: []string{"GET /user", "GET /users/alice/tokens?limit=20&page=1"},
		},
		{
			name: "user_tokens_create", tool: "user",
			args: map[string]any{"action": "tokens_create", "name": "ci", "scopes": []any{"read:repository", "write:issue"}, "repos": []any{"o/r"}},
			routes: map[string]reply{
				"GET /user":                      {body: meAdmin},
				"POST /admin/users/alice/tokens": {status: 201, body: `{"id":9,"name":"ci","sha1":"0123456789abcdef0123456789abcdef01234567","scopes":["read:repository","write:issue"],"token_last_eight":"01234567","repositories":[{"id":1,"name":"r","owner":"o","full_name":"o/r"}]}`},
			},
			want: []string{"GET /user", `POST /admin/users/alice/tokens {"name":"ci","repositories":[{"name":"r","owner":"o"}],"scopes":["read:repository","write:issue"]}`},
		},
		{
			name: "user_tokens_create_not_admin", tool: "user",
			args:   map[string]any{"action": "tokens_create", "name": "ci", "scopes": []any{"all"}},
			routes: map[string]reply{"GET /user": {body: meUser}},
			want:   []string{"GET /user"},
			isErr:  true,
		},
		{
			name: "user_tokens_create_forbidden", tool: "user",
			args: map[string]any{"action": "tokens_create", "user": "bob", "name": "ci", "scopes": []any{"public-only", "read:user"}},
			routes: map[string]reply{
				"GET /user":                    {body: meAdmin},
				"POST /admin/users/bob/tokens": {status: 403, body: `{"message":"user should be the site admin"}`},
			},
			want:  []string{"GET /user", `POST /admin/users/bob/tokens {"name":"ci","scopes":["public-only","read:user"]}`},
			isErr: true,
		},
		{
			name: "user_tokens_create_missing_scope", tool: "user",
			args: map[string]any{"action": "tokens_create", "name": "ci", "scopes": []any{"all"}},
			routes: map[string]reply{
				"GET /user":                      {body: meAdmin},
				"POST /admin/users/alice/tokens": {status: 403, body: `{"message":"token does not have at least one of required scope(s): [write:admin]"}`},
			},
			want:  []string{"GET /user", `POST /admin/users/alice/tokens {"name":"ci","scopes":["all"]}`},
			isErr: true,
		},
		{
			name: "user_tokens_create_bad_scope", tool: "user",
			args:  map[string]any{"action": "tokens_create", "name": "ci", "scopes": []any{"read:everything"}},
			isErr: true,
		},
		{
			name: "user_tokens_create_bad_repo", tool: "user",
			args:  map[string]any{"action": "tokens_create", "name": "ci", "scopes": []any{"all"}, "repos": []any{"justname"}},
			isErr: true,
		},
		{
			name: "user_tokens_delete", tool: "user", args: map[string]any{"action": "tokens_delete", "id": 9, "user": "bob"},
			routes: map[string]reply{
				"GET /user":                        {body: meAdmin},
				"DELETE /admin/users/bob/tokens/9": {status: 204},
			},
			want: []string{"GET /user", "DELETE /admin/users/bob/tokens/9"},
		},
		{
			name: "user_unknown_param_note", tool: "user", args: map[string]any{"action": "me", "q": "x"},
			routes: map[string]reply{"GET /user": {body: meUser}},
			want:   []string{"GET /user"},
		},
	}
	runCases(t, cases)
}

func TestOrgActions(t *testing.T) {
	cases := []tcase{
		{
			name: "org_list", tool: "org", args: map[string]any{"action": "list"},
			routes: map[string]reply{"GET /user/orgs": {body: orgList, total: "2"}},
			want:   []string{"GET /user/orgs?limit=20&page=1"},
		},
		{
			name: "org_list_all", tool: "org", args: map[string]any{"action": "list", "all": true, "limit": 1},
			routes: map[string]reply{"GET /orgs": {body: orgList[:strings.Index(orgList, "},")+1] + "]", total: "2"}},
			want:   []string{"GET /orgs?limit=1&page=1"},
		},
		{
			name: "org_get", tool: "org", args: map[string]any{"action": "get", "org": "acme"},
			routes: map[string]reply{"GET /orgs/acme": {body: `{"id":1,"name":"acme","username":"acme","full_name":"Acme Inc","visibility":"limited","description":"We build things","website":"https://acme.example","location":"Berlin","email":"hi@acme.example","created":"2026-03-04T05:06:07Z","avatar_url":"x"}`}},
			want:   []string{"GET /orgs/acme"},
		},
		{
			name: "org_create", tool: "org", args: map[string]any{"action": "create", "name": "acme", "full_name": "Acme Inc", "visibility": "limited"},
			routes: map[string]reply{"POST /orgs": {status: 201, body: `{"id":7,"name":"acme","visibility":"limited"}`}},
			want:   []string{`POST /orgs {"full_name":"Acme Inc","username":"acme","visibility":"limited"}`},
		},
		{
			name: "org_create_bad_visibility", tool: "org", args: map[string]any{"action": "create", "name": "acme", "visibility": "secret"},
			isErr: true,
		},
		{
			name: "org_edit", tool: "org", args: map[string]any{"action": "edit", "org": "acme", "description": "New", "website": "https://x.example"},
			routes: map[string]reply{"PATCH /orgs/acme": {body: `{"id":7}`}},
			want:   []string{`PATCH /orgs/acme {"description":"New","website":"https://x.example"}`},
		},
		{
			name: "org_edit_nothing", tool: "org", args: map[string]any{"action": "edit", "org": "acme"},
			isErr: true,
		},
		{
			name: "org_delete_needs_confirm", tool: "org", args: map[string]any{"action": "delete", "org": "acme"},
			isErr: true,
		},
		{
			name: "org_delete_wrong_confirm", tool: "org", args: map[string]any{"action": "delete", "org": "acme", "confirm": "other"},
			isErr: true,
		},
		{
			name: "org_delete", tool: "org", args: map[string]any{"action": "delete", "org": "acme", "confirm": "acme"},
			routes: map[string]reply{"DELETE /orgs/acme": {status: 204}},
			want:   []string{"DELETE /orgs/acme"},
		},
		{
			name: "org_delete_500", tool: "org", args: map[string]any{"action": "delete", "org": "acme", "confirm": "acme"},
			routes: map[string]reply{"DELETE /orgs/acme": {status: 500, body: `{}`}},
			want:   []string{"DELETE /orgs/acme"},
			isErr:  true,
		},
		{
			name: "org_rename", tool: "org", args: map[string]any{"action": "rename", "org": "acme", "new_name": "acme2"},
			routes: map[string]reply{"POST /orgs/acme/rename": {status: 204}},
			want:   []string{`POST /orgs/acme/rename {"new_name":"acme2"}`},
		},
		{
			name: "org_members", tool: "org", args: map[string]any{"action": "members", "org": "acme"},
			routes: map[string]reply{"GET /orgs/acme/members": {body: userList, total: "2"}},
			want:   []string{"GET /orgs/acme/members?limit=20&page=1"},
		},
		{
			name: "org_member_remove", tool: "org", args: map[string]any{"action": "member_remove", "org": "acme", "user": "bob"},
			routes: map[string]reply{"DELETE /orgs/acme/members/bob": {status: 204}},
			want:   []string{"DELETE /orgs/acme/members/bob"},
		},
		{
			name: "org_teams", tool: "org", args: map[string]any{"action": "teams", "org": "acme"},
			routes: map[string]reply{"GET /orgs/acme/teams": {body: `[` + teamJSON + `,{"id":6,"name":"all","permission":"admin","includes_all_repositories":true}]`, total: "2"}},
			want:   []string{"GET /orgs/acme/teams?limit=20&page=1"},
		},
		{
			name: "org_team_get", tool: "org", args: map[string]any{"action": "team_get", "team_id": 5},
			routes: map[string]reply{"GET /teams/5": {body: teamJSON}},
			want:   []string{"GET /teams/5"},
		},
		{
			name: "org_team_create_default_units", tool: "org", args: map[string]any{"action": "team_create", "org": "acme", "name": "devs", "description": "Devs", "includes_all_repositories": true},
			routes: map[string]reply{"POST /orgs/acme/teams": {status: 201, body: teamJSON}},
			want: []string{`POST /orgs/acme/teams {"description":"Devs","includes_all_repositories":true,"name":"devs","permission":"read","units_map":` +
				`{"repo.actions":"read","repo.code":"read","repo.issues":"read","repo.packages":"read","repo.projects":"read","repo.pulls":"read","repo.releases":"read","repo.wiki":"read"}}`},
		},
		{
			name: "org_team_create_units_map", tool: "org", args: map[string]any{"action": "team_create", "org": "acme", "name": "devs", "permission": "write", "units_map": map[string]any{"code": "write", "repo.issues": "Read"}},
			routes: map[string]reply{"POST /orgs/acme/teams": {status: 201, body: teamJSON}},
			want:   []string{`POST /orgs/acme/teams {"name":"devs","permission":"write","units_map":{"repo.code":"write","repo.issues":"read"}}`},
		},
		{
			name: "org_team_create_admin", tool: "org", args: map[string]any{"action": "team_create", "org": "acme", "name": "admins", "permission": "admin"},
			routes: map[string]reply{"POST /orgs/acme/teams": {status: 201, body: `{"id":8,"name":"admins","permission":"admin"}`}},
			want:   []string{`POST /orgs/acme/teams {"name":"admins","permission":"admin"}`},
		},
		{
			name: "org_team_create_bad_units", tool: "org", args: map[string]any{"action": "team_create", "org": "acme", "name": "x", "units_map": map[string]any{"code": "everything"}},
			isErr: true,
		},
		{
			name: "org_team_edit", tool: "org", args: map[string]any{"action": "team_edit", "team_id": 5, "description": "New", "includes_all_repositories": true},
			routes: map[string]reply{"GET /teams/5": {body: teamJSON}, "PATCH /teams/5": {body: teamJSON}},
			want: []string{
				"GET /teams/5",
				`PATCH /teams/5 {"description":"New","includes_all_repositories":true,"name":"devs","permission":"write"}`,
			},
		},
		{
			name: "org_team_edit_rename", tool: "org", args: map[string]any{"action": "team_edit", "team_id": 5, "name": "core", "permission": "admin"},
			routes: map[string]reply{"GET /teams/5": {body: teamJSON}, "PATCH /teams/5": {body: teamJSON}},
			want:   []string{"GET /teams/5", `PATCH /teams/5 {"name":"core","permission":"admin"}`},
		},
		{
			name: "org_team_edit_nothing", tool: "org", args: map[string]any{"action": "team_edit", "team_id": 5},
			isErr: true,
		},
		{
			name: "org_team_delete_wrong_confirm", tool: "org", args: map[string]any{"action": "team_delete", "team_id": 5, "confirm": "nope"},
			routes: map[string]reply{"GET /teams/5": {body: teamJSON}},
			want:   []string{"GET /teams/5"},
			isErr:  true,
		},
		{
			name: "org_team_delete", tool: "org", args: map[string]any{"action": "team_delete", "team_id": 5, "confirm": "devs"},
			routes: map[string]reply{"GET /teams/5": {body: teamJSON}, "DELETE /teams/5": {status: 204}},
			want:   []string{"GET /teams/5", "DELETE /teams/5"},
		},
		{
			name: "org_team_delete_by_id", tool: "org", args: map[string]any{"action": "team_delete", "team_id": 5, "confirm": "5"},
			routes: map[string]reply{"GET /teams/5": {body: teamJSON}, "DELETE /teams/5": {status: 204}},
			want:   []string{"GET /teams/5", "DELETE /teams/5"},
		},
		{
			name: "org_team_members", tool: "org", args: map[string]any{"action": "team_members", "team_id": 5},
			routes: map[string]reply{"GET /teams/5/members": {body: userList, total: "2"}},
			want:   []string{"GET /teams/5/members?limit=20&page=1"},
		},
		{
			name: "org_team_add_member", tool: "org", args: map[string]any{"action": "team_add_member", "team_id": 5, "user": "bob"},
			routes: map[string]reply{"PUT /teams/5/members/bob": {status: 204}, "GET /teams/5/members/bob": {body: meUser}},
			want:   []string{"PUT /teams/5/members/bob", "GET /teams/5/members/bob"},
		},
		{
			name: "org_team_add_member_invited", tool: "org", args: map[string]any{"action": "team_add_member", "team_id": 5, "user": "bob"},
			routes: map[string]reply{"PUT /teams/5/members/bob": {status: 204}, "GET /teams/5/members/bob": {status: 404, body: `{"message":"The target couldn't be found."}`}},
			want:   []string{"PUT /teams/5/members/bob", "GET /teams/5/members/bob"},
		},
		{
			name: "org_team_add_member_conflict", tool: "org", args: map[string]any{"action": "team_add_member", "team_id": 5, "user": "bob"},
			routes: map[string]reply{"PUT /teams/5/members/bob": {status: 409, body: `{"message":"team invite already exists [team_id: 5, email: bob@example.com]"}`}},
			want:   []string{"PUT /teams/5/members/bob"},
			isErr:  true,
		},
		{
			name: "org_team_remove_member", tool: "org", args: map[string]any{"action": "team_remove_member", "team_id": 5, "user": "bob"},
			routes: map[string]reply{"DELETE /teams/5/members/bob": {status: 204}},
			want:   []string{"DELETE /teams/5/members/bob"},
		},
		{
			name: "org_team_repos", tool: "org", args: map[string]any{"action": "team_repos", "team_id": 5},
			routes: map[string]reply{"GET /teams/5/repos": {body: repoList, total: "2"}},
			want:   []string{"GET /teams/5/repos?limit=20&page=1"},
		},
		{
			name: "org_team_add_repo", tool: "org", args: map[string]any{"action": "team_add_repo", "team_id": 5, "repo": "acme/web"},
			routes: map[string]reply{"PUT /teams/5/repos/acme/web": {status: 204}},
			want:   []string{"PUT /teams/5/repos/acme/web"},
		},
		{
			name: "org_team_remove_repo", tool: "org", args: map[string]any{"action": "team_remove_repo", "team_id": 5, "repo": "acme/web"},
			routes: map[string]reply{"DELETE /teams/5/repos/acme/web": {status: 204}},
			want:   []string{"DELETE /teams/5/repos/acme/web"},
		},
		{
			name: "org_team_add_repo_bad", tool: "org", args: map[string]any{"action": "team_add_repo", "team_id": 5, "repo": "web"},
			isErr: true,
		},
	}
	runCases(t, cases)
}

func TestNotifyActions(t *testing.T) {
	thread := `{"id":77,"unread":true,"pinned":false,"updated_at":"2026-11-22T09:30:00Z","url":"u","repository":{"full_name":"o/r","id":1},"subject":{"title":"Fix the login","url":"https://h/api/v1/repos/o/r/issues/12","html_url":"https://h/o/r/pulls/12","type":"Pull","state":"open"}}`
	list := `[` + thread + `,{"id":78,"unread":false,"pinned":true,"updated_at":"2026-11-21T08:00:00Z","repository":{"full_name":"o/r"},"subject":{"title":"Bug in parser","url":"https://h/api/v1/repos/o/r/issues/9","type":"Issue","state":"closed"}},{"id":79,"unread":true,"updated_at":"2026-11-20T07:00:00Z","repository":{"full_name":"o/x"},"subject":{"title":"o/x","type":"Repository"}}]`
	cases := []tcase{
		{
			name: "notify_count", tool: "notify", args: map[string]any{"action": "count"},
			routes: map[string]reply{"GET /notifications/new": {body: `{"new":3}`}},
			want:   []string{"GET /notifications/new"},
		},
		{
			name: "notify_list", tool: "notify", args: map[string]any{"action": "list"},
			routes: map[string]reply{"GET /notifications": {body: list, total: "3"}},
			want:   []string{"GET /notifications?limit=20&page=1&status-types=unread"},
		},
		{
			name: "notify_list_all_repo_types", tool: "notify",
			args:   map[string]any{"action": "list", "repo": "o/r", "status": "all", "types": []any{"issue", "pull"}, "since": "2026-11-01T00:00:00Z", "page": 2, "limit": 3},
			routes: map[string]reply{"GET /repos/o/r/notifications": {body: list, total: "9"}},
			want:   []string{"GET /repos/o/r/notifications?all=true&limit=3&page=2&since=2026-11-01T00%3A00%3A00Z&subject-type=issue&subject-type=pull"},
		},
		{
			name: "notify_list_empty", tool: "notify", args: map[string]any{"action": "list", "status": "pinned"},
			routes: map[string]reply{"GET /notifications": {body: `[]`, total: "0"}},
			want:   []string{"GET /notifications?limit=20&page=1&status-types=pinned"},
		},
		{
			name: "notify_list_bad_status", tool: "notify", args: map[string]any{"action": "list", "status": "new"},
			isErr: true,
		},
		{
			name: "notify_list_bad_type", tool: "notify", args: map[string]any{"action": "list", "types": []any{"commit"}},
			isErr: true,
		},
		{
			name: "notify_list_bad_repo", tool: "notify", args: map[string]any{"action": "list", "repo": "nope"},
			isErr: true,
		},
		{
			name: "notify_get", tool: "notify", args: map[string]any{"action": "get", "id": 77},
			routes: map[string]reply{"GET /notifications/threads/77": {body: thread}},
			want:   []string{"GET /notifications/threads/77"},
		},
		{
			name: "notify_mark_thread", tool: "notify", args: map[string]any{"action": "mark", "id": 77, "to": "pinned"},
			routes: map[string]reply{"PATCH /notifications/threads/77": {status: 205, body: thread}},
			want:   []string{"PATCH /notifications/threads/77?to-status=pinned"},
		},
		{
			name: "notify_mark_all", tool: "notify", args: map[string]any{"action": "mark"},
			routes: map[string]reply{"PUT /notifications": {status: 205, body: list}},
			want:   []string{"PUT /notifications?last_read_at=2026-11-22T10%3A00%3A00Z&status-types=unread&to-status=read"},
		},
		{
			name: "notify_mark_repo_unread", tool: "notify", args: map[string]any{"action": "mark", "repo": "o/r", "to": "unread"},
			routes: map[string]reply{"PUT /repos/o/r/notifications": {status: 205, body: `[]`}},
			want:   []string{"PUT /repos/o/r/notifications?last_read_at=2026-11-22T10%3A00%3A00Z&status-types=read&to-status=unread"},
		},
		{
			name: "notify_mark_bad_to", tool: "notify", args: map[string]any{"action": "mark", "to": "deleted"},
			isErr: true,
		},
	}
	runCases(t, cases)
}

func runCases(t *testing.T, cases []tcase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(tc.routes)
			got, isErr := run(t, f, tc.tool, tc.args)
			if isErr != tc.isErr {
				t.Fatalf("isErr = %v, want %v; output:\n%s", isErr, tc.isErr, got)
			}
			if reqs := f.requests(); !reflect.DeepEqual(reqs, append([]string(nil), tc.want...)) && !(len(reqs) == 0 && len(tc.want) == 0) {
				t.Errorf("requests:\n got %q\nwant %q", reqs, tc.want)
			}
			if strings.Contains(got, "tok-secret") {
				t.Errorf("token leaked into output: %s", got)
			}
			testutil.Golden(t, tc.name, got)
		})
	}
}
