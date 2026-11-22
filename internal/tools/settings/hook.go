package settings

import (
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

var (
	hookScopes   = []string{"repo", "org", "user", "admin"}
	hookTypes    = []string{"forgejo", "gitea", "gogs", "slack", "discord", "dingtalk", "telegram", "msteams", "feishu", "wechatwork", "packagist"}
	hookContents = []string{"json", "form"}
)

// hookParams are the flat parameters of the hook tool (section 8.8).
type hookParams struct {
	Action              string   `json:"action"`
	Scope               string   `json:"scope,omitempty" jsonschema:"repo (default), org, user (token owner) or admin (system hooks)"`
	Repo                string   `json:"repo,omitempty" jsonschema:"owner/name for scope=repo; default repo"`
	Org                 string   `json:"org,omitempty" jsonschema:"organization name for scope=org"`
	ID                  int64    `json:"id,omitempty" jsonschema:"hook id"`
	Type                string   `json:"type,omitempty" jsonschema:"forgejo (default), gitea, gogs, slack, discord, dingtalk, telegram, msteams, feishu, wechatwork, packagist"`
	URL                 *string  `json:"url,omitempty" jsonschema:"target URL"`
	ContentType         *string  `json:"content_type,omitempty" jsonschema:"json (default) or form"`
	Secret              *string  `json:"secret,omitempty" jsonschema:"signing secret; settable at create only"`
	Events              []string `json:"events,omitempty" jsonschema:"event names, default [push]"`
	BranchFilter        *string  `json:"branch_filter,omitempty" jsonschema:"branch glob filter; empty string clears"`
	AuthorizationHeader *string  `json:"authorization_header,omitempty" jsonschema:"Authorization header sent to the target; cannot be cleared via API on edit"`
	Active              *bool    `json:"active,omitempty" jsonschema:"enabled; default true on create"`
	Ref                 string   `json:"ref,omitempty" jsonschema:"branch or tag for the test delivery; default branch"`
	Page                int      `json:"page,omitempty" jsonschema:"page, from 1"`
	Limit               int      `json:"limit,omitempty" jsonschema:"items per page, 1..100"`
}

const hookNeeds = "repo admin (scope=org: org owner; scope=admin: site admin)"

func registerHook(r *tools.Registry) {
	r.Add(tools.Spec{
		Name:        "hook",
		Toolset:     "settings",
		Description: "Webhooks at repo/org/user/admin scope: list, get, create, edit, delete, test.",
		Params:      hookParams{},
		Actions: []tools.Action{
			tools.NewAction("list", tools.Read, hookList,
				tools.Summary("list webhooks"),
				tools.Params("scope", "repo", "org", "page", "limit"),
				tools.Needs(hookNeeds),
				tools.Endpoints(
					"GET /repos/{owner}/{repo}/hooks", "GET /orgs/{org}/hooks",
					"GET /user/hooks", "GET /admin/hooks"),
			),
			tools.NewAction("get", tools.Read, hookGet,
				tools.Summary("show one webhook (secrets hidden)"),
				tools.Params("scope", "repo", "org", "id"),
				tools.Required("id"),
				tools.Needs(hookNeeds),
				tools.Endpoints(
					"GET /repos/{owner}/{repo}/hooks/{id}", "GET /orgs/{org}/hooks/{id}",
					"GET /user/hooks/{id}", "GET /admin/hooks/{id}"),
			),
			tools.NewAction("create", tools.Write, hookCreate,
				tools.Summary("create a webhook (active, events [push] by default; scope=admin makes a system hook)"),
				tools.Params("scope", "repo", "org", "type", "url", "content_type", "secret", "events", "branch_filter", "authorization_header", "active"),
				tools.Required("url"),
				tools.Needs(hookNeeds),
				tools.Endpoints(
					"POST /repos/{owner}/{repo}/hooks", "POST /orgs/{org}/hooks",
					"POST /user/hooks", "POST /admin/hooks"),
			),
			tools.NewAction("edit", tools.Write, hookEdit,
				tools.Summary("change a webhook; omitted fields keep their value"),
				tools.Params("scope", "repo", "org", "id", "url", "content_type", "secret", "events", "branch_filter", "authorization_header", "active"),
				tools.Required("id"),
				tools.Needs(hookNeeds),
				tools.Endpoints(
					"GET /repos/{owner}/{repo}/hooks/{id}", "GET /orgs/{org}/hooks/{id}",
					"GET /user/hooks/{id}", "GET /admin/hooks/{id}",
					"PATCH /repos/{owner}/{repo}/hooks/{id}", "PATCH /orgs/{org}/hooks/{id}",
					"PATCH /user/hooks/{id}", "PATCH /admin/hooks/{id}"),
			),
			tools.NewAction("delete", tools.Write, hookDelete,
				tools.Summary("delete a webhook"),
				tools.Params("scope", "repo", "org", "id"),
				tools.Required("id"),
				tools.Needs(hookNeeds),
				tools.Endpoints(
					"DELETE /repos/{owner}/{repo}/hooks/{id}", "DELETE /orgs/{org}/hooks/{id}",
					"DELETE /user/hooks/{id}", "DELETE /admin/hooks/{id}"),
			),
			tools.NewAction("test", tools.Write, hookTest,
				tools.Summary("queue a test delivery (scope=repo only)"),
				tools.Params("scope", "repo", "id", "ref"),
				tools.Required("id"),
				tools.Needs("repo admin"),
				tools.Endpoints("POST /repos/{owner}/{repo}/hooks/{id}/tests"),
			),
		},
	})
}

// hookTarget resolves scope, API prefix and a human label ("repo o/r").
func hookTarget(c *tools.Call, p *hookParams) (prefix, label string, fail *tools.Result) {
	scope := p.Scope
	if scope == "" {
		scope = "repo"
	}
	switch scope {
	case "repo":
		if c.Env.Repo == nil {
			return "", "", c.Fail("invalid: no repository resolver configured")
		}
		owner, name, _, err := c.Env.Repo.Resolve(p.Repo)
		if err != nil {
			return "", "", c.Fail("%s", err.Error())
		}
		if _, _, err := shared.ParseOwnerRepo(owner + "/" + name); err != nil {
			return "", "", c.Fail("invalid: %s", err.Error())
		}
		return forgejo.Path("repos", owner, name, "hooks"), "repo " + owner + "/" + name, nil
	case "org":
		org := strings.TrimSpace(p.Org)
		if org == "" {
			return "", "", c.Fail("invalid: missing param org for hook.%s scope=org", c.Action)
		}
		if strings.ContainsAny(org, "/\\") || org == "." || org == ".." {
			return "", "", c.Fail("invalid: org %q", org)
		}
		return forgejo.Path("orgs", org, "hooks"), "org " + org, nil
	case "user":
		return "/user/hooks", "user", nil
	case "admin":
		return "/admin/hooks", "admin", nil
	}
	return "", "", c.Fail("invalid: scope %q; valid: %s", scope, strings.Join(hookScopes, ","))
}

func checkID(c *tools.Call, id int64) *tools.Result {
	if id <= 0 {
		return c.Fail("invalid: id must be a positive number")
	}
	return nil
}

func hookPath(prefix string, id int64) string { return prefix + "/" + strconv.FormatInt(id, 10) }

// pageArgs normalises page and limit (default FORGEJO_DEFAULT_LIMIT, max 100).
func pageArgs(c *tools.Call, page, limit int) (int, int) {
	if page < 1 {
		page = 1
	}
	if limit <= 0 && c.Env.Cfg != nil {
		limit = c.Env.Cfg.DefaultLimit
	}
	if limit <= 0 {
		limit = 20
	}
	return page, min(limit, 100)
}

func hookList(c *tools.Call, p *hookParams) (*tools.Result, error) {
	prefix, _, bad := hookTarget(c, p)
	if bad != nil {
		return bad, nil
	}
	page, limit := pageArgs(c, p.Page, p.Limit)
	var hooks []hookObj
	pg, err := c.Env.Client.List(c.Ctx, prefix, nil, page, limit, &hooks)
	if err != nil {
		return nil, err
	}
	var b format.B
	b.Raw(format.ListHeader("hooks", pg) + "\n")
	for _, h := range hooks {
		b.Raw(hookLine(h) + "\n")
	}
	return &tools.Result{Text: strings.TrimRight(b.String(), "\n")}, nil
}

func hookGet(c *tools.Call, p *hookParams) (*tools.Result, error) {
	prefix, label, bad := hookTarget(c, p)
	if bad != nil {
		return bad, nil
	}
	if bad := checkID(c, p.ID); bad != nil {
		return bad, nil
	}
	var h hookObj
	if _, err := c.Env.Client.GetJSON(c.Ctx, hookPath(prefix, p.ID), nil, &h); err != nil {
		return nil, err
	}
	return &tools.Result{Text: hookObject(h, label)}, nil
}

func hookCreate(c *tools.Call, p *hookParams) (*tools.Result, error) {
	prefix, label, bad := hookTarget(c, p)
	if bad != nil {
		return bad, nil
	}
	target := strVal(p.URL)
	if target == "" {
		return c.Fail("invalid: missing param url for hook.create"), nil
	}
	typ := p.Type
	if typ == "" {
		typ = "forgejo"
	}
	if !slices.Contains(hookTypes, typ) {
		return c.Fail("invalid: type %q; valid: %s", typ, strings.Join(hookTypes, ",")), nil
	}
	if typ == "slack" {
		return c.Fail("invalid: type slack needs config.channel, which this tool cannot set; use forgejo_api POST .../hooks"), nil
	}
	ct := strVal(p.ContentType)
	if ct == "" {
		ct = "json"
	}
	if !slices.Contains(hookContents, ct) {
		return c.Fail("invalid: content_type %q; valid: json,form", ct), nil
	}
	cfg := map[string]string{"url": target, "content_type": ct}
	if s := strVal(p.Secret); s != "" {
		cfg["secret"] = s
	}
	if p.Scope == "admin" {
		// GET /admin/hooks lists system webhooks only; without this flag the
		// server stores a "default webhook" template that no list shows.
		cfg["is_system_webhook"] = "true"
	}
	events := p.Events
	if len(events) == 0 {
		events = []string{"push"}
	}
	active := true // the server default is false; a hook people create is meant to run
	if p.Active != nil {
		active = *p.Active
	}
	body := map[string]any{
		"type":   typ,
		"config": cfg,
		"events": normalizeEvents(events, false),
		"active": active,
	}
	if bf := strVal(p.BranchFilter); bf != "" {
		body["branch_filter"] = bf
	}
	if ah := strVal(p.AuthorizationHeader); ah != "" {
		body["authorization_header"] = ah
	}
	var h hookObj
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPost, prefix, nil, body, &h); err != nil {
		return nil, err
	}
	text := "ok: created hook " + strconv.FormatInt(h.ID, 10) + " (" + label + "): " +
		typ + " " + strings.Join(events, ",") + " -> " + safeHookURL(typ, target) + stateSuffix(active)
	if miss := missingEvents(body["events"].([]string), h.Events); len(miss) > 0 {
		text += "\nnote: server did not apply events: " + strings.Join(miss, ",")
	}
	return &tools.Result{Text: text}, nil
}

func hookEdit(c *tools.Call, p *hookParams) (*tools.Result, error) {
	prefix, label, bad := hookTarget(c, p)
	if bad != nil {
		return bad, nil
	}
	if bad := checkID(c, p.ID); bad != nil {
		return bad, nil
	}
	if p.Secret != nil {
		return c.Fail("invalid: hook secret cannot be changed via API; delete and recreate the hook"), nil
	}
	if ct := strVal(p.ContentType); p.ContentType != nil && !slices.Contains(hookContents, ct) {
		return c.Fail("invalid: content_type %q; valid: json,form", ct), nil
	}
	if p.URL != nil && strVal(p.URL) == "" {
		return c.Fail("invalid: url must not be empty"), nil
	}
	if p.AuthorizationHeader != nil && *p.AuthorizationHeader == "" {
		// Forgejo keeps the old header when PATCH carries an empty one (verified on 17.0.2).
		return c.Fail("invalid: the authorization header cannot be cleared via API on this server; delete and recreate the hook"), nil
	}
	var changed []string
	for _, f := range []struct {
		name string
		set  bool
	}{
		{"url", p.URL != nil},
		{"content_type", p.ContentType != nil},
		{"events", len(p.Events) > 0},
		{"branch_filter", p.BranchFilter != nil},
		{"authorization_header", p.AuthorizationHeader != nil},
		{"active", p.Active != nil},
	} {
		if f.set {
			changed = append(changed, f.name)
		}
	}
	if len(changed) == 0 {
		return c.Fail("invalid: nothing to change; pass url, content_type, events, branch_filter, authorization_header or active"), nil
	}

	// Read-modify-write: the server resets omitted events to ["push"] and
	// clears an omitted branch filter or authorization header, so the full
	// current state is sent back with the requested changes merged in.
	path := hookPath(prefix, p.ID)
	var cur hookObj
	if _, err := c.Env.Client.GetJSON(c.Ctx, path, nil, &cur); err != nil {
		return nil, err
	}
	// GET reports "issues"/"pull_request" for the *_only flags and three
	// review names for pull_request_review; editHook reads them differently.
	events := normalizeEvents(cur.Events, true)
	if len(p.Events) > 0 {
		events = normalizeEvents(p.Events, false)
	}
	branchFilter := cur.BranchFilter
	if p.BranchFilter != nil {
		branchFilter = *p.BranchFilter
	}
	authHeader := cur.AuthorizationHeader
	if p.AuthorizationHeader != nil {
		authHeader = *p.AuthorizationHeader
	}
	active := cur.Active
	if p.Active != nil {
		active = *p.Active
	}
	cfg := map[string]string{}
	if u := cur.hookURL(); u != "" {
		cfg["url"] = u
	}
	if v := cur.hookContentType(); v != "" {
		cfg["content_type"] = v
	}
	if p.URL != nil {
		cfg["url"] = *p.URL
	}
	if p.ContentType != nil {
		cfg["content_type"] = *p.ContentType
	}
	body := map[string]any{
		"events":               events,
		"branch_filter":        branchFilter,
		"authorization_header": authHeader,
		"active":               active,
		"config":               cfg,
	}
	var res hookObj
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPatch, path, nil, body, &res); err != nil {
		return nil, err
	}
	text := "ok: edited hook " + strconv.FormatInt(p.ID, 10) + " (" + label + "): set " + strings.Join(changed, ",")
	if len(p.Events) > 0 {
		if hasFixedEvent(symDiff(cur.Events, p.Events)) {
			text += "\nnote: package/action_run_*/workflow_* events cannot be changed by edit; recreate the hook"
		}
		if miss := missingEvents(events, res.Events); len(miss) > 0 {
			text += "\nnote: server did not apply events: " + strings.Join(miss, ",")
		}
	}
	return &tools.Result{Text: text}, nil
}

func hookDelete(c *tools.Call, p *hookParams) (*tools.Result, error) {
	prefix, label, bad := hookTarget(c, p)
	if bad != nil {
		return bad, nil
	}
	if bad := checkID(c, p.ID); bad != nil {
		return bad, nil
	}
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodDelete, hookPath(prefix, p.ID), nil, nil, nil); err != nil {
		return nil, err
	}
	return &tools.Result{Text: "ok: deleted hook " + strconv.FormatInt(p.ID, 10) + " (" + label + ")"}, nil
}

func hookTest(c *tools.Call, p *hookParams) (*tools.Result, error) {
	if p.Scope != "" && p.Scope != "repo" {
		return c.Fail("invalid: hook test works for scope=repo only"), nil
	}
	prefix, _, bad := hookTarget(c, p)
	if bad != nil {
		return bad, nil
	}
	if bad := checkID(c, p.ID); bad != nil {
		return bad, nil
	}
	var q url.Values
	if p.Ref != "" {
		q = url.Values{"ref": {p.Ref}} // unchanged: the test payload carries it verbatim
	}
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPost, hookPath(prefix, p.ID)+"/tests", q, nil, nil); err != nil {
		return nil, err
	}
	return &tools.Result{Text: "ok: test queued (result is not visible via API)"}, nil
}

func strVal(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

func stateSuffix(active bool) string {
	if active {
		return ""
	}
	return " (inactive)"
}

var reviewEvents = []string{"pull_request_review_approved", "pull_request_review_rejected", "pull_request_review_comment"}

// normalizeEvents maps event names to what the hook endpoints accept as
// input. The three review names become pull_request_review. With fromServer
// set the names come from a GET, where "issues"/"pull_request" only mean the
// *_only flags, so they become issues_only/pull_request_only.
func normalizeEvents(events []string, fromServer bool) []string {
	out := make([]string, 0, len(events))
	add := func(e string) {
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	for _, e := range events {
		switch {
		case slices.Contains(reviewEvents, e):
			add("pull_request_review")
		case fromServer && e == "issues":
			add("issues_only")
		case fromServer && e == "pull_request":
			add("pull_request_only")
		default:
			add(e)
		}
	}
	return out
}

// missingEvents lists requested (normalised) events that the returned event
// list does not reflect. An empty returned list means the response carried no
// events, so nothing can be compared.
func missingEvents(want, got []string) []string {
	if len(got) == 0 {
		return nil
	}
	var miss []string
	for _, w := range want {
		if isFixedEvent(w) {
			continue
		}
		var ok bool
		switch w {
		case "pull_request_review":
			ok = slices.Contains(got, w) || slices.ContainsFunc(reviewEvents, func(r string) bool { return slices.Contains(got, r) })
		case "issues_only":
			ok = slices.Contains(got, "issues")
		case "pull_request_only":
			ok = slices.Contains(got, "pull_request")
		default:
			ok = slices.Contains(got, w)
		}
		if !ok {
			miss = append(miss, w)
		}
	}
	return miss
}

// isFixedEvent reports events editHook cannot change (create only).
func isFixedEvent(e string) bool {
	return e == "package" || strings.HasPrefix(e, "action_run_") || strings.HasPrefix(e, "workflow_")
}

func hasFixedEvent(events []string) bool { return slices.ContainsFunc(events, isFixedEvent) }

// symDiff returns the names present in only one of a and b.
func symDiff(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	for _, x := range b {
		if !slices.Contains(a, x) {
			out = append(out, x)
		}
	}
	return out
}
