package admin

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

func runQuotaInfo(c *tools.Call, p *adminParams) (*tools.Result, error) {
	var path, label string
	switch {
	case p.User != "" && p.Org != "":
		return invalid(c, "pass user or org, not both"), nil
	case p.User != "":
		path, label = forgejo.Path("admin", "users", p.User, "quota"), "user "+p.User
	case p.Org != "":
		path, label = forgejo.Path("orgs", p.Org, "quota"), "org "+p.Org
	default:
		path, label = "/user/quota", "me"
	}
	var q quotaInfo
	if _, err := c.Env.Client.GetJSON(c.Ctx, path, nil, &q); err != nil {
		return quotaErr(c, err)
	}
	return &tools.Result{Text: c.Cap(renderQuota(label, &q))}, nil
}

func runQuotaGroups(c *tools.Call, p *adminParams) (*tools.Result, error) {
	if p.Group != "" {
		var g quotaGroup
		if _, err := c.Env.Client.GetJSON(c.Ctx, forgejo.Path("admin", "quota", "groups", p.Group), nil, &g); err != nil {
			return quotaErr(c, err)
		}
		return &tools.Result{Text: c.Cap(renderGroup(&g))}, nil
	}
	var groups []quotaGroup
	if _, err := c.Env.Client.GetJSON(c.Ctx, "/admin/quota/groups", nil, &groups); err != nil {
		return quotaErr(c, err)
	}
	return windowed(c, "quota groups", groups, p, groupLine), nil
}

func runQuotaRules(c *tools.Call, p *adminParams) (*tools.Result, error) {
	if p.Rule != "" {
		var r quotaRule
		if _, err := c.Env.Client.GetJSON(c.Ctx, forgejo.Path("admin", "quota", "rules", p.Rule), nil, &r); err != nil {
			return quotaErr(c, err)
		}
		if r.Name == "" {
			r.Name = p.Rule
		}
		return &tools.Result{Text: "rule " + ruleLine(&r) + "\n"}, nil
	}
	var rules []quotaRule
	if _, err := c.Env.Client.GetJSON(c.Ctx, "/admin/quota/rules", nil, &rules); err != nil {
		return quotaErr(c, err)
	}
	return windowed(c, "quota rules", rules, p, ruleLine), nil
}

func runQuotaGroupSet(c *tools.Call, p *adminParams) (*tools.Result, error) {
	if !okName(p.Group) {
		return invalid(c, "group"), nil
	}
	cl := c.Env.Client
	created := false
	if _, err := cl.GetJSON(c.Ctx, forgejo.Path("admin", "quota", "groups", p.Group), nil, nil); err != nil {
		if !isStatus(err, http.StatusNotFound) {
			return nil, err
		}
		if res, _ := quotaErr(c, err); res != nil {
			return res, nil
		}
		if _, err := cl.SendJSON(c.Ctx, http.MethodPost, "/admin/quota/groups", nil, map[string]any{"name": p.Group}, nil); err != nil {
			return nil, err
		}
		created = true
	}
	text := "ok: quota group " + p.Group + " exists"
	if created {
		text = "ok: created quota group " + p.Group
	}
	if p.Rule != "" {
		if !okName(p.Rule) {
			return invalid(c, "rule"), nil
		}
		if _, err := cl.Do(c.Ctx, forgejo.Request{Method: http.MethodPut, Path: forgejo.Path("admin", "quota", "groups", p.Group, "rules", p.Rule)}); err != nil {
			return nil, err
		}
		text += "; added rule " + p.Rule
	}
	return &tools.Result{Text: text}, nil
}

func runQuotaGroupDelete(c *tools.Call, p *adminParams) (*tools.Result, error) {
	if !okName(p.Group) {
		return invalid(c, "group"), nil
	}
	if p.Rule != "" {
		if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodDelete, Path: forgejo.Path("admin", "quota", "groups", p.Group, "rules", p.Rule)}); err != nil {
			return quotaErr(c, err)
		}
		return &tools.Result{Text: fmt.Sprintf("ok: removed rule %s from quota group %s", p.Rule, p.Group)}, nil
	}
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodDelete, Path: forgejo.Path("admin", "quota", "groups", p.Group)}); err != nil {
		return quotaErr(c, err)
	}
	return &tools.Result{Text: "ok: deleted quota group " + p.Group}, nil
}

func runQuotaRuleSet(c *tools.Call, p *adminParams) (*tools.Result, error) {
	if !okName(p.Rule) {
		return invalid(c, "rule"), nil
	}
	cl := c.Env.Client
	exists := true
	if _, err := cl.GetJSON(c.Ctx, forgejo.Path("admin", "quota", "rules", p.Rule), nil, nil); err != nil {
		if !isStatus(err, http.StatusNotFound) {
			return nil, err
		}
		if res, _ := quotaErr(c, err); res != nil { // a 404 on every quota route: quota is off
			return res, nil
		}
		exists = false
	}
	if exists {
		body := map[string]any{}
		if p.LimitBytes != nil {
			body["limit"] = *p.LimitBytes
		}
		if p.Subjects != nil {
			body["subjects"] = p.Subjects
		}
		if len(body) == 0 {
			return invalid(c, "rule %s exists; pass limit_bytes and/or subjects to change it", p.Rule), nil
		}
		if _, err := cl.SendJSON(c.Ctx, http.MethodPatch, forgejo.Path("admin", "quota", "rules", p.Rule), nil, body, nil); err != nil {
			return nil, err
		}
		return &tools.Result{Text: "ok: updated quota rule " + p.Rule}, nil
	}
	if p.LimitBytes == nil || len(p.Subjects) == 0 {
		return invalid(c, "limit_bytes and subjects are required to create rule %s", p.Rule), nil
	}
	body := map[string]any{"name": p.Rule, "limit": *p.LimitBytes, "subjects": p.Subjects}
	if _, err := cl.SendJSON(c.Ctx, http.MethodPost, "/admin/quota/rules", nil, body, nil); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: created quota rule %s (limit %s)", p.Rule, limitText(*p.LimitBytes))}, nil
}

func runQuotaRuleDelete(c *tools.Call, p *adminParams) (*tools.Result, error) {
	if !okName(p.Rule) {
		return invalid(c, "rule"), nil
	}
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodDelete, Path: forgejo.Path("admin", "quota", "rules", p.Rule)}); err != nil {
		return quotaErr(c, err)
	}
	return &tools.Result{Text: "ok: deleted quota rule " + p.Rule}, nil
}

func runQuotaUserGroupsSet(c *tools.Call, p *adminParams) (*tools.Result, error) {
	if !okName(p.User) {
		return invalid(c, "user"), nil
	}
	if p.Groups == nil {
		return invalid(c, "groups (a list of quota group names; [] removes all)"), nil
	}
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPost, forgejo.Path("admin", "users", p.User, "quota", "groups"), nil,
		map[string]any{"groups": p.Groups}, nil); err != nil {
		return quotaErr(c, err)
	}
	return &tools.Result{Text: fmt.Sprintf("ok: quota groups of %s set to [%s]", p.User, joinNames(p.Groups))}, nil
}

func runQuotaCheck(c *tools.Call, p *adminParams) (*tools.Result, error) {
	if p.Subject == "" {
		return invalid(c, "subject, e.g. size:repos:all"), nil
	}
	path := "/user/quota/check"
	if p.Org != "" {
		path = forgejo.Path("orgs", p.Org, "quota", "check")
	}
	var accepted bool
	if _, err := c.Env.Client.GetJSON(c.Ctx, path, url.Values{"subject": {p.Subject}}, &accepted); err != nil {
		return quotaErr(c, err)
	}
	if accepted {
		return &tools.Result{Text: "ok: within quota for " + p.Subject}, nil
	}
	return &tools.Result{Text: "over quota for " + p.Subject + " (accepted: " + strconv.FormatBool(accepted) + ")"}, nil
}

func joinNames(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += ","
		}
		out += x
	}
	return out
}
