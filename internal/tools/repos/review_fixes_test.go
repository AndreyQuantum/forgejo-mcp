package repos

import (
	"strings"
	"testing"
)

func TestWikiHistoryShortLastPage(t *testing.T) {
	commits := make([]string, 10)
	for i := range commits {
		commits[i] = `{"sha":"1111111122","message":"m","author":{"name":"A","date":"2026-11-22T10:15:00Z"}}`
	}
	f := newFake(t).on("GET /repos/o/r/wiki/revisions/Home", jsonOK(`{"count":60,"commits":[`+strings.Join(commits, ",")+`]}`))
	out := mustOK(t, f, "wiki", map[string]any{"action": "history", "page_name": "Home", "page": 2})
	if !strings.HasPrefix(out, "# revisions 10/60 page 2/2\n") {
		t.Fatal(out)
	}
	f.on("GET /repos/o/r/wiki/revisions/Home", jsonOK(`{"count":60,"commits":[]}`))
	out = mustOK(t, f, "wiki", map[string]any{"action": "history", "page_name": "Home", "page": 3})
	if strings.Contains(out, "next:") {
		t.Fatal(out)
	}
}

func TestWikiListLimitOne(t *testing.T) {
	f := newFake(t).on("GET /repos/o/r/wiki/pages", withTotal(`[{"title":"A","sub_url":"A"},{"title":"B","sub_url":"B"}]`, "2"))
	out := mustOK(t, f, "wiki", map[string]any{"action": "list", "limit": 1})
	if !strings.HasPrefix(out, "# wiki pages 1/2 page 1/2 next: page=2\n") || strings.Contains(out, "B") {
		t.Fatal(out)
	}
	if q := f.last().Query.Get("limit"); q != "2" {
		t.Fatalf("query %q", q)
	}
}

func TestWikiGetBodyMaxCeiling(t *testing.T) {
	long := strings.Repeat("0123456789\n", 1900) // 20900 chars
	f := newFake(t).on("GET /repos/o/r/wiki/page/Big", jsonOK(`{"title":"Big","sub_url":"Big","content_base64":"`+b64(long)+`"}`))
	out := mustOK(t, f, "wiki", map[string]any{"action": "get", "page_name": "Big", "body_max": 0})
	if !strings.Contains(out, "[truncated: ") || !strings.Contains(out, "max_chars") {
		t.Fatal(out[max(0, len(out)-200):])
	}
}

func TestRepoPushMirrorIntervalTooShort(t *testing.T) {
	f := newFake(t).on("POST /repos/o/r/push_mirrors", reply{status: 400, body: `{"message":"%!s(<nil>)"}`})
	out := mustErr(t, f, "repo", map[string]any{"action": "pushmirror_add", "remote_address": "https://x/y.git", "interval": "5m"})
	if !strings.Contains(out, "server minimum mirror interval") {
		t.Fatal(out)
	}
}

func TestRepoMigrateGitSendsPassword(t *testing.T) {
	f := newFake(t).on("POST /repos/migrate", reply{status: 201, body: `{"full_name":"acme/imp"}`})
	mustOK(t, f, "repo", map[string]any{"action": "migrate", "clone_addr": "https://h/x/y.git", "name": "imp", "auth_username": "u", "auth_token": "tok"})
	if b := f.bodyMap(f.last()); b["auth_password"] != "tok" || b["auth_token"] != "tok" {
		t.Fatalf("body %v", b)
	}
	mustOK(t, f, "repo", map[string]any{"action": "migrate", "clone_addr": "https://h/x/y.git", "name": "imp", "service": "github", "auth_token": "tok"})
	if b := f.bodyMap(f.last()); b["auth_password"] != nil {
		t.Fatalf("body %v", b)
	}
}

func TestRepoTopicsSetEmptyClears(t *testing.T) {
	f := newFake(t).on("PUT /repos/o/r/topics", reply{status: 204})
	mustOK(t, f, "repo", map[string]any{"action": "topics_set", "topics": []string{}})
	if l := f.last(); l.Method != "PUT" || l.Body != `{"topics":[]}` {
		t.Fatalf("%+v", l)
	}
	if out := mustErr(t, f, "repo", map[string]any{"action": "topics_set"}); !strings.Contains(out, "missing param topics") {
		t.Fatal(out)
	}
}

func TestRepoForkStatusIdenticalFork(t *testing.T) {
	f := newFake(t).on("GET /repos/o/r/sync_fork", jsonOK(`{"allowed":false,"fork_commit":"abc","base_commit":"abc","commits_behind":0}`))
	if out := mustOK(t, f, "repo", map[string]any{"action": "fork_status"}); out != "up to date\n" {
		t.Fatal(out)
	}
	f.on("GET /repos/o/r/sync_fork", jsonOK(`{"allowed":false,"fork_commit":"abc","base_commit":"def","commits_behind":0}`))
	if out := mustOK(t, f, "repo", map[string]any{"action": "fork_status"}); out != "up to date, sync not allowed (diverged)\n" {
		t.Fatal(out)
	}
}
