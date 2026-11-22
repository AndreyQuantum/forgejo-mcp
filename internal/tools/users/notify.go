package users

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared"
)

// notifyParams are the parameters of the notify tool (section 8.9).
type notifyParams struct {
	Action string   `json:"action"`
	Repo   string   `json:"repo,omitempty" jsonschema:"owner/name to narrow to one repo; default all repos"`
	ID     int64    `json:"id,omitempty" jsonschema:"notification thread id"`
	Status string   `json:"status,omitempty" jsonschema:"unread|read|pinned|all; default unread"`
	Types  []string `json:"types,omitempty" jsonschema:"subject types: issue, pull, repository"`
	Since  string   `json:"since,omitempty" jsonschema:"only updated after this RFC 3339 time"`
	To     string   `json:"to,omitempty" jsonschema:"new status: read|unread|pinned; default read"`
	Page   int      `json:"page,omitempty"`
	Limit  int      `json:"limit,omitempty"`
}

var (
	listStatuses  = []string{"unread", "read", "pinned", "all"}
	markStatuses  = []string{"read", "unread", "pinned"}
	subjectTypes  = []string{"issue", "pull", "repository"}
	statusesByTo  = map[string][]string{"read": {"unread"}, "unread": {"read"}, "pinned": {"unread", "read"}}
	notifyScopeEp = []string{"GET /notifications", "GET /repos/{owner}/{repo}/notifications"}
)

func registerNotify(r *tools.Registry) {
	t := &notifyTool{}
	r.Add(tools.Spec{
		Name:        "notify",
		Toolset:     "users",
		Description: "Notifications: count, list, get, mark read/unread/pinned.",
		Params:      notifyParams{},
		Actions: []tools.Action{
			tools.NewAction("count", tools.Read, t.count,
				tools.Summary("number of unread notifications"),
				tools.Endpoints("GET /notifications/new")),
			tools.NewAction("list", tools.Read, t.list,
				tools.Summary("notifications (default unread), optionally of one repo"),
				tools.Params("repo", "status", "types", "since", "page", "limit"),
				tools.Endpoints(notifyScopeEp...)),
			tools.NewAction("get", tools.Read, t.get,
				tools.Summary("one notification thread"),
				tools.Required("id"),
				tools.Endpoints("GET /notifications/threads/{id}")),
			tools.NewAction("mark", tools.Write, t.mark,
				tools.Summary("set status of one thread (id) or of all notifications up to now"),
				tools.Params("id", "repo", "to"),
				tools.Endpoints("PATCH /notifications/threads/{id}", "PUT /notifications", "PUT /repos/{owner}/{repo}/notifications")),
		},
	})
}

type notifyTool struct{}

func (notifyTool) count(c *tools.Call, _ *notifyParams) (*tools.Result, error) {
	var out struct {
		New int `json:"new"`
	}
	if _, err := c.Env.Client.GetJSON(c.Ctx, "/notifications/new", nil, &out); err != nil {
		return nil, err
	}
	return &tools.Result{Text: "unread: " + strconv.Itoa(out.New)}, nil
}

// notifyPath returns `/notifications` or `/repos/{o}/{r}/notifications`.
func notifyPath(c *tools.Call, repo string) (string, *tools.Result) {
	if strings.TrimSpace(repo) == "" {
		return "/notifications", nil
	}
	o, n, err := shared.ParseOwnerRepo(repo)
	if err != nil {
		return "", c.Fail("invalid: repo %q: want owner/name", repo)
	}
	return forgejo.Path("repos", o, n, "notifications"), nil
}

func (notifyTool) list(c *tools.Call, p *notifyParams) (*tools.Result, error) {
	path, bad := notifyPath(c, p.Repo)
	if bad != nil {
		return bad, nil
	}
	status := p.Status
	if status == "" {
		status = "unread"
	}
	if !slices.Contains(listStatuses, status) {
		return c.Fail("invalid: status %q; valid: %s", status, strings.Join(listStatuses, "|")), nil
	}
	q := url.Values{}
	if status == "all" {
		q.Set("all", "true")
	} else {
		q.Add("status-types", status)
	}
	for _, t := range p.Types {
		if !slices.Contains(subjectTypes, t) {
			return c.Fail("invalid: types entry %q; valid: %s", t, strings.Join(subjectTypes, "|")), nil
		}
		q.Add("subject-type", t)
	}
	if p.Since != "" {
		q.Set("since", p.Since)
	}
	return listPage(c, path, q, p.Page, p.Limit, "notifications", threadLine)
}

func (notifyTool) get(c *tools.Call, p *notifyParams) (*tools.Result, error) {
	var t thread
	if _, err := c.Env.Client.GetJSON(c.Ctx, forgejo.Path("notifications", "threads", strconv.FormatInt(p.ID, 10)), nil, &t); err != nil {
		return nil, err
	}
	return &tools.Result{Text: c.Cap(threadObject(t))}, nil
}

func (notifyTool) mark(c *tools.Call, p *notifyParams) (*tools.Result, error) {
	to := p.To
	if to == "" {
		to = "read"
	}
	if !slices.Contains(markStatuses, to) {
		return c.Fail("invalid: to %q; valid: %s", to, strings.Join(markStatuses, "|")), nil
	}
	if p.ID != 0 {
		q := url.Values{"to-status": {to}}
		path := forgejo.Path("notifications", "threads", strconv.FormatInt(p.ID, 10))
		if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPatch, path, q, nil, nil); err != nil {
			return nil, err
		}
		return ok("marked n%d as %s", p.ID, to), nil
	}
	path, bad := notifyPath(c, p.Repo)
	if bad != nil {
		return bad, nil
	}
	q := url.Values{
		"last_read_at": {c.Env.Clock.Now().UTC().Format(time.RFC3339)},
		"to-status":    {to},
		"status-types": statusesByTo[to],
	}
	resp, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodPut, Path: path, Query: q})
	if err != nil {
		return nil, err
	}
	// The 205 answer carries the list of changed threads; it is only counted.
	var changed []json.RawMessage
	if len(resp.Body) > 0 && json.Unmarshal(resp.Body, &changed) == nil {
		return ok("marked %d as %s", len(changed), to), nil
	}
	return ok("marked notifications as %s", to), nil
}
