package integration

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Integration tests of the repos area (repo, branch, tag, wiki) against a
// real Forgejo 17.0.2 (DESIGN.md section 12.2). Every test creates its own
// repositories and organisations with random names and removes them again.

// reposWhoami returns the login of the token owner.
func reposWhoami(t *testing.T) string {
	t.Helper()
	st, body := rawAPI(t, http.MethodGet, "/user", nil)
	if st != http.StatusOK {
		t.Fatalf("GET /user -> %d", st)
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &u); err != nil || u.Login == "" {
		t.Fatalf("GET /user: bad body")
	}
	return u.Login
}

// reposNewRepo creates a repository with a first commit on main through the
// raw API (so the test of one tool does not depend on another) and removes it
// on cleanup. It returns owner/name.
func reposNewRepo(t *testing.T, owner string) string {
	t.Helper()
	name := uniqueName("it-repo")
	path := "/user/repos"
	if owner != "" {
		path = "/orgs/" + owner + "/repos"
	}
	st, body := rawAPI(t, http.MethodPost, path, map[string]any{
		"name": name, "auto_init": true, "default_branch": "main", "private": true,
	})
	if st != http.StatusCreated {
		t.Fatalf("create repo: %d %s", st, body)
	}
	var r struct {
		FullName string `json:"full_name"`
	}
	_ = json.Unmarshal(body, &r)
	t.Cleanup(func() { rawAPI(t, http.MethodDelete, "/repos/"+r.FullName, nil) })
	return r.FullName
}

// reposNewOrg creates an organisation and removes it on cleanup. Cleanups
// run last-in first-out, so repositories created afterwards go first.
func reposNewOrg(t *testing.T) string {
	t.Helper()
	org := uniqueName("it-org")
	if st, body := rawAPI(t, http.MethodPost, "/orgs", map[string]any{"username": org}); st != http.StatusCreated {
		t.Fatalf("create org: %d %s", st, body)
	}
	t.Cleanup(func() { rawAPI(t, http.MethodDelete, "/orgs/"+org, nil) })
	return org
}

func reposWant(t *testing.T, what, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("%s: missing %q in:\n%s", what, w, got)
		}
	}
}

func TestReposRepoLifecycle(t *testing.T) {
	skipUnlessIntegration(t)
	me := reposWhoami(t)
	name := uniqueName("it-life")
	full := me + "/" + name
	t.Cleanup(func() { rawAPI(t, http.MethodDelete, "/repos/"+full, nil) })

	out := mustOK(t, "repo", map[string]any{"action": "create", "name": name, "description": "integration", "private": true})
	reposWant(t, "create", out, "ok: created "+full+" (private)")

	out = mustOK(t, "repo", map[string]any{"action": "get", "repo": full})
	reposWant(t, "get", out, "repo "+full, "visibility: private", "default_branch: main", "perms: admin")
	out = mustOK(t, "repo", map[string]any{"action": "get", "repo": full, "verbose": true})
	reposWant(t, "get verbose", out, "clone_url:", "merge_styles:")

	out = mustOK(t, "repo", map[string]any{"action": "edit", "repo": full, "set": map[string]any{"description": "changed", "has_wiki": true}})
	reposWant(t, "edit", out, "ok: edited "+full+": description,has_wiki")
	mustOK(t, "repo", map[string]any{"action": "get", "repo": full})
	reposWant(t, "edit applied", mustOK(t, "repo", map[string]any{"action": "get", "repo": full}), "description: changed")
	reposWant(t, "edit bad key", mustErr(t, "repo", map[string]any{"action": "edit", "repo": full, "set": map[string]any{"bogus": 1}}), "unknown set key bogus")

	out = mustOK(t, "repo", map[string]any{"action": "topics_set", "repo": full, "topics": []string{"it-test", "mcp"}})
	reposWant(t, "topics_set", out, "ok: topics set: it-test,mcp")
	reposWant(t, "topics", mustOK(t, "repo", map[string]any{"action": "topics", "repo": full}), "topics: it-test,mcp")
	reposWant(t, "topics invalid", mustErr(t, "repo", map[string]any{"action": "topics_set", "repo": full, "topics": []string{"Bad Topic!"}}), "422")
	reposWant(t, "languages", mustOK(t, "repo", map[string]any{"action": "languages", "repo": full}), "")

	// list and search find the new repository
	out = mustOK(t, "repo", map[string]any{"action": "list", "sort": "created", "limit": 50})
	reposWant(t, "list mine", out, "# repos", full)
	out = mustOK(t, "repo", map[string]any{"action": "list", "which": "user", "owner": me, "limit": 50})
	reposWant(t, "list user", out, "(page may be short)", full)
	out = mustOK(t, "repo", map[string]any{"action": "search", "q": name, "private": true})
	reposWant(t, "search", out, full)
	out = mustOK(t, "repo", map[string]any{"action": "search", "q": name, "private": false})
	if strings.Contains(out, full) {
		t.Errorf("private=false search returned the private repo:\n%s", out)
	}

	// star and unstar
	reposWant(t, "star", mustOK(t, "repo", map[string]any{"action": "star", "repo": full}), "ok: starred "+full)
	reposWant(t, "starred", mustOK(t, "repo", map[string]any{"action": "list", "which": "starred", "limit": 50}), full)
	reposWant(t, "unstar", mustOK(t, "repo", map[string]any{"action": "unstar", "repo": full}), "ok: unstarred "+full)

	// destructive delete needs the exact target
	reposWant(t, "delete without confirm", mustErr(t, "repo", map[string]any{"action": "delete", "repo": full}), "confirm=")
	reposWant(t, "delete wrong confirm", mustErr(t, "repo", map[string]any{"action": "delete", "repo": full, "confirm": name}), `confirm="`+full+`"`)
	reposWant(t, "delete", mustOK(t, "repo", map[string]any{"action": "delete", "repo": full, "confirm": full}), "ok: deleted "+full+" (irreversible)")
	reposWant(t, "get after delete", mustErr(t, "repo", map[string]any{"action": "get", "repo": full}), "404")
}

func TestReposRepoOrgTransferFork(t *testing.T) {
	skipUnlessIntegration(t)
	org := reposNewOrg(t)
	other := reposNewOrg(t)

	// create inside an org
	name := uniqueName("it-orgrepo")
	t.Cleanup(func() { rawAPI(t, http.MethodDelete, "/repos/"+org+"/"+name, nil) })
	out := mustOK(t, "repo", map[string]any{"action": "create", "name": name, "owner": org, "auto_init": true})
	full := org + "/" + name
	reposWant(t, "org create", out, "ok: created "+full)
	reposWant(t, "list org", mustOK(t, "repo", map[string]any{"action": "list", "which": "org", "owner": org}), full)

	// duplicate gives a 409 with a hint
	reposWant(t, "duplicate", mustErr(t, "repo", map[string]any{"action": "create", "name": name, "owner": org}), "409", "hint:")

	// fork into the other org, then report fork status
	out = mustOK(t, "repo", map[string]any{"action": "fork", "repo": full, "owner": other, "name": "forked"})
	reposWant(t, "fork", out, "ok: forked to "+other+"/forked")
	t.Cleanup(func() { rawAPI(t, http.MethodDelete, "/repos/"+other+"/forked", nil) })
	reposWant(t, "forks", mustOK(t, "repo", map[string]any{"action": "forks", "repo": full}), other+"/forked")
	reposWant(t, "fork_status", mustOK(t, "repo", map[string]any{"action": "fork_status", "repo": other + "/forked"}), "up to date")
	// a fresh fork is not behind its parent, so the server refuses to sync it
	reposWant(t, "fork_sync", mustErr(t, "repo", map[string]any{"action": "fork_sync", "repo": other + "/forked"}), "400", "hint:")
	reposWant(t, "fork_status on a non-fork", mustErr(t, "repo", map[string]any{"action": "fork_status", "repo": full}), "400")

	// transfer between organisations (admin: done immediately)
	out = mustOK(t, "repo", map[string]any{"action": "transfer", "repo": full, "new_owner": other})
	reposWant(t, "transfer", out, "ok:")
	moved := other + "/" + name
	t.Cleanup(func() { rawAPI(t, http.MethodDelete, "/repos/"+moved, nil) })
	reposWant(t, "get after transfer", mustOK(t, "repo", map[string]any{"action": "get", "repo": moved}), "repo "+moved)
}

func TestReposPushMirror(t *testing.T) {
	skipUnlessIntegration(t)
	full := reposNewRepo(t, "")
	out := mustOK(t, "repo", map[string]any{"action": "pushmirror_list", "repo": full})
	reposWant(t, "empty list", out, "# push mirrors 0")
	// the server rejects credentials in the URL and unreachable targets; the
	// point here is that the request is well-formed (interval always sent)
	text, isErr := call(t, "repo", map[string]any{
		"action": "pushmirror_add", "repo": full, "remote_address": "https://example.invalid/x/y.git", "interval": "0",
	})
	t.Logf("pushmirror_add (isErr=%v): %s", isErr, text)
	if isErr && strings.Contains(text, "interval") {
		t.Errorf("interval must always be accepted: %s", text)
	}
	if !isErr {
		out = mustOK(t, "repo", map[string]any{"action": "pushmirror_list", "repo": full})
		reposWant(t, "list after add", out, "remote_mirror_", "on-commit")
		name := strings.Fields(strings.SplitN(out, "\n", 3)[1])[0]
		mustOK(t, "repo", map[string]any{"action": "pushmirror_delete", "repo": full, "remote_name": name})
	}
	reposWant(t, "ssh + credentials", mustErr(t, "repo", map[string]any{
		"action": "pushmirror_add", "repo": full, "remote_address": "git@example.invalid:x/y.git", "use_ssh": true, "auth_token": "x",
	}), "use_ssh cannot be combined with credentials")
}

func TestReposBranches(t *testing.T) {
	skipUnlessIntegration(t)
	me := reposWhoami(t)
	full := reposNewRepo(t, "")

	reposWant(t, "list", mustOK(t, "branch", map[string]any{"action": "list", "repo": full}), "# branches 1/1", "main |")
	out := mustOK(t, "branch", map[string]any{"action": "create", "repo": full, "name": "feat/one", "from": "main"})
	reposWant(t, "create", out, "ok: created feat/one from main@")
	reposWant(t, "list filtered", mustOK(t, "branch", map[string]any{"action": "list", "repo": full, "q": "feat"}), "feat/one", "filtered client-side")
	reposWant(t, "get slash", mustOK(t, "branch", map[string]any{"action": "get", "repo": full, "name": "feat/one"}), "branch feat/one")
	reposWant(t, "create exists", mustErr(t, "branch", map[string]any{"action": "create", "repo": full, "name": "feat/one"}), "409")
	reposWant(t, "create bad from", mustErr(t, "branch", map[string]any{"action": "create", "repo": full, "name": "x", "from": "no-such-ref"}), "error:")

	reposWant(t, "rename", mustOK(t, "branch", map[string]any{"action": "rename", "repo": full, "name": "feat/one", "new_name": "feat/two"}), "ok: renamed branch feat/one -> feat/two")
	reposWant(t, "get renamed", mustOK(t, "branch", map[string]any{"action": "get", "repo": full, "name": "feat/two"}), "branch feat/two")

	// protection round trip with a glob rule (one escaped path segment)
	out = mustOK(t, "branch", map[string]any{"action": "protect_set", "repo": full, "rule": "release/*", "set": map[string]any{"required_approvals": 1, "enable_status_check": true, "status_check_contexts": []string{"ci/*"}}})
	reposWant(t, "protect create", out, "ok: protection release/* created:")
	out = mustOK(t, "branch", map[string]any{"action": "protect_set", "repo": full, "rule": "release/*", "set": map[string]any{"required_approvals": 2}})
	reposWant(t, "protect update", out, "ok: protection release/* updated: required_approvals")
	out = mustOK(t, "branch", map[string]any{"action": "protect_get", "repo": full, "rule": "release/*"})
	reposWant(t, "protect get", out, "protection release/*", "required_approvals: 2", "status_check_contexts: ci/*")
	out = mustOK(t, "branch", map[string]any{"action": "protect_list", "repo": full})
	reposWant(t, "protect list", out, "# protections 1/1", "rule release/* | approvals:2 | checks:ci/*")

	// whitelist options need enable_push:true, which the tool adds
	out = mustOK(t, "branch", map[string]any{"action": "protect_set", "repo": full, "rule": "main", "set": map[string]any{"enable_push_whitelist": true, "push_whitelist_usernames": []string{me}}})
	reposWant(t, "whitelist", out, "ok: protection main created:", "note: enable_push:true added")
	out = mustOK(t, "branch", map[string]any{"action": "protect_get", "repo": full, "rule": "main"})
	reposWant(t, "whitelist applied", out, "enable_push: true", "enable_push_whitelist: true", "push_whitelist_usernames: "+me)
	reposWant(t, "branch get shows protection", mustOK(t, "branch", map[string]any{"action": "get", "repo": full, "name": "main"}), "protected: yes")
	reposWant(t, "default branch delete", mustErr(t, "branch", map[string]any{"action": "delete", "repo": full, "name": "main"}), "403")

	reposWant(t, "protect delete", mustOK(t, "branch", map[string]any{"action": "protect_delete", "repo": full, "rule": "release/*"}), "ok: deleted protection release/*")
	mustOK(t, "branch", map[string]any{"action": "protect_delete", "repo": full, "rule": "main"})
	reposWant(t, "protect get gone", mustErr(t, "branch", map[string]any{"action": "protect_get", "repo": full, "rule": "release/*"}), "404")
	reposWant(t, "delete", mustOK(t, "branch", map[string]any{"action": "delete", "repo": full, "name": "feat/two"}), "ok: deleted branch feat/two")
	reposWant(t, "get deleted", mustErr(t, "branch", map[string]any{"action": "get", "repo": full, "name": "feat/two"}), "404")
}

func TestReposTags(t *testing.T) {
	skipUnlessIntegration(t)
	me := reposWhoami(t)
	full := reposNewRepo(t, "")

	reposWant(t, "create light", mustOK(t, "tag", map[string]any{"action": "create", "repo": full, "name": "v0.1.0", "target": "main"}), "ok: created tag v0.1.0 at", "(lightweight)")
	reposWant(t, "create annotated", mustOK(t, "tag", map[string]any{"action": "create", "repo": full, "name": "v0.2.0", "message": "second release"}), "(annotated)")
	reposWant(t, "create exists", mustErr(t, "tag", map[string]any{"action": "create", "repo": full, "name": "v0.1.0"}), "409")
	reposWant(t, "list", mustOK(t, "tag", map[string]any{"action": "list", "repo": full}), "# tags 2/2", "v0.1.0 |", "v0.2.0 |", "second release")

	reposWant(t, "get light", mustOK(t, "tag", map[string]any{"action": "get", "repo": full, "name": "v0.1.0", "verbose": true}), "tag v0.1.0", "type: lightweight")
	out := mustOK(t, "tag", map[string]any{"action": "get", "repo": full, "name": "v0.2.0", "verbose": true})
	reposWant(t, "get annotated", out, "type: annotated", "message: second release", "tagger:")

	// protection
	out = mustOK(t, "tag", map[string]any{"action": "protect_set", "repo": full, "set": map[string]any{"name_pattern": "v*", "whitelist_usernames": []string{me}}})
	reposWant(t, "protect create", out, "ok: tag protection created: id ")
	fields := strings.Fields(out)
	id := ""
	for i, f := range fields {
		if f == "id" && i+1 < len(fields) {
			id = fields[i+1]
		}
	}
	if id == "" {
		t.Fatalf("no id in %q", out)
	}
	var rid int
	if err := json.Unmarshal([]byte(id), &rid); err != nil {
		t.Fatalf("id %q: %v", id, err)
	}
	reposWant(t, "protect list", mustOK(t, "tag", map[string]any{"action": "protect_list", "repo": full}), "v* | users:"+me)
	reposWant(t, "protect get", mustOK(t, "tag", map[string]any{"action": "protect_get", "repo": full, "rule_id": rid}), "v* | users:"+me)
	reposWant(t, "protect update", mustOK(t, "tag", map[string]any{"action": "protect_set", "repo": full, "rule_id": rid, "set": map[string]any{"name_pattern": "rel-*"}}), "updated: name_pattern")
	reposWant(t, "protect both empty", mustErr(t, "tag", map[string]any{"action": "protect_set", "repo": full, "set": map[string]any{"name_pattern": "x*"}}), "are empty")
	reposWant(t, "protect delete", mustOK(t, "tag", map[string]any{"action": "protect_delete", "repo": full, "rule_id": rid}), "ok: deleted tag protection")

	reposWant(t, "delete", mustOK(t, "tag", map[string]any{"action": "delete", "repo": full, "name": "v0.1.0"}), "ok: deleted tag v0.1.0")
	reposWant(t, "get deleted", mustErr(t, "tag", map[string]any{"action": "get", "repo": full, "name": "v0.1.0"}), "404")
}

func TestReposWiki(t *testing.T) {
	skipUnlessIntegration(t)
	full := reposNewRepo(t, "")

	reposWant(t, "create", mustOK(t, "wiki", map[string]any{"action": "create", "repo": full, "title": "Home", "content": "# Hello\nfirst line\n", "message": "init"}), "ok: created wiki page Home")
	reposWant(t, "create exists", mustErr(t, "wiki", map[string]any{"action": "create", "repo": full, "title": "Home", "content": "x"}), "error:")
	reposWant(t, "create spaces", mustOK(t, "wiki", map[string]any{"action": "create", "repo": full, "title": "Getting Started", "content": "steps"}), "ok: created wiki page Getting Started")

	out := mustOK(t, "wiki", map[string]any{"action": "list", "repo": full})
	reposWant(t, "list", out, "# wiki pages 2/2", "Home | Home", "Getting Started |")
	sub := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "Getting Started | ") {
			sub = strings.Fields(strings.TrimPrefix(l, "Getting Started | "))[0]
		}
	}
	if sub == "" {
		t.Fatalf("no sub_url for the page with a space:\n%s", out)
	}
	reposWant(t, "get sub_url", mustOK(t, "wiki", map[string]any{"action": "get", "repo": full, "page_name": sub}), "wiki Getting Started", "steps")
	reposWant(t, "get", mustOK(t, "wiki", map[string]any{"action": "get", "repo": full, "page_name": "Home"}), "wiki Home", "# Hello", "first line")

	reposWant(t, "edit", mustOK(t, "wiki", map[string]any{"action": "edit", "repo": full, "page_name": "Home", "content": "# Hello\nsecond version\n", "message": "edit"}), "ok: edited wiki page Home")
	reposWant(t, "edit applied", mustOK(t, "wiki", map[string]any{"action": "get", "repo": full, "page_name": "Home"}), "second version")
	out = mustOK(t, "wiki", map[string]any{"action": "edit", "repo": full, "page_name": "Home", "title": "Welcome"})
	reposWant(t, "rename", out, "ok: edited wiki page Welcome")
	reposWant(t, "renamed keeps content", mustOK(t, "wiki", map[string]any{"action": "get", "repo": full, "page_name": "Welcome"}), "second version")
	reposWant(t, "old name gone", mustErr(t, "wiki", map[string]any{"action": "get", "repo": full, "page_name": "Home"}), "404")

	reposWant(t, "history", mustOK(t, "wiki", map[string]any{"action": "history", "repo": full, "page_name": "Welcome"}), "# revisions", "|")
	reposWant(t, "delete", mustOK(t, "wiki", map[string]any{"action": "delete", "repo": full, "page_name": "Welcome"}), "ok: deleted wiki page Welcome")
	reposWant(t, "delete again", mustErr(t, "wiki", map[string]any{"action": "delete", "repo": full, "page_name": "Welcome"}), "404")
}

func TestReposReadOnlyHidesWrites(t *testing.T) {
	skipUnlessIntegration(t)
	s := newSession(t, map[string]string{"FORGEJO_READ_ONLY": "1"})
	full := reposNewRepo(t, "")
	text, isErr := s.call(t, "repo", map[string]any{"action": "get", "repo": full})
	if isErr || !strings.Contains(text, "repo "+full) {
		t.Fatalf("read in read-only mode: %s", text)
	}
	text, isErr = s.call(t, "repo", map[string]any{"action": "delete", "repo": full, "confirm": full})
	if !isErr || !strings.Contains(text, "disabled (read-only mode)") && !strings.Contains(text, "valid:") {
		t.Fatalf("delete must be refused in read-only mode: %s", text)
	}
	text, isErr = s.call(t, "branch", map[string]any{"action": "create", "repo": full, "name": "nope"})
	if !isErr {
		t.Fatalf("branch create must be refused in read-only mode: %s", text)
	}
	// the repository is untouched
	mustOK(t, "repo", map[string]any{"action": "get", "repo": full})
}
