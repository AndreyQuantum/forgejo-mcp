package issues

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared"
)

// issueParams is the flat parameter struct of the `issue` tool (section 8.5).
type issueParams struct {
	Action       string   `json:"action"`
	Repo         string   `json:"repo,omitempty" jsonschema:"owner/name; default repo when omitted"`
	N            int64    `json:"n,omitempty" jsonschema:"issue or PR number"`
	State        string   `json:"state,omitempty" jsonschema:"open, closed or all"`
	Type         string   `json:"type,omitempty" jsonschema:"issues, pulls or all"`
	Q            string   `json:"q,omitempty" jsonschema:"search text"`
	Labels       []string `json:"labels,omitempty" jsonschema:"label names"`
	AddLabels    []string `json:"add_labels,omitempty" jsonschema:"label names to add"`
	RemoveLabels []string `json:"remove_labels,omitempty" jsonschema:"label names to remove"`
	SetLabels    []string `json:"set_labels,omitempty" jsonschema:"replace all labels; [] clears"`
	Milestone    string   `json:"milestone,omitempty" jsonschema:"title or id; none clears"`
	Assignees    []string `json:"assignees,omitempty" jsonschema:"logins; replaces on edit"`
	Author       string   `json:"author,omitempty" jsonschema:"login of the creator"`
	AssignedTo   string   `json:"assigned_to,omitempty" jsonschema:"login of an assignee"`
	Mentioned    string   `json:"mentioned,omitempty" jsonschema:"login mentioned"`
	Since        string   `json:"since,omitempty" jsonschema:"updated after, RFC 3339"`
	Before       string   `json:"before,omitempty" jsonschema:"updated before, RFC 3339"`
	Sort         string   `json:"sort,omitempty" jsonschema:"latest, oldest, recentupdate, leastupdate, mostcomment, leastcomment, nearduedate, farduedate"`
	Title        string   `json:"title,omitempty"`
	Body         *string  `json:"body,omitempty" jsonschema:"markdown; empty string clears on edit"`
	Due          string   `json:"due,omitempty" jsonschema:"YYYY-MM-DD"`
	UnsetDue     bool     `json:"unset_due,omitempty" jsonschema:"remove the due date"`
	Ref          string   `json:"ref,omitempty" jsonschema:"branch or tag ref"`
	Closed       *bool    `json:"closed,omitempty" jsonschema:"create: close right away; edit: set state"`
	Reason       string   `json:"reason,omitempty" jsonschema:"lock reason, default resolved"`
	Relation     string   `json:"relation,omitempty" jsonschema:"blocked_by or blocks"`
	Other        string   `json:"other,omitempty" jsonschema:"owner/repo#N or #N"`
	Content      string   `json:"content,omitempty" jsonschema:"reaction, e.g. +1, heart, rocket"`
	CommentID    int64    `json:"comment_id,omitempty" jsonschema:"react to this comment instead"`
	Remove       bool     `json:"remove,omitempty" jsonschema:"remove the reaction"`
	Flags        []string `json:"flags,omitempty" jsonschema:"assigned, created, mentioned, review_requested, reviewed"`
	Owner        string   `json:"owner,omitempty" jsonschema:"limit to this user or org"`
	BodyMax      *int     `json:"body_max,omitempty" jsonschema:"body chars, default 1500; 0 = up to 20000"`
	Include      []string `json:"include,omitempty" jsonschema:"comments, timeline"`
	Confirm      string   `json:"confirm,omitempty" jsonschema:"owner/name#n of the issue to delete"`
	Page         int      `json:"page,omitempty"`
	Limit        int      `json:"limit,omitempty"`
	Full         bool     `json:"full,omitempty" jsonschema:"lift per-field caps"`
	Verbose      bool     `json:"verbose,omitempty" jsonschema:"also ids"`
}

var (
	searchFlags    = []string{"assigned", "created", "mentioned", "review_requested", "reviewed"}
	issueSorts     = []string{"relevance", "latest", "oldest", "recentupdate", "leastupdate", "mostcomment", "leastcomment", "nearduedate", "farduedate"}
	issueStates    = []string{"open", "closed", "all"}
	issueTypes     = []string{"issues", "pulls", "all"}
	includeOptions = []string{"comments", "timeline"}
)

func registerIssue(r *tools.Registry) {
	t := &issueTool{}
	R, W, D := tools.Read, tools.Write, tools.Destructive
	r.Add(tools.Spec{
		Name:        "issue",
		Toolset:     "issues",
		Description: "Issues (and PR metadata by number): list, search, get, create, edit, delete, lock, pin, link, react, subscribe.",
		Params:      issueParams{},
		RepoScoped:  true,
		Actions: []tools.Action{
			tools.NewAction("list", R, t.list,
				tools.Summary("issues of a repo"),
				tools.Params("state", "type", "q", "labels", "milestone", "author", "assigned_to", "mentioned", "since", "before", "sort", "page", "limit"),
				tools.Endpoints("GET /repos/{owner}/{repo}/issues")),
			tools.NewAction("search", R, t.search,
				tools.Summary("issues across repos (labels match any-of)"),
				tools.Params("q", "state", "type", "labels", "milestone", "owner", "flags", "since", "before", "sort", "page", "limit"),
				tools.NoRepo(),
				tools.Endpoints("GET /repos/issues/search")),
			tools.NewAction("get", R, t.get,
				tools.Summary("one issue or PR; include=comments,timeline"),
				tools.Params("n", "include", "body_max", "full", "verbose"), tools.Required("n"),
				tools.Endpoints("GET /repos/{owner}/{repo}/issues/{index}", "GET /repos/{owner}/{repo}/issues/{index}/comments", "GET /repos/{owner}/{repo}/issues/{index}/timeline")),
			tools.NewAction("create", W, t.create,
				tools.Summary("create an issue (labels and milestone by name)"),
				tools.Params("title", "body", "labels", "milestone", "assignees", "due", "ref", "closed"), tools.Required("title"),
				tools.Needs("repo read access to issues"),
				tools.Endpoints("POST /repos/{owner}/{repo}/issues")),
			tools.NewAction("edit", W, t.edit,
				tools.Summary("edit fields, state and labels of an issue or PR"),
				tools.Params("n", "title", "body", "state", "closed", "milestone", "assignees", "due", "unset_due", "ref", "add_labels", "remove_labels", "set_labels"), tools.Required("n"),
				tools.Needs("repo write access"),
				tools.Endpoints("PATCH /repos/{owner}/{repo}/issues/{index}", "POST /repos/{owner}/{repo}/issues/{index}/labels", "PUT /repos/{owner}/{repo}/issues/{index}/labels", "DELETE /repos/{owner}/{repo}/issues/{index}/labels/{identifier}")),
			tools.NewAction("delete", D, t.delete,
				tools.Summary("delete an issue for good (repo admin)"),
				tools.Params("n", "confirm"), tools.Required("n"),
				tools.Needs("repo admin"),
				tools.Endpoints("DELETE /repos/{owner}/{repo}/issues/{index}")),
			tools.NewAction("lock", W, t.lock,
				tools.Summary("lock the conversation"),
				tools.Params("n", "reason"), tools.Required("n"),
				tools.Endpoints("PUT /repos/{owner}/{repo}/issues/{index}/lock")),
			tools.NewAction("unlock", W, t.unlock,
				tools.Summary("unlock the conversation"),
				tools.Params("n"), tools.Required("n"),
				tools.Endpoints("DELETE /repos/{owner}/{repo}/issues/{index}/lock")),
			tools.NewAction("pin", W, t.pin,
				tools.Summary("pin an issue (repo admin)"),
				tools.Params("n"), tools.Required("n"), tools.Needs("repo admin"),
				tools.Endpoints("POST /repos/{owner}/{repo}/issues/{index}/pin")),
			tools.NewAction("unpin", W, t.unpin,
				tools.Summary("unpin an issue (repo admin)"),
				tools.Params("n"), tools.Required("n"), tools.Needs("repo admin"),
				tools.Endpoints("DELETE /repos/{owner}/{repo}/issues/{index}/pin")),
			tools.NewAction("link", W, t.link,
				tools.Summary("add a dependency: n blocked_by|blocks other"),
				tools.Params("n", "relation", "other"), tools.Required("n", "relation", "other"),
				tools.Hint500("dependency already exists or would be circular"),
				tools.Endpoints("POST /repos/{owner}/{repo}/issues/{index}/dependencies", "POST /repos/{owner}/{repo}/issues/{index}/blocks")),
			tools.NewAction("unlink", W, t.unlink,
				tools.Summary("remove a dependency"),
				tools.Params("n", "relation", "other"), tools.Required("n", "relation", "other"),
				tools.Endpoints("DELETE /repos/{owner}/{repo}/issues/{index}/dependencies", "DELETE /repos/{owner}/{repo}/issues/{index}/blocks")),
			tools.NewAction("links", R, t.links,
				tools.Summary("what blocks n and what n blocks"),
				tools.Params("n"), tools.Required("n"),
				tools.Endpoints("GET /repos/{owner}/{repo}/issues/{index}/dependencies", "GET /repos/{owner}/{repo}/issues/{index}/blocks")),
			tools.NewAction("react", W, t.react,
				tools.Summary("add or remove a reaction on an issue or comment"),
				tools.Params("n", "comment_id", "content", "remove"), tools.Required("content"),
				tools.Endpoints("POST /repos/{owner}/{repo}/issues/{index}/reactions", "DELETE /repos/{owner}/{repo}/issues/{index}/reactions",
					"POST /repos/{owner}/{repo}/issues/comments/{id}/reactions", "DELETE /repos/{owner}/{repo}/issues/comments/{id}/reactions")),
			tools.NewAction("subscribe", W, t.subscribe,
				tools.Summary("watch an issue as the current user"),
				tools.Params("n"), tools.Required("n"),
				tools.Endpoints("PUT /repos/{owner}/{repo}/issues/{index}/subscriptions/{user}")),
			tools.NewAction("unsubscribe", W, t.unsubscribe,
				tools.Summary("stop watching an issue"),
				tools.Params("n"), tools.Required("n"),
				tools.Endpoints("DELETE /repos/{owner}/{repo}/issues/{index}/subscriptions/{user}")),
			tools.NewAction("templates", R, t.templates,
				tools.Summary("issue templates of the repo"),
				tools.Endpoints("GET /repos/{owner}/{repo}/issue_templates")),
			tools.NewAction("assignable", R, t.assignable,
				tools.Summary("users that can be assigned"),
				tools.Endpoints("GET /repos/{owner}/{repo}/assignees")),
		},
	})
}

type issueTool struct{}

func checkEnum(name, val string, valid []string) error {
	if val == "" || slices.Contains(valid, val) {
		return nil
	}
	return fmt.Errorf("invalid: %s %q; valid: %s", name, val, strings.Join(valid, ","))
}

// listQuery builds the filters shared by list and search.
func listQuery(p *issueParams, typeDefault string) (url.Values, error) {
	if err := checkEnum("state", p.State, issueStates); err != nil {
		return nil, err
	}
	if err := checkEnum("type", p.Type, issueTypes); err != nil {
		return nil, err
	}
	if err := checkEnum("sort", p.Sort, issueSorts); err != nil {
		return nil, err
	}
	q := url.Values{}
	state := p.State
	if state == "" {
		state = "open"
	}
	q.Set("state", state)
	typ := p.Type
	if typ == "" {
		typ = typeDefault
	}
	if typ != "all" {
		q.Set("type", typ)
	}
	addIf(q, "q", p.Q)
	addIf(q, "sort", p.Sort)
	since, err := rfc3339(p.Since, "since")
	if err != nil {
		return nil, err
	}
	addIf(q, "since", since)
	before, err := rfc3339(p.Before, "before")
	if err != nil {
		return nil, err
	}
	addIf(q, "before", before)
	if len(p.Labels) > 0 {
		q.Set("labels", csv(p.Labels))
	}
	return q, nil
}

func listKind(p *issueParams, typeDefault string) string {
	typ := p.Type
	if typ == "" {
		typ = typeDefault
	}
	switch typ {
	case "pulls":
		return "prs"
	case "all":
		return "issues+prs"
	}
	return "issues"
}

func (t *issueTool) list(c *tools.Call, p *issueParams) (*tools.Result, error) {
	q, err := listQuery(p, "issues")
	if err != nil {
		return c.Fail("%s", err), nil
	}
	cl := c.Env.Client
	// The server matches names exactly and drops unknown ones without an
	// error (widening the result): send canonical label names and the id.
	if len(p.Labels) > 0 {
		refs, err := labelRefs(c, p.Labels)
		if err != nil {
			return nil, err
		}
		q.Set("labels", csv(labelNames(refs)))
	}
	if ms := strings.TrimSpace(p.Milestone); ms != "" && ms != "none" {
		id, err := shared.ResolveMilestone(c.Ctx, cl, c.Owner, c.Repo, ms)
		if err != nil {
			return nil, err
		}
		q.Set("milestones", strconv.FormatInt(id, 10))
	}
	addIf(q, "created_by", p.Author)
	addIf(q, "assigned_by", p.AssignedTo)
	addIf(q, "mentioned_by", p.Mentioned)
	page, limit := pageArgs(c, p.Page, p.Limit)
	var items []*issue
	pg, err := cl.List(c.Ctx, repoBase(c)+"/issues", q, page, limit, &items)
	if err != nil {
		return nil, err
	}
	return &tools.Result{Text: renderIssueList(listKind(p, "issues"), pg, items, false, "")}, nil
}

func renderIssueList(kind string, pg forgejo.Page, items []*issue, search bool, note string) string {
	var b format.B
	hidden := 0
	var lines format.B
	for _, it := range items {
		if it == nil || (it.Number == 0 && it.ID == 0) {
			hidden++
			continue
		}
		lines.Raw(issueLine(it, search))
	}
	pg.Shown -= hidden // More was decided on the raw count
	b.Raw(format.ListHeader(kind, pg))
	if hidden > 0 {
		b.Raw(fmt.Sprintf(" (%d hidden)", hidden))
	}
	b.Raw("\n")
	if note != "" {
		b.Raw("note: " + note + "\n")
	}
	b.Raw(lines.String())
	return b.String()
}

func (t *issueTool) search(c *tools.Call, p *issueParams) (*tools.Result, error) {
	q, err := listQuery(p, "all")
	if err != nil {
		return c.Fail("%s", err), nil
	}
	for _, f := range p.Flags {
		if !slices.Contains(searchFlags, f) {
			return c.Fail("invalid: flag %q; valid: %s", f, strings.Join(searchFlags, ",")), nil
		}
		q.Set(f, "true")
	}
	if ms := strings.TrimSpace(p.Milestone); ms != "" && ms != "none" {
		if _, perr := strconv.ParseInt(ms, 10, 64); perr == nil {
			return c.Fail("invalid: milestone %q; search matches milestone titles only, not ids", ms), nil
		}
		q.Set("milestones", ms)
	}
	addIf(q, "owner", p.Owner)
	page, limit := pageArgs(c, p.Page, p.Limit)
	var items []*issue
	pg, err := c.Env.Client.List(c.Ctx, "/repos/issues/search", q, page, limit, &items)
	if err != nil {
		return nil, err
	}
	note := ""
	if len(p.Labels) > 0 {
		note = "labels match any-of in search"
	}
	return &tools.Result{Text: renderIssueList(listKind(p, "all"), pg, items, true, note)}, nil
}

// fetchIssue reads one issue and guards against the empty object the server
// answers when a sub-load fails.
func fetchIssue(c *tools.Call, n int64) (*issue, error) {
	var is issue
	if _, err := c.Env.Client.GetJSON(c.Ctx, issuePath(c, n), nil, &is); err != nil {
		return nil, err
	}
	if is.ID == 0 && is.Number == 0 {
		return nil, fmt.Errorf("empty issue object returned for #%d (the server failed to load it)", n)
	}
	return &is, nil
}

func (t *issueTool) get(c *tools.Call, p *issueParams) (*tools.Result, error) {
	for _, inc := range p.Include {
		if !slices.Contains(includeOptions, inc) {
			return c.Fail("invalid: include %q; valid: %s", inc, strings.Join(includeOptions, ",")), nil
		}
	}
	is, err := fetchIssue(c, p.N)
	if err != nil {
		return nil, err
	}
	var b format.B
	b.Raw(renderIssue(is, p.Verbose, bodyText(is.Body, p.BodyMax, p.Full, defaultBodyMax)))
	if slices.Contains(p.Include, "comments") {
		var cms []*comment
		if _, err := c.Env.Client.GetJSON(c.Ctx, issuePath(c, p.N, "comments"), nil, &cms); err != nil {
			return nil, err
		}
		cms = dropNilComments(cms)
		show := cms[max(0, len(cms)-10):]
		b.Raw(fmt.Sprintf("# comments %d/%d newest\n", len(show), len(cms)))
		for _, cm := range show {
			b.Raw(commentLine(cm, commentMax))
		}
	}
	if slices.Contains(p.Include, "timeline") {
		out, err := timelineText(c, p.N, nil, 1, 20)
		if err != nil {
			return nil, err
		}
		b.Raw(out)
	}
	return &tools.Result{Text: b.String()}, nil
}

func dropNilComments(in []*comment) []*comment {
	out := in[:0:0]
	for _, c := range in {
		if c != nil {
			out = append(out, c)
		}
	}
	return out
}

// resolveCreateRefs resolves label names and the milestone to ids.
func resolveCreateRefs(c *tools.Call, labels []string, milestone string) (labelIDs []int64, msID int64, err error) {
	cl := c.Env.Client
	if len(labels) > 0 {
		if labelIDs, err = shared.ResolveLabels(c.Ctx, cl, c.Owner, c.Repo, labels); err != nil {
			return nil, 0, err
		}
	}
	if ms := strings.TrimSpace(milestone); ms != "" && ms != "none" {
		if msID, err = shared.ResolveMilestone(c.Ctx, cl, c.Owner, c.Repo, ms); err != nil {
			return nil, 0, err
		}
	}
	return labelIDs, msID, nil
}

func (t *issueTool) create(c *tools.Call, p *issueParams) (*tools.Result, error) {
	labelIDs, msID, err := resolveCreateRefs(c, p.Labels, p.Milestone)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"title": p.Title}
	if p.Body != nil && *p.Body != "" {
		body["body"] = *p.Body
	}
	if p.Ref != "" {
		body["ref"] = p.Ref
	}
	if len(p.Assignees) > 0 {
		body["assignees"] = p.Assignees
	}
	if len(labelIDs) > 0 {
		body["labels"] = labelIDs
	}
	if msID != 0 {
		body["milestone"] = msID
	}
	if p.Due != "" {
		due, err := parseDue(p.Due)
		if err != nil {
			return c.Fail("%s", err), nil
		}
		body["due_date"] = due
	}
	if p.Closed != nil && *p.Closed {
		body["closed"] = true
	}
	var out issue
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPost, repoBase(c)+"/issues", nil, body, &out); err != nil {
		return nil, err
	}
	text := fmt.Sprintf("ok: created issue #%d %s", out.Number, out.HTMLURL)
	var ignored []string
	if len(p.Labels) > len(out.Labels) {
		ignored = append(ignored, "labels")
	}
	if len(p.Assignees) > 0 && len(out.Assignees) == 0 {
		ignored = append(ignored, "assignees")
	}
	if msID != 0 && out.Milestone == nil {
		ignored = append(ignored, "milestone")
	}
	if len(ignored) > 0 {
		text += "\nnote: server ignored " + strings.Join(ignored, "/") + " (no write access)"
	}
	return &tools.Result{Text: text}, nil
}

func (t *issueTool) edit(c *tools.Call, p *issueParams) (*tools.Result, error) {
	cl := c.Env.Client
	patch := map[string]any{}
	var changed []string
	if p.Title != "" {
		patch["title"] = p.Title
		changed = append(changed, "title")
	}
	if p.Body != nil {
		patch["body"] = *p.Body
		changed = append(changed, "body")
	}
	state := p.State
	if p.Closed != nil {
		state = "open"
		if *p.Closed {
			state = "closed"
		}
	}
	if state != "" {
		if state != "open" && state != "closed" {
			return c.Fail("invalid: state %q; valid for edit: open,closed", state), nil
		}
		patch["state"] = state
		changed = append(changed, "state="+state)
	}
	if ms := strings.TrimSpace(p.Milestone); ms != "" {
		if ms == "none" {
			patch["milestone"] = 0
			changed = append(changed, "milestone=none")
		} else {
			id, err := shared.ResolveMilestone(c.Ctx, cl, c.Owner, c.Repo, ms)
			if err != nil {
				return nil, err
			}
			patch["milestone"] = id
			changed = append(changed, "milestone")
		}
	}
	if p.Assignees != nil {
		patch["assignees"] = p.Assignees
		changed = append(changed, "assignees")
	}
	switch {
	case p.UnsetDue:
		patch["unset_due_date"] = true
		changed = append(changed, "due=none")
	case p.Due != "":
		due, err := parseDue(p.Due)
		if err != nil {
			return c.Fail("%s", err), nil
		}
		patch["due_date"] = due
		changed = append(changed, "due")
	}
	if p.Ref != "" {
		patch["ref"] = p.Ref
		changed = append(changed, "ref")
	}
	// Resolve every label name before the first write.
	addRefs, err := labelRefs(c, p.AddLabels)
	if err != nil {
		return nil, err
	}
	setRefs, err := labelRefs(c, p.SetLabels)
	if err != nil {
		return nil, err
	}
	removeRefs, err := labelRefs(c, p.RemoveLabels)
	if err != nil {
		return nil, err
	}
	if len(patch) == 0 && len(p.AddLabels) == 0 && len(p.RemoveLabels) == 0 && p.SetLabels == nil {
		return c.Fail("invalid: nothing to edit; pass title, body, state, milestone, assignees, due, ref or label params"), nil
	}
	if len(patch) > 0 {
		if _, err := cl.SendJSON(c.Ctx, http.MethodPatch, issuePath(c, p.N), nil, patch, nil); err != nil {
			return nil, err
		}
	}
	var notes []string
	labelCall := func(method string, refs []forgejo.Label, tag string) error {
		ids := labelIDs(refs)
		var got []forgejo.Label
		if _, err := cl.SendJSON(c.Ctx, method, issuePath(c, p.N, "labels"), nil, map[string]any{"labels": ids}, &got); err != nil {
			return err
		}
		have := map[int64]bool{}
		for _, l := range got {
			have[l.ID] = true
		}
		for _, l := range refs {
			if !have[l.ID] {
				notes = append(notes, "label "+l.Name+" was not applied")
			}
		}
		changed = append(changed, tag)
		return nil
	}
	if p.SetLabels != nil {
		if err := labelCall(http.MethodPut, setRefs, "labels="+csv(labelNames(setRefs))); err != nil {
			return nil, err
		}
	}
	if len(addRefs) > 0 {
		if err := labelCall(http.MethodPost, addRefs, "labels+"+csv(labelNames(addRefs))); err != nil {
			return nil, err
		}
	}
	for _, l := range removeRefs {
		if _, err := cl.Do(c.Ctx, forgejo.Request{Method: http.MethodDelete, Path: issuePath(c, p.N, "labels", l.Name)}); err != nil {
			return nil, err
		}
		changed = append(changed, "labels-"+l.Name)
	}
	text := fmt.Sprintf("ok: edited #%d: %s", p.N, strings.Join(changed, ","))
	for _, n := range notes {
		text += "\nnote: " + n
	}
	return &tools.Result{Text: text}, nil
}

func (t *issueTool) delete(c *tools.Call, p *issueParams) (*tools.Result, error) {
	if res := c.Confirm(p.Confirm, fmt.Sprintf("%s/%s#%d", c.Owner, c.Repo, p.N)); res != nil {
		return res, nil
	}
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodDelete, Path: issuePath(c, p.N)}); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: deleted #%d", p.N)}, nil
}

func (t *issueTool) lock(c *tools.Call, p *issueParams) (*tools.Result, error) {
	reason := strings.TrimSpace(p.Reason)
	if reason == "" {
		reason = "resolved"
	}
	_, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPut, issuePath(c, p.N, "lock"), nil, map[string]string{"reason": reason}, nil)
	if apiStatus(err) == http.StatusConflict {
		return &tools.Result{Text: fmt.Sprintf("ok: #%d already locked", p.N)}, nil
	}
	if err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: locked #%d (%s)", p.N, reason)}, nil
}

func (t *issueTool) unlock(c *tools.Call, p *issueParams) (*tools.Result, error) {
	_, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodDelete, Path: issuePath(c, p.N, "lock")})
	if apiStatus(err) == http.StatusConflict {
		return &tools.Result{Text: fmt.Sprintf("ok: #%d already unlocked", p.N)}, nil
	}
	if err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: unlocked #%d", p.N)}, nil
}

func (t *issueTool) pin(c *tools.Call, p *issueParams) (*tools.Result, error) {
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodPost, Path: issuePath(c, p.N, "pin")}); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: pinned #%d", p.N)}, nil
}

func (t *issueTool) unpin(c *tools.Call, p *issueParams) (*tools.Result, error) {
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodDelete, Path: issuePath(c, p.N, "pin")}); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: unpinned #%d", p.N)}, nil
}

// dependencyBody resolves `other` into the IssueMeta body. For the `#N` form
// owner/repo come from the canonical repo meta (the server compares them
// case-sensitively and treats any difference as cross-repo).
func dependencyBody(c *tools.Call, p *issueParams) (body map[string]any, ref string, err error) {
	if p.Relation != "blocked_by" && p.Relation != "blocks" {
		return nil, "", fmt.Errorf("invalid: relation %q; valid: blocked_by,blocks", p.Relation)
	}
	owner, repo, n, err := splitOther(p.Other)
	if err != nil {
		return nil, "", err
	}
	if owner == "" || (strings.EqualFold(owner, c.Owner) && strings.EqualFold(repo, c.Repo)) {
		meta, err := shared.RepoMeta(c.Ctx, c.Env.Client, c.Owner, c.Repo)
		if err != nil {
			return nil, "", err
		}
		owner, repo = c.Owner, c.Repo
		if o, r, ok := strings.Cut(meta.FullName, "/"); ok {
			owner, repo = o, r
		}
	}
	ref = fmt.Sprintf("%s/%s#%d", owner, repo, n)
	if strings.EqualFold(owner+"/"+repo, c.Owner+"/"+c.Repo) {
		ref = fmt.Sprintf("#%d", n)
	}
	return map[string]any{"index": n, "owner": owner, "repo": repo}, ref, nil
}

func relationPath(rel string) string {
	if rel == "blocks" {
		return "blocks"
	}
	return "dependencies"
}

func (t *issueTool) link(c *tools.Call, p *issueParams) (*tools.Result, error) {
	body, ref, err := dependencyBody(c, p)
	if err != nil {
		return c.Fail("%s", err), nil
	}
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPost, issuePath(c, p.N, relationPath(p.Relation)), nil, body, nil); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: #%d %s %s", p.N, p.Relation, ref)}, nil
}

func (t *issueTool) unlink(c *tools.Call, p *issueParams) (*tools.Result, error) {
	body, ref, err := dependencyBody(c, p)
	if err != nil {
		return c.Fail("%s", err), nil
	}
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodDelete, issuePath(c, p.N, relationPath(p.Relation)), nil, body, nil); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: #%d no longer %s %s", p.N, p.Relation, ref)}, nil
}

func (t *issueTool) links(c *tools.Call, p *issueParams) (*tools.Result, error) {
	cl := c.Env.Client
	q := url.Values{"limit": {"50"}}
	var deps, blocks []*issue
	var b format.B
	if _, err := cl.GetJSON(c.Ctx, issuePath(c, p.N, "dependencies"), q, &deps); err != nil {
		if apiStatus(err) != http.StatusNotFound {
			return nil, err
		}
		b.Raw("blocked_by: unavailable (dependencies are disabled for this repo)\n")
	}
	if _, err := cl.GetJSON(c.Ctx, issuePath(c, p.N, "blocks"), q, &blocks); err != nil {
		return nil, err
	}
	emit := func(label string, list []*issue) {
		for _, it := range list {
			if it == nil {
				continue
			}
			full := ""
			if it.Repository != nil {
				full = it.Repository.FullName
			}
			b.Raw(fmt.Sprintf("%s: %s %s %q\n", label, refOf(c, full, it.Number), it.State, format.OneLine(it.Title, titleMax)))
		}
	}
	emit("blocked_by", deps)
	emit("blocks", blocks)
	if b.String() == "" {
		return &tools.Result{Text: fmt.Sprintf("#%d has no dependencies", p.N)}, nil
	}
	return &tools.Result{Text: b.String()}, nil
}

func (t *issueTool) react(c *tools.Call, p *issueParams) (*tools.Result, error) {
	var path, target string
	switch {
	case p.CommentID > 0:
		path = forgejo.Path("repos", c.Owner, c.Repo, "issues", "comments", strconv.FormatInt(p.CommentID, 10), "reactions")
		target = fmt.Sprintf("comment c%d", p.CommentID)
	case p.N > 0:
		path = issuePath(c, p.N, "reactions")
		target = fmt.Sprintf("#%d", p.N)
	default:
		return c.Fail("invalid: pass n (issue) or comment_id"), nil
	}
	body := map[string]string{"content": p.Content}
	method, verb := http.MethodPost, "reacted "+p.Content+" on "
	if p.Remove {
		method, verb = http.MethodDelete, "removed reaction "+p.Content+" from "
	}
	if _, err := c.Env.Client.SendJSON(c.Ctx, method, path, nil, body, nil); err != nil {
		return nil, err
	}
	return &tools.Result{Text: "ok: " + verb + target}, nil
}

func (t *issueTool) subscription(c *tools.Call, p *issueParams, method, verb string) (*tools.Result, error) {
	me, err := c.Env.Client.Me(c.Ctx)
	if err != nil {
		return nil, err
	}
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: method, Path: issuePath(c, p.N, "subscriptions", me.Login)}); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: %s #%d as %s", verb, p.N, me.Login)}, nil
}

func (t *issueTool) subscribe(c *tools.Call, p *issueParams) (*tools.Result, error) {
	return t.subscription(c, p, http.MethodPut, "subscribed to")
}

func (t *issueTool) unsubscribe(c *tools.Call, p *issueParams) (*tools.Result, error) {
	return t.subscription(c, p, http.MethodDelete, "unsubscribed from")
}

func (t *issueTool) templates(c *tools.Call, _ *issueParams) (*tools.Result, error) {
	var tpls []*issueTemplate
	if _, err := c.Env.Client.GetJSON(c.Ctx, repoBase(c)+"/issue_templates", nil, &tpls); err != nil {
		return nil, err
	}
	var b format.B
	var lines format.B
	n := 0
	for _, tp := range tpls {
		if tp == nil {
			continue
		}
		n++
		var fields []string
		for _, f := range tp.Body {
			if f.Type == "markdown" {
				continue // display-only, nothing to fill in
			}
			desc := f.Type
			if f.Validations.Required {
				desc += ",required"
			}
			if f.ID != "" {
				fields = append(fields, f.ID+"("+desc+")")
			} else {
				fields = append(fields, "("+desc+")")
			}
		}
		fs := ""
		if len(fields) > 0 {
			fs = "fields: " + strings.Join(fields, " ")
		}
		lines.Line(tp.Name, format.OneLine(tp.Title, titleMax), format.Tags(tp.Labels), fs)
	}
	b.Raw(fmt.Sprintf("# templates %d\n", n))
	b.Raw(lines.String())
	return &tools.Result{Text: b.String()}, nil
}

func (t *issueTool) assignable(c *tools.Call, _ *issueParams) (*tools.Result, error) {
	var users []*login
	if _, err := c.Env.Client.GetJSON(c.Ctx, repoBase(c)+"/assignees", nil, &users); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(users))
	for _, u := range users {
		if u != nil {
			names = append(names, u.Login)
		}
	}
	return &tools.Result{Text: fmt.Sprintf("# assignees %d\n%s", len(names), csv(names))}, nil
}
