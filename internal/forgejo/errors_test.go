package forgejo

import (
	"net/http"
	"strings"
	"testing"
)

func mapErr(method, path string, status int, body string, hasToken bool) *APIError {
	return newAPIError(method, path, status, "application/json", []byte(body), hasToken, func(s string) string { return s })
}

func TestErrorTable(t *testing.T) {
	const notFoundHint = "hint: check names; private resources also return 404 without access"
	tests := []struct {
		name         string
		method, path string
		status       int
		body         string
		token        bool
		want         string // the full Error() text
	}{
		{
			"401 with token", "GET", "/user", 401, `{"message":"user does not exist [uid: 0, name: ]"}`, true,
			"error: 401 unauthorized: user does not exist [uid: 0, name: ]\nhint: FORGEJO_TOKEN missing or invalid",
		},
		{
			"401 anonymous", "GET", "/user", 401, `{"message":"token is required"}`, false,
			"error: 401 unauthorized: token is required\nhint: set FORGEJO_TOKEN; anonymous access is read-only on public repos",
		},
		{
			"403 scope", "POST", "/repos/o/r/issues", 403, `{"message":"token does not have at least one of required scope(s): [write:issue write:repository]"}`, true,
			"error: 403 forbidden: token does not have at least one of required scope(s): [write:issue write:repository]\nhint: create a token with scope write:issue",
		},
		{
			"403 public only", "GET", "/repos/o/r", 403, `{"message":"token scope is limited to public repos"}`, true,
			"error: 403 forbidden: token scope is limited to public repos\nhint: token is public-only; use a token without public-only",
		},
		{
			"403 sudo", "GET", "/user", 403, `{"message":"Only administrators allowed to sudo."}`, true,
			"error: 403 forbidden: Only administrators allowed to sudo.\nhint: needs a site-admin token",
		},
		{
			"403 hooks", "GET", "/repos/o/r/hooks", 403, `{"message":"webhooks disabled by administrator"}`, true,
			"error: 403 forbidden: webhooks disabled by administrator\nhint: feature disabled by the administrator",
		},
		{"403 empty", "GET", "/x", 403, `{}`, true, "error: 403 forbidden: no permission"},
		{
			"404 generic", "GET", "/repos/o/r/pulls/9", 404, `{"message":"The target couldn't be found.","errors":[]}`, true,
			"error: 404 not found: no such resource: GET /repos/o/r/pulls/9\n" + notFoundHint,
		},
		{
			"404 func name with errors", "GET", "/repos/o/r/pulls/9", 404, `{"message":"GetPullRequestByIndex","errors":["pull request does not exist [id: 0, repo_id: 3, index: 9]"]}`, true,
			"error: 404 not found: pull request does not exist [id: 0, repo_id: 3, index: 9]\n" + notFoundHint,
		},
		{
			"404 func name no errors", "GET", "/repos/o/r/contents/x", 404, `{"message":"GetContentsOrList"}`, true,
			"error: 404 not found: no such resource: GET /repos/o/r/contents/x\n" + notFoundHint,
		},
		{
			"404 informative", "GET", "/x", 404, `{"message":"user redirect does not exist [name: bob]"}`, true,
			"error: 404 not found: user redirect does not exist [name: bob]\n" + notFoundHint,
		},
		{"404 null body", "GET", "/x", 404, `null`, true, "error: 404 not found: no such resource: GET /x\n" + notFoundHint},
		{"405 already merged", "POST", "/repos/o/r/pulls/5/merge", 405, `{"message":""}`, true, "error: 405 pr already merged"},
		{
			"405 wip", "POST", "/repos/o/r/pulls/5/merge", 405, `{"message":"Work in progress PRs cannot be merged"}`, true,
			"error: 405 method not allowed: Work in progress PRs cannot be merged\nhint: pr edit draft=false",
		},
		{
			"405 approvals", "POST", "/repos/o/r/pulls/5/merge", 405, `{"message":"Does not have enough approvals yet. 1 of 2 approvals granted"}`, true,
			"error: 405 method not allowed: Does not have enough approvals yet. 1 of 2 approvals granted\nhint: needs N approvals; see pr get",
		},
		{
			"405 status", "POST", "/repos/o/r/pulls/5/merge", 405, `{"message":"Required status check is missing"}`, true,
			"error: 405 method not allowed: Required status check is missing\nhint: wait for checks (ci_wait) or use when_green=true",
		},
		{
			"405 not allowed", "POST", "/repos/o/r/pulls/5/merge", 405, `{"message":"User not allowed to merge PR"}`, true,
			"error: 405 method not allowed: User not allowed to merge PR\nhint: needs write access or merge whitelist; force needs admin",
		},
		{"405 try later", "POST", "/repos/o/r/pulls/5/merge", 405, `{"message":"Please try again later"}`, true, "error: 405 merge check still running"},
		{
			"405 style", "POST", "/repos/o/r/pulls/5/merge", 405, `{"message":"squash is not an allowed merge style for this repository"}`, true,
			"error: 405 method not allowed: squash is not an allowed merge style for this repository\nhint: use a merge style the repo allows (see repo get)",
		},
		{"405 outside merge", "PUT", "/repos/o/r/x", 405, `{"message":""}`, true, "error: 405 method not allowed"},
		{
			"409 conflict", "POST", "/repos/o/r/pulls/5/merge", 409,
			`{"Message":"","Style":"merge","CONFLICT (content): Merge conflict in a.go\nCONFLICT (content): Merge conflict in dir/b.txt\n":"x","StdOut":"Auto-merging a.go\nCONFLICT (content): Merge conflict in a.go\nCONFLICT (content): Merge conflict in dir/b.txt\n"}`, true,
			"error: 409 merge conflict in a.go, dir/b.txt\nhint: pr update, or resolve locally",
		},
		{
			"409 already scheduled", "POST", "/repos/o/r/pulls/5/merge", 409, `{"message":"auto merge is already scheduled"}`, true,
			"error: 409 auto-merge already scheduled; pr automerge_cancel first",
		},
		{
			"409 head out of date", "POST", "/repos/o/r/pulls/5/merge", 409, `{"message":"head out of date"}`, true,
			"error: 409 conflict: head out of date\nhint: re-fetch the PR and retry",
		},
		{
			"409 push rejected", "POST", "/repos/o/r/pulls/5/merge", 409, `{"message":"PushRejected with remote message: hook declined"}`, true,
			"error: 409 conflict: PushRejected with remote message: hook declined",
		},
		{"409 empty merge body", "POST", "/repos/o/r/pulls/5/merge", 409, `{}`, true, "error: 409 conflict"},
		{
			"409 exists", "POST", "/repos/o/r/pulls", 409, `{"message":"pull request already exists for these targets"}`, true,
			"error: 409 exists: pull request already exists for these targets",
		},
		{
			"409 contents sha", "PUT", "/repos/o/r/contents/a.txt", 409, `{"message":"sha does not match [given: x, expected: y]"}`, true,
			"error: 409 conflict: sha does not match [given: x, expected: y]\nhint: file changed since read; re-read and retry",
		},
		{
			"400 delete contents sha", "DELETE", "/repos/o/r/contents/a.txt", 400, `{"message":"sha does not match [given: x, expected: y]"}`, true,
			"error: 400 bad request: sha does not match [given: x, expected: y]\nhint: file changed since read; re-read and retry",
		},
		{
			"422 diffpatch sha", "POST", "/repos/o/r/diffpatch", 422, `{"message":"sha does not match"}`, true,
			"error: 422 invalid: sha does not match\nhint: file changed since read; re-read and retry",
		},
		{
			"412 merged", "PATCH", "/repos/o/r/pulls/5", 412, `{"message":"pull request is already merged"}`, true,
			"error: 412 precondition failed: pull request is already merged\nhint: merged PRs cannot be reopened",
		},
		{
			"412 deps", "PATCH", "/repos/o/r/issues/5", 412, `{"message":"cannot close: has open dependencies"}`, true,
			"error: 412 precondition failed: cannot close: has open dependencies\nhint: close dependent issues first",
		},
		{
			"413", "POST", "/user/repos", 413, `{"message":"quota exceeded"}`, true,
			"error: 413 quota exceeded\nhint: free space or raise the quota (admin quota_*)",
		},
		{"422 binding", "POST", "/repos/o/r/issues", 422, `{"message":"[Title]: Required","errors":null}`, true, "error: 422 invalid: [Title]: Required"},
		{
			"422 topics", "PUT", "/repos/o/r/topics", 422, `{"invalidTopics":["a b","c!"],"message":"Topics don't match"}`, true,
			"error: 422 invalid topics: a b, c!",
		},
		{
			"422 pr update literal", "POST", "/repos/o/r/pulls/5/update", 422, `{"message":"%!s(<nil>)"}`, true,
			"error: 422 pr is merged or closed; nothing to update",
		},
		{
			"423", "POST", "/repos/o/r/issues", 423, `{"message":"repo is archived"}`, true,
			"error: 423 repo is archived\nhint: repo edit set={archived:false}",
		},
		{
			"500 hidden", "POST", "/repos/o/r/branches", 500, `{"message":""}`, true,
			"error: 500 server error (message hidden by server)\nhint: check server logs\nat: POST /repos/o/r/branches",
		},
		{"500 message", "GET", "/x", 500, `{"message":"boom"}`, true, "error: 500 internal server error: boom\nat: GET /x"},
		{"other 4xx text body", "GET", "/x", 418, "teapot said no\nin two lines", true, "error: 418 i'm a teapot: teapot said no in two lines"},
		{"json without message", "GET", "/x", 400, `{"foo":"bar"}`, true, `error: 400 bad request: {"foo":"bar"}`},
		{
			"html proxy page", "GET", "/x", 502, `<!DOCTYPE html><html>nginx</html>`, true,
			"error: 502 bad gateway (non-API response from proxy)\nat: GET /x",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapErr(tt.method, tt.path, tt.status, tt.body, tt.token).Error()
			if got != tt.want {
				t.Fatalf("\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestErrorMessageLongBodyCapped(t *testing.T) {
	e := mapErr("GET", "/x", 400, `{"foo":"`+strings.Repeat("x", 1000)+`"}`, true)
	if n := len([]rune(e.Message)); n > 301 {
		t.Fatalf("message not capped: %d", n)
	}
}

func TestAnnotate(t *testing.T) {
	e := mapErr("PUT", "/repos/o/r/branch_protections/main", 403, `{"message":""}`, true)
	e.Annotate(Annotation{Needs: "repo admin"})
	if e.Error() != "error: 403 forbidden: no permission\nhint: needs: repo admin" {
		t.Fatal(e.Error())
	}
	e = mapErr("GET", "/x", 403, `{"message":"token scope is limited to public repos"}`, true)
	e.Annotate(Annotation{Needs: "repo admin"})
	if !strings.Contains(e.Hint, "public-only") {
		t.Fatalf("specific hint overwritten: %q", e.Hint)
	}
	e = mapErr("POST", "/repos/o/r/branches", 500, `{"message":""}`, true)
	e.Annotate(Annotation{Hint500: "unknown from ref"})
	if e.Hint != "likely: unknown from ref; else check server logs" {
		t.Fatal(e.Hint)
	}
	e = mapErr("GET", "/repos/o/r/actions/runs", 404, `{"message":"The target couldn't be found."}`, true)
	e.Annotate(Annotation{MinMajorGap: true, ServerVersion: "15.0.1"})
	if e.Hint != "endpoint may not exist on Forgejo 15.0.1" {
		t.Fatal(e.Hint)
	}
}

func TestHelpers(t *testing.T) {
	if !mapErr("POST", "/repos/o/r/pulls/5/merge", 405, `{"message":"Please try again later"}`, true).IsMergeNotReady() {
		t.Fatal("IsMergeNotReady")
	}
	if msg, ok := mapErr("GET", "/x", 404, `{"message":"job has not been executed yet"}`, true).NoLog(); !ok || msg == "" {
		t.Fatal("NoLog not executed")
	}
	if _, ok := mapErr("GET", "/x", 404, `{"message":"logs have expired"}`, true).NoLog(); !ok {
		t.Fatal("NoLog expired")
	}
	if _, ok := mapErr("GET", "/x", 404, `{"message":"x y"}`, true).NoLog(); ok {
		t.Fatal("NoLog false positive")
	}
	if !mapErr("POST", "/repos/o/r/pulls/5/merge", 409, `{}`, true).MessageHidden() {
		t.Fatal("MessageHidden")
	}
}

func TestErrorThroughClientStatusText(t *testing.T) {
	e := &APIError{Status: http.StatusTeapot, Message: "m", Hint: "h"}
	if e.Error() != "error: 418 i'm a teapot: m\nhint: h" {
		t.Fatal(e.Error())
	}
}

func TestPathsAndVersionAndEscape(t *testing.T) {
	if got := Path("repos", "o", "r", "branches", Wild("feat/a b"), "x/y"); got != "/repos/o/r/branches/feat/a%20b/x%2Fy" {
		t.Fatal(got)
	}
	esc := []struct{ in, want string }{
		{"a b/c d", "a%20b/c%20d"},
		{"a%20b/c", "a%20b/c"},
		{"100%/x", "100%25/x"},
		{"%zz", "%25zz"},
		{"%2", "%252"},
		{"q?x#y", "q%3Fx%23y"},
		{"é", "%C3%A9"},
		{"a:b@c+d", "a:b@c+d"},
	}
	for _, e := range esc {
		if got := EscapeKeepPct(e.in); got != e.want {
			t.Errorf("EscapeKeepPct(%q) = %q want %q", e.in, got, e.want)
		}
	}
	vs := []struct {
		in         string
		maj, mi, p int
		bad        bool
	}{
		{"17.0.2", 17, 0, 2, false},
		{"v16.1.0+gitea-1.22.0", 16, 1, 0, false},
		{"15.0.0-dev-123-abc", 15, 0, 0, false},
		{" 7.0.1 ", 7, 0, 1, false},
		{"", 0, 0, 0, true},
		{"1.2", 0, 0, 0, true},
		{"x1.2.3", 0, 0, 0, true},
	}
	for _, v := range vs {
		got, err := ParseVersion(v.in)
		if (err != nil) != v.bad || (!v.bad && (got.Major != v.maj || got.Minor != v.mi || got.Patch != v.p)) {
			t.Errorf("ParseVersion(%q) = %+v, %v", v.in, got, err)
		}
	}
}
