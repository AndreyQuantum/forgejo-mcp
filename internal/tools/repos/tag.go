package repos

import (
	"net/http"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// tagParams are the parameters of the tag tool (section 8.2).
type tagParams struct {
	Action  string         `json:"action"`
	Repo    string         `json:"repo,omitempty" jsonschema:"owner/name; default: detected repo"`
	Name    string         `json:"name,omitempty" jsonschema:"tag name"`
	Target  string         `json:"target,omitempty" jsonschema:"branch or sha to tag; default: default branch"`
	Message string         `json:"message,omitempty" jsonschema:"annotation; makes an annotated tag"`
	RuleID  int            `json:"rule_id,omitempty" jsonschema:"tag protection id from protect_list"`
	Set     map[string]any `json:"set,omitempty" jsonschema:"name_pattern, whitelist_usernames, whitelist_teams"`
	Verbose bool           `json:"verbose,omitempty" jsonschema:"annotated tag details"`
	Page    int            `json:"page,omitempty" jsonschema:"page from 1"`
	Limit   int            `json:"limit,omitempty" jsonschema:"items per page, max 100"`
}

var tagProtectKeys = []string{"name_pattern", "whitelist_usernames", "whitelist_teams"}

type tagTool struct{}

func registerTag(r *tools.Registry) {
	t := &tagTool{}
	R := "/repos/{owner}/{repo}"
	r.Add(tools.Spec{
		Name:        "tag",
		Toolset:     "repos",
		Description: "Tags and tag protection: list, get, create, delete, protect_*.",
		Params:      tagParams{},
		RepoScoped:  true,
		Actions: []tools.Action{
			tools.NewAction("list", tools.Read, t.list,
				tools.Summary("list tags"), tools.Params("page", "limit"),
				tools.Endpoints("GET "+R+"/tags")),
			tools.NewAction("get", tools.Read, t.get,
				tools.Summary("show one tag"), tools.Params("verbose"), tools.Required("name"),
				tools.Endpoints("GET "+R+"/tags/{tag}", "GET "+R+"/git/tags/{sha}")),
			tools.NewAction("create", tools.Write, t.create,
				tools.Summary("create a tag"), tools.Params("target", "message"), tools.Required("name"),
				tools.Needs("write access"),
				tools.Endpoints("POST "+R+"/tags")),
			tools.NewAction("delete", tools.Write, t.del,
				tools.Summary("delete a tag"), tools.Required("name"), tools.Needs("write access"),
				tools.Endpoints("DELETE "+R+"/tags/{tag}")),
			tools.NewAction("protect_list", tools.Read, t.protectList,
				tools.Summary("list tag protection rules"), tools.Params("page", "limit"), tools.Needs("repo admin"),
				tools.Endpoints("GET "+R+"/tag_protections")),
			tools.NewAction("protect_get", tools.Read, t.protectGet,
				tools.Summary("show one tag protection rule"), tools.Required("rule_id"), tools.Needs("repo admin"),
				tools.Endpoints("GET "+R+"/tag_protections/{id}")),
			tools.NewAction("protect_set", tools.Write, t.protectSet,
				tools.Summary("create (no rule_id) or update a tag protection rule"), tools.Params("rule_id"),
				tools.Required("set"), tools.Needs("repo admin"),
				tools.Endpoints("POST "+R+"/tag_protections", "PATCH "+R+"/tag_protections/{id}")),
			tools.NewAction("protect_delete", tools.Write, t.protectDelete,
				tools.Summary("delete a tag protection rule"), tools.Required("rule_id"), tools.Needs("repo admin"),
				tools.Endpoints("DELETE "+R+"/tag_protections/{id}")),
		},
	})
}

func tagPath(c *tools.Call, name string) string {
	return repoPath(c, "tags", forgejo.Wild(name))
}

func (t *tagTool) list(c *tools.Call, p *tagParams) (*tools.Result, error) {
	var items []apiTag
	pg, err := c.Env.Client.List(c.Ctx, repoPath(c, "tags"), nil, pageOf(p.Page), limitOf(c, p.Limit), &items)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString(format.ListHeader("tags", pg) + "\n")
	for i := range items {
		b.WriteString(tagLine(&items[i]))
	}
	return text(b.String()), nil
}

func (t *tagTool) get(c *tools.Call, p *tagParams) (*tools.Result, error) {
	cl := c.Env.Client
	var tg apiTag
	if _, err := cl.GetJSON(c.Ctx, tagPath(c, p.Name), nil, &tg); err != nil {
		return nil, err
	}
	// the git/tags endpoint answers 400 for a lightweight tag, whose object
	// id equals the commit sha: it is only asked for when the ids differ
	annotated := tg.ID != "" && tg.ID != tg.Commit.SHA
	verbose := p.Verbose && annotated
	out := renderTag(&tg, annotated, !verbose)
	if verbose {
		var at annotatedTag
		if _, err := cl.GetJSON(c.Ctx, repoPath(c, "git", "tags", tg.ID), nil, &at); err != nil {
			return nil, err
		}
		var b format.B
		b.Raw(out)
		if at.Tagger != nil {
			b.KV("tagger", strings.TrimSpace(at.Tagger.Name+" "+format.T(at.Tagger.Date)))
		}
		b.KV("target", at.Object.Type+" "+format.SHA(at.Object.SHA, 8))
		ann := format.Trunc(strings.TrimSpace(at.Message), 1500, "tag message")
		lines := strings.Split(ann, "\n")
		for i, ln := range lines {
			if ln != "" {
				lines[i] = "  " + ln
			}
		}
		b.Raw("annotation:\n" + strings.Join(lines, "\n") + "\n")
		out = b.String()
	}
	return text(out), nil
}

func (t *tagTool) create(c *tools.Call, p *tagParams) (*tools.Result, error) {
	body := map[string]any{"tag_name": p.Name}
	setStr(body, "target", p.Target)
	setStr(body, "message", p.Message)
	var tg apiTag
	if _, err := send(c.Ctx, c.Env.Client, http.MethodPost, repoPath(c, "tags"), nil, body, &tg, 0); err != nil {
		return nil, addHint(err, http.StatusConflict, "tag exists; delete it first with tag.delete")
	}
	kind := "lightweight"
	if p.Message != "" {
		kind = "annotated"
	}
	return okf("created tag %s at %s (%s)", tg.Name, format.SHA(tg.Commit.SHA, 8), kind), nil
}

func (t *tagTool) del(c *tools.Call, p *tagParams) (*tools.Result, error) {
	if _, err := send(c.Ctx, c.Env.Client, http.MethodDelete, tagPath(c, p.Name), nil, nil, nil, 0); err != nil {
		return nil, addHint(err, http.StatusConflict, "a tag attached to a release cannot be deleted directly; delete the release first with release.delete")
	}
	return okf("deleted tag %s", p.Name), nil
}

func (t *tagTool) protectList(c *tools.Call, p *tagParams) (*tools.Result, error) {
	var rules []tagProtection
	if _, err := c.Env.Client.GetJSON(c.Ctx, repoPath(c, "tag_protections"), nil, &rules); err != nil {
		return nil, err
	}
	page, limit := pageOf(p.Page), limitOf(c, p.Limit)
	win := window(rules, page, limit)
	hdr := format.ListHeader("tag protections", forgejo.Page{
		Page: page, Limit: limit, Total: int64(len(rules)), Shown: len(win), More: page*limit < len(rules),
	})
	var b strings.Builder
	b.WriteString(hdr + "\n")
	for i := range win {
		b.WriteString(tagProtectionLine(&win[i]))
	}
	return text(b.String()), nil
}

func tagRulePath(c *tools.Call, id int) string {
	return repoPath(c, "tag_protections", itoa(id))
}

func (t *tagTool) protectGet(c *tools.Call, p *tagParams) (*tools.Result, error) {
	var r tagProtection
	if _, err := c.Env.Client.GetJSON(c.Ctx, tagRulePath(c, p.RuleID), nil, &r); err != nil {
		return nil, err
	}
	return text("tag protection " + tagProtectionLine(&r)), nil
}

func (t *tagTool) protectSet(c *tools.Call, p *tagParams) (*tools.Result, error) {
	if len(p.Set) == 0 {
		return c.Fail("invalid: set is empty; valid keys: %s", strings.Join(tagProtectKeys, ",")), nil
	}
	if bad := checkKeys(c, "set", p.Set, tagProtectKeys); bad != nil {
		return bad, nil
	}
	users, teams := listLen(p.Set["whitelist_usernames"]), listLen(p.Set["whitelist_teams"])
	create := p.RuleID == 0
	_, hasUsers := p.Set["whitelist_usernames"]
	_, hasTeams := p.Set["whitelist_teams"]
	if (create || (hasUsers && hasTeams)) && users == 0 && teams == 0 {
		return c.Fail("invalid: both whitelist_usernames and whitelist_teams are empty"), nil
	}
	if create {
		if s, _ := p.Set["name_pattern"].(string); strings.TrimSpace(s) == "" {
			return c.Fail("invalid: set.name_pattern is required to create a rule"), nil
		}
	}
	var r tagProtection
	if create {
		if _, err := send(c.Ctx, c.Env.Client, http.MethodPost, repoPath(c, "tag_protections"), nil, p.Set, &r, 0); err != nil {
			return nil, addHint(err, http.StatusForbidden, "a rule with this pattern may already exist; use protect_list")
		}
		return okf("tag protection created: id %d %s", r.ID, r.Pattern), nil
	}
	if _, err := send(c.Ctx, c.Env.Client, http.MethodPatch, tagRulePath(c, p.RuleID), nil, p.Set, &r, 0); err != nil {
		return nil, err
	}
	return okf("tag protection %d updated: %s", p.RuleID, strings.Join(sortedKeys(p.Set), ",")), nil
}

func (t *tagTool) protectDelete(c *tools.Call, p *tagParams) (*tools.Result, error) {
	if _, err := send(c.Ctx, c.Env.Client, http.MethodDelete, tagRulePath(c, p.RuleID), nil, nil, nil, 0); err != nil {
		return nil, err
	}
	return okf("deleted tag protection %d", p.RuleID), nil
}

func listLen(v any) int {
	arr, _ := v.([]any)
	return len(arr)
}
