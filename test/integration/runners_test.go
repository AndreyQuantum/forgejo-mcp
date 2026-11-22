package integration

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/runners"
)

var reRnRegistered = regexp.MustCompile(`registered runner (\d+) "([^"]+)"`)

// rnRegister registers a runner through the tool and returns its id and
// the text. The runner is deleted again with t.Cleanup (best effort, at the
// scope it was registered at).
func rnRegister(t *testing.T, args map[string]any) (int, string) {
	t.Helper()
	a := map[string]any{"action": "register"}
	for k, v := range args {
		a[k] = v
	}
	out := mustOK(t, "runner", a)
	m := reRnRegistered.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no runner id in register output:\n%s", out)
	}
	id, _ := strconv.Atoi(m[1])
	t.Cleanup(func() {
		d := map[string]any{"action": "delete", "id": id}
		for _, k := range []string{"scope", "repo", "org"} {
			if v, ok := args[k]; ok {
				d[k] = v
			}
		}
		_, _ = call(t, "runner", d)
	})
	return id, out
}

func rnOrg(t *testing.T) string {
	t.Helper()
	org := uniqueName("it-org")
	if st, b := rawAPI(t, http.MethodPost, "/orgs", map[string]any{"username": org}); st != http.StatusCreated {
		t.Fatalf("create org: %d %s", st, b)
	}
	t.Cleanup(func() { rawAPI(t, http.MethodDelete, "/orgs/"+org, nil) })
	return org
}

func rnRepo(t *testing.T) string {
	t.Helper()
	name := uniqueName("it-run")
	if st, b := rawAPI(t, http.MethodPost, "/user/repos", map[string]any{"name": name}); st != http.StatusCreated {
		t.Fatalf("create repo: %d %s", st, b)
	}
	st, b := rawAPI(t, http.MethodGet, "/user", nil)
	if st != http.StatusOK {
		t.Fatalf("whoami: %d %s", st, b)
	}
	m := regexp.MustCompile(`"login":"([^"]+)"`).FindStringSubmatch(string(b))
	if m == nil {
		t.Fatalf("no login in %s", b)
	}
	full := m[1] + "/" + name
	t.Cleanup(func() { rawAPI(t, http.MethodDelete, "/repos/"+full, nil) })
	return full
}

func TestRunnerRepoScope(t *testing.T) {
	skipUnlessIntegration(t)
	repo := rnRepo(t)
	name := uniqueName("it-rn")

	id, out := rnRegister(t, map[string]any{"repo": repo, "name": name, "labels": "self-hosted:host", "description": "integration"})
	if !strings.Contains(out, "(repo scope)") || !strings.Contains(out, "token: ") {
		t.Fatalf("register output:\n%s", out)
	}
	line, ok := runners.ExtractRunnerFile(out)
	if !ok || !strings.Contains(line, `"labels":["self-hosted:host"]`) || !strings.Contains(line, `"name":"`+name+`"`) {
		t.Fatalf(".runner line missing or wrong: %q", line)
	}

	list := mustOK(t, "runner", map[string]any{"action": "list", "repo": repo, "limit": 50})
	if !strings.Contains(list, "runner "+strconv.Itoa(id)+" "+name+" offline | repo") || !strings.Contains(list, "visible=true") {
		t.Errorf("list:\n%s", list)
	}
	if got := mustOK(t, "runner", map[string]any{"action": "list", "repo": repo, "status": "online"}); strings.Contains(got, name) || !strings.Contains(got, "filter: status=online") {
		t.Errorf("online filter:\n%s", got)
	}

	get := mustOK(t, "runner", map[string]any{"action": "get", "repo": repo, "id": id})
	for _, want := range []string{"runner " + strconv.Itoa(id), "name: " + name, "status: offline", "level: repo", "description: integration"} {
		if !strings.Contains(get, want) {
			t.Errorf("get lacks %q:\n%s", want, get)
		}
	}

	queue := mustOK(t, "runner", map[string]any{"action": "queue", "repo": repo})
	if !strings.HasPrefix(queue, "waiting: ") {
		t.Errorf("queue:\n%s", queue)
	}
	tok := mustOK(t, "runner", map[string]any{"action": "reg_token", "repo": repo})
	if !strings.HasPrefix(tok, "token: ") || !strings.Contains(tok, "deprecated") {
		t.Errorf("reg_token:\n%s", tok)
	}

	if del := mustOK(t, "runner", map[string]any{"action": "delete", "repo": repo, "id": id}); del != "ok: deleted runner "+strconv.Itoa(id)+" (repo scope)" {
		t.Errorf("delete: %q", del)
	}
	if list := mustOK(t, "runner", map[string]any{"action": "list", "repo": repo, "limit": 50}); strings.Contains(list, name) {
		t.Errorf("runner still listed:\n%s", list)
	}
	if e := mustErr(t, "runner", map[string]any{"action": "get", "repo": repo, "id": id}); !strings.HasPrefix(e, "error: 404") {
		t.Errorf("get after delete: %s", e)
	}
	if e := mustErr(t, "runner", map[string]any{"action": "get", "repo": repo}); !strings.Contains(e, "missing param id") {
		t.Errorf("missing id: %s", e)
	}
}

func TestRunnerOrgUserAdminScopes(t *testing.T) {
	requireAdmin(t)
	org := rnOrg(t)
	repo := rnRepo(t)

	orgName, userName, globalName := uniqueName("it-orgrn"), uniqueName("it-usrrn"), uniqueName("it-glbrn")
	orgID, out := rnRegister(t, map[string]any{"scope": "org", "org": org, "name": orgName, "ephemeral": true})
	if !strings.Contains(out, "(org scope)") || !strings.Contains(out, "note: pass labels=a,b") {
		t.Errorf("org register:\n%s", out)
	}
	userID, _ := rnRegister(t, map[string]any{"scope": "user", "name": userName})
	globalID, out := rnRegister(t, map[string]any{"scope": "admin", "name": globalName})
	if !strings.Contains(out, "(admin scope)") {
		t.Errorf("admin register:\n%s", out)
	}

	// org scope: visible=true shows the org's and the global runners, false only the org's
	vis := mustOK(t, "runner", map[string]any{"action": "list", "scope": "org", "org": org, "limit": 50})
	if !strings.Contains(vis, orgName) || !strings.Contains(vis, "| owner |") || !strings.Contains(vis, globalName) || !strings.Contains(vis, "| global") || !strings.Contains(vis, "| eph") {
		t.Errorf("org list visible=true:\n%s", vis)
	}
	exact := mustOK(t, "runner", map[string]any{"action": "list", "scope": "org", "org": org, "visible": false, "limit": 50})
	if !strings.Contains(exact, orgName) || strings.Contains(exact, globalName) || !strings.Contains(exact, "visible=false") {
		t.Errorf("org list visible=false:\n%s", exact)
	}

	// user scope
	if ul := mustOK(t, "runner", map[string]any{"action": "list", "scope": "user", "visible": false, "limit": 50}); !strings.Contains(ul, userName) || strings.Contains(ul, globalName) {
		t.Errorf("user list:\n%s", ul)
	}
	if g := mustOK(t, "runner", map[string]any{"action": "get", "scope": "user", "id": userID}); !strings.Contains(g, "name: "+userName) {
		t.Errorf("user get:\n%s", g)
	}

	// admin scope: visible=false only the global ones, visible=true everything
	if al := mustOK(t, "runner", map[string]any{"action": "list", "scope": "admin", "visible": false, "limit": 50}); !strings.Contains(al, globalName) || strings.Contains(al, orgName) || strings.Contains(al, userName) {
		t.Errorf("admin list visible=false:\n%s", al)
	}
	if al := mustOK(t, "runner", map[string]any{"action": "list", "scope": "admin", "limit": 50}); !strings.Contains(al, globalName) || !strings.Contains(al, orgName) || !strings.Contains(al, userName) {
		t.Errorf("admin list visible=true:\n%s", al)
	}

	// repo scope sees the global runner through visible=true
	if rl := mustOK(t, "runner", map[string]any{"action": "list", "repo": repo, "limit": 50}); !strings.Contains(rl, globalName) || strings.Contains(rl, orgName) {
		t.Errorf("repo list:\n%s", rl)
	}
	if q := mustOK(t, "runner", map[string]any{"action": "queue", "scope": "org", "org": org, "labels": "nosuchlabel"}); q != "waiting: 0, running: 0" {
		t.Errorf("org queue: %q", q)
	}
	if q := mustOK(t, "runner", map[string]any{"action": "queue", "scope": "admin"}); !strings.HasPrefix(q, "waiting: ") {
		t.Errorf("admin queue: %q", q)
	}

	// a global runner cannot be deleted at repo level
	e := mustErr(t, "runner", map[string]any{"action": "delete", "repo": repo, "id": globalID})
	if !strings.HasPrefix(e, "error: 404") || !strings.Contains(e, "hint:") {
		t.Errorf("cross-level delete: %s", e)
	}
	if e := mustErr(t, "runner", map[string]any{"action": "list", "scope": "org"}); !strings.Contains(e, "missing param org") {
		t.Errorf("missing org: %s", e)
	}
	if e := mustErr(t, "runner", map[string]any{"action": "list", "scope": "org", "org": "it-no-such-org-" + uniqueName("x")}); !strings.HasPrefix(e, "error: 404") {
		t.Errorf("unknown org: %s", e)
	}

	// deletes at the right level (also exercised by the cleanups)
	for _, d := range []struct {
		args map[string]any
		word string
	}{
		{map[string]any{"scope": "org", "org": org, "id": orgID}, "org scope"},
		{map[string]any{"scope": "user", "id": userID}, "user scope"},
		{map[string]any{"scope": "admin", "id": globalID}, "admin scope"},
	} {
		d.args["action"] = "delete"
		if out := mustOK(t, "runner", d.args); !strings.Contains(out, d.word) {
			t.Errorf("delete: %s", out)
		}
	}
}

func TestRunnerReadOnlyHidesMutations(t *testing.T) {
	skipUnlessIntegration(t)
	s := newSession(t, map[string]string{"FORGEJO_READ_ONLY": "1"})
	out, isErr := s.call(t, "runner", map[string]any{"action": "register", "name": "x", "scope": "admin"})
	if !isErr || !strings.Contains(out, "disabled") {
		t.Errorf("register in read-only mode: %s", out)
	}
	if out, isErr := s.call(t, "runner", map[string]any{"action": "queue", "scope": "user"}); isErr {
		t.Errorf("queue in read-only mode: %s", out)
	}
}
