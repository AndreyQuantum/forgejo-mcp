package repos

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// branchParams are the parameters of the branch tool (section 8.2).
type branchParams struct {
	Action  string         `json:"action"`
	Repo    string         `json:"repo,omitempty" jsonschema:"owner/name; default: detected repo"`
	Name    string         `json:"name,omitempty" jsonschema:"branch name"`
	From    string         `json:"from,omitempty" jsonschema:"base branch, tag or sha; default: default branch"`
	NewName string         `json:"new_name,omitempty" jsonschema:"new branch name"`
	Rule    string         `json:"rule,omitempty" jsonschema:"protection rule name or glob such as release/*; default: name"`
	Set     map[string]any `json:"set,omitempty" jsonschema:"protection fields, e.g. {required_approvals:1}"`
	Q       string         `json:"q,omitempty" jsonschema:"substring filter on branch names"`
	Page    int            `json:"page,omitempty" jsonschema:"page from 1"`
	Limit   int            `json:"limit,omitempty" jsonschema:"items per page, max 100"`
}

// branchProtectKeys are the writable fields of a branch protection.
var branchProtectKeys = []string{
	"enable_push", "enable_push_whitelist", "push_whitelist_usernames", "push_whitelist_teams", "push_whitelist_deploy_keys",
	"enable_merge_whitelist", "merge_whitelist_usernames", "merge_whitelist_teams",
	"enable_status_check", "status_check_contexts", "required_approvals",
	"enable_approvals_whitelist", "approvals_whitelist_username", "approvals_whitelist_teams",
	"block_on_rejected_reviews", "block_on_official_review_requests", "block_on_outdated_branch",
	"dismiss_stale_approvals", "ignore_stale_approvals", "require_signed_commits",
	"protected_file_patterns", "unprotected_file_patterns", "apply_to_admins",
}

const (
	branchScanPages = 5
	branchScanLimit = 100
)

type branchTool struct{}

func registerBranch(r *tools.Registry) {
	t := &branchTool{}
	R := "/repos/{owner}/{repo}"
	r.Add(tools.Spec{
		Name:        "branch",
		Toolset:     "repos",
		Description: "Branches and branch protection: list, get, create, rename, delete, protect_*.",
		Params:      branchParams{},
		RepoScoped:  true,
		Actions: []tools.Action{
			tools.NewAction("list", tools.Read, t.list,
				tools.Summary("list branches"), tools.Params("q", "page", "limit"),
				tools.Endpoints("GET "+R+"/branches")),
			tools.NewAction("get", tools.Read, t.get,
				tools.Summary("show one branch with its protection summary"), tools.Required("name"),
				tools.Endpoints("GET "+R+"/branches/{branch}")),
			tools.NewAction("create", tools.Write, t.create,
				tools.Summary("create a branch"), tools.Params("from"), tools.Required("name"),
				tools.Needs("write access"), tools.Hint500("unknown from ref"),
				tools.Endpoints("POST "+R+"/branches")),
			tools.NewAction("rename", tools.Write, t.rename,
				tools.Summary("rename a branch"), tools.Required("name", "new_name"), tools.Needs("write access"),
				tools.Endpoints("PATCH "+R+"/branches/{branch}")),
			tools.NewAction("delete", tools.Write, t.del,
				tools.Summary("delete a branch"), tools.Required("name"), tools.Needs("write access"),
				tools.Endpoints("DELETE "+R+"/branches/{branch}")),
			tools.NewAction("protect_list", tools.Read, t.protectList,
				tools.Summary("list branch protection rules"), tools.Params("page", "limit"), tools.Needs("repo admin"),
				tools.Endpoints("GET "+R+"/branch_protections")),
			tools.NewAction("protect_get", tools.Read, t.protectGet,
				tools.Summary("show one protection rule"), tools.Params("rule", "name"), tools.Needs("repo admin"),
				tools.Endpoints("GET "+R+"/branch_protections/{name}")),
			tools.NewAction("protect_set", tools.Write, t.protectSet,
				tools.Summary("create or update a protection rule"), tools.Params("rule", "name"),
				tools.Required("set"), tools.Needs("repo admin"),
				tools.Endpoints("GET "+R+"/branch_protections/{name}", "POST "+R+"/branch_protections",
					"PATCH "+R+"/branch_protections/{name}")),
			tools.NewAction("protect_delete", tools.Write, t.protectDelete,
				tools.Summary("delete a protection rule"), tools.Params("rule", "name"), tools.Needs("repo admin"),
				tools.Endpoints("DELETE "+R+"/branch_protections/{name}")),
		},
	})
}

func branchPath(c *tools.Call, name string) string {
	return repoPath(c, "branches", forgejo.Wild(name))
}

// ruleOf returns the protection rule name (rule, else name).
func ruleOf(p *branchParams) string {
	if p.Rule != "" {
		return p.Rule
	}
	return p.Name
}

func protectPath(c *tools.Call, rule string) string {
	// ONE segment: a rule such as release/* is sent as release%2F*
	return repoPath(c, "branch_protections", rule)
}

func (t *branchTool) list(c *tools.Call, p *branchParams) (*tools.Result, error) {
	cl := c.Env.Client
	page, limit := pageOf(p.Page), limitOf(c, p.Limit)
	if p.Q == "" {
		var items []apiBranch
		pg, err := cl.List(c.Ctx, repoPath(c, "branches"), nil, page, limit, &items)
		if err != nil {
			return nil, err
		}
		var b strings.Builder
		b.WriteString(format.ListHeader("branches", pg) + "\n")
		for i := range items {
			b.WriteString(branchLine(&items[i]))
		}
		return text(b.String()), nil
	}

	// the server has no name filter: scan a few pages and filter here
	needle := strings.ToLower(p.Q)
	var matches []apiBranch
	scanned, capped := 0, false
	for pgNo := 1; pgNo <= branchScanPages; pgNo++ {
		var items []apiBranch
		pg, err := cl.List(c.Ctx, repoPath(c, "branches"), nil, pgNo, branchScanLimit, &items)
		if err != nil {
			return nil, err
		}
		scanned += len(items)
		for i := range items {
			if strings.Contains(strings.ToLower(items[i].Name), needle) {
				matches = append(matches, items[i])
			}
		}
		if !pg.More {
			break
		}
		if pgNo == branchScanPages {
			capped = true
		}
	}
	win := window(matches, page, limit)
	hdr := format.ListHeader("branches", forgejo.Page{
		Page: page, Limit: limit, Total: int64(len(matches)), Shown: len(win), More: page*limit < len(matches),
	})
	hdr += fmt.Sprintf(" (filtered client-side, scanned %d)", scanned)
	if capped {
		hdr += " (more branches not scanned; narrow q)"
	}
	var b strings.Builder
	b.WriteString(hdr + "\n")
	for i := range win {
		b.WriteString(branchLine(&win[i]))
	}
	return text(b.String()), nil
}

func (t *branchTool) get(c *tools.Call, p *branchParams) (*tools.Result, error) {
	var br apiBranch
	if _, err := c.Env.Client.GetJSON(c.Ctx, branchPath(c, p.Name), nil, &br); err != nil {
		return nil, err
	}
	return text(renderBranch(&br)), nil
}

func (t *branchTool) create(c *tools.Call, p *branchParams) (*tools.Result, error) {
	body := map[string]any{"new_branch_name": p.Name}
	setStr(body, "old_ref_name", p.From)
	var br apiBranch
	if _, err := send(c.Ctx, c.Env.Client, http.MethodPost, repoPath(c, "branches"), nil, body, &br, 0); err != nil {
		return nil, addHint(err, http.StatusConflict, "a branch or tag with this name exists")
	}
	from := p.From
	if from == "" {
		from = "default branch"
	}
	return okf("created %s from %s@%s", p.Name, from, format.SHA(br.Commit.ID, 8)), nil
}

func (t *branchTool) rename(c *tools.Call, p *branchParams) (*tools.Result, error) {
	if _, err := send(c.Ctx, c.Env.Client, http.MethodPatch, branchPath(c, p.Name), nil, map[string]any{"name": p.NewName}, nil, 0); err != nil {
		return nil, err
	}
	return okf("renamed branch %s -> %s", p.Name, p.NewName), nil
}

func (t *branchTool) del(c *tools.Call, p *branchParams) (*tools.Result, error) {
	if _, err := send(c.Ctx, c.Env.Client, http.MethodDelete, branchPath(c, p.Name), nil, nil, nil, 0); err != nil {
		return nil, addHint(err, http.StatusForbidden, "the default branch and protected branches cannot be deleted; mirrors are read-only")
	}
	return okf("deleted branch %s", p.Name), nil
}

func (t *branchTool) protectList(c *tools.Call, p *branchParams) (*tools.Result, error) {
	var rules []map[string]any
	if _, err := c.Env.Client.GetJSON(c.Ctx, repoPath(c, "branch_protections"), nil, &rules); err != nil {
		return nil, err
	}
	page, limit := pageOf(p.Page), limitOf(c, p.Limit)
	win := window(rules, page, limit)
	hdr := format.ListHeader("protections", forgejo.Page{
		Page: page, Limit: limit, Total: int64(len(rules)), Shown: len(win), More: page*limit < len(rules),
	})
	var b strings.Builder
	b.WriteString(hdr + "\n")
	for _, r := range win {
		b.WriteString(protectionLine(r))
	}
	return text(b.String()), nil
}

func (t *branchTool) protectGet(c *tools.Call, p *branchParams) (*tools.Result, error) {
	rule := ruleOf(p)
	if rule == "" {
		return c.Fail("invalid: pass rule (or name)"), nil
	}
	var m map[string]any
	if _, err := c.Env.Client.GetJSON(c.Ctx, protectPath(c, rule), nil, &m); err != nil {
		return nil, err
	}
	return text(renderProtection(m)), nil
}

func (t *branchTool) protectSet(c *tools.Call, p *branchParams) (*tools.Result, error) {
	rule := ruleOf(p)
	if rule == "" {
		return c.Fail("invalid: pass rule (or name)"), nil
	}
	if len(p.Set) == 0 {
		return c.Fail("invalid: set is empty; valid keys: %s", strings.Join(branchProtectKeys, ",")), nil
	}
	if bad := checkKeys(c, "set", p.Set, branchProtectKeys); bad != nil {
		return bad, nil
	}
	body := make(map[string]any, len(p.Set)+2)
	for k, v := range p.Set {
		body[k] = v
	}
	// the server applies the push whitelist fields only inside an
	// enable_push=true update and would silently ignore them otherwise
	note := ""
	_, hasWL := p.Set["enable_push_whitelist"]
	_, hasKeys := p.Set["push_whitelist_deploy_keys"]
	if hasWL || hasKeys {
		if v, has := p.Set["enable_push"]; has {
			if on, _ := v.(bool); !on {
				return c.Fail("invalid: enable_push:false cannot be combined with enable_push_whitelist or push_whitelist_deploy_keys"), nil
			}
		} else {
			body["enable_push"] = true
			note = "note: enable_push:true added; whitelist options need it\n"
		}
	}
	if v, has := p.Set["enable_push"]; has && !hasWL && !hasKeys {
		if on, _ := v.(bool); !on {
			note += "note: enable_push:false also clears the push whitelist\n"
		}
	}

	cl := c.Env.Client
	var existing map[string]any
	_, err := cl.GetJSON(c.Ctx, protectPath(c, rule), nil, &existing)
	verb := "updated"
	switch {
	case err == nil:
		if _, err := send(c.Ctx, cl, http.MethodPatch, protectPath(c, rule), nil, body, nil, 0); err != nil {
			return nil, err
		}
	case apiStatus(err) == http.StatusNotFound:
		body["rule_name"] = rule
		if _, err := send(c.Ctx, cl, http.MethodPost, repoPath(c, "branch_protections"), nil, body, nil, 0); err != nil {
			return nil, err
		}
		verb = "created"
	default:
		return nil, err
	}
	return text(fmt.Sprintf("ok: protection %s %s: %s\n%s", rule, verb, strings.Join(sortedKeys(p.Set), ","), note)), nil
}

func (t *branchTool) protectDelete(c *tools.Call, p *branchParams) (*tools.Result, error) {
	rule := ruleOf(p)
	if rule == "" {
		return c.Fail("invalid: pass rule (or name)"), nil
	}
	if _, err := send(c.Ctx, c.Env.Client, http.MethodDelete, protectPath(c, rule), nil, nil, nil, 0); err != nil {
		return nil, err
	}
	return okf("deleted protection %s", rule), nil
}
