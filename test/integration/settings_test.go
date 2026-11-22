package integration

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// settingsMkRepo creates a private repo owned by the admin and removes it
// when the test ends. It returns "owner/name".
func settingsMkRepo(t *testing.T) string {
	t.Helper()
	skipUnlessIntegration(t)
	name := uniqueName("it")
	st, body := rawAPI(t, http.MethodPost, "/user/repos", map[string]any{"name": name, "private": true, "auto_init": true})
	if st != http.StatusCreated {
		t.Fatalf("create repo: %d %s", st, body)
	}
	var r struct {
		FullName string `json:"full_name"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.FullName == "" {
		t.Fatalf("repo response: %s", body)
	}
	t.Cleanup(func() { rawAPI(t, http.MethodDelete, "/repos/"+r.FullName, nil) })
	return r.FullName
}

// settingsMkOrg creates an organization and removes it when the test ends.
func settingsMkOrg(t *testing.T) string {
	t.Helper()
	skipUnlessIntegration(t)
	name := uniqueName("it")
	st, body := rawAPI(t, http.MethodPost, "/orgs", map[string]any{"username": name})
	if st != http.StatusCreated {
		t.Fatalf("create org: %d %s", st, body)
	}
	t.Cleanup(func() { rawAPI(t, http.MethodDelete, "/orgs/"+name, nil) })
	return name
}

var settingsHookID = regexp.MustCompile(`created hook (\d+)`)

// settingsCreateHook creates a hook through the tool and returns its id.
func settingsCreateHook(t *testing.T, args map[string]any) int {
	t.Helper()
	args["action"] = "create"
	out := mustOK(t, "hook", args)
	m := settingsHookID.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no hook id in %q", out)
	}
	id, _ := strconv.Atoi(m[1])
	return id
}

// settingsRawHook reads a hook straight from the API.
func settingsRawHook(t *testing.T, path string) map[string]any {
	t.Helper()
	st, body := rawAPI(t, http.MethodGet, path, nil)
	if st != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, st, body)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func settingsEvents(h map[string]any) string {
	var out []string
	for _, e := range h["events"].([]any) {
		out = append(out, e.(string))
	}
	return strings.Join(out, ",")
}

func TestSettingsHookRepoLifecycle(t *testing.T) {
	skipUnlessIntegration(t)
	repo := settingsMkRepo(t)
	base := "/repos/" + repo + "/hooks/"

	id := settingsCreateHook(t, map[string]any{
		"repo": repo, "url": "http://hooks.example.test/hook", "secret": "it-secret-value",
		"events": []string{"push", "pull_request"}, "branch_filter": "main",
		"authorization_header": "Bearer it-header-value",
	})
	ids := strconv.Itoa(id)

	raw := settingsRawHook(t, base+ids)
	if raw["active"] != true || raw["branch_filter"] != "main" || raw["authorization_header"] != "Bearer it-header-value" {
		t.Fatalf("created hook: %v", raw)
	}
	createdEvents := settingsEvents(raw)

	list := mustOK(t, "hook", map[string]any{"action": "list", "repo": repo})
	if !strings.HasPrefix(list, "# hooks 1/1 page 1/1") || !strings.Contains(list, "id "+ids+" | forgejo | active | ") {
		t.Errorf("list:\n%s", list)
	}
	for _, s := range []string{"it-secret-value", "it-header-value"} {
		if strings.Contains(list, s) {
			t.Errorf("list leaks %q", s)
		}
	}

	get := mustOK(t, "hook", map[string]any{"action": "get", "repo": repo, "id": id})
	if strings.Contains(get, "it-header-value") || strings.Contains(get, "it-secret-value") {
		t.Errorf("get leaks a secret:\n%s", get)
	}
	golden(t, "settings_hook_get", settingsIDs.ReplaceAllString(strings.ReplaceAll(get, repo, "owner/repo"), "$1 <n>"))

	// Editing the URL must keep events, filter and header (read-modify-write).
	mustOK(t, "hook", map[string]any{"action": "edit", "repo": repo, "id": id, "url": "http://hooks.example.test/other"})
	raw = settingsRawHook(t, base+ids)
	if raw["url"] != "http://hooks.example.test/other" || settingsEvents(raw) != createdEvents ||
		raw["branch_filter"] != "main" || raw["authorization_header"] != "Bearer it-header-value" {
		t.Errorf("edit lost state: %v (events before: %s)", raw, createdEvents)
	}

	// An empty branch filter clears it, active=false is applied, the header is replaced.
	mustOK(t, "hook", map[string]any{"action": "edit", "repo": repo, "id": id, "branch_filter": "", "authorization_header": "Bearer it-header-2", "active": false, "events": []string{"issues"}})
	raw = settingsRawHook(t, base+ids)
	if raw["active"] != false || raw["branch_filter"] != "" || raw["authorization_header"] != "Bearer it-header-2" || !strings.HasPrefix(settingsEvents(raw), "issues") {
		t.Errorf("edit clear: %v", raw)
	}

	mustOK(t, "hook", map[string]any{"action": "edit", "repo": repo, "id": id, "authorization_header": ""})
	if raw = settingsRawHook(t, base+ids); raw["authorization_header"] != "" {
		t.Errorf("header not cleared: %v", raw)
	}
	if msg := mustErr(t, "hook", map[string]any{"action": "edit", "repo": repo, "id": id, "secret": "x"}); !strings.Contains(msg, "secret cannot be changed") {
		t.Errorf("secret edit: %s", msg)
	}

	if out := mustOK(t, "hook", map[string]any{"action": "test", "repo": repo, "id": id}); !strings.HasPrefix(out, "ok: test queued") {
		t.Errorf("test: %s", out)
	}

	if out := mustOK(t, "hook", map[string]any{"action": "delete", "repo": repo, "id": id}); !strings.HasPrefix(out, "ok: deleted hook "+ids) {
		t.Errorf("delete: %s", out)
	}
	if msg := mustErr(t, "hook", map[string]any{"action": "get", "repo": repo, "id": id}); !strings.Contains(msg, "404") {
		t.Errorf("get after delete: %s", msg)
	}
	if out := mustOK(t, "hook", map[string]any{"action": "list", "repo": repo}); out != "# hooks 0/0" {
		t.Errorf("list after delete: %s", out)
	}
}

func TestSettingsHookOrgUserAdminScopes(t *testing.T) {
	requireAdmin(t)
	org := settingsMkOrg(t)

	orgID := settingsCreateHook(t, map[string]any{"scope": "org", "org": org, "url": "http://hooks.example.test/org", "events": []string{"push"}})
	if out := mustOK(t, "hook", map[string]any{"action": "list", "scope": "org", "org": org}); !strings.Contains(out, "http://hooks.example.test/org") {
		t.Errorf("org list: %s", out)
	}
	mustOK(t, "hook", map[string]any{"action": "edit", "scope": "org", "org": org, "id": orgID, "active": false})
	if out := mustOK(t, "hook", map[string]any{"action": "get", "scope": "org", "org": org, "id": orgID}); !strings.Contains(out, "state: inactive") {
		t.Errorf("org get: %s", out)
	}
	mustOK(t, "hook", map[string]any{"action": "delete", "scope": "org", "org": org, "id": orgID})

	userID := settingsCreateHook(t, map[string]any{"scope": "user", "url": "http://hooks.example.test/user"})
	t.Cleanup(func() { rawAPI(t, http.MethodDelete, "/user/hooks/"+strconv.Itoa(userID), nil) })
	if out := mustOK(t, "hook", map[string]any{"action": "get", "scope": "user", "id": userID}); !strings.Contains(out, "url: http://hooks.example.test/user") {
		t.Errorf("user get: %s", out)
	}
	mustOK(t, "hook", map[string]any{"action": "delete", "scope": "user", "id": userID})

	adminID := settingsCreateHook(t, map[string]any{"scope": "admin", "url": "http://hooks.example.test/admin", "events": []string{"push"}})
	t.Cleanup(func() { rawAPI(t, http.MethodDelete, "/admin/hooks/"+strconv.Itoa(adminID), nil) })
	if out := mustOK(t, "hook", map[string]any{"action": "list", "scope": "admin"}); !strings.Contains(out, "http://hooks.example.test/admin") {
		t.Errorf("admin list: %s", out)
	}
	mustOK(t, "hook", map[string]any{"action": "edit", "scope": "admin", "id": adminID, "events": []string{"push", "create"}})
	mustOK(t, "hook", map[string]any{"action": "delete", "scope": "admin", "id": adminID})

	if msg := mustErr(t, "hook", map[string]any{"action": "test", "scope": "org", "org": org, "id": 1}); !strings.Contains(msg, "scope=repo only") {
		t.Errorf("org test: %s", msg)
	}
}

func TestSettingsHookReviewOnlyEdit(t *testing.T) {
	skipUnlessIntegration(t)
	repo := settingsMkRepo(t)
	id := settingsCreateHook(t, map[string]any{"repo": repo, "url": "http://hooks.example.test/rev", "events": []string{"pull_request_review"}})
	ids := strconv.Itoa(id)
	base := "/repos/" + repo + "/hooks/"
	before := settingsEvents(settingsRawHook(t, base+ids))
	if !strings.Contains(before, "pull_request_review_approved") {
		t.Fatalf("review hook events: %s", before)
	}
	mustOK(t, "hook", map[string]any{"action": "edit", "repo": repo, "id": id, "url": "http://hooks.example.test/rev2"})
	if after := settingsEvents(settingsRawHook(t, base+ids)); after != before {
		t.Errorf("edit changed events: %s -> %s", before, after)
	}
	// The names printed by get are accepted as input.
	mustOK(t, "hook", map[string]any{"action": "edit", "repo": repo, "id": id, "events": []string{"pull_request_review_approved"}})
	if after := settingsEvents(settingsRawHook(t, base+ids)); after != before {
		t.Errorf("review names not accepted: %s", after)
	}
}

func TestSettingsHookErrors(t *testing.T) {
	skipUnlessIntegration(t)
	repo := settingsMkRepo(t)
	if msg := mustErr(t, "hook", map[string]any{"action": "create", "repo": repo, "url": "not a url"}); !strings.Contains(msg, "422") {
		t.Errorf("bad url: %s", msg)
	}
	if msg := mustErr(t, "hook", map[string]any{"action": "get", "repo": repo, "id": 999999}); !strings.Contains(msg, "404") {
		t.Errorf("missing hook: %s", msg)
	}
	if msg := mustErr(t, "hook", map[string]any{"action": "edit", "repo": repo, "id": 999999, "active": true}); !strings.Contains(msg, "404") {
		t.Errorf("edit missing hook: %s", msg)
	}
}

// settingsPublicKey returns a fresh ed25519 key in authorized_keys format.
func settingsPublicKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var blob []byte
	for _, part := range [][]byte{[]byte("ssh-ed25519"), pub} {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(part)))
		blob = append(append(blob, n[:]...), part...)
	}
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " it-key"
}

func TestSettingsAccessCollaborator(t *testing.T) {
	requireAdmin(t)
	repo := settingsMkRepo(t)

	user := uniqueName("it-usr")
	st, body := rawAPI(t, http.MethodPost, "/admin/users", map[string]any{
		"username": user, "email": user + "@example.invalid", "password": uniqueName("Pw") + "-Aa1!x",
		"must_change_password": false,
	})
	if st != http.StatusCreated {
		t.Fatalf("create user: %d %s", st, body)
	}
	t.Cleanup(func() { rawAPI(t, http.MethodDelete, "/admin/users/"+user+"?purge=true", nil) })

	if out := mustOK(t, "access", map[string]any{"action": "list", "repo": repo, "kind": "collaborator"}); out != "# collaborators 0/0" {
		t.Errorf("empty list: %s", out)
	}
	if out := mustOK(t, "access", map[string]any{"action": "add", "repo": repo, "kind": "collaborator", "user": user}); out != "ok: added collaborator "+user+" (write) to "+repo {
		t.Errorf("add: %s", out)
	}
	if out := mustOK(t, "access", map[string]any{"action": "list", "repo": repo, "kind": "collaborator"}); out != "# collaborators 1/1 page 1/1\n"+user {
		t.Errorf("list: %s", out)
	}
	if out := mustOK(t, "access", map[string]any{"action": "permission", "repo": repo, "kind": "collaborator", "user": user}); !strings.HasPrefix(out, user+": write") {
		t.Errorf("permission: %s", out)
	}
	mustOK(t, "access", map[string]any{"action": "add", "repo": repo, "kind": "collaborator", "user": user, "permission": "read"})
	if out := mustOK(t, "access", map[string]any{"action": "permission", "repo": repo, "kind": "collaborator", "user": user}); !strings.HasPrefix(out, user+": read") {
		t.Errorf("permission after downgrade: %s", out)
	}
	if out := mustOK(t, "access", map[string]any{"action": "remove", "repo": repo, "kind": "collaborator", "user": user}); !strings.HasPrefix(out, "ok: removed collaborator "+user) {
		t.Errorf("remove: %s", out)
	}
	if out := mustOK(t, "access", map[string]any{"action": "list", "repo": repo, "kind": "collaborator"}); out != "# collaborators 0/0" {
		t.Errorf("list after remove: %s", out)
	}
}

var settingsFingerprint = regexp.MustCompile(`SHA256:[A-Za-z0-9+/]+`)

// settingsIDs masks server-assigned numeric ids in golden output.
var settingsIDs = regexp.MustCompile(`(hook|id) \d+`)

var settingsKeyID = regexp.MustCompile(`added deploy key (\d+)`)

func TestSettingsAccessDeployKey(t *testing.T) {
	skipUnlessIntegration(t)
	repo := settingsMkRepo(t)

	out := mustOK(t, "access", map[string]any{"action": "add", "repo": repo, "kind": "key", "title": "it-ro", "key": settingsPublicKey(t)})
	m := settingsKeyID.FindStringSubmatch(out)
	if m == nil || !strings.Contains(out, "(ro)") {
		t.Fatalf("add ro key: %s", out)
	}
	roID, _ := strconv.Atoi(m[1])

	out = mustOK(t, "access", map[string]any{"action": "add", "repo": repo, "kind": "key", "title": "it-rw", "key": settingsPublicKey(t), "read_only": false})
	if !strings.Contains(out, "(read-write)") {
		t.Errorf("add rw key: %s", out)
	}

	list := mustOK(t, "access", map[string]any{"action": "list", "repo": repo, "kind": "key"})
	if !strings.HasPrefix(list, "# keys 2/2 page 1/1") || !strings.Contains(list, "| it-ro | SHA256:") || !strings.Contains(list, "it-rw | SHA256:") {
		t.Errorf("list:\n%s", list)
	}
	for _, line := range strings.Split(list, "\n") {
		if strings.Contains(line, "it-ro") && !strings.HasSuffix(line, "| ro") {
			t.Errorf("ro flag missing: %s", line)
		}
		if strings.Contains(line, "it-rw") && strings.HasSuffix(line, "| ro") {
			t.Errorf("rw key marked ro: %s", line)
		}
	}
	golden(t, "settings_access_keys", settingsIDs.ReplaceAllString(settingsFingerprint.ReplaceAllString(list, "SHA256:<fp>"), "$1 <n>"))

	if msg := mustErr(t, "access", map[string]any{"action": "add", "repo": repo, "kind": "key", "title": "bad", "key": "not-a-key"}); !strings.Contains(msg, "error:") {
		t.Errorf("bad key: %s", msg)
	}
	if out := mustOK(t, "access", map[string]any{"action": "remove", "repo": repo, "kind": "key", "id": roID}); !strings.HasPrefix(out, "ok: removed deploy key") {
		t.Errorf("remove: %s", out)
	}
	if out := mustOK(t, "access", map[string]any{"action": "list", "repo": repo, "kind": "key"}); !strings.HasPrefix(out, "# keys 1/1") {
		t.Errorf("list after remove: %s", out)
	}
}
