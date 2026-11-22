package forgejo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// APIError is a non-2xx answer (or a transport failure mapped to a status).
//
// Status 0 marks a failure that never produced an HTTP answer (timeout,
// connection failure, invalid response); Message then holds the full text.
type APIError struct {
	Status       int
	Message      string
	Method, Path string
	Body         string
	Hint         string

	// head, when set, replaces the "<status> <reason>[: message]" part of the
	// first line (the rows of section 11.2 that have their own wording).
	head string
	// hidden500 is set when a 500 carried no message (production servers hide it).
	hidden500 bool
	// retryable marks transport failures and 429/502/503/504 answers.
	retryable bool
}

// Error returns the compact output of section 11.1: up to three lines
// (`error:`, `hint:`, `at:`).
func (e *APIError) Error() string {
	var b strings.Builder
	switch {
	case e.head != "":
		b.WriteString("error: " + e.head)
	case e.Status == 0:
		b.WriteString("error: " + e.Message)
	default:
		fmt.Fprintf(&b, "error: %d %s", e.Status, strings.ToLower(http.StatusText(e.Status)))
		if e.Message != "" {
			b.WriteString(": " + e.Message)
		}
	}
	if e.Hint != "" {
		b.WriteString("\nhint: " + e.Hint)
	}
	if e.Status >= 500 && e.Method != "" {
		b.WriteString("\nat: " + e.Method + " " + e.Path)
	}
	return b.String()
}

// Annotation carries the per-action context that only the tool layer knows.
type Annotation struct {
	Needs         string // Action.Needs, used for 403 hints
	Hint500       string // Action.Hint500, used for hidden 500 messages
	MinMajorGap   bool   // the action's MinMajor is above the server major
	ServerVersion string // printed with MinMajorGap
}

// Annotate fills the hints that need the action context (section 11.2):
// `needs: <Needs>` for a plain 403, `likely: <Hint500>` for a hidden 500 and
// the version hint for a generic 404 on a too-new endpoint. Existing, more
// specific hints are kept except where stated.
func (e *APIError) Annotate(a Annotation) {
	switch {
	case e.Status == http.StatusForbidden && e.Hint == "" && a.Needs != "":
		e.Hint = "needs: " + a.Needs
	case e.Status == http.StatusInternalServerError && e.hidden500:
		if a.Hint500 != "" {
			e.Hint = "likely: " + a.Hint500 + "; else check server logs"
		}
	case e.Status == http.StatusNotFound && a.MinMajorGap:
		e.Hint = "endpoint may not exist on Forgejo " + a.ServerVersion
	}
}

// IsMergeNotReady reports the 405 "Please try again later" answer of a PR
// merge (the mergeability check has not finished; retried by 10.3).
func (e *APIError) IsMergeNotReady() bool {
	return e.Status == http.StatusMethodNotAllowed && strings.Contains(e.Message, "Please try again later")
}

// NoLog reports the 404 answers of job logs that are not errors
// ("job has not been executed yet", "logs have expired") and returns the text.
func (e *APIError) NoLog() (string, bool) {
	if e.Status != http.StatusNotFound {
		return "", false
	}
	m := strings.ToLower(e.Message)
	if strings.Contains(m, "has not been executed yet") || strings.Contains(m, "logs have expired") {
		return e.Message, true
	}
	return "", false
}

// MessageHidden reports an answer without a usable server message (empty
// JSON body or `{}`), which PR merge handling must resolve by a re-GET.
func (e *APIError) MessageHidden() bool { return e.Message == "" }

const genericNotFound = "The target couldn't be found."

var (
	reFuncName   = regexp.MustCompile(`^[A-Za-z]+$`)
	reConflict   = regexp.MustCompile(`CONFLICT \([^)]*\): Merge conflict in ([^\s"\\]+)`)
	reScopeList  = regexp.MustCompile(`\[([^\]]*)\]`)
	rePRUpdate   = regexp.MustCompile(`/pulls/\d+/update$`)
	reMergePath  = regexp.MustCompile(`/pulls/\d+/merge$`)
	reContents   = regexp.MustCompile(`/contents(/|$)`)
	reDiffpatch  = regexp.MustCompile(`/diffpatch$`)
	nilFormatMsg = "%!s(<nil>)"
)

// serverBody is the shape of Forgejo error bodies.
type serverBody struct {
	MessageSet    *string  `json:"message"`
	Message       string   `json:"-"`
	Errors        []string `json:"errors"`
	InvalidTopics []string `json:"invalidTopics"`
}

// newAPIError maps one non-2xx answer to an *APIError (table 11.2). hasToken
// selects the 401 hint; redact scrubs token strings.
func newAPIError(method, path string, status int, contentType string, body []byte, hasToken bool, redact func(string) string) *APIError {
	e := &APIError{Status: status, Method: method, Path: path}
	raw := strings.TrimSpace(string(body))
	e.Body = redact(truncRunes(raw, 2000))

	if looksLikeHTML(contentType, raw) {
		e.head = fmt.Sprintf("%d %s (non-API response from proxy)", status, strings.ToLower(http.StatusText(status)))
		return e
	}

	var sb serverBody
	jsonOK := false
	if raw != "" && (raw[0] == '{') {
		if json.Unmarshal(body, &sb) == nil {
			jsonOK = true
			if sb.MessageSet != nil {
				sb.Message = *sb.MessageSet
			}
		}
	}
	msg := strings.TrimSpace(sb.Message)
	if status == http.StatusNotFound {
		if msg == genericNotFound || (msg != "" && reFuncName.MatchString(msg)) {
			msg = ""
		}
		if msg == "" && len(sb.Errors) > 0 {
			msg = sb.Errors[0]
		}
		if msg == "" {
			msg = "no such resource: " + method + " " + path
		}
	} else if msg == "" && len(sb.Errors) > 0 && jsonOK {
		msg = sb.Errors[0]
	}
	if msg == "" && !jsonOK && raw != "" && raw != "null" {
		msg = oneLineTrunc(raw, 300)
	}
	e.Message = redact(oneLineTrunc(msg, 300))
	lower := strings.ToLower(e.Message)

	switch status {
	case http.StatusUnauthorized:
		if hasToken {
			e.Hint = "FORGEJO_TOKEN missing or invalid"
		} else {
			e.Hint = "set FORGEJO_TOKEN; anonymous access is read-only on public repos"
		}
	case http.StatusForbidden:
		switch {
		case strings.Contains(lower, "required scope"):
			if m := reScopeList.FindStringSubmatch(e.Message); m != nil {
				if f := strings.Fields(m[1]); len(f) > 0 {
					e.Hint = "create a token with scope " + f[0]
				}
			}
			if e.Hint == "" {
				e.Hint = "create a token with the scope named in the message"
			}
		case strings.Contains(lower, "token scope is limited to public"):
			e.Hint = "token is public-only; use a token without public-only"
		case strings.Contains(lower, "allowed to sudo") || strings.Contains(lower, "site admin"):
			e.Hint = "needs a site-admin token"
		case strings.Contains(lower, "webhooks disabled"):
			e.Hint = "feature disabled by the administrator"
		case strings.Contains(lower, "signed in user"):
			e.Hint = "sign in: set FORGEJO_TOKEN"
		case e.Message == "":
			e.Message = "no permission"
		}
	case http.StatusNotFound:
		e.Hint = "check names; private resources also return 404 without access"
	case http.StatusMethodNotAllowed:
		mapMethodNotAllowed(e, method, path, lower)
	case http.StatusConflict:
		mapConflict(e, method, path, body, lower)
	case http.StatusPreconditionFailed:
		switch {
		case strings.Contains(lower, "dependen"):
			e.Hint = "close dependent issues first"
		case strings.Contains(lower, "merged"):
			e.Hint = "merged PRs cannot be reopened"
		}
	case http.StatusRequestEntityTooLarge:
		e.head = "413 quota exceeded"
		e.Hint = "free space or raise the quota (admin quota_*)"
	case http.StatusUnprocessableEntity:
		mapUnprocessable(e, method, path, sb, lower)
	case http.StatusLocked:
		e.head = "423 repo is archived"
		e.Hint = "repo edit set={archived:false}"
	case http.StatusInternalServerError:
		if e.Message == "" {
			e.hidden500 = true
			e.head = "500 server error (message hidden by server)"
			e.Hint = "check server logs"
		}
	}
	if e.Message == "" && e.head == "" && jsonOK && sb.MessageSet == nil && raw != "{}" {
		// rule 1: JSON without a message shows its first 300 chars compactly
		e.Message = redact(oneLineTrunc(raw, 300))
	}
	if status == http.StatusBadRequest && method == http.MethodDelete && reContents.MatchString(path) && (strings.Contains(lower, "sha") || strings.Contains(lower, "does not match")) {
		e.Hint = "file changed since read; re-read and retry"
	}
	return e
}

func mapMethodNotAllowed(e *APIError, method, path, lower string) {
	if method != http.MethodPost || !reMergePath.MatchString(path) {
		return
	}
	switch {
	case e.Message == "":
		e.head = "405 pr already merged"
	case strings.Contains(lower, "please try again later"):
		e.head = "405 merge check still running"
	case strings.Contains(lower, "not an allowed merge style"):
		e.Hint = "use a merge style the repo allows (see repo get)"
	case strings.Contains(lower, "approvals"):
		e.Hint = "needs N approvals; see pr get"
	case strings.Contains(lower, "status"):
		e.Hint = "wait for checks (ci_wait) or use when_green=true"
	case strings.Contains(lower, "work in progress"):
		e.Hint = "pr edit draft=false"
	case strings.Contains(lower, "behind"):
		e.Hint = "head branch is behind base; pr update, then retry"
	case strings.Contains(lower, "requested changes"), strings.Contains(lower, "official review"):
		e.Hint = "address the reviews; see pr get / pr reviews"
	case strings.Contains(lower, "user not allowed to merge"):
		e.Hint = "needs write access or merge whitelist; force needs admin"
	}
}

func mapConflict(e *APIError, method, path string, body []byte, lower string) {
	isMerge := method == http.MethodPost && reMergePath.MatchString(path)
	switch {
	case isMerge && strings.Contains(lower, "already scheduled"):
		e.head = "409 auto-merge already scheduled; pr automerge_cancel first"
	case isMerge && (strings.Contains(lower, "head out of date") || strings.Contains(lower, "merge push out of date")):
		e.Hint = "re-fetch the PR and retry"
	case isMerge && strings.Contains(lower, "pushrejected"):
		// message passes through unchanged
	case isMerge && e.Message == "":
		if m := reConflict.FindAllSubmatch(body, -1); len(m) > 0 {
			files := make([]string, 0, len(m))
			seen := map[string]bool{}
			for _, x := range m {
				f := string(x[1])
				if !seen[f] {
					seen[f] = true
					files = append(files, f)
				}
			}
			e.head = "409 merge conflict in " + strings.Join(files, ", ")
			e.Hint = "pr update, or resolve locally"
		} else if e.Body != "" && e.Body != "{}" {
			e.Message = oneLineTrunc(e.Body, 300)
		}
	case strings.Contains(lower, "already exist") || strings.Contains(lower, "already been"):
		e.head = "409 exists: " + e.Message
	case reContents.MatchString(path) && (method == http.MethodPost || method == http.MethodPut):
		e.Hint = "file changed since read; re-read and retry"
	}
}

func mapUnprocessable(e *APIError, method, path string, sb serverBody, lower string) {
	switch {
	case method == http.MethodPost && rePRUpdate.MatchString(path):
		e.head = "422 pr is merged or closed; nothing to update"
		return
	case e.Message == nilFormatMsg:
		e.Message = ""
		return
	case len(sb.InvalidTopics) > 0:
		e.head = "422 invalid topics: " + strings.Join(sb.InvalidTopics, ", ")
		return
	}
	if method == http.MethodPost && reDiffpatch.MatchString(path) && strings.Contains(lower, "sha") {
		e.Hint = "file changed since read; re-read and retry"
	}
	if e.Message != "" {
		e.head = "422 invalid: " + e.Message
	}
}

func looksLikeHTML(contentType, raw string) bool {
	if strings.Contains(strings.ToLower(contentType), "text/html") {
		return true
	}
	l := strings.ToLower(raw)
	return strings.HasPrefix(l, "<!doctype html") || strings.HasPrefix(l, "<html")
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func oneLineTrunc(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
