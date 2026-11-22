package repos

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/clock"
	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared"
)

// repoParams are the parameters of the repo tool (section 8.2).
type repoParams struct {
	Action        string         `json:"action"`
	Repo          string         `json:"repo,omitempty" jsonschema:"owner/name; default: detected repo"`
	Which         string         `json:"which,omitempty" jsonschema:"mine, user, org, starred or watched; default mine"`
	Owner         string         `json:"owner,omitempty" jsonschema:"user or org name"`
	Q             string         `json:"q,omitempty" jsonschema:"search text"`
	Topic         bool           `json:"topic,omitempty" jsonschema:"q matches topics"`
	Mode          string         `json:"mode,omitempty" jsonschema:"source, fork, mirror or collaborative"`
	Sort          string         `json:"sort,omitempty" jsonschema:"alpha, created, updated, size, stars, forks or id"`
	Order         string         `json:"order,omitempty" jsonschema:"asc or desc"`
	Archived      *bool          `json:"archived,omitempty" jsonschema:"filter by archived"`
	Private       *bool          `json:"private,omitempty" jsonschema:"filter (search) or visibility (create, migrate)"`
	Name          string         `json:"name,omitempty" jsonschema:"repo name"`
	Description   string         `json:"description,omitempty" jsonschema:"repo description"`
	AutoInit      *bool          `json:"auto_init,omitempty" jsonschema:"create first commit; default true"`
	Readme        string         `json:"readme,omitempty" jsonschema:"readme template"`
	License       string         `json:"license,omitempty" jsonschema:"license name"`
	Gitignores    string         `json:"gitignores,omitempty" jsonschema:"comma list of gitignore templates"`
	DefaultBranch string         `json:"default_branch,omitempty" jsonschema:"initial default branch"`
	Template      bool           `json:"template,omitempty" jsonschema:"make it a template repo"`
	FromTemplate  string         `json:"from_template,omitempty" jsonschema:"template owner/name to generate from"`
	Set           map[string]any `json:"set,omitempty" jsonschema:"EditRepoOption fields, e.g. {description:x, private:true}"`
	NewOwner      string         `json:"new_owner,omitempty" jsonschema:"recipient user or org"`
	TeamIDs       []int          `json:"team_ids,omitempty" jsonschema:"org team ids"`
	CloneAddr     string         `json:"clone_addr,omitempty" jsonschema:"source URL without credentials"`
	Service       string         `json:"service,omitempty" jsonschema:"git, github, gitea, gitlab, forgejo ..."`
	Mirror        bool           `json:"mirror,omitempty" jsonschema:"keep as pull mirror"`
	AuthUsername  string         `json:"auth_username,omitempty" jsonschema:"remote username"`
	AuthToken     string         `json:"auth_token,omitempty" jsonschema:"remote token or password"`
	Include       []string       `json:"include,omitempty" jsonschema:"wiki, issues, pull_requests, releases, labels, milestones, lfs"`
	Interval      string         `json:"interval,omitempty" jsonschema:"Go duration like 8h; 0 = on commit only"`
	Branch        string         `json:"branch,omitempty" jsonschema:"branch name or filter"`
	RemoteAddress string         `json:"remote_address,omitempty" jsonschema:"push mirror target URL"`
	RemoteName    string         `json:"remote_name,omitempty" jsonschema:"push mirror name from pushmirror_list"`
	UseSSH        bool           `json:"use_ssh,omitempty" jsonschema:"authenticate with a generated SSH key"`
	SyncOnCommit  *bool          `json:"sync_on_commit,omitempty" jsonschema:"push on every commit"`
	Topics        []string       `json:"topics,omitempty" jsonschema:"new topic list"`
	Confirm       string         `json:"confirm,omitempty" jsonschema:"exact target for delete: owner/name"`
	Verbose       bool           `json:"verbose,omitempty" jsonschema:"more fields"`
	Page          int            `json:"page,omitempty" jsonschema:"page from 1"`
	Limit         int            `json:"limit,omitempty" jsonschema:"items per page, max 100"`
}

// editRepoKeys are the keys of EditRepoOption (v17.0.2) grouped by the step
// of the server handler that applies them (the edit is not atomic).
var editRepoGroups = map[string]string{
	"name": "basic", "description": "basic", "website": "basic", "private": "basic", "template": "basic",
	"default_branch": "basic", "wiki_branch": "basic",
	"has_issues": "units", "internal_tracker": "units", "external_tracker": "units", "has_wiki": "units",
	"external_wiki": "units", "globally_editable_wiki": "units", "has_pull_requests": "units",
	"has_projects": "units", "has_releases": "units", "has_packages": "units", "has_actions": "units",
	"ignore_whitespace_conflicts": "units", "allow_merge_commits": "units", "allow_rebase": "units",
	"allow_rebase_explicit": "units", "allow_squash_merge": "units", "allow_fast_forward_only_merge": "units",
	"allow_manual_merge": "units", "autodetect_manual_merge": "units", "allow_rebase_update": "units",
	"default_delete_branch_after_merge": "units", "default_merge_style": "units", "default_update_style": "units",
	"default_allow_maintainer_edit": "units",
	"archived":                      "archive",
	"mirror_interval":               "mirror", "enable_prune": "mirror",
}

var editRepoKeys = []string{
	"allow_fast_forward_only_merge", "allow_manual_merge", "allow_merge_commits", "allow_rebase", "allow_rebase_explicit",
	"allow_rebase_update", "allow_squash_merge", "archived", "autodetect_manual_merge", "default_allow_maintainer_edit",
	"default_branch", "default_delete_branch_after_merge", "default_merge_style", "default_update_style", "description",
	"enable_prune", "external_tracker", "external_wiki", "globally_editable_wiki", "has_actions", "has_issues",
	"has_packages", "has_projects", "has_pull_requests", "has_releases", "has_wiki", "ignore_whitespace_conflicts",
	"internal_tracker", "mirror_interval", "name", "private", "template", "website", "wiki_branch",
}

var migrateIncludes = []string{"wiki", "issues", "pull_requests", "releases", "labels", "milestones", "lfs"}

const (
	migrateTimeout  = 10 * time.Minute
	migrateProgress = 10 * time.Second
)

type repoTool struct{}

func registerRepo(r *tools.Registry) {
	t := &repoTool{}
	R := "/repos/{owner}/{repo}"
	r.Add(tools.Spec{
		Name:        "repo",
		Toolset:     "repos",
		Description: "Repositories: get, list, search, create, edit, delete, fork, transfer, migrate, topics, mirrors, star.",
		Params:      repoParams{},
		RepoScoped:  true,
		Actions: []tools.Action{
			tools.NewAction("get", tools.Read, t.get,
				tools.Summary("show one repository"), tools.Params("verbose"),
				tools.Endpoints("GET "+R)),
			tools.NewAction("list", tools.Read, t.list,
				tools.Summary("list repositories of me, a user, an org, starred or watched"),
				tools.Params("which", "owner", "sort", "order", "page", "limit"), tools.NoRepo(),
				tools.Endpoints("GET /user/repos", "GET /users/{username}/repos", "GET /orgs/{org}/repos",
					"GET /user/starred", "GET /user/subscriptions")),
			tools.NewAction("search", tools.Read, t.search,
				tools.Summary("search repositories"),
				tools.Params("q", "topic", "mode", "archived", "private", "sort", "order", "page", "limit"),
				tools.NoRepo(), tools.Endpoints("GET /repos/search")),
			tools.NewAction("create", tools.Write, t.create,
				tools.Summary("create a repository (or generate one from a template)"),
				tools.Params("owner", "description", "private", "auto_init", "readme", "license", "gitignores",
					"default_branch", "template", "from_template"),
				tools.Required("name"), tools.NoRepo(), tools.Needs("write access to the owner"),
				tools.Endpoints("POST /user/repos", "POST /orgs/{org}/repos", "POST /repos/{template_owner}/{template_repo}/generate")),
			tools.NewAction("edit", tools.Write, t.edit,
				tools.Summary("edit repository settings"), tools.Required("set"), tools.Needs("repo admin"),
				tools.Endpoints("PATCH "+R)),
			tools.NewAction("delete", tools.Destructive, t.del,
				tools.Summary("delete a repository (irreversible)"), tools.Params("confirm"), tools.Needs("repo owner"),
				tools.Endpoints("DELETE "+R)),
			tools.NewAction("fork", tools.Write, t.fork,
				tools.Summary("fork the repository"), tools.Params("owner", "name"),
				tools.Endpoints("POST "+R+"/forks", "GET "+R)),
			tools.NewAction("forks", tools.Read, t.forks,
				tools.Summary("list forks"), tools.Params("page", "limit"),
				tools.Endpoints("GET "+R+"/forks")),
			tools.NewAction("transfer", tools.Write, t.transfer,
				tools.Summary("transfer ownership"), tools.Params("team_ids"), tools.Required("new_owner"), tools.Needs("repo owner"),
				tools.Endpoints("POST "+R+"/transfer")),
			tools.NewAction("transfer_accept", tools.Write, t.transferDecide("accept"),
				tools.Summary("accept a pending transfer"),
				tools.Endpoints("POST "+R+"/transfer/accept")),
			tools.NewAction("transfer_reject", tools.Write, t.transferDecide("reject"),
				tools.Summary("reject a pending transfer"),
				tools.Endpoints("POST "+R+"/transfer/reject")),
			tools.NewAction("migrate", tools.Write, t.migrate,
				tools.Summary("import a repository from another host"),
				tools.Params("owner", "service", "mirror", "private", "description", "auth_username", "auth_token", "include", "interval"),
				tools.Required("clone_addr", "name"), tools.NoRepo(),
				tools.Endpoints("POST /repos/migrate")),
			tools.NewAction("topics", tools.Read, t.topics,
				tools.Summary("list topics"), tools.Endpoints("GET "+R+"/topics")),
			tools.NewAction("topics_set", tools.Write, t.topicsSet,
				tools.Summary("replace all topics (topics=[] clears them)"), tools.Params("topics"), tools.Needs("repo admin"),
				tools.Endpoints("PUT "+R+"/topics")),
			tools.NewAction("languages", tools.Read, t.languages,
				tools.Summary("language breakdown"), tools.Endpoints("GET "+R+"/languages")),
			tools.NewAction("mirror_sync", tools.Write, t.mirrorSync,
				tools.Summary("queue a pull-mirror sync"), tools.Endpoints("POST "+R+"/mirror-sync")),
			tools.NewAction("pushmirror_list", tools.Read, t.pushMirrorList,
				tools.Summary("list push mirrors"), tools.Params("page", "limit"), tools.Needs("repo admin"),
				tools.Endpoints("GET "+R+"/push_mirrors")),
			tools.NewAction("pushmirror_add", tools.Write, t.pushMirrorAdd,
				tools.Summary("add a push mirror"),
				tools.Params("auth_username", "auth_token", "interval", "sync_on_commit", "use_ssh", "branch"),
				tools.Required("remote_address"), tools.Needs("repo admin"),
				tools.Endpoints("POST "+R+"/push_mirrors")),
			tools.NewAction("pushmirror_delete", tools.Write, t.pushMirrorDelete,
				tools.Summary("remove a push mirror"), tools.Required("remote_name"), tools.Needs("repo admin"),
				tools.Endpoints("DELETE "+R+"/push_mirrors/{name}")),
			tools.NewAction("pushmirror_sync", tools.Write, t.pushMirrorSync,
				tools.Summary("sync all push mirrors now"), tools.Needs("repo admin"),
				tools.Endpoints("POST "+R+"/push_mirrors-sync")),
			tools.NewAction("fork_status", tools.Read, t.forkStatus,
				tools.Summary("how far a fork is behind its parent"), tools.Params("branch"),
				tools.Endpoints("GET "+R+"/sync_fork", "GET "+R+"/sync_fork/{branch}")),
			tools.NewAction("fork_sync", tools.Write, t.forkSync,
				tools.Summary("update a fork from its parent"), tools.Params("branch"),
				tools.Endpoints("POST "+R+"/sync_fork", "POST "+R+"/sync_fork/{branch}")),
			tools.NewAction("star", tools.Write, t.star(true),
				tools.Summary("star the repository"), tools.Endpoints("PUT /user/starred/{owner}/{repo}")),
			tools.NewAction("unstar", tools.Write, t.star(false),
				tools.Summary("remove the star"), tools.Endpoints("DELETE /user/starred/{owner}/{repo}")),
		},
	})
}

func (t *repoTool) get(c *tools.Call, p *repoParams) (*tools.Result, error) {
	var r apiRepo
	if _, err := c.Env.Client.GetJSON(c.Ctx, repoPath(c), nil, &r); err != nil {
		return nil, err
	}
	return text(renderRepo(&r, p.Verbose)), nil
}

// orderBy maps the tool's sort/order to the `order_by` of GET /user/repos.
func orderBy(sort, order string) string {
	asc := strings.EqualFold(order, "asc")
	desc := strings.EqualFold(order, "desc")
	pick := func(ascName, descName string, defaultAsc bool) string {
		switch {
		case asc:
			return ascName
		case desc:
			return descName
		case defaultAsc:
			return ascName
		}
		return descName
	}
	switch strings.ToLower(sort) {
	case "alpha":
		return pick("alphabetically", "reversealphabetically", true)
	case "created":
		return pick("oldest", "newest", false)
	case "updated":
		return pick("leastupdate", "recentupdate", false)
	case "size":
		return pick("size", "reversesize", true)
	case "stars":
		return pick("feweststars", "moststars", false)
	case "forks":
		return pick("fewestforks", "mostforks", false)
	case "id":
		return "id"
	}
	return ""
}

func (t *repoTool) list(c *tools.Call, p *repoParams) (*tools.Result, error) {
	which := strings.ToLower(strings.TrimSpace(p.Which))
	if which == "" {
		which = "mine"
	}
	q := url.Values{}
	var path string
	switch which {
	case "mine":
		path = "/user/repos"
		if ob := orderBy(p.Sort, p.Order); ob != "" {
			q.Set("order_by", ob)
		}
	case "user", "org":
		if p.Owner == "" {
			return c.Fail("invalid: owner is required for which=%s", which), nil
		}
		if which == "user" {
			path = forgejo.Path("users", p.Owner, "repos")
		} else {
			path = forgejo.Path("orgs", p.Owner, "repos")
		}
	case "starred":
		path = "/user/starred"
	case "watched":
		path = "/user/subscriptions"
	default:
		return c.Fail("invalid: which %q; valid: mine,user,org,starred,watched", p.Which), nil
	}
	var items []apiRepo
	pg, err := c.Env.Client.List(c.Ctx, path, q, pageOf(p.Page), limitOf(c, p.Limit), &items)
	if err != nil {
		return nil, err
	}
	hdr := ""
	if which == "user" || which == "org" {
		// the server post-filters these lists, so the total is unreliable
		pg = forgejo.PageUnknownTotal(pg.Page, pg.Limit, pg.Shown, pg.Shown)
		hdr = format.ListHeader("repos", pg) + " (page may be short)"
	} else {
		hdr = format.ListHeader("repos", pg)
	}
	return text(hdr + "\n" + repoLines(items)), nil
}

func (t *repoTool) search(c *tools.Call, p *repoParams) (*tools.Result, error) {
	q := url.Values{}
	if p.Q != "" {
		q.Set("q", p.Q)
	}
	if p.Topic {
		q.Set("topic", "true")
	}
	if p.Mode != "" {
		q.Set("mode", p.Mode)
	}
	if p.Archived != nil {
		q.Set("archived", boolStr(*p.Archived))
	}
	if p.Private != nil {
		// the server's own `private` means "include private repos" and is never sent
		q.Set("is_private", boolStr(*p.Private))
	}
	if p.Sort != "" {
		q.Set("sort", p.Sort)
	}
	if p.Order != "" {
		q.Set("order", p.Order)
	}
	var res searchResult
	pg, err := c.Env.Client.List(c.Ctx, "/repos/search", q, pageOf(p.Page), limitOf(c, p.Limit), &res)
	if err != nil {
		return nil, err
	}
	return text(format.ListHeader("repos", pg) + "\n" + repoLines(res.Data)), nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func (t *repoTool) create(c *tools.Call, p *repoParams) (*tools.Result, error) {
	cl := c.Env.Client
	if p.FromTemplate != "" {
		return t.generate(c, p)
	}
	body := map[string]any{"name": p.Name}
	setStr(body, "description", p.Description)
	setStr(body, "readme", p.Readme)
	setStr(body, "license", p.License)
	setStr(body, "gitignores", p.Gitignores)
	setStr(body, "default_branch", p.DefaultBranch)
	if p.Private != nil {
		body["private"] = *p.Private
	}
	// an empty repo breaks most other actions, so auto_init defaults to true
	body["auto_init"] = p.AutoInit == nil || *p.AutoInit
	if p.Template {
		body["template"] = true
	}
	path := "/user/repos"
	if p.Owner != "" {
		me, err := cl.Me(c.Ctx)
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(me.Login, p.Owner) {
			path = forgejo.Path("orgs", p.Owner, "repos")
		}
	}
	var r apiRepo
	if _, err := send(c.Ctx, cl, http.MethodPost, path, nil, body, &r, 0); err != nil {
		return nil, addHint(err, http.StatusConflict, "a repository with this name exists; use repo get")
	}
	return okf("created %s (%s) %s", r.FullName, visibility(r.Private), r.HTMLURL), nil
}

func setStr(m map[string]any, k, v string) {
	if v != "" {
		m[k] = v
	}
}

// generate creates a repository from a template.
func (t *repoTool) generate(c *tools.Call, p *repoParams) (*tools.Result, error) {
	cl := c.Env.Client
	to, tn, err := shared.ParseOwnerRepo(p.FromTemplate)
	if err != nil {
		return c.Fail("invalid: from_template: %v", err), nil
	}
	owner := p.Owner
	if owner == "" {
		me, err := cl.Me(c.Ctx)
		if err != nil {
			return nil, err
		}
		owner = me.Login
	}
	body := map[string]any{"owner": owner, "name": p.Name, "git_content": true, "topics": true, "labels": true}
	setStr(body, "description", p.Description)
	setStr(body, "default_branch", p.DefaultBranch)
	if p.Private != nil {
		body["private"] = *p.Private
	}
	var r apiRepo
	if _, err := send(c.Ctx, cl, http.MethodPost, forgejo.Path("repos", to, tn, "generate"), nil, body, &r, 0); err != nil {
		return nil, err
	}
	return okf("created %s (%s) from template %s/%s %s", r.FullName, visibility(r.Private), to, tn, r.HTMLURL), nil
}

func (t *repoTool) edit(c *tools.Call, p *repoParams) (*tools.Result, error) {
	if len(p.Set) == 0 {
		return c.Fail("invalid: set is empty; valid keys: %s", strings.Join(editRepoKeys, ",")), nil
	}
	if bad := checkKeys(c, "set", p.Set, editRepoKeys); bad != nil {
		return bad, nil
	}
	var r apiRepo
	if _, err := send(c.Ctx, c.Env.Client, http.MethodPatch, repoPath(c), nil, p.Set, &r, 0); err != nil {
		return nil, err
	}
	shared.InvalidateRepo(c.Env.Client, c.Owner, c.Repo)
	if name, ok := p.Set["name"].(string); ok && name != "" {
		shared.InvalidateRepo(c.Env.Client, c.Owner, name)
	}
	groups := map[string]bool{}
	for k := range p.Set {
		groups[editRepoGroups[k]] = true
	}
	name := r.FullName
	if name == "" {
		name = fullName(c)
	}
	out := "ok: edited " + name + ": " + strings.Join(sortedKeys(p.Set), ",") + "\n"
	if len(groups) > 1 {
		out += "note: repo edits are not atomic\n"
	}
	return text(out), nil
}

func (t *repoTool) del(c *tools.Call, p *repoParams) (*tools.Result, error) {
	if res := c.Confirm(p.Confirm, fullName(c)); res != nil {
		return res, nil
	}
	if _, err := send(c.Ctx, c.Env.Client, http.MethodDelete, repoPath(c), nil, nil, nil, 0); err != nil {
		return nil, err
	}
	shared.InvalidateRepo(c.Env.Client, c.Owner, c.Repo)
	return okf("deleted %s (irreversible)", fullName(c)), nil
}

func (t *repoTool) fork(c *tools.Call, p *repoParams) (*tools.Result, error) {
	cl := c.Env.Client
	body := map[string]any{}
	setStr(body, "organization", p.Owner)
	setStr(body, "name", p.Name)
	var r apiRepo
	if _, err := send(c.Ctx, cl, http.MethodPost, repoPath(c, "forks"), nil, body, &r, 0); err != nil {
		return nil, addHint(err, http.StatusConflict, "a fork or repo with this name exists; pass name=")
	}
	out := "ok: forked to " + r.FullName
	// the fork is complete when the server answers; one GET reports its state
	if fo, fn, err := shared.ParseOwnerRepo(r.FullName); err == nil {
		var cur apiRepo
		if _, err := cl.GetJSON(c.Ctx, forgejo.Path("repos", fo, fn), nil, &cur); err == nil && cur.Empty {
			out += " (empty source repo)"
		}
	}
	return text(out + "\n"), nil
}

func (t *repoTool) forks(c *tools.Call, p *repoParams) (*tools.Result, error) {
	var items []apiRepo
	pg, err := c.Env.Client.List(c.Ctx, repoPath(c, "forks"), nil, pageOf(p.Page), limitOf(c, p.Limit), &items)
	if err != nil {
		return nil, err
	}
	return text(format.ListHeader("forks", pg) + "\n" + repoLines(items)), nil
}

func (t *repoTool) transfer(c *tools.Call, p *repoParams) (*tools.Result, error) {
	body := map[string]any{"new_owner": p.NewOwner}
	if len(p.TeamIDs) > 0 {
		body["team_ids"] = p.TeamIDs
	}
	resp, err := send(c.Ctx, c.Env.Client, http.MethodPost, repoPath(c, "transfer"), nil, body, nil, 0)
	if err != nil {
		return nil, err
	}
	if resp.Status == http.StatusAccepted {
		return okf("transfer pending; recipient runs repo.transfer_accept"), nil
	}
	shared.InvalidateRepo(c.Env.Client, c.Owner, c.Repo)
	return okf("transferred %s to %s", fullName(c), p.NewOwner), nil
}

func (t *repoTool) transferDecide(verb string) func(c *tools.Call, p *repoParams) (*tools.Result, error) {
	return func(c *tools.Call, _ *repoParams) (*tools.Result, error) {
		if _, err := send(c.Ctx, c.Env.Client, http.MethodPost, repoPath(c, "transfer", verb), nil, nil, nil, 0); err != nil {
			return nil, err
		}
		shared.InvalidateRepo(c.Env.Client, c.Owner, c.Repo)
		return okf("transfer of %s %s", fullName(c), pastTense[verb]), nil
	}
}

var pastTense = map[string]string{"accept": "accepted", "reject": "rejected"}

// migrateOut is the outcome of the detached migration request.
type migrateOut struct {
	repo apiRepo
	err  error
}

func (t *repoTool) migrate(c *tools.Call, p *repoParams) (*tools.Result, error) {
	cl := c.Env.Client
	if u, err := url.Parse(p.CloneAddr); err == nil && u.User != nil {
		return c.Fail("invalid: clone_addr must not contain credentials; use auth_username and auth_token"), nil
	}
	body := map[string]any{"clone_addr": p.CloneAddr, "repo_name": p.Name}
	setStr(body, "repo_owner", p.Owner)
	setStr(body, "service", p.Service)
	setStr(body, "description", p.Description)
	setStr(body, "auth_username", p.AuthUsername)
	setStr(body, "auth_token", p.AuthToken)
	if p.Service == "" || strings.EqualFold(p.Service, "git") {
		// plain git imports build the clone URL from auth_username and
		// auth_password; auth_token is only used by the API-based services
		setStr(body, "auth_password", p.AuthToken)
	}
	setStr(body, "mirror_interval", p.Interval)
	if p.Mirror {
		body["mirror"] = true
	}
	if p.Private != nil {
		body["private"] = *p.Private
	}
	for _, inc := range p.Include {
		inc = strings.ToLower(strings.TrimSpace(inc))
		valid := false
		for _, v := range migrateIncludes {
			if v == inc {
				valid = true
			}
		}
		if !valid {
			return c.Fail("invalid: include %q; valid: %s", inc, strings.Join(migrateIncludes, ",")), nil
		}
		body[inc] = true
	}

	clk := c.Env.Clock
	if clk == nil {
		clk = clock.Real()
	}
	waitMax := 55 * time.Second
	if c.Env.Cfg != nil && c.Env.Cfg.WaitMax > 0 {
		waitMax = c.Env.Cfg.WaitMax
	}

	// the request is detached from the call so a slow import keeps running
	// on the server even when the client gives up waiting
	dctx, cancel := context.WithTimeout(context.WithoutCancel(c.Ctx), migrateTimeout)
	done := make(chan migrateOut, 1)
	go func() {
		defer cancel()
		var r apiRepo
		_, err := send(dctx, cl, http.MethodPost, "/repos/migrate", nil, body, &r, migrateTimeout)
		done <- migrateOut{r, err}
	}()

	deadline := clk.After(waitMax)
	began := 0.0
	for {
		select {
		case o := <-done:
			if o.err != nil {
				return nil, o.err
			}
			kind := ""
			if p.Mirror {
				kind = " (pull mirror)"
			}
			return okf("migrated %s%s", o.repo.FullName, kind), nil
		case <-clk.After(migrateProgress):
			began += migrateProgress.Seconds()
			c.Progress("migration running", began, 0)
		case <-deadline:
			target := p.Name
			if p.Owner != "" {
				target = p.Owner + "/" + p.Name
			}
			return text("ok: started; migration still running on the server; check with repo get repo=" + target + "\n"), nil
		case <-c.Ctx.Done():
			return nil, c.Ctx.Err()
		}
	}
}

func (t *repoTool) topics(c *tools.Call, _ *repoParams) (*tools.Result, error) {
	var tl topicList
	if _, err := c.Env.Client.GetJSON(c.Ctx, repoPath(c, "topics"), nil, &tl); err != nil {
		return nil, err
	}
	if len(tl.Topics) == 0 {
		return text("topics: (none)\n"), nil
	}
	return text("topics: " + strings.Join(tl.Topics, ",") + "\n"), nil
}

func (t *repoTool) topicsSet(c *tools.Call, p *repoParams) (*tools.Result, error) {
	// an empty list is valid (clears all topics); only an absent key is an error
	if p.Topics == nil {
		return c.Fail("invalid: missing param topics for repo.topics_set (pass [] to clear all topics)"), nil
	}
	topics := make([]string, 0, len(p.Topics))
	for _, tp := range p.Topics {
		if tp = strings.TrimSpace(tp); tp != "" {
			topics = append(topics, tp)
		}
	}
	if _, err := send(c.Ctx, c.Env.Client, http.MethodPut, repoPath(c, "topics"), nil, map[string]any{"topics": topics}, nil, 0); err != nil {
		return nil, err
	}
	return okf("topics set: %s", strings.Join(topics, ",")), nil
}

func (t *repoTool) languages(c *tools.Call, _ *repoParams) (*tools.Result, error) {
	langs := map[string]int64{}
	if _, err := c.Env.Client.GetJSON(c.Ctx, repoPath(c, "languages"), nil, &langs); err != nil {
		return nil, err
	}
	if len(langs) == 0 {
		return text("languages: (none)\n"), nil
	}
	return text(languageText(langs) + "\n"), nil
}

func (t *repoTool) mirrorSync(c *tools.Call, _ *repoParams) (*tools.Result, error) {
	if _, err := send(c.Ctx, c.Env.Client, http.MethodPost, repoPath(c, "mirror-sync"), nil, nil, nil, 0); err != nil {
		return nil, err
	}
	return okf("pull-mirror sync queued"), nil
}

func (t *repoTool) pushMirrorList(c *tools.Call, p *repoParams) (*tools.Result, error) {
	page, limit := pageOf(p.Page), limitOf(c, p.Limit)
	var items []pushMirror
	pg, err := c.Env.Client.List(c.Ctx, repoPath(c, "push_mirrors"), nil, page, limit, &items)
	if err != nil {
		return nil, err
	}
	if len(items) > limit {
		// some builds ignore paging for this list: window it here
		all := len(items)
		items = window(items, page, limit)
		pg = forgejo.Page{Page: page, Limit: limit, Total: int64(all), Shown: len(items), More: page*limit < all}
	}
	var b strings.Builder
	b.WriteString(format.ListHeader("push mirrors", pg) + "\n")
	for i := range items {
		b.WriteString(pushMirrorLine(&items[i]))
	}
	return text(b.String()), nil
}

func (t *repoTool) pushMirrorAdd(c *tools.Call, p *repoParams) (*tools.Result, error) {
	if p.UseSSH && (p.AuthUsername != "" || p.AuthToken != "") {
		return c.Fail("invalid: use_ssh cannot be combined with credentials"), nil
	}
	// the server runs time.ParseDuration on interval and answers 400 for an
	// empty string, so it is always sent
	interval := 8 * time.Hour
	if s := strings.TrimSpace(p.Interval); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d < 0 {
			return c.Fail("invalid: interval %q; use a Go duration like 8h or 30m, or 0 for on-commit only", p.Interval), nil
		}
		interval = d
	}
	syncOnCommit := interval == 0
	if p.SyncOnCommit != nil {
		syncOnCommit = *p.SyncOnCommit
	}
	body := map[string]any{
		"remote_address": p.RemoteAddress,
		"interval":       interval.String(),
		"sync_on_commit": syncOnCommit,
	}
	setStr(body, "remote_username", p.AuthUsername)
	setStr(body, "remote_password", p.AuthToken)
	setStr(body, "branch_filter", p.Branch)
	if p.UseSSH {
		body["use_ssh"] = true
	}
	var m pushMirror
	if _, err := send(c.Ctx, c.Env.Client, http.MethodPost, repoPath(c, "push_mirrors"), nil, body, &m, 0); err != nil {
		// the server answers 401 when the remote refuses the credentials or the key
		err = addHint(err, http.StatusBadRequest, "interval must be 0 or >= the server minimum mirror interval (default 10m)")
		return nil, replaceHint(err, http.StatusUnauthorized, "the remote rejected the credentials or the key, or is unreachable; check remote_address and auth_*")
	}
	when := "every " + interval.String()
	if interval == 0 {
		when = "on commit only"
	}
	out := "ok: push mirror " + m.RemoteName + " -> " + m.RemoteAddr + " (" + when + ")\n"
	if m.PublicKey != "" {
		out += "public_key: " + strings.TrimSpace(m.PublicKey) + "\nnote: add this key as a deploy key (write access) on the remote\n"
	}
	return text(out), nil
}

func (t *repoTool) pushMirrorDelete(c *tools.Call, p *repoParams) (*tools.Result, error) {
	if _, err := send(c.Ctx, c.Env.Client, http.MethodDelete, repoPath(c, "push_mirrors", p.RemoteName), nil, nil, nil, 0); err != nil {
		return nil, err
	}
	return okf("deleted push mirror %s", p.RemoteName), nil
}

func (t *repoTool) pushMirrorSync(c *tools.Call, _ *repoParams) (*tools.Result, error) {
	if _, err := send(c.Ctx, c.Env.Client, http.MethodPost, repoPath(c, "push_mirrors-sync"), nil, nil, nil, 0); err != nil {
		return nil, err
	}
	return okf("push mirror sync queued"), nil
}

func syncPath(c *tools.Call, branch string) string {
	if branch == "" {
		return repoPath(c, "sync_fork")
	}
	return repoPath(c, "sync_fork", branch)
}

func (t *repoTool) forkStatus(c *tools.Call, p *repoParams) (*tools.Result, error) {
	var si syncInfo
	if _, err := c.Env.Client.GetJSON(c.Ctx, syncPath(c, p.Branch), nil, &si); err != nil {
		return nil, err
	}
	// the server answers allowed=false for a fork identical to its parent
	if si.CommitsBehind == 0 && (si.Allowed || si.ForkCommit == si.BaseCommit) {
		return text("up to date\n"), nil
	}
	state := "up to date"
	if si.CommitsBehind > 0 {
		state = "behind " + plural(si.CommitsBehind, "commit")
	}
	if si.Allowed {
		return text(state + ", sync allowed\n"), nil
	}
	return text(state + ", sync not allowed (diverged)\n"), nil
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func (t *repoTool) forkSync(c *tools.Call, p *repoParams) (*tools.Result, error) {
	if _, err := send(c.Ctx, c.Env.Client, http.MethodPost, syncPath(c, p.Branch), nil, nil, nil, 0); err != nil {
		return nil, addHint(err, http.StatusBadRequest, "the repo must be a fork, and the branch must be behind its parent without diverging; see fork_status")
	}
	return okf("synced %s from its parent", fullName(c)), nil
}

func (t *repoTool) star(on bool) func(c *tools.Call, p *repoParams) (*tools.Result, error) {
	return func(c *tools.Call, _ *repoParams) (*tools.Result, error) {
		method, word := http.MethodPut, "starred"
		if !on {
			method, word = http.MethodDelete, "unstarred"
		}
		if _, err := send(c.Ctx, c.Env.Client, method, forgejo.Path("user", "starred", c.Owner, c.Repo), nil, nil, nil, 0); err != nil {
			return nil, err
		}
		return okf("%s %s", word, fullName(c)), nil
	}
}
