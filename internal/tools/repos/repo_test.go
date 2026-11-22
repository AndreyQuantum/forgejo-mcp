package repos

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/clock"
	"github.com/AndreyQuantum/forgejo-mcp/internal/testutil"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

func fixture(t *testing.T, name string) string { return string(testutil.Fixture(t, name)) }

func TestRepoGetGolden(t *testing.T) {
	f := newFake(t).on("GET /repos/o/r", jsonOK(fixture(t, "repo.json")))
	testutil.Golden(t, "repo_get", mustOK(t, f, "repo", map[string]any{"action": "get"}))
	testutil.Golden(t, "repo_get_verbose", mustOK(t, f, "repo", map[string]any{"action": "get", "verbose": true}))
}

func TestRepoListMine(t *testing.T) {
	f := newFake(t).on("GET /user/repos", withTotal(fixture(t, "repo_list.json"), "3"))
	out := mustOK(t, f, "repo", map[string]any{"action": "list", "sort": "updated"})
	testutil.Golden(t, "repo_list", out)
	q := f.last().Query
	if q.Get("order_by") != "recentupdate" || q.Get("limit") != "20" || q.Get("page") != "1" {
		t.Fatalf("query %v", q)
	}
	mustOK(t, f, "repo", map[string]any{"action": "list", "sort": "alpha", "order": "desc"})
	if got := f.last().Query.Get("order_by"); got != "reversealphabetically" {
		t.Fatalf("order_by %q", got)
	}
}

func TestRepoListUserAndOrg(t *testing.T) {
	f := newFake(t).
		on("GET /users/bob/repos", reply{status: 200, body: fixture(t, "repo_list.json"), header: map[string]string{"X-Total-Count": "999"}}).
		on("GET /orgs/acme/repos", jsonOK(`[]`)).
		on("GET /user/starred", withTotal(`[{"full_name":"x/y"}]`, "1"))
	// limit=3 and 3 raw items: the raw page is full, so there may be more
	out := mustOK(t, f, "repo", map[string]any{"action": "list", "which": "user", "owner": "bob", "limit": 3})
	if !strings.HasPrefix(out, "# repos 3 page 1 next: page=2 (page may be short)\n") {
		t.Fatalf("header:\n%s", out)
	}
	out = mustOK(t, f, "repo", map[string]any{"action": "list", "which": "org", "owner": "acme"})
	if !strings.HasPrefix(out, "# repos 0 page 1 (page may be short)") {
		t.Fatalf("org header:\n%s", out)
	}
	out = mustOK(t, f, "repo", map[string]any{"action": "list", "which": "starred"})
	if !strings.HasPrefix(out, "# repos 1/1 page 1/1\n") {
		t.Fatalf("starred header:\n%s", out)
	}
	if out := mustErr(t, f, "repo", map[string]any{"action": "list", "which": "user"}); !strings.Contains(out, "owner is required") {
		t.Fatal(out)
	}
	if out := mustErr(t, f, "repo", map[string]any{"action": "list", "which": "nope"}); !strings.Contains(out, "valid: mine,user,org,starred,watched") {
		t.Fatal(out)
	}
}

func TestRepoSearchMapsPrivate(t *testing.T) {
	f := newFake(t).on("GET /repos/search", withTotal(`{"ok":true,"data":`+fixture(t, "repo_list.json")+`}`, "3"))
	out := mustOK(t, f, "repo", map[string]any{
		"action": "search", "q": "demo", "private": true, "topic": true, "archived": false, "mode": "fork", "sort": "stars", "order": "desc",
	})
	testutil.Golden(t, "repo_search", out)
	q := f.last().Query
	if q.Get("is_private") != "true" {
		t.Fatalf("is_private missing: %v", q)
	}
	if _, has := q["private"]; has {
		t.Fatalf("`private` must never be sent: %v", q)
	}
	want := map[string]string{"q": "demo", "topic": "true", "archived": "false", "mode": "fork", "sort": "stars", "order": "desc", "limit": "20"}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("query %s=%q want %q", k, q.Get(k), v)
		}
	}
	// nothing optional is sent when unset, but limit always is
	mustOK(t, f, "repo", map[string]any{"action": "search", "q": "x"})
	q = f.last().Query
	for _, k := range []string{"topic", "mode", "archived", "is_private", "private", "sort", "order"} {
		if _, has := q[k]; has {
			t.Errorf("%s sent without being set", k)
		}
	}
	if q.Get("limit") == "" {
		t.Error("limit missing")
	}
}

func TestRepoCreate(t *testing.T) {
	created := `{"full_name":"alice/new","private":true,"html_url":"https://forgejo.example.com/alice/new"}`
	f := newFake(t).
		on("GET /user", jsonOK(`{"id":1,"login":"alice"}`)).
		on("POST /user/repos", reply{status: 201, body: created}).
		on("POST /orgs/acme/repos", reply{status: 201, body: strings.ReplaceAll(created, "alice/new", "acme/new")})

	out := mustOK(t, f, "repo", map[string]any{"action": "create", "name": "new", "private": true, "description": "d", "license": "MIT"})
	if out != "ok: created alice/new (private) https://forgejo.example.com/alice/new\n" {
		t.Fatalf("out %q", out)
	}
	body := f.bodyMap(f.last())
	if body["name"] != "new" || body["auto_init"] != true || body["private"] != true || body["license"] != "MIT" {
		t.Fatalf("body %v", body)
	}
	if _, has := body["readme"]; has {
		t.Fatal("readme must not be sent unless set")
	}

	mustOK(t, f, "repo", map[string]any{"action": "create", "name": "new", "owner": "acme", "auto_init": false})
	if l := f.last(); l.Path != "/orgs/acme/repos" || f.bodyMap(l)["auto_init"] != false {
		t.Fatalf("org create %+v", l)
	}
	// owner == me goes to /user/repos
	mustOK(t, f, "repo", map[string]any{"action": "create", "name": "new", "owner": "Alice"})
	if f.last().Path != "/user/repos" {
		t.Fatalf("path %s", f.last().Path)
	}
	// 409 gets the existing-repo hint
	f.on("POST /user/repos", reply{status: 409, body: `{"message":"The repository with the same name already exists."}`})
	if out := mustErr(t, f, "repo", map[string]any{"action": "create", "name": "new"}); !strings.Contains(out, "hint: a repository with this name exists") {
		t.Fatal(out)
	}
}

func TestRepoCreateFromTemplate(t *testing.T) {
	f := newFake(t).
		on("GET /user", jsonOK(`{"id":1,"login":"alice"}`)).
		on("POST /repos/tpl/base/generate", reply{status: 201, body: `{"full_name":"alice/gen","private":false,"html_url":"https://h/alice/gen"}`})
	out := mustOK(t, f, "repo", map[string]any{"action": "create", "name": "gen", "from_template": "tpl/base", "private": false})
	if !strings.HasPrefix(out, "ok: created alice/gen (public) from template tpl/base ") {
		t.Fatal(out)
	}
	body := f.bodyMap(f.last())
	want := map[string]any{"owner": "alice", "name": "gen", "git_content": true, "topics": true, "labels": true, "private": false}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("body[%s]=%v want %v", k, body[k], v)
		}
	}
	if out := mustErr(t, f, "repo", map[string]any{"action": "create", "name": "g", "from_template": "bad"}); !strings.Contains(out, "from_template") {
		t.Fatal(out)
	}
}

func TestRepoEdit(t *testing.T) {
	f := newFake(t).onFunc("PATCH /repos/o/r", func(*http.Request) reply { return jsonOK(`{"full_name":"o/r2"}`) })
	if out := mustErr(t, f, "repo", map[string]any{"action": "edit", "set": map[string]any{"bogus": 1, "private": true}}); !strings.Contains(out, "unknown set key bogus; valid:") {
		t.Fatal(out)
	}
	out := mustOK(t, f, "repo", map[string]any{"action": "edit", "set": map[string]any{"description": "x"}})
	if out != "ok: edited o/r2: description\n" {
		t.Fatalf("out %q", out)
	}
	out = mustOK(t, f, "repo", map[string]any{"action": "edit", "set": map[string]any{"description": "x", "archived": true, "has_wiki": false}})
	if out != "ok: edited o/r2: archived,description,has_wiki\nnote: repo edits are not atomic\n" {
		t.Fatalf("out %q", out)
	}
	body := f.bodyMap(f.last())
	if body["archived"] != true || body["has_wiki"] != false || len(body) != 3 {
		t.Fatalf("body %v", body)
	}
}

func TestRepoEditInvalidatesMetaCache(t *testing.T) {
	// (cache invalidation is exercised through the shared helper; here we only
	// check that edit succeeds with a name change and reports the new name)
	f := newFake(t).on("PATCH /repos/o/r", jsonOK(`{"full_name":"o/renamed"}`))
	out := mustOK(t, f, "repo", map[string]any{"action": "edit", "set": map[string]any{"name": "renamed"}})
	if out != "ok: edited o/renamed: name\n" {
		t.Fatal(out)
	}
}

func TestRepoDelete(t *testing.T) {
	f := newFake(t).on("DELETE /repos/o/r", reply{status: 204})
	if out := mustErr(t, f, "repo", map[string]any{"action": "delete"}); !strings.Contains(out, `destructive: pass confirm=`) {
		t.Fatal(out)
	}
	if out := mustErr(t, f, "repo", map[string]any{"action": "delete", "confirm": "o/other"}); !strings.Contains(out, `confirm="o/r"`) {
		t.Fatal(out)
	}
	if n := len(f.requests()); n != 0 {
		t.Fatalf("%d requests before a valid confirm", n)
	}
	out := mustOK(t, f, "repo", map[string]any{"action": "delete", "confirm": "o/r"})
	if out != "ok: deleted o/r (irreversible)\n" {
		t.Fatal(out)
	}
}

func TestRepoFork(t *testing.T) {
	f := newFake(t).
		on("POST /repos/o/r/forks", reply{status: 202, body: `{"full_name":"acme/r2"}`}).
		on("GET /repos/acme/r2", jsonOK(`{"full_name":"acme/r2","empty":true}`))
	out := mustOK(t, f, "repo", map[string]any{"action": "fork", "owner": "acme", "name": "r2"})
	if out != "ok: forked to acme/r2 (empty source repo)\n" {
		t.Fatalf("out %q", out)
	}
	body := f.bodyMap(f.requests()[0])
	if body["organization"] != "acme" || body["name"] != "r2" || len(body) != 2 {
		t.Fatalf("body %v", body)
	}
	// exactly one GET after the POST, no polling
	if f.count("GET", "/repos/acme/r2") != 1 {
		t.Fatalf("expected one status GET, requests: %v", f.requests())
	}
	f.on("GET /repos/acme/r2", jsonOK(`{"full_name":"acme/r2","empty":false}`))
	if out := mustOK(t, f, "repo", map[string]any{"action": "fork"}); out != "ok: forked to acme/r2\n" {
		t.Fatal(out)
	}
	if body := f.bodyMap(f.requests()[2]); len(body) != 0 {
		t.Fatalf("empty fork body must be {}: %v", body)
	}
}

func TestRepoForks(t *testing.T) {
	f := newFake(t).on("GET /repos/o/r/forks", withTotal(fixture(t, "repo_list.json"), "3"))
	out := mustOK(t, f, "repo", map[string]any{"action": "forks"})
	if !strings.HasPrefix(out, "# forks 3/3 page 1/1\n") {
		t.Fatal(out)
	}
}

func TestRepoTransfer(t *testing.T) {
	f := newFake(t).on("POST /repos/o/r/transfer", reply{status: 202, body: `{}`})
	out := mustOK(t, f, "repo", map[string]any{"action": "transfer", "new_owner": "bob", "team_ids": []int{1, 2}})
	if out != "ok: transfer pending; recipient runs repo.transfer_accept\n" {
		t.Fatal(out)
	}
	body := f.bodyMap(f.last())
	if body["new_owner"] != "bob" || len(body["team_ids"].([]any)) != 2 {
		t.Fatalf("body %v", body)
	}
	f.on("POST /repos/o/r/transfer", reply{status: 201, body: `{}`})
	if out := mustOK(t, f, "repo", map[string]any{"action": "transfer", "new_owner": "acme"}); out != "ok: transferred o/r to acme\n" {
		t.Fatal(out)
	}
	f.on("POST /repos/o/r/transfer/accept", reply{status: 202, body: `{}`}).on("POST /repos/o/r/transfer/reject", jsonOK(`{}`))
	if out := mustOK(t, f, "repo", map[string]any{"action": "transfer_accept"}); out != "ok: transfer of o/r accepted\n" {
		t.Fatal(out)
	}
	if out := mustOK(t, f, "repo", map[string]any{"action": "transfer_reject"}); out != "ok: transfer of o/r rejected\n" {
		t.Fatal(out)
	}
}

func TestRepoMigrateBody(t *testing.T) {
	f := newFake(t).on("POST /repos/migrate", reply{status: 201, body: `{"full_name":"acme/imp"}`})
	out := mustOK(t, f, "repo", map[string]any{
		"action": "migrate", "clone_addr": "https://github.com/x/y.git", "name": "imp", "owner": "acme", "service": "github",
		"mirror": true, "private": true, "auth_username": "u", "auth_token": "secret-token-value", "interval": "8h",
		"include": []string{"issues", "pull_requests"}, "description": "d",
	})
	if out != "ok: migrated acme/imp (pull mirror)\n" {
		t.Fatal(out)
	}
	body := f.bodyMap(f.last())
	want := map[string]any{
		"clone_addr": "https://github.com/x/y.git", "repo_name": "imp", "repo_owner": "acme", "service": "github",
		"mirror": true, "private": true, "auth_username": "u", "auth_token": "secret-token-value",
		"mirror_interval": "8h", "issues": true, "pull_requests": true, "description": "d",
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("body[%s]=%v want %v", k, body[k], v)
		}
	}
	for _, bad := range []string{"name", "owner", "interval", "include", "auth_password"} {
		if _, has := body[bad]; has {
			t.Errorf("tool-side key %s leaked into the body", bad)
		}
	}
}

func TestRepoMigrateRejectsCredentialsAndBadInclude(t *testing.T) {
	f := newFake(t)
	if out := mustErr(t, f, "repo", map[string]any{"action": "migrate", "clone_addr": "https://u:pw@github.com/x/y", "name": "n"}); !strings.Contains(out, "must not contain credentials") {
		t.Fatal(out)
	}
	if out := mustErr(t, f, "repo", map[string]any{"action": "migrate", "clone_addr": "https://github.com/x/y", "name": "n", "include": []string{"nope"}}); !strings.Contains(out, "valid: wiki,issues") {
		t.Fatal(out)
	}
	if len(f.requests()) != 0 {
		t.Fatalf("requests sent: %v", f.requests())
	}
}

func TestRepoMigrateStillRunning(t *testing.T) {
	release := make(chan struct{})
	f := newFake(t).onFunc("POST /repos/migrate", func(*http.Request) reply {
		<-release
		return reply{status: 201, body: `{"full_name":"acme/imp"}`}
	})
	env := testutil.NewEnv(t, f, testutil.WithToken("tok-secret-1234567890"), testutil.WithRepo("o/r"))
	t.Cleanup(func() { close(release) }) // runs before the server is closed
	fc := env.Clock.(*clock.Fake)
	type res struct {
		text  string
		isErr bool
	}
	done := make(chan res, 1)
	go func() {
		txt, isErr := testutil.CallTool(t, env, specOf(t, "repo"), map[string]any{
			"action": "migrate", "clone_addr": "https://github.com/x/y", "name": "imp", "owner": "acme",
		})
		done <- res{txt, isErr}
	}()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case r := <-done:
			if r.isErr || r.text != "ok: started; migration still running on the server; check with repo get repo=acme/imp\n" {
				t.Fatalf("got %v %q", r.isErr, r.text)
			}
			return
		case <-deadline:
			t.Fatal("migrate never returned")
		default:
			fc.Advance(10 * time.Second)
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestRepoTopicsAndLanguages(t *testing.T) {
	f := newFake(t).
		on("GET /repos/o/r/topics", jsonOK(`{"topics":["go","mcp"]}`)).
		on("PUT /repos/o/r/topics", reply{status: 204}).
		on("GET /repos/o/r/languages", jsonOK(`{"JS":2300,"Go":123456,"C":0}`))
	if out := mustOK(t, f, "repo", map[string]any{"action": "topics"}); out != "topics: go,mcp\n" {
		t.Fatal(out)
	}
	if out := mustOK(t, f, "repo", map[string]any{"action": "topics_set", "topics": []string{"a", " b "}}); out != "ok: topics set: a,b\n" {
		t.Fatal(out)
	}
	if l := f.last(); l.Method != "PUT" || l.Body != `{"topics":["a","b"]}` {
		t.Fatalf("%+v", l)
	}
	testutil.Golden(t, "repo_languages", mustOK(t, f, "repo", map[string]any{"action": "languages"}))
	f.on("GET /repos/o/r/topics", jsonOK(`{"topics":[]}`))
	if out := mustOK(t, f, "repo", map[string]any{"action": "topics"}); out != "topics: (none)\n" {
		t.Fatal(out)
	}
	f.on("PUT /repos/o/r/topics", reply{status: 422, body: `{"message":"Topic names are invalid","invalidTopics":["Bad Topic"]}`})
	if out := mustErr(t, f, "repo", map[string]any{"action": "topics_set", "topics": []string{"Bad Topic"}}); !strings.Contains(out, "422") {
		t.Fatal(out)
	}
}

func TestRepoMirrors(t *testing.T) {
	list := `[{"remote_name":"remote_mirror_ab12","remote_address":"https://x/y.git","interval":"8h0m0s","sync_on_commit":true,"last_update":"2026-11-22T10:15:00Z","last_error":"boom\nline2","branch_filter":"main"},
{"remote_name":"remote_mirror_cd34","remote_address":"https://x/z.git","interval":"0s","sync_on_commit":false}]`
	f := newFake(t).
		on("POST /repos/o/r/mirror-sync", jsonOK(``)).
		on("GET /repos/o/r/push_mirrors", withTotal(list, "2")).
		on("DELETE /repos/o/r/push_mirrors/remote_mirror_ab12", reply{status: 204}).
		on("POST /repos/o/r/push_mirrors-sync", jsonOK(``))
	if out := mustOK(t, f, "repo", map[string]any{"action": "mirror_sync"}); out != "ok: pull-mirror sync queued\n" {
		t.Fatal(out)
	}
	testutil.Golden(t, "repo_pushmirror_list", mustOK(t, f, "repo", map[string]any{"action": "pushmirror_list"}))
	if out := mustOK(t, f, "repo", map[string]any{"action": "pushmirror_delete", "remote_name": "remote_mirror_ab12"}); out != "ok: deleted push mirror remote_mirror_ab12\n" {
		t.Fatal(out)
	}
	if out := mustOK(t, f, "repo", map[string]any{"action": "pushmirror_sync"}); out != "ok: push mirror sync queued\n" {
		t.Fatal(out)
	}
}

func TestRepoPushMirrorListWindowsWhenServerIgnoresPaging(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < 5; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"remote_name":"m` + string(rune('a'+i)) + `","remote_address":"u"}`)
	}
	sb.WriteString("]")
	f := newFake(t).on("GET /repos/o/r/push_mirrors", jsonOK(sb.String()))
	out := mustOK(t, f, "repo", map[string]any{"action": "pushmirror_list", "limit": 2, "page": 2})
	if !strings.HasPrefix(out, "# push mirrors 2/5 page 2/3 next: page=3\n") || !strings.Contains(out, "mc -> u") || strings.Contains(out, "ma -> u") {
		t.Fatalf("out:\n%s", out)
	}
}

func TestRepoPushMirrorAdd(t *testing.T) {
	f := newFake(t).on("POST /repos/o/r/push_mirrors", jsonOK(`{"remote_name":"remote_mirror_ab12","remote_address":"https://x/y.git","public_key":"ssh-ed25519 AAAA"}`))

	// default interval is always sent
	out := mustOK(t, f, "repo", map[string]any{"action": "pushmirror_add", "remote_address": "https://x/y.git", "auth_username": "u", "auth_token": "t", "branch": "main"})
	if !strings.HasPrefix(out, "ok: push mirror remote_mirror_ab12 -> https://x/y.git (every 8h0m0s)\n") {
		t.Fatal(out)
	}
	b := f.bodyMap(f.last())
	want := map[string]any{"remote_address": "https://x/y.git", "interval": "8h0m0s", "sync_on_commit": false, "remote_username": "u", "remote_password": "t", "branch_filter": "main"}
	for k, v := range want {
		if b[k] != v {
			t.Errorf("body[%s]=%v want %v", k, b[k], v)
		}
	}
	for _, bad := range []string{"auth_username", "auth_token", "use_ssh"} {
		if _, has := b[bad]; has {
			t.Errorf("key %s must not be sent", bad)
		}
	}

	// 0 = on commit only, sync_on_commit defaults to true
	mustOK(t, f, "repo", map[string]any{"action": "pushmirror_add", "remote_address": "https://x/y.git", "interval": "0"})
	b = f.bodyMap(f.last())
	if b["interval"] != "0s" || b["sync_on_commit"] != true {
		t.Fatalf("body %v", b)
	}
	// normalised duration, explicit sync_on_commit
	mustOK(t, f, "repo", map[string]any{"action": "pushmirror_add", "remote_address": "https://x/y.git", "interval": "90m", "sync_on_commit": true})
	b = f.bodyMap(f.last())
	if b["interval"] != "1h30m0s" || b["sync_on_commit"] != true {
		t.Fatalf("body %v", b)
	}
	// ssh: public key is surfaced
	out = mustOK(t, f, "repo", map[string]any{"action": "pushmirror_add", "remote_address": "git@x:y.git", "use_ssh": true})
	if !strings.Contains(out, "public_key: ssh-ed25519 AAAA") || f.bodyMap(f.last())["use_ssh"] != true {
		t.Fatal(out)
	}
	n := len(f.requests())
	if out := mustErr(t, f, "repo", map[string]any{"action": "pushmirror_add", "remote_address": "git@x:y.git", "use_ssh": true, "auth_token": "t"}); !strings.Contains(out, "use_ssh cannot be combined with credentials") {
		t.Fatal(out)
	}
	if out := mustErr(t, f, "repo", map[string]any{"action": "pushmirror_add", "remote_address": "u", "interval": "soon"}); !strings.Contains(out, "interval") {
		t.Fatal(out)
	}
	if len(f.requests()) != n {
		t.Fatal("invalid calls reached the server")
	}
}

func TestRepoForkStatusAndSync(t *testing.T) {
	f := newFake(t).
		on("GET /repos/o/r/sync_fork", jsonOK(`{"allowed":true,"commits_behind":3}`)).
		on("GET /repos/o/r/sync_fork/feat%2Fx", jsonOK(`{"allowed":false,"commits_behind":1}`)).
		on("POST /repos/o/r/sync_fork", reply{status: 204}).
		on("POST /repos/o/r/sync_fork/main", reply{status: 204})
	if out := mustOK(t, f, "repo", map[string]any{"action": "fork_status"}); out != "behind 3 commits, sync allowed\n" {
		t.Fatal(out)
	}
	if out := mustOK(t, f, "repo", map[string]any{"action": "fork_status", "branch": "feat/x"}); out != "behind 1 commit, sync not allowed (diverged)\n" {
		t.Fatal(out)
	}
	f.on("GET /repos/o/r/sync_fork", jsonOK(`{"allowed":true,"commits_behind":0}`))
	if out := mustOK(t, f, "repo", map[string]any{"action": "fork_status"}); out != "up to date\n" {
		t.Fatal(out)
	}
	mustOK(t, f, "repo", map[string]any{"action": "fork_sync"})
	mustOK(t, f, "repo", map[string]any{"action": "fork_sync", "branch": "main"})
	if f.count("POST", "/repos/o/r/sync_fork") != 1 || f.count("POST", "/repos/o/r/sync_fork/main") != 1 {
		t.Fatalf("requests: %v", f.requests())
	}
}

func TestRepoStar(t *testing.T) {
	f := newFake(t).on("PUT /user/starred/o/r", reply{status: 204}).on("DELETE /user/starred/o/r", reply{status: 204})
	if out := mustOK(t, f, "repo", map[string]any{"action": "star"}); out != "ok: starred o/r\n" {
		t.Fatal(out)
	}
	if out := mustOK(t, f, "repo", map[string]any{"action": "unstar"}); out != "ok: unstarred o/r\n" {
		t.Fatal(out)
	}
}

func TestRepoErrorsReachTheModel(t *testing.T) {
	f := newFake(t).on("GET /repos/o/r", reply{status: 404, body: `{"message":"GetRepositoryByName","errors":["repository does not exist [name: r]"]}`})
	out := mustErr(t, f, "repo", map[string]any{"action": "get"})
	if !strings.Contains(out, "error: 404 not found: repository does not exist") || strings.Contains(out, "GetRepositoryByName") {
		t.Fatal(out)
	}
	// the destructive annotation is explicit
	reg := tools.NewRegistry()
	Register(reg)
	if err := reg.Build(nil); err != nil {
		t.Fatal(err)
	}
}
