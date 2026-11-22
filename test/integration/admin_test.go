package integration

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// admMust calls the admin tool and fails the test on an error result.
func admMust(t *testing.T, s *session, args map[string]any) string {
	t.Helper()
	out, isErr := s.call(t, "admin", args)
	if isErr {
		t.Fatalf("admin %s failed: %s", argSummary(args), out)
	}
	return out
}

// adminSession returns a session with the admin toolset on (it is off by default).
func adminSession(t *testing.T) *session {
	t.Helper()
	requireAdmin(t)
	return newSession(t, map[string]string{"FORGEJO_TOOLSETS": "all"})
}

// admRandomPassword returns a throw-away password that meets the default
// complexity rules; it is never printed.
func admRandomPassword(t *testing.T) string {
	t.Helper()
	b := make([]byte, 9)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "Aa1!" + hex.EncodeToString(b)
}

// admUserExists reports whether GET /users/{name} finds the user.
func admUserExists(t *testing.T, name string) bool {
	t.Helper()
	st, _ := rawAPI(t, http.MethodGet, "/users/"+name, nil)
	return st == http.StatusOK
}

func TestAdminUsersLifecycle(t *testing.T) {
	s := adminSession(t)
	name := uniqueName("it")
	renamed := name + "r"
	t.Cleanup(func() {
		for _, n := range []string{name, renamed} {
			rawAPI(t, http.MethodDelete, "/admin/users/"+n+"?purge=true", nil)
		}
	})
	pw := admRandomPassword(t)

	// create: password never echoed
	out, isErr := s.call(t, "admin", map[string]any{
		"action": "users_create", "user": name, "email": name + "@example.invalid",
		"password": pw, "full_name": "IT Admin Test", "must_change_password": false,
	})
	if isErr || !strings.HasPrefix(out, "ok: created user "+name+" (id ") || strings.Contains(out, pw) {
		t.Fatalf("users_create: %q (isErr %v)", out, isErr)
	}

	// duplicate create is refused
	if out, isErr = s.call(t, "admin", map[string]any{
		"action": "users_create", "user": name, "email": name + "@example.invalid", "password": pw,
	}); !isErr {
		t.Fatalf("duplicate create succeeded: %s", out)
	}

	// create without password fails locally
	if out, isErr = s.call(t, "admin", map[string]any{
		"action": "users_create", "user": name + "x", "email": name + "x@example.invalid",
	}); !isErr || !strings.Contains(out, "password is required") {
		t.Fatalf("create without password: %q (isErr %v)", out, isErr)
	}

	// edit: changed field names only, never values
	out = admMust(t, s, map[string]any{
		"action": "users_edit", "user": name, "full_name": "Renamed Person", "active": true, "restricted": true,
	})
	if out != "ok: updated user "+name+": full_name,active,restricted" {
		t.Fatalf("users_edit: %q", out)
	}
	st, body := rawAPI(t, http.MethodGet, "/users/"+name, nil)
	if st != http.StatusOK || !strings.Contains(string(body), `"full_name":"Renamed Person"`) || !strings.Contains(string(body), `"restricted":true`) {
		t.Fatalf("user after edit: %d %s", st, body)
	}

	// login_name without source_id: the tool fills source_id from the current record
	out = admMust(t, s, map[string]any{"action": "users_edit", "user": name, "login_name": name})
	if !strings.HasPrefix(out, "ok: updated user "+name) {
		t.Fatalf("users_edit login_name: %q", out)
	}

	// edit with nothing to change
	if out, isErr = s.call(t, "admin", map[string]any{"action": "users_edit", "user": name}); !isErr || !strings.Contains(out, "nothing to change") {
		t.Fatalf("empty edit: %q (isErr %v)", out, isErr)
	}

	// list shows the user (sorted newest first so it is on page 1)
	out = admMust(t, s, map[string]any{"action": "users_list", "limit": 100, "sort": "newest"})
	if !strings.HasPrefix(out, "# users ") || !strings.Contains(out, name) {
		t.Fatalf("users_list does not show %s: %q", name, out)
	}

	// rename uses new_username on the wire
	out = admMust(t, s, map[string]any{"action": "users_rename", "user": name, "new_name": renamed})
	if out != "ok: renamed user "+name+" -> "+renamed || !admUserExists(t, renamed) {
		t.Fatalf("users_rename: %q", out)
	}

	// delete needs the exact confirm
	if out, isErr = s.call(t, "admin", map[string]any{"action": "users_delete", "user": renamed}); !isErr || !strings.Contains(out, "confirm") {
		t.Fatalf("delete without confirm: %q (isErr %v)", out, isErr)
	}
	if out, isErr = s.call(t, "admin", map[string]any{"action": "users_delete", "user": renamed, "confirm": name}); !isErr || !strings.Contains(out, `confirm="`+renamed+`"`) {
		t.Fatalf("delete with the old name: %q (isErr %v)", out, isErr)
	}
	if !admUserExists(t, renamed) {
		t.Fatal("user vanished before a valid confirm")
	}
	out = admMust(t, s, map[string]any{"action": "users_delete", "user": renamed, "confirm": renamed, "purge": true})
	if out != "ok: deleted user "+renamed+" (purged)" || admUserExists(t, renamed) {
		t.Fatalf("users_delete: %q", out)
	}
}

func TestAdminListsAndEmails(t *testing.T) {
	s := adminSession(t)
	for _, tc := range []struct {
		args   map[string]any
		prefix string
	}{
		{map[string]any{"action": "orgs_list", "limit": 5}, "# orgs "},
		{map[string]any{"action": "emails_list", "limit": 5}, "# emails "},
		{map[string]any{"action": "emails_search", "q": "it-", "limit": 5}, "# emails "},
		{map[string]any{"action": "cron_list", "limit": 100}, "# cron "},
		{map[string]any{"action": "unadopted_list", "limit": 5}, "# unadopted "},
	} {
		out := admMust(t, s, tc.args)
		if !strings.HasPrefix(out, tc.prefix) {
			t.Errorf("%v: header %q, want prefix %q", tc.args["action"], firstLine(out), tc.prefix)
		}
	}
}

func TestAdminCron(t *testing.T) {
	s := adminSession(t)
	out := admMust(t, s, map[string]any{"action": "cron_list", "limit": 100})
	// lines look like `name | schedule | next T | runs:n`
	var task string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "cleanup_hook_task_table ") || strings.HasPrefix(line, "cleanup_hook_task_table|") {
			task = "cleanup_hook_task_table"
		}
	}
	if task == "" {
		t.Skip("no harmless cron task found to run")
	}
	out = admMust(t, s, map[string]any{"action": "cron_run", "task": task})
	if !strings.HasPrefix(out, "ok: cron task "+task+" finished") && !strings.HasPrefix(out, "started: cron task "+task) {
		t.Fatalf("cron_run: %q", out)
	}
	if out, isErr := s.call(t, "admin", map[string]any{"action": "cron_run", "task": "no_such_task_" + uniqueName("x")}); !isErr || !strings.Contains(out, "404") {
		t.Fatalf("unknown cron task: %q (isErr %v)", out, isErr)
	}
}

func TestAdminUnadoptedErrors(t *testing.T) {
	s := adminSession(t)
	out, isErr := s.call(t, "admin", map[string]any{"action": "unadopted_adopt", "owner": "it-nobody", "repo": uniqueName("it")})
	if !isErr {
		t.Fatalf("adopting a missing directory succeeded: %s", out)
	}
	if out, isErr = s.call(t, "admin", map[string]any{"action": "unadopted_delete", "owner": "a", "repo": "b", "confirm": "wrong"}); !isErr || !strings.Contains(out, `confirm="a/b"`) {
		t.Fatalf("unadopted_delete confirm: %q (isErr %v)", out, isErr)
	}
}

// TestAdminQuota covers the quota actions: on a server without [quota]
// every one answers "quota is disabled on this server" (the routes do not
// exist); on a server with quota the round trip rule -> group -> user works.
func TestAdminQuota(t *testing.T) {
	s := adminSession(t)
	out, isErr := s.call(t, "admin", map[string]any{"action": "quota_rules"})
	if isErr {
		if out != "error: quota is disabled on this server" {
			t.Fatalf("quota_rules error: %q", out)
		}
		for _, args := range []map[string]any{
			{"action": "quota_info"},
			{"action": "quota_groups"},
			{"action": "quota_check", "subject": "size:all"},
			{"action": "quota_group_set", "group": "g-" + uniqueName("it")},
			{"action": "quota_rule_set", "rule": "r-" + uniqueName("it"), "limit_bytes": 1, "subjects": []string{"size:all"}},
		} {
			if o, e := s.call(t, "admin", args); !e || o != "error: quota is disabled on this server" {
				t.Errorf("%v with quota off: %q (isErr %v)", args["action"], o, e)
			}
		}
		return
	}
	rule, group := "it-r-"+uniqueName("q"), "it-g-"+uniqueName("q")
	t.Cleanup(func() {
		rawAPI(t, http.MethodDelete, "/admin/quota/groups/"+group, nil)
		rawAPI(t, http.MethodDelete, "/admin/quota/rules/"+rule, nil)
	})
	admMust(t, s, map[string]any{"action": "quota_rule_set", "rule": rule, "limit_bytes": 5000000, "subjects": []string{"size:repos:all"}})
	if o := admMust(t, s, map[string]any{"action": "quota_rule_set", "rule": rule, "limit_bytes": 6000000}); o != "ok: updated quota rule "+rule {
		t.Fatalf("rule update: %q", o)
	}
	if o := admMust(t, s, map[string]any{"action": "quota_rules", "rule": rule}); !strings.Contains(o, "limit 6.0MB") {
		t.Fatalf("rule get: %q", o)
	}
	if o := admMust(t, s, map[string]any{"action": "quota_group_set", "group": group, "rule": rule}); !strings.Contains(o, "created quota group "+group) {
		t.Fatalf("group set: %q", o)
	}
	if o := admMust(t, s, map[string]any{"action": "quota_groups", "group": group}); !strings.Contains(o, "rule "+rule) {
		t.Fatalf("group get: %q", o)
	}
	admMust(t, s, map[string]any{"action": "quota_info"})
	if o := admMust(t, s, map[string]any{"action": "quota_check", "subject": "size:repos:all"}); !strings.HasPrefix(o, "ok: within quota") && !strings.HasPrefix(o, "over quota") {
		t.Fatalf("quota_check: %q", o)
	}
	admMust(t, s, map[string]any{"action": "quota_group_delete", "group": group, "rule": rule})
	admMust(t, s, map[string]any{"action": "quota_group_delete", "group": group})
	admMust(t, s, map[string]any{"action": "quota_rule_delete", "rule": rule})
}

func TestAdminHiddenByDefault(t *testing.T) {
	skipUnlessIntegration(t)
	for _, n := range sharedSession(t).toolNames(t) {
		if n == "admin" {
			t.Fatal("admin tool is listed without the admin toolset")
		}
	}
	out, isErr := call(t, "admin", map[string]any{"action": "users_list"})
	if !isErr {
		t.Fatalf("admin call without the toolset: %s", out)
	}
}

func TestAdminReadOnly(t *testing.T) {
	requireAdmin(t)
	s := newSession(t, map[string]string{"FORGEJO_TOOLSETS": "all", "FORGEJO_READ_ONLY": "1"})
	out, isErr := s.call(t, "admin", map[string]any{"action": "users_create", "user": "x", "email": "x@example.invalid", "password": "x"})
	if !isErr || !strings.Contains(out, "read-only") {
		t.Fatalf("users_create in read-only mode: %q (isErr %v)", out, isErr)
	}
	if out, isErr = s.call(t, "admin", map[string]any{"action": "users_list", "limit": 1}); isErr {
		t.Fatalf("users_list in read-only mode: %s", out)
	}
}

// ---- package ---------------------------------------------------------------

// pkgOwner returns the login of the token's user.
func pkgOwner(t *testing.T) string {
	t.Helper()
	st, body := rawAPI(t, http.MethodGet, "/user", nil)
	var me struct {
		Login string `json:"login"`
	}
	if st != http.StatusOK || json.Unmarshal(body, &me) != nil || me.Login == "" {
		t.Fatalf("GET /user: %d", st)
	}
	return me.Login
}

// pkgUpload stores a generic package through the package registry route
// (outside /api/v1) and returns the owner login.
func pkgUpload(t *testing.T, name, version, file, content string) string {
	t.Helper()
	login := pkgOwner(t)
	url := instURL + "/api/packages/" + login + "/generic/" + name + "/" + version + "/" + file
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader([]byte(content)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "token "+instToken)
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatal("package upload: transport error")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("package upload: status %d", resp.StatusCode)
	}
	t.Cleanup(func() {
		rawAPI(t, http.MethodDelete, "/packages/"+login+"/generic/"+name+"/"+version, nil)
	})
	return login
}

// pkgMkRepo creates a private repo owned by the token's user and removes it
// when the test ends (local, so this file does not depend on other WPs' helpers).
func pkgMkRepo(t *testing.T) string {
	t.Helper()
	st, body := rawAPI(t, http.MethodPost, "/user/repos", map[string]any{"name": uniqueName("pk"), "private": true, "auto_init": true})
	var r struct {
		FullName string `json:"full_name"`
	}
	if st != http.StatusCreated || json.Unmarshal(body, &r) != nil || r.FullName == "" {
		t.Fatalf("create repo: %d %s", st, body)
	}
	t.Cleanup(func() { rawAPI(t, http.MethodDelete, "/repos/"+r.FullName, nil) })
	return r.FullName
}

func TestPackageLifecycle(t *testing.T) {
	skipUnlessIntegration(t)
	name, version := uniqueName("it-pkg"), "1.0.0"
	owner := pkgUpload(t, name, version, "hello.txt", "hello package")
	repo := pkgMkRepo(t) // admin-owned repo, same owner as the package
	repoName := strings.TrimPrefix(repo, owner+"/")

	// list finds it by name, filtered by type
	out := mustOK(t, "package", map[string]any{"action": "list", "owner": owner, "type": "generic", "q": name})
	if !strings.HasPrefix(out, "# packages 1/1 page 1/1") || !strings.Contains(out, "generic "+name+" "+version) {
		t.Fatalf("list: %s", out)
	}

	// get
	out = mustOK(t, "package", map[string]any{"action": "get", "owner": owner, "type": "generic", "name": name, "version": version})
	if !strings.HasPrefix(out, "package generic "+name+" "+version+"\n") || !strings.Contains(out, "owner: "+owner) {
		t.Fatalf("get: %s", out)
	}
	if strings.Contains(out, "repo:") {
		t.Fatalf("package is linked before link: %s", out)
	}

	// files
	out = mustOK(t, "package", map[string]any{"action": "files", "owner": owner, "type": "generic", "name": name, "version": version})
	if !strings.HasPrefix(out, "# files 1/1 page 1/1") || !strings.Contains(out, "| hello.txt | 13B | sha256:") {
		t.Fatalf("files: %s", out)
	}

	// link / unlink
	out = mustOK(t, "package", map[string]any{"action": "link", "owner": owner, "type": "generic", "name": name, "repo": repoName})
	if !strings.HasPrefix(out, "ok: linked package generic "+name+" to "+repo) {
		t.Fatalf("link: %s", out)
	}
	out = mustOK(t, "package", map[string]any{"action": "get", "owner": owner, "type": "generic", "name": name, "version": version})
	if !strings.Contains(out, "repo: "+repo) {
		t.Fatalf("get after link: %s", out)
	}
	mustOK(t, "package", map[string]any{"action": "unlink", "owner": owner, "type": "generic", "name": name})
	out = mustOK(t, "package", map[string]any{"action": "get", "owner": owner, "type": "generic", "name": name, "version": version})
	if strings.Contains(out, "repo:") {
		t.Fatalf("get after unlink: %s", out)
	}

	// default owner comes from the token's user when no default repo exists
	if out = mustOK(t, "package", map[string]any{"action": "list", "q": name}); !strings.Contains(out, name) {
		t.Fatalf("list with default owner: %s", out)
	}

	// delete needs the exact confirm
	if out = mustErr(t, "package", map[string]any{"action": "delete", "owner": owner, "type": "generic", "name": name, "version": version, "confirm": name}); !strings.Contains(out, "confirm=") {
		t.Fatalf("delete with a wrong confirm: %s", out)
	}
	want := owner + "/generic/" + name + "/" + version
	out = mustOK(t, "package", map[string]any{"action": "delete", "owner": owner, "type": "generic", "name": name, "version": version, "confirm": want})
	if !strings.HasPrefix(out, "ok: deleted package generic "+name+" "+version) {
		t.Fatalf("delete: %s", out)
	}
	out = mustErr(t, "package", map[string]any{"action": "get", "owner": owner, "type": "generic", "name": name, "version": version})
	if !strings.Contains(out, "404") {
		t.Fatalf("get after delete: %s", out)
	}
}

func TestPackageListEmptyAndErrors(t *testing.T) {
	skipUnlessIntegration(t)
	out := mustOK(t, "package", map[string]any{"action": "list", "owner": pkgOwner(t), "q": "no-such-" + uniqueName("pk")})
	if !strings.HasPrefix(out, "# packages 0/0") {
		t.Fatalf("empty list: %s", out)
	}
	if out = mustErr(t, "package", map[string]any{"action": "list", "owner": "no-such-owner-" + uniqueName("pk")}); !strings.Contains(out, "404") {
		t.Fatalf("unknown owner: %s", out)
	}
	if out = mustErr(t, "package", map[string]any{"action": "get", "owner": pkgOwner(t), "type": "bogus", "name": "n", "version": "1"}); !strings.Contains(out, "valid:") {
		t.Fatalf("bad type: %s", out)
	}
}

func TestPackageReadOnly(t *testing.T) {
	skipUnlessIntegration(t)
	s := newSession(t, map[string]string{"FORGEJO_READ_ONLY": "1"})
	out, isErr := s.call(t, "package", map[string]any{"action": "delete", "owner": "o", "type": "npm", "name": "n", "version": "1", "confirm": "o/npm/n/1"})
	if !isErr || !strings.Contains(out, "read-only") {
		t.Fatalf("delete in read-only mode: %q (isErr %v)", out, isErr)
	}
	if out, isErr = s.call(t, "package", map[string]any{"action": "list", "owner": pkgOwner(t)}); isErr && !strings.Contains(out, "404") {
		t.Fatalf("list in read-only mode: %s", out)
	}
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}
