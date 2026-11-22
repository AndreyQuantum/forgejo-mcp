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

// labelParams is the flat parameter struct of the `label` tool.
type labelParams struct {
	Action      string `json:"action"`
	Repo        string `json:"repo,omitempty" jsonschema:"owner/name; default repo when omitted"`
	Org         string `json:"org,omitempty" jsonschema:"organization: act on its labels instead of a repo"`
	ID          int64  `json:"id,omitempty" jsonschema:"label id (or pass name)"`
	Name        string `json:"name,omitempty" jsonschema:"label name"`
	NewName     string `json:"new_name,omitempty" jsonschema:"rename to"`
	Color       string `json:"color,omitempty" jsonschema:"hex like d73a4a, default ededed"`
	Description string `json:"description,omitempty"`
	Exclusive   *bool  `json:"exclusive,omitempty" jsonschema:"scope/name labels exclude each other"`
	Archived    *bool  `json:"archived,omitempty"`
	Sort        string `json:"sort,omitempty" jsonschema:"mostissues, leastissues or reversealphabetically"`
	Page        int    `json:"page,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

func registerLabel(r *tools.Registry) {
	t := &labelTool{}
	R, W := tools.Read, tools.Write
	r.Add(tools.Spec{
		Name:        "label",
		Toolset:     "issues",
		Description: "Labels of a repo or org: list, create, edit, delete.",
		Params:      labelParams{},
		RepoScoped:  true,
		Actions: []tools.Action{
			tools.NewAction("list", R, t.list,
				tools.Summary("labels of the repo (org labels with org=)"),
				tools.Params("org", "sort", "page", "limit"), tools.NoRepo(),
				tools.Endpoints("GET /repos/{owner}/{repo}/labels", "GET /orgs/{org}/labels")),
			tools.NewAction("create", W, t.create,
				tools.Summary("create a label"),
				tools.Params("org", "name", "color", "description", "exclusive", "archived"), tools.Required("name"), tools.NoRepo(),
				tools.Endpoints("POST /repos/{owner}/{repo}/labels", "POST /orgs/{org}/labels")),
			tools.NewAction("edit", W, t.edit,
				tools.Summary("edit a label by id or name; the archived flag is kept"),
				tools.Params("org", "id", "name", "new_name", "color", "description", "exclusive", "archived"), tools.NoRepo(),
				tools.Endpoints("GET /repos/{owner}/{repo}/labels/{id}", "PATCH /repos/{owner}/{repo}/labels/{id}", "GET /orgs/{org}/labels/{id}", "PATCH /orgs/{org}/labels/{id}")),
			tools.NewAction("delete", W, t.delete,
				tools.Summary("delete a label by id or name"),
				tools.Params("org", "id", "name"), tools.NoRepo(),
				tools.Endpoints("DELETE /repos/{owner}/{repo}/labels/{id}", "DELETE /orgs/{org}/labels/{id}")),
		},
	})
}

type labelTool struct{}

// scope resolves the label collection path and a cache invalidator.
func (t *labelTool) scope(c *tools.Call, p *labelParams) (base string, invalidate func(), err error) {
	cl := c.Env.Client
	if p.Org != "" {
		if _, _, perr := shared.ParseOwnerRepo(p.Org + "/x"); perr != nil {
			return "", nil, fmt.Errorf("invalid: org %q", p.Org)
		}
		return forgejo.Path("orgs", p.Org, "labels"), func() {
			cl.Cache().Invalidate(cl.CacheKey("labels:" + p.Org + "/"))
		}, nil
	}
	owner, repo, err := resolveRepo(c, p.Repo)
	if err != nil {
		return "", nil, err
	}
	return forgejo.Path("repos", owner, repo, "labels"), func() { shared.InvalidateLabels(cl, owner, repo) }, nil
}

func labelLine(l forgejo.Label) string {
	var b format.B
	color := ""
	if l.Color != "" {
		color = "#" + strings.TrimPrefix(l.Color, "#")
	}
	excl, arch := "", ""
	if l.Exclusive {
		excl = "exclusive"
	}
	if l.IsArchived {
		arch = "archived"
	}
	b.Line("id "+strconv.FormatInt(l.ID, 10), l.Name, color, format.OneLine(l.Description, 60), excl, arch)
	return b.String()
}

func (t *labelTool) list(c *tools.Call, p *labelParams) (*tools.Result, error) {
	if p.Sort != "" && !slices.Contains([]string{"mostissues", "leastissues", "reversealphabetically"}, p.Sort) {
		return c.Fail("invalid: sort %q; valid: mostissues,leastissues,reversealphabetically", p.Sort), nil
	}
	base, _, err := t.scope(c, p)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	addIf(q, "sort", p.Sort)
	page, limit := pageArgs(c, p.Page, p.Limit)
	var items []forgejo.Label
	pg, err := c.Env.Client.List(c.Ctx, base, q, page, limit, &items)
	if err != nil {
		return nil, err
	}
	var b format.B
	b.Raw(format.ListHeader("labels", pg) + "\n")
	for _, l := range items {
		b.Raw(labelLine(l))
	}
	return &tools.Result{Text: b.String()}, nil
}

func (t *labelTool) create(c *tools.Call, p *labelParams) (*tools.Result, error) {
	base, invalidate, err := t.scope(c, p)
	if err != nil {
		return nil, err
	}
	color := strings.TrimSpace(p.Color)
	if color == "" {
		color = "ededed"
	}
	body := map[string]any{"name": p.Name, "color": color}
	if p.Description != "" {
		body["description"] = p.Description
	}
	if p.Exclusive != nil {
		body["exclusive"] = *p.Exclusive
	}
	if p.Archived != nil {
		body["is_archived"] = *p.Archived
	}
	var out forgejo.Label
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPost, base, nil, body, &out); err != nil {
		return nil, err
	}
	invalidate()
	return &tools.Result{Text: fmt.Sprintf("ok: created label %s (id %d)", out.Name, out.ID)}, nil
}

// labelID returns the id, looking the name up in the collection when needed.
func (t *labelTool) labelID(c *tools.Call, base string, p *labelParams) (int64, error) {
	if p.ID > 0 {
		return p.ID, nil
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return 0, fmt.Errorf("invalid: pass id or name")
	}
	var all []forgejo.Label
	for page := 1; page <= 20; page++ {
		var items []forgejo.Label
		pg, err := c.Env.Client.List(c.Ctx, base, nil, page, 100, &items)
		if err != nil {
			return 0, err
		}
		all = append(all, items...)
		if !pg.More {
			break
		}
	}
	for _, l := range all {
		if l.Name == name {
			return l.ID, nil
		}
	}
	for _, l := range all {
		if strings.EqualFold(l.Name, name) {
			return l.ID, nil
		}
	}
	names := make([]string, len(all))
	for i, l := range all {
		names[i] = l.Name
	}
	if len(names) > 30 {
		names = names[:30]
	}
	return 0, fmt.Errorf("unknown label %q; known: %s", name, csv(names))
}

func (t *labelTool) edit(c *tools.Call, p *labelParams) (*tools.Result, error) {
	base, invalidate, err := t.scope(c, p)
	if err != nil {
		return nil, err
	}
	id, err := t.labelID(c, base, p)
	if err != nil {
		return nil, err
	}
	path := base + "/" + strconv.FormatInt(id, 10)
	// The server resets is_archived when it is omitted: always resend it.
	var cur forgejo.Label
	if _, err := c.Env.Client.GetJSON(c.Ctx, path, nil, &cur); err != nil {
		return nil, err
	}
	archived := cur.IsArchived
	if p.Archived != nil {
		archived = *p.Archived
	}
	body := map[string]any{"is_archived": archived}
	var changed []string
	if p.NewName != "" {
		body["name"] = p.NewName
		changed = append(changed, "name")
	}
	if p.Color != "" {
		body["color"] = strings.TrimSpace(p.Color)
		changed = append(changed, "color")
	}
	if p.Description != "" {
		body["description"] = p.Description
		changed = append(changed, "description")
	}
	if p.Exclusive != nil {
		body["exclusive"] = *p.Exclusive
		changed = append(changed, "exclusive")
	}
	if p.Archived != nil {
		changed = append(changed, "archived")
	}
	if len(changed) == 0 {
		return c.Fail("invalid: nothing to edit; pass new_name, color, description, exclusive or archived"), nil
	}
	var out forgejo.Label
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPatch, path, nil, body, &out); err != nil {
		return nil, err
	}
	invalidate()
	name := out.Name
	if name == "" {
		name = cur.Name
	}
	return &tools.Result{Text: fmt.Sprintf("ok: edited label %s (id %d): %s", name, id, strings.Join(changed, ","))}, nil
}

func (t *labelTool) delete(c *tools.Call, p *labelParams) (*tools.Result, error) {
	base, invalidate, err := t.scope(c, p)
	if err != nil {
		return nil, err
	}
	id, err := t.labelID(c, base, p)
	if err != nil {
		return nil, err
	}
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodDelete, Path: base + "/" + strconv.FormatInt(id, 10)}); err != nil {
		return nil, err
	}
	invalidate()
	return &tools.Result{Text: fmt.Sprintf("ok: deleted label id %d", id)}, nil
}
