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

// milestoneParams is the flat parameter struct of the `milestone` tool.
type milestoneParams struct {
	Action      string `json:"action"`
	Repo        string `json:"repo,omitempty" jsonschema:"owner/name; default repo when omitted"`
	ID          int64  `json:"id,omitempty" jsonschema:"milestone id (or pass title)"`
	Title       string `json:"title,omitempty"`
	NewTitle    string `json:"new_title,omitempty" jsonschema:"rename to"`
	Description string `json:"description,omitempty"`
	Due         string `json:"due,omitempty" jsonschema:"YYYY-MM-DD; cannot be cleared later"`
	State       string `json:"state,omitempty" jsonschema:"open, closed or all; list default open"`
	Page        int    `json:"page,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

func registerMilestone(r *tools.Registry) {
	t := &milestoneTool{}
	R, W := tools.Read, tools.Write
	r.Add(tools.Spec{
		Name:        "milestone",
		Toolset:     "issues",
		Description: "Milestones: list, create, edit, delete.",
		Params:      milestoneParams{},
		RepoScoped:  true,
		Actions: []tools.Action{
			tools.NewAction("list", R, t.list,
				tools.Summary("milestones with progress and due date"),
				tools.Params("state", "page", "limit"),
				tools.Endpoints("GET /repos/{owner}/{repo}/milestones")),
			tools.NewAction("create", W, t.create,
				tools.Summary("create a milestone"),
				tools.Params("title", "description", "due", "state"), tools.Required("title"),
				tools.Endpoints("POST /repos/{owner}/{repo}/milestones")),
			tools.NewAction("edit", W, t.edit,
				tools.Summary("edit, close or reopen a milestone by id or title"),
				tools.Params("id", "title", "new_title", "description", "due", "state"),
				tools.Endpoints("PATCH /repos/{owner}/{repo}/milestones/{id}")),
			tools.NewAction("delete", W, t.delete,
				tools.Summary("delete a milestone by id or title"),
				tools.Params("id", "title"),
				tools.Endpoints("DELETE /repos/{owner}/{repo}/milestones/{id}")),
		},
	})
}

type milestoneTool struct{}

func milestoneLine(m forgejo.Milestone) string {
	var b format.B
	total := m.OpenIssues + m.ClosedIssues
	due := ""
	if d := datePart(m.DueOn); d != "" && !strings.HasPrefix(d, "9999") && !strings.HasPrefix(d, "0001") {
		due = "due " + d
	}
	b.Line("id "+strconv.FormatInt(m.ID, 10), m.Title, m.State, fmt.Sprintf("%d/%d closed", m.ClosedIssues, total), due)
	return b.String()
}

func (t *milestoneTool) list(c *tools.Call, p *milestoneParams) (*tools.Result, error) {
	state := p.State
	if state == "" {
		state = "open"
	}
	if !slices.Contains(issueStates, state) {
		return c.Fail("invalid: state %q; valid: open,closed,all", state), nil
	}
	// state is always sent: the server returns every milestone when it is omitted.
	q := url.Values{"state": {state}}
	page, limit := pageArgs(c, p.Page, p.Limit)
	var items []forgejo.Milestone
	pg, err := c.Env.Client.List(c.Ctx, repoBase(c)+"/milestones", q, page, limit, &items)
	if err != nil {
		return nil, err
	}
	var b format.B
	b.Raw(format.ListHeader("milestones", pg) + "\n")
	for _, m := range items {
		b.Raw(milestoneLine(m))
	}
	return &tools.Result{Text: b.String()}, nil
}

func (t *milestoneTool) create(c *tools.Call, p *milestoneParams) (*tools.Result, error) {
	body := map[string]any{"title": p.Title}
	if p.Description != "" {
		body["description"] = p.Description
	}
	if p.Due != "" {
		due, err := parseDue(p.Due)
		if err != nil {
			return c.Fail("%s", err), nil
		}
		body["due_on"] = due
	}
	if p.State != "" {
		if p.State != "open" && p.State != "closed" {
			return c.Fail("invalid: state %q; valid for create: open,closed", p.State), nil
		}
		body["state"] = p.State
	}
	var out forgejo.Milestone
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPost, repoBase(c)+"/milestones", nil, body, &out); err != nil {
		return nil, err
	}
	shared.InvalidateMilestones(c.Env.Client, c.Owner, c.Repo)
	return &tools.Result{Text: fmt.Sprintf("ok: created milestone %s (id %d)", out.Title, out.ID)}, nil
}

func (t *milestoneTool) edit(c *tools.Call, p *milestoneParams) (*tools.Result, error) {
	id := p.ID
	if id <= 0 {
		if strings.TrimSpace(p.Title) == "" {
			return c.Fail("invalid: pass id or title"), nil
		}
		var err error
		if id, err = milestoneByTitle(c, p.Title); err != nil {
			return nil, err
		}
	}
	seg := strconv.FormatInt(id, 10)
	body := map[string]any{}
	var changed []string
	if p.NewTitle != "" {
		body["title"] = p.NewTitle
		changed = append(changed, "title")
	}
	if p.Description != "" {
		body["description"] = p.Description
		changed = append(changed, "description")
	}
	if p.Due != "" {
		due, err := parseDue(p.Due)
		if err != nil {
			return c.Fail("%s", err), nil
		}
		body["due_on"] = due
		changed = append(changed, "due")
	}
	if p.State != "" {
		if p.State != "open" && p.State != "closed" {
			return c.Fail("invalid: state %q; valid for edit: open,closed", p.State), nil
		}
		body["state"] = p.State
		changed = append(changed, "state="+p.State)
	}
	if len(changed) == 0 {
		return c.Fail("invalid: nothing to edit; pass new_title, description, due or state"), nil
	}
	var out forgejo.Milestone
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPatch, forgejo.Path("repos", c.Owner, c.Repo, "milestones", seg), nil, body, &out); err != nil {
		return nil, err
	}
	shared.InvalidateMilestones(c.Env.Client, c.Owner, c.Repo)
	return &tools.Result{Text: fmt.Sprintf("ok: edited milestone %s (id %d): %s", out.Title, out.ID, strings.Join(changed, ","))}, nil
}

func (t *milestoneTool) delete(c *tools.Call, p *milestoneParams) (*tools.Result, error) {
	id := p.ID
	if id <= 0 {
		if strings.TrimSpace(p.Title) == "" {
			return c.Fail("invalid: pass id or title"), nil
		}
		var err error
		if id, err = milestoneByTitle(c, p.Title); err != nil {
			return nil, err
		}
	}
	path := forgejo.Path("repos", c.Owner, c.Repo, "milestones", strconv.FormatInt(id, 10))
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodDelete, Path: path}); err != nil {
		return nil, err
	}
	shared.InvalidateMilestones(c.Env.Client, c.Owner, c.Repo)
	return &tools.Result{Text: fmt.Sprintf("ok: deleted milestone id %d", id)}, nil
}
