package integration

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// issuesName returns a unique repo/org name that always matches the random
// suffix pattern of stabilize.
func issuesName() string { return fmt.Sprintf("it-iss1%06x", rand.IntN(1<<24)) }

// issuesMkRepo creates a repo with the default issue settings owned by the
// admin and removes it when the test ends. It returns "owner/name".
func issuesMkRepo(t *testing.T) string {
	t.Helper()
	skipUnlessIntegration(t)
	st, body := rawAPI(t, http.MethodPost, "/user/repos", map[string]any{"name": issuesName(), "private": true, "auto_init": true})
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

// issuesMe returns the login of the token owner.
func issuesMe(t *testing.T) string {
	t.Helper()
	st, body := rawAPI(t, http.MethodGet, "/user", nil)
	if st != http.StatusOK {
		t.Fatalf("GET /user: %d", st)
	}
	var u struct {
		Login string `json:"login"`
	}
	_ = json.Unmarshal(body, &u)
	return u.Login
}

var (
	issuesCreated   = regexp.MustCompile(`created issue #(\d+)`)
	issuesCommentID = regexp.MustCompile(`comment c(\d+) on`)
	issuesIDRe      = regexp.MustCompile(`\(id (\d+)\)`)
	issuesAttach    = regexp.MustCompile(`uploaded attachment (\d+) `)
)

func issuesNum(t *testing.T, re *regexp.Regexp, text string) int {
	t.Helper()
	m := re.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no id in %q", text)
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func issuesWant(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("output lacks %q:\n%s", w, got)
		}
	}
}

func issuesNot(t *testing.T, got string, unwanted ...string) {
	t.Helper()
	for _, w := range unwanted {
		if strings.Contains(got, w) {
			t.Errorf("output must not contain %q:\n%s", w, got)
		}
	}
}

func TestIssuesLifecycle(t *testing.T) {
	skipUnlessIntegration(t)
	repo := issuesMkRepo(t)
	me := issuesMe(t)
	a := func(args map[string]any) map[string]any { args["repo"] = repo; return args }

	// labels and a milestone by name
	mustOK(t, "label", a(map[string]any{"action": "create", "name": "bug", "color": "d73a4a"}))
	mustOK(t, "label", a(map[string]any{"action": "create", "name": "ui", "color": "0075ca"}))
	mustOK(t, "milestone", a(map[string]any{"action": "create", "title": "v1.0", "due": "2030-01-31"}))

	out := mustOK(t, "issue", a(map[string]any{
		"action": "create", "title": "Login fails", "body": "steps\nto reproduce",
		"labels": []string{"bug"}, "milestone": "v1.0", "assignees": []string{me}, "due": "2030-01-15",
	}))
	n := issuesNum(t, issuesCreated, out)

	got := mustOK(t, "issue", a(map[string]any{"action": "get", "n": n}))
	issuesWant(t, got, fmt.Sprintf("issue #%d", n), "state: open", "title: Login fails", "labels: [bug]",
		"assignees: @"+me, "milestone: v1.0", "due: 2030-01-15", "body: steps\n  to reproduce")

	// unknown label names are rejected before the server silently drops them
	errText := mustErr(t, "issue", a(map[string]any{"action": "create", "title": "x", "labels": []string{"buug"}}))
	issuesWant(t, errText, `unknown label "buug"; known: `, "bug")
	mustErr(t, "issue", a(map[string]any{"action": "list", "labels": []string{"buug"}}))

	// edit: fields and labels
	out = mustOK(t, "issue", a(map[string]any{
		"action": "edit", "n": n, "title": "Login fails on Safari", "add_labels": []string{"ui"}, "remove_labels": []string{"bug"},
	}))
	issuesWant(t, out, fmt.Sprintf("ok: edited #%d: title,labels+ui,labels-bug", n))
	got = mustOK(t, "issue", a(map[string]any{"action": "get", "n": n}))
	issuesWant(t, got, "title: Login fails on Safari", "labels: [ui]")

	out = mustOK(t, "issue", a(map[string]any{"action": "edit", "n": n, "set_labels": []string{"bug", "ui"}, "milestone": "none", "unset_due": true}))
	issuesWant(t, out, "labels=bug,ui")
	got = mustOK(t, "issue", a(map[string]any{"action": "get", "n": n}))
	issuesNot(t, got, "milestone:", "due:")
	mustOK(t, "issue", a(map[string]any{"action": "edit", "n": n, "set_labels": []string{}, "assignees": []string{}, "body": ""}))
	got = mustOK(t, "issue", a(map[string]any{"action": "get", "n": n}))
	issuesNot(t, got, "labels:", "assignees:", "body:")

	// list / search
	mustOK(t, "issue", a(map[string]any{"action": "edit", "n": n, "add_labels": []string{"bug"}}))
	list := mustOK(t, "issue", a(map[string]any{"action": "list", "labels": []string{"bug"}}))
	issuesWant(t, list, "# issues 1/1 page 1/1", fmt.Sprintf("#%d open issue | Login fails on Safari", n), "[bug]")
	list = mustOK(t, "issue", a(map[string]any{"action": "list", "state": "closed"}))
	issuesWant(t, list, "# issues 0/0")
	search := mustOK(t, "issue", map[string]any{"action": "search", "owner": strings.Split(repo, "/")[0], "flags": []string{"created"}})
	issuesWant(t, search, repo+fmt.Sprintf("#%d open issue", n))

	// lock / unlock (409 on repeats is an ok)
	issuesWant(t, mustOK(t, "issue", a(map[string]any{"action": "lock", "n": n})), "ok: locked #")
	issuesWant(t, mustOK(t, "issue", a(map[string]any{"action": "lock", "n": n})), "already locked")
	got = mustOK(t, "issue", a(map[string]any{"action": "get", "n": n}))
	issuesWant(t, got, "locked: yes")
	issuesWant(t, mustOK(t, "issue", a(map[string]any{"action": "unlock", "n": n})), "ok: unlocked #")
	issuesWant(t, mustOK(t, "issue", a(map[string]any{"action": "unlock", "n": n})), "already unlocked")

	// pin / unpin, reaction, subscription
	mustOK(t, "issue", a(map[string]any{"action": "pin", "n": n}))
	mustOK(t, "issue", a(map[string]any{"action": "unpin", "n": n}))
	issuesWant(t, mustOK(t, "issue", a(map[string]any{"action": "react", "n": n, "content": "+1"})), "ok: reacted +1 on")
	issuesWant(t, mustOK(t, "issue", a(map[string]any{"action": "react", "n": n, "content": "+1", "remove": true})), "removed reaction")
	issuesWant(t, mustOK(t, "issue", a(map[string]any{"action": "subscribe", "n": n})), "subscribed to", "as "+me)
	mustOK(t, "issue", a(map[string]any{"action": "unsubscribe", "n": n}))

	issuesWant(t, mustOK(t, "issue", a(map[string]any{"action": "assignable"})), "# assignees", me)
	issuesWant(t, mustOK(t, "issue", a(map[string]any{"action": "templates"})), "# templates 0")

	// close and reopen through edit
	mustOK(t, "issue", a(map[string]any{"action": "edit", "n": n, "closed": true}))
	issuesWant(t, mustOK(t, "issue", a(map[string]any{"action": "list", "state": "closed"})), fmt.Sprintf("#%d closed issue", n))
	mustOK(t, "issue", a(map[string]any{"action": "edit", "n": n, "state": "open"}))

	// delete needs the exact confirm
	issuesWant(t, mustErr(t, "issue", a(map[string]any{"action": "delete", "n": n})), "destructive")
	issuesWant(t, mustErr(t, "issue", a(map[string]any{"action": "delete", "n": n, "confirm": "wrong"})), `destructive: pass confirm="`+repo+fmt.Sprintf(`#%d"`, n))
	issuesWant(t, mustOK(t, "issue", a(map[string]any{"action": "delete", "n": n, "confirm": fmt.Sprintf("%s#%d", repo, n)})), "ok: deleted")
	mustErr(t, "issue", a(map[string]any{"action": "get", "n": n}))
}

func TestIssuesComments(t *testing.T) {
	skipUnlessIntegration(t)
	repo := issuesMkRepo(t)
	a := func(args map[string]any) map[string]any { args["repo"] = repo; return args }
	n := issuesNum(t, issuesCreated, mustOK(t, "issue", a(map[string]any{"action": "create", "title": "Thread"})))

	var ids []int
	for i := 1; i <= 12; i++ {
		out := mustOK(t, "comment", a(map[string]any{"action": "add", "n": n, "body": fmt.Sprintf("comment number %d", i)}))
		ids = append(ids, issuesNum(t, issuesCommentID, out))
	}
	got := mustOK(t, "comment", a(map[string]any{"action": "list", "n": n}))
	issuesWant(t, got, "# comments 10/12 (newest 10; pass last=N or page=)", "comment number 12", "comment number 3")
	issuesNot(t, got, "comment number 2\n", "comment number 1\n")
	got = mustOK(t, "comment", a(map[string]any{"action": "list", "n": n, "last": 2}))
	issuesWant(t, got, "# comments 2/12", "comment number 12", "comment number 11")
	got = mustOK(t, "comment", a(map[string]any{"action": "list", "n": n, "page": 2, "limit": 5}))
	issuesWant(t, got, "# comments 5/12 page 2/3 next: page=3", "comment number 6", "comment number 10")
	got = mustOK(t, "comment", a(map[string]any{"action": "list", "n": n, "from_id": ids[9]}))
	issuesWant(t, got, "# comments 2/12", "comment number 11", "comment number 12")

	// edit and delete a plain comment
	issuesWant(t, mustOK(t, "comment", a(map[string]any{"action": "edit", "id": ids[0], "body": "edited text"})), fmt.Sprintf("ok: edited comment c%d", ids[0]))
	issuesWant(t, mustOK(t, "comment", a(map[string]any{"action": "list", "n": n, "last": 12})), "edited text")
	issuesWant(t, mustOK(t, "comment", a(map[string]any{"action": "delete", "id": ids[1]})), "ok: deleted comment")
	got = mustErr(t, "comment", a(map[string]any{"action": "delete", "id": ids[1]}))
	issuesWant(t, got, "404")

	// events and the repo-wide feed
	mustOK(t, "label", a(map[string]any{"action": "create", "name": "bug", "color": "d73a4a"}))
	mustOK(t, "issue", a(map[string]any{"action": "edit", "n": n, "add_labels": []string{"bug"}, "title": "Thread 2"}))
	mustOK(t, "issue", a(map[string]any{"action": "edit", "n": n, "closed": true}))
	tl := mustOK(t, "comment", a(map[string]any{"action": "timeline", "n": n, "limit": 50}))
	issuesWant(t, tl, "# timeline ", "label +bug", `title "Thread"->"Thread 2"`, " close", "comment: comment number 12")
	issuesNot(t, tl, "total", "/")
	only := mustOK(t, "comment", a(map[string]any{"action": "timeline", "n": n, "types": []string{"close"}}))
	issuesWant(t, only, " close")
	issuesNot(t, only, "label +bug", "comment:")
	feed := mustOK(t, "comment", a(map[string]any{"action": "feed", "limit": 3}))
	issuesWant(t, feed, "# comments 3/", "page 1/", fmt.Sprintf("#%d", n), "edited text")
}

func TestIssuesLabelsAndMilestones(t *testing.T) {
	skipUnlessIntegration(t)
	repo := issuesMkRepo(t)
	a := func(args map[string]any) map[string]any { args["repo"] = repo; return args }

	out := mustOK(t, "label", a(map[string]any{"action": "create", "name": "scope/a", "color": "#00ff00", "description": "first", "exclusive": true}))
	id := issuesNum(t, issuesIDRe, out)
	got := mustOK(t, "label", a(map[string]any{"action": "list"}))
	issuesWant(t, got, "# labels 1/1 page 1/1", fmt.Sprintf("id %d | scope/a | #00ff00 | first | exclusive", id))

	// archive by name, then edit something else: the archived flag survives
	issuesWant(t, mustOK(t, "label", a(map[string]any{"action": "edit", "name": "scope/a", "archived": true})), "archived")
	mustOK(t, "label", a(map[string]any{"action": "edit", "id": id, "new_name": "scope/b", "color": "ff0000"}))
	got = mustOK(t, "label", a(map[string]any{"action": "list"}))
	issuesWant(t, got, fmt.Sprintf("id %d | scope/b | #ff0000 | first | exclusive | archived", id))
	mustOK(t, "label", a(map[string]any{"action": "edit", "id": id, "archived": false}))
	issuesNot(t, mustOK(t, "label", a(map[string]any{"action": "list"})), "archived")
	issuesWant(t, mustErr(t, "label", a(map[string]any{"action": "edit", "name": "ghost", "color": "fff"})), `unknown label "ghost"`)
	issuesWant(t, mustOK(t, "label", a(map[string]any{"action": "delete", "name": "scope/b"})), fmt.Sprintf("ok: deleted label id %d", id))
	issuesWant(t, mustOK(t, "label", a(map[string]any{"action": "list"})), "# labels 0/0")

	// organization labels
	org := issuesName()
	if st, body := rawAPI(t, http.MethodPost, "/orgs", map[string]any{"username": org}); st != http.StatusCreated {
		t.Fatalf("create org: %d %s", st, body)
	}
	t.Cleanup(func() { rawAPI(t, http.MethodDelete, "/orgs/"+org, nil) })
	mustOK(t, "label", map[string]any{"action": "create", "org": org, "name": "org-bug", "color": "123456"})
	issuesWant(t, mustOK(t, "label", map[string]any{"action": "list", "org": org}), "org-bug | #123456")
	mustOK(t, "label", map[string]any{"action": "delete", "org": org, "name": "org-bug"})

	// milestones: list always sends state
	mustOK(t, "milestone", a(map[string]any{"action": "create", "title": "v1", "description": "first", "due": "2031-02-03"}))
	mustOK(t, "milestone", a(map[string]any{"action": "create", "title": "v2"}))
	got = mustOK(t, "milestone", a(map[string]any{"action": "list"}))
	issuesWant(t, got, "# milestones 2/2", "| v1 | open | 0/0 closed | due 2031-02-03", "| v2 | open |")
	mustOK(t, "milestone", a(map[string]any{"action": "edit", "title": "v1", "state": "closed"}))
	issuesNot(t, mustOK(t, "milestone", a(map[string]any{"action": "list"})), "| v1 |")
	issuesWant(t, mustOK(t, "milestone", a(map[string]any{"action": "list", "state": "closed"})), "| v1 | closed |")
	issuesWant(t, mustOK(t, "milestone", a(map[string]any{"action": "list", "state": "all"})), "# milestones 2/2")
	mustOK(t, "milestone", a(map[string]any{"action": "edit", "title": "v2", "new_title": "v2.0"}))
	issuesWant(t, mustOK(t, "milestone", a(map[string]any{"action": "list"})), "| v2.0 |")
	mustOK(t, "milestone", a(map[string]any{"action": "delete", "title": "v2.0"}))
	issuesWant(t, mustErr(t, "milestone", a(map[string]any{"action": "delete", "title": "v2.0"})), "unknown milestone")
	mustOK(t, "milestone", a(map[string]any{"action": "delete", "id": 0, "title": "v1"}))
}

func TestIssuesDependencies(t *testing.T) {
	skipUnlessIntegration(t)
	repo := issuesMkRepo(t)
	a := func(args map[string]any) map[string]any { args["repo"] = repo; return args }
	n1 := issuesNum(t, issuesCreated, mustOK(t, "issue", a(map[string]any{"action": "create", "title": "Base"})))
	n2 := issuesNum(t, issuesCreated, mustOK(t, "issue", a(map[string]any{"action": "create", "title": "Dependent"})))
	n3 := issuesNum(t, issuesCreated, mustOK(t, "issue", a(map[string]any{"action": "create", "title": "Downstream"})))

	// n2 is blocked by n1 (the #N form takes owner/repo from the repo meta)
	issuesWant(t, mustOK(t, "issue", a(map[string]any{"action": "link", "n": n2, "relation": "blocked_by", "other": fmt.Sprintf("#%d", n1)})),
		fmt.Sprintf("ok: #%d blocked_by #%d", n2, n1))
	// n2 blocks n3
	issuesWant(t, mustOK(t, "issue", a(map[string]any{"action": "link", "n": n2, "relation": "blocks", "other": fmt.Sprintf("%s#%d", repo, n3)})),
		fmt.Sprintf("ok: #%d blocks #%d", n2, n3))
	got := mustOK(t, "issue", a(map[string]any{"action": "links", "n": n2}))
	issuesWant(t, got, fmt.Sprintf(`blocked_by: #%d open "Base"`, n1), fmt.Sprintf(`blocks: #%d open "Downstream"`, n3))
	got = mustOK(t, "issue", a(map[string]any{"action": "links", "n": n1}))
	issuesWant(t, got, fmt.Sprintf(`blocks: #%d open "Dependent"`, n2))
	issuesNot(t, got, "blocked_by:")

	// a circular dependency is refused with a hint
	circ := mustErr(t, "issue", a(map[string]any{"action": "link", "n": n1, "relation": "blocked_by", "other": fmt.Sprintf("#%d", n2)}))
	t.Logf("circular dependency answer: %s", circ)

	issuesWant(t, mustOK(t, "issue", a(map[string]any{"action": "unlink", "n": n2, "relation": "blocked_by", "other": fmt.Sprintf("#%d", n1)})),
		"no longer blocked_by")
	issuesWant(t, mustOK(t, "issue", a(map[string]any{"action": "unlink", "n": n2, "relation": "blocks", "other": fmt.Sprintf("#%d", n3)})),
		"no longer blocks")
	issuesWant(t, mustOK(t, "issue", a(map[string]any{"action": "links", "n": n2})), "has no dependencies")
}

func TestIssuesAttachments(t *testing.T) {
	skipUnlessIntegration(t)
	repo := issuesMkRepo(t)
	a := func(args map[string]any) map[string]any { args["repo"] = repo; return args }
	n := issuesNum(t, issuesCreated, mustOK(t, "issue", a(map[string]any{"action": "create", "title": "With files"})))
	cid := issuesNum(t, issuesCommentID, mustOK(t, "comment", a(map[string]any{"action": "add", "n": n, "body": "see file"})))

	// the harness session uses os.TempDir() as FORGEJO_SAVE_DIR
	suffix := strings.TrimPrefix(issuesName(), "it-")
	src := "forgejo-mcp-" + suffix + ".txt"
	content := "attachment payload " + suffix
	if err := os.WriteFile(filepath.Join(os.TempDir(), src), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(filepath.Join(os.TempDir(), src)) })

	out := mustOK(t, "attach", a(map[string]any{"action": "upload", "n": n, "file": src, "name": "notes.txt"}))
	issuesWant(t, out, "ok: uploaded attachment", "notes.txt", fmt.Sprintf("to #%d", n))
	id := issuesNum(t, issuesAttach, out)
	cOut := mustOK(t, "attach", a(map[string]any{"action": "upload", "comment_id": cid, "file": src, "name": "on-comment.txt"}))
	cAtt := issuesNum(t, issuesAttach, cOut)

	list := mustOK(t, "attach", a(map[string]any{"action": "list", "n": n}))
	issuesWant(t, list, "# attachments 1", fmt.Sprintf("id %d | notes.txt | %dB", id, len(content)))
	issuesWant(t, mustOK(t, "attach", a(map[string]any{"action": "list", "comment_id": cid})), "on-comment.txt")
	issuesWant(t, mustOK(t, "issue", a(map[string]any{"action": "get", "n": n})), "attachments: "+strconv.Itoa(id)+":notes.txt")

	dest := "forgejo-mcp-dl-" + suffix + ".txt"
	t.Cleanup(func() { _ = os.Remove(filepath.Join(os.TempDir(), dest)) })
	dl := mustOK(t, "attach", a(map[string]any{"action": "download", "n": n, "id": id, "save_to": dest}))
	issuesWant(t, dl, "ok: saved attachment", fmt.Sprintf("(%dB)", len(content)))
	b, err := os.ReadFile(filepath.Join(os.TempDir(), dest))
	if err != nil || string(b) != content {
		t.Fatalf("downloaded file: %q %v", b, err)
	}
	issuesWant(t, mustErr(t, "attach", a(map[string]any{"action": "download", "n": n, "id": id, "save_to": dest})), "exists:")

	mustOK(t, "attach", a(map[string]any{"action": "rename", "n": n, "id": id, "name": "renamed.txt"}))
	issuesWant(t, mustOK(t, "attach", a(map[string]any{"action": "list", "n": n})), "renamed.txt")
	mustOK(t, "attach", a(map[string]any{"action": "delete", "n": n, "id": id}))
	issuesWant(t, mustOK(t, "attach", a(map[string]any{"action": "list", "n": n})), "# attachments 0")
	mustOK(t, "attach", a(map[string]any{"action": "delete", "comment_id": cid, "id": cAtt}))

	// local file params stay inside the save dir
	issuesWant(t, mustErr(t, "attach", a(map[string]any{"action": "upload", "n": n, "file": "../outside.txt"})), "dot")
}
