package integration

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// usersSSHKey returns a fresh ed25519 key in authorized_keys format (the
// server rejects a key that any account already holds, so each run needs its own).
func usersSSHKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var blob []byte
	for _, part := range [][]byte{[]byte("ssh-ed25519"), pub} {
		blob = binary.BigEndian.AppendUint32(blob, uint32(len(part)))
		blob = append(blob, part...)
	}
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " it@example.invalid"
}

// usersRawAPIAs is rawAPI with a Sudo header (acts as another user; needs the admin token).
func usersRawAPIAs(t testing.TB, sudo, method, path string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, instURL+"/api/v1"+path, rd)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "token "+instToken)
	if sudo != "" {
		req.Header.Set("Sudo", sudo)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: transport error", method, path)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// usersNewAccount makes a throwaway account and deletes it on cleanup.
func usersNewAccount(t *testing.T, prefix string) string {
	t.Helper()
	name := uniqueName(prefix)
	st, body := rawAPI(t, http.MethodPost, "/admin/users", map[string]any{
		"username": name, "email": name + "@example.invalid", "password": uniqueName("Pw") + "-Aa1!x9",
		"must_change_password": false, "visibility": "public",
	})
	if st != http.StatusCreated {
		t.Fatalf("create user %s: %d %s", name, st, body)
	}
	t.Cleanup(func() { rawAPI(t, http.MethodDelete, "/admin/users/"+name+"?purge=true", nil) })
	return name
}

var (
	reUsersNID    = regexp.MustCompile(`\bn(\d+)\b`)
	reUsersID     = regexp.MustCompile(`\bid (\d+)\b`)
	reUsersTeamID = regexp.MustCompile(`created team (\d+)\b`)
)

func TestUserTool(t *testing.T) {
	requireAdmin(t)

	t.Run("me_get_search", func(t *testing.T) {
		out := mustOK(t, "user", map[string]any{"action": "me"})
		if !strings.HasPrefix(out, "user ") || !strings.Contains(out, "admin: yes") {
			t.Fatalf("user me:\n%s", out)
		}
		login := strings.TrimSpace(strings.TrimPrefix(strings.SplitN(out, "\n", 2)[0], "user "))
		if got := mustOK(t, "user", map[string]any{"action": "get", "user": login}); !strings.HasPrefix(got, "user "+login+"\n") {
			t.Errorf("user get:\n%s", got)
		}
		list := mustOK(t, "user", map[string]any{"action": "search", "q": login})
		if !strings.HasPrefix(list, "# users ") || !strings.Contains(list, login) {
			t.Errorf("user search:\n%s", list)
		}
		if e := mustErr(t, "user", map[string]any{"action": "get", "user": "it-no-such-user-" + uniqueName("x")}); !strings.Contains(e, "404") {
			t.Errorf("get unknown user:\n%s", e)
		}
	})

	t.Run("lists", func(t *testing.T) {
		for _, a := range []string{"followers", "following", "starred", "orgs", "keys_list", "tokens_list"} {
			out := mustOK(t, "user", map[string]any{"action": a})
			if !strings.HasPrefix(out, "# ") {
				t.Errorf("user %s: no list header:\n%s", a, out)
			}
		}
	})

	t.Run("keys", func(t *testing.T) {
		title := uniqueName("it-key")
		out := mustOK(t, "user", map[string]any{"action": "keys_add", "title": title, "key": usersSSHKey(t), "read_only": true})
		m := reUsersID.FindStringSubmatch(out)
		if m == nil || !strings.HasPrefix(out, "ok: added key") {
			t.Fatalf("keys_add:\n%s", out)
		}
		id, _ := strconv.Atoi(m[1])
		t.Cleanup(func() { rawAPI(t, http.MethodDelete, "/user/keys/"+m[1], nil) })
		list := mustOK(t, "user", map[string]any{"action": "keys_list"})
		if !strings.Contains(list, title) {
			t.Errorf("keys_list misses the new key:\n%s", list)
		}
		if del := mustOK(t, "user", map[string]any{"action": "keys_delete", "id": id}); !strings.HasPrefix(del, "ok: deleted key") {
			t.Errorf("keys_delete:\n%s", del)
		}
		mustErr(t, "user", map[string]any{"action": "keys_delete", "id": id})
		if e := mustErr(t, "user", map[string]any{"action": "keys_add", "title": "bad", "key": "not a key"}); !strings.Contains(e, "error:") {
			t.Errorf("bad key:\n%s", e)
		}
	})

	t.Run("tokens", func(t *testing.T) {
		name := uniqueName("it-tok")
		out := mustOK(t, "user", map[string]any{"action": "tokens_create", "name": name, "scopes": []any{"read:repository", "read:user"}})
		m := reUsersID.FindStringSubmatch(out)
		if m == nil || !strings.Contains(out, "\ntoken: ") || !strings.Contains(out, "shown once") {
			t.Fatalf("tokens_create (secret hidden):\n%s", reUsersSecret.ReplaceAllString(out, "token: <hidden>"))
		}
		id, _ := strconv.Atoi(m[1])
		list := mustOK(t, "user", map[string]any{"action": "tokens_list"})
		if !strings.Contains(list, name) || !strings.Contains(list, "read:repository") {
			t.Errorf("tokens_list misses the token:\n%s", list)
		}
		if strings.Contains(list, "token:") {
			t.Errorf("tokens_list printed a secret:\n%s", list)
		}
		if del := mustOK(t, "user", map[string]any{"action": "tokens_delete", "id": id}); !strings.HasPrefix(del, "ok: deleted token") {
			t.Errorf("tokens_delete:\n%s", del)
		}
		if e := mustErr(t, "user", map[string]any{"action": "tokens_create", "name": name, "scopes": []any{"read:nothing"}}); !strings.Contains(e, "invalid: scope") {
			t.Errorf("bad scope:\n%s", e)
		}
	})

	t.Run("tokens_need_admin", func(t *testing.T) {
		// a session acting as a non-admin account gets the explanatory error
		// without any token being created
		other := usersNewAccount(t, "it-nonadm")
		s := newSession(t, map[string]string{"FORGEJO_SUDO": other})
		out, isErr := s.call(t, "user", map[string]any{"action": "tokens_create", "name": uniqueName("it-tok"), "scopes": []any{"read:user"}})
		if !isErr || !strings.Contains(out, "needs a site-admin token") {
			t.Errorf("tokens_create as non-admin: err=%v: %s", isErr, out)
		}
		if me, _ := s.call(t, "user", map[string]any{"action": "me"}); !strings.HasPrefix(me, "user "+other) {
			t.Errorf("sudo session is not %s: %s", other, me)
		}
	})
}

var reUsersSecret = regexp.MustCompile(`(?m)^token: .*$`)

func TestOrgTool(t *testing.T) {
	requireAdmin(t)
	orgName := uniqueName("it-org")
	member := usersNewAccount(t, "it-mem")

	out := mustOK(t, "org", map[string]any{"action": "create", "name": orgName, "full_name": "IT Org", "visibility": "public", "description": "d1"})
	if !strings.HasPrefix(out, "ok: created org "+orgName) {
		t.Fatalf("org create:\n%s", out)
	}
	current := orgName
	t.Cleanup(func() {
		// repos first, then the org (idempotent; ignore errors)
		rawAPI(t, http.MethodDelete, "/repos/"+current+"/web", nil)
		rawAPI(t, http.MethodDelete, "/orgs/"+current, nil)
	})
	mustErr(t, "org", map[string]any{"action": "create", "name": orgName})

	got := mustOK(t, "org", map[string]any{"action": "get", "org": orgName})
	for _, want := range []string{"org " + orgName, "name: IT Org", "visibility: public", "description: d1"} {
		if !strings.Contains(got, want) {
			t.Errorf("org get misses %q:\n%s", want, got)
		}
	}
	if list := mustOK(t, "org", map[string]any{"action": "list"}); !strings.Contains(list, orgName) {
		t.Errorf("org list misses the new org:\n%s", list)
	}
	if list := mustOK(t, "org", map[string]any{"action": "list", "all": true, "limit": 100}); !strings.HasPrefix(list, "# orgs ") {
		t.Errorf("org list all:\n%s", list)
	}
	if e := mustOK(t, "org", map[string]any{"action": "edit", "org": orgName, "description": "d2", "website": "https://example.invalid"}); !strings.HasPrefix(e, "ok: updated org") {
		t.Errorf("org edit:\n%s", e)
	}
	if g := mustOK(t, "org", map[string]any{"action": "get", "org": orgName}); !strings.Contains(g, "description: d2") || !strings.Contains(g, "website: https://example.invalid") {
		t.Errorf("org get after edit:\n%s", g)
	}
	mustErr(t, "org", map[string]any{"action": "edit", "org": orgName})

	// teams: the creator owns the Owners team; create one with the default unit map
	teams := mustOK(t, "org", map[string]any{"action": "teams", "org": orgName})
	if !strings.Contains(teams, "Owners") {
		t.Errorf("org teams misses Owners:\n%s", teams)
	}
	cr := mustOK(t, "org", map[string]any{"action": "team_create", "org": orgName, "name": "devs", "permission": "write", "description": "Developers"})
	m := reUsersTeamID.FindStringSubmatch(cr)
	if m == nil {
		t.Fatalf("team_create:\n%s", cr)
	}
	teamID, _ := strconv.Atoi(m[1])
	tg := mustOK(t, "org", map[string]any{"action": "team_get", "team_id": teamID})
	for _, want := range []string{"name: devs", "org: " + orgName, "permission: write", "units: ", "code:write"} {
		if !strings.Contains(tg, want) {
			t.Errorf("team_get misses %q:\n%s", want, tg)
		}
	}
	// a team with an explicit units map
	cr2 := mustOK(t, "org", map[string]any{"action": "team_create", "org": orgName, "name": "readers", "units_map": map[string]any{"code": "read", "issues": "write"}})
	if !reUsersTeamID.MatchString(cr2) {
		t.Fatalf("team_create units_map:\n%s", cr2)
	}
	if e := mustOK(t, "org", map[string]any{"action": "team_edit", "team_id": teamID, "description": "Devs 2", "includes_all_repositories": true}); !strings.HasPrefix(e, "ok: updated team") {
		t.Errorf("team_edit:\n%s", e)
	}
	tg = mustOK(t, "org", map[string]any{"action": "team_get", "team_id": teamID})
	if !strings.Contains(tg, "repos: all") || !strings.Contains(tg, "description: Devs 2") || !strings.Contains(tg, "name: devs") {
		t.Errorf("team_get after edit:\n%s", tg)
	}
	if tl := mustOK(t, "org", map[string]any{"action": "teams", "org": orgName}); !strings.Contains(tl, fmt.Sprintf("team %d devs write | all-repos", teamID)) {
		t.Errorf("teams line:\n%s", tl)
	}

	// members: add through the team (invite or direct add), then list and remove
	add := mustOK(t, "org", map[string]any{"action": "team_add_member", "team_id": teamID, "user": member})
	if !strings.HasPrefix(add, "ok: added @"+member) && !strings.HasPrefix(add, "ok: accepted @"+member) {
		t.Errorf("team_add_member:\n%s", add)
	}
	t.Logf("team_add_member: %s", add)
	if strings.HasPrefix(add, "ok: added") {
		if tm := mustOK(t, "org", map[string]any{"action": "team_members", "team_id": teamID}); !strings.Contains(tm, member) {
			t.Errorf("team_members misses %s:\n%s", member, tm)
		}
		if om := mustOK(t, "org", map[string]any{"action": "members", "org": orgName}); !strings.Contains(om, member) {
			t.Errorf("members misses %s:\n%s", member, om)
		}
		if rm := mustOK(t, "org", map[string]any{"action": "team_remove_member", "team_id": teamID, "user": member}); !strings.HasPrefix(rm, "ok: removed") {
			t.Errorf("team_remove_member:\n%s", rm)
		}
		if rm := mustOK(t, "org", map[string]any{"action": "member_remove", "org": orgName, "user": member}); !strings.HasPrefix(rm, "ok: removed") {
			t.Errorf("member_remove:\n%s", rm)
		}
	} else {
		t.Logf("team_add_member answered with the invite warning (mailer enabled): %s", add)
	}
	if om := mustOK(t, "org", map[string]any{"action": "members", "org": orgName}); !strings.HasPrefix(om, "# members ") {
		t.Errorf("members:\n%s", om)
	}

	// repos of a team
	if st, body := rawAPI(t, http.MethodPost, "/orgs/"+orgName+"/repos", map[string]any{"name": "web", "auto_init": false}); st != http.StatusCreated {
		t.Fatalf("create org repo: %d %s", st, body)
	}
	repo := orgName + "/web"
	if r := mustOK(t, "org", map[string]any{"action": "team_add_repo", "team_id": teamID, "repo": repo}); !strings.HasPrefix(r, "ok: added "+repo) {
		t.Errorf("team_add_repo:\n%s", r)
	}
	if r := mustOK(t, "org", map[string]any{"action": "team_repos", "team_id": teamID}); !strings.Contains(r, repo) {
		t.Errorf("team_repos misses the repo:\n%s", r)
	}
	if r := mustOK(t, "org", map[string]any{"action": "team_remove_repo", "team_id": teamID, "repo": repo}); !strings.HasPrefix(r, "ok: removed "+repo) {
		t.Errorf("team_remove_repo:\n%s", r)
	}
	mustErr(t, "org", map[string]any{"action": "team_add_repo", "team_id": teamID, "repo": "web"})

	// destructive actions need the exact name
	if e := mustErr(t, "org", map[string]any{"action": "team_delete", "team_id": teamID}); !strings.Contains(e, "confirm") {
		t.Errorf("team_delete without confirm:\n%s", e)
	}
	if e := mustErr(t, "org", map[string]any{"action": "team_delete", "team_id": teamID, "confirm": "nope"}); !strings.Contains(e, `confirm="devs"`) {
		t.Errorf("team_delete wrong confirm:\n%s", e)
	}
	if d := mustOK(t, "org", map[string]any{"action": "team_delete", "team_id": teamID, "confirm": "devs"}); !strings.HasPrefix(d, "ok: deleted team") {
		t.Errorf("team_delete:\n%s", d)
	}
	mustErr(t, "org", map[string]any{"action": "team_get", "team_id": teamID})

	// rename, then delete (the repo must go first)
	renamed := orgName + "r"
	if r := mustOK(t, "org", map[string]any{"action": "rename", "org": orgName, "new_name": renamed}); !strings.HasPrefix(r, "ok: renamed org") {
		t.Fatalf("org rename:\n%s", r)
	}
	current = renamed
	mustOK(t, "org", map[string]any{"action": "get", "org": renamed})
	if e := mustErr(t, "org", map[string]any{"action": "delete", "org": renamed, "confirm": orgName}); !strings.Contains(e, "confirm") {
		t.Errorf("org delete wrong confirm:\n%s", e)
	}
	// the org still owns a repo; the server refuses (hidden 500 or an explicit error)
	e := mustErr(t, "org", map[string]any{"action": "delete", "org": renamed, "confirm": renamed})
	t.Logf("org delete with a repo present: %s", strings.ReplaceAll(e, "\n", " | "))
	if st, _ := rawAPI(t, http.MethodDelete, "/repos/"+renamed+"/web", nil); st != http.StatusNoContent {
		t.Fatalf("delete org repo: %d", st)
	}
	if d := mustOK(t, "org", map[string]any{"action": "delete", "org": renamed, "confirm": renamed}); !strings.HasPrefix(d, "ok: deleted org") {
		t.Errorf("org delete:\n%s", d)
	}
	mustErr(t, "org", map[string]any{"action": "get", "org": renamed})
}

func TestNotifyTool(t *testing.T) {
	requireAdmin(t)
	skipUnlessIntegration(t)
	other := usersNewAccount(t, "it-nfy")
	repo := uniqueName("it-nfy")
	st, me := rawAPI(t, http.MethodGet, "/user", nil)
	if st != http.StatusOK {
		t.Fatalf("GET /user: %d", st)
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(me, &u); err != nil {
		t.Fatal(err)
	}
	if st, body := rawAPI(t, http.MethodPost, "/user/repos", map[string]any{"name": repo, "private": false}); st != http.StatusCreated {
		t.Fatalf("create repo: %d %s", st, body)
	}
	t.Cleanup(func() { rawAPI(t, http.MethodDelete, "/repos/"+u.Login+"/"+repo, nil) })
	full := u.Login + "/" + repo

	// another account opens an issue on our public repo: the owner is notified
	if st, body := usersRawAPIAs(t, other, http.MethodPost, "/repos/"+full+"/issues", map[string]any{"title": "Notify me", "body": "hello"}); st != http.StatusCreated {
		t.Fatalf("create issue as %s: %d %s", other, st, body)
	}

	if c := mustOK(t, "notify", map[string]any{"action": "count"}); !regexp.MustCompile(`^unread: \d+$`).MatchString(c) {
		t.Fatalf("notify count: %q", c)
	}
	// notifications are created by a background queue: wait for the thread
	var list string
	for i := 0; i < 40; i++ {
		list = mustOK(t, "notify", map[string]any{"action": "list", "repo": full, "status": "all"})
		if strings.Contains(list, "Notify me") {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !strings.Contains(list, full+"#1") || !strings.Contains(list, "Notify me") || !strings.Contains(list, "issue open") {
		t.Fatalf("notify list:\n%s", list)
	}
	m := reUsersNID.FindStringSubmatch(list)
	if m == nil {
		t.Fatalf("no thread id in:\n%s", list)
	}
	id, _ := strconv.Atoi(m[1])
	got := mustOK(t, "notify", map[string]any{"action": "get", "id": id})
	for _, want := range []string{"notification " + m[1], "repo: " + full, "title: Notify me", "number: 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("notify get misses %q:\n%s", want, got)
		}
	}
	if l := mustOK(t, "notify", map[string]any{"action": "list", "repo": full, "types": []any{"pull"}}); !strings.HasPrefix(l, "# notifications 0/0") {
		t.Errorf("pull filter should hide the issue thread:\n%s", l)
	}
	if mk := mustOK(t, "notify", map[string]any{"action": "mark", "id": id, "to": "read"}); !strings.HasPrefix(mk, "ok: marked n"+m[1]+" as read") {
		t.Errorf("mark thread:\n%s", mk)
	}
	if l := mustOK(t, "notify", map[string]any{"action": "list", "repo": full}); !strings.HasPrefix(l, "# notifications 0/0") {
		t.Errorf("unread list after mark:\n%s", l)
	}
	if mk := mustOK(t, "notify", map[string]any{"action": "mark", "id": id, "to": "unread"}); !strings.HasPrefix(mk, "ok: marked") {
		t.Errorf("mark unread:\n%s", mk)
	}
	if l := mustOK(t, "notify", map[string]any{"action": "list", "repo": full}); !strings.Contains(l, full+"#1") {
		t.Errorf("unread list after mark unread:\n%s", l)
	}
	// bulk mark of the repo, then everything
	if mk := mustOK(t, "notify", map[string]any{"action": "mark", "repo": full}); !regexp.MustCompile(`^ok: marked \d+ as read`).MatchString(mk) {
		t.Errorf("bulk mark repo:\n%s", mk)
	}
	if l := mustOK(t, "notify", map[string]any{"action": "list", "repo": full}); !strings.HasPrefix(l, "# notifications 0/0") {
		t.Errorf("unread list after bulk mark:\n%s", l)
	}
	if mk := mustOK(t, "notify", map[string]any{"action": "mark"}); !strings.HasPrefix(mk, "ok: marked") {
		t.Errorf("bulk mark all:\n%s", mk)
	}
	if c := mustOK(t, "notify", map[string]any{"action": "count"}); c != "unread: 0" {
		t.Errorf("count after marking everything: %q", c)
	}
	mustErr(t, "notify", map[string]any{"action": "get", "id": 999999999})
}
