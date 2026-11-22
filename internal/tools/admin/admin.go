// Package admin implements the `admin` tool (DESIGN.md section 8.11). Every
// action needs a site-admin token with the admin scope.
package admin

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// adminParams are the parameters of the admin tool.
type adminParams struct {
	Action             string   `json:"action"`
	User               string   `json:"user,omitempty" jsonschema:"username"`
	Org                string   `json:"org,omitempty" jsonschema:"organization name"`
	Q                  string   `json:"q,omitempty" jsonschema:"search text"`
	Email              string   `json:"email,omitempty"`
	Password           string   `json:"password,omitempty" jsonschema:"never echoed"`
	FullName           string   `json:"full_name,omitempty"`
	LoginName          string   `json:"login_name,omitempty" jsonschema:"external login name; needs source_id"`
	SourceID           *int64   `json:"source_id,omitempty" jsonschema:"auth source id; 0 = local"`
	MustChangePassword *bool    `json:"must_change_password,omitempty"`
	Admin              *bool    `json:"admin,omitempty" jsonschema:"site administrator"`
	Active             *bool    `json:"active,omitempty"`
	ProhibitLogin      *bool    `json:"prohibit_login,omitempty"`
	Restricted         *bool    `json:"restricted,omitempty"`
	Visibility         string   `json:"visibility,omitempty" jsonschema:"public|limited|private"`
	Purge              bool     `json:"purge,omitempty" jsonschema:"also delete the user's repos and data"`
	NewName            string   `json:"new_name,omitempty" jsonschema:"new username"`
	Task               string   `json:"task,omitempty" jsonschema:"cron task name"`
	Pattern            string   `json:"pattern,omitempty" jsonschema:"repo name filter"`
	Owner              string   `json:"owner,omitempty" jsonschema:"owner of the unadopted repo"`
	Repo               string   `json:"repo,omitempty" jsonschema:"unadopted repo name"`
	Group              string   `json:"group,omitempty" jsonschema:"quota group name"`
	Rule               string   `json:"rule,omitempty" jsonschema:"quota rule name"`
	LimitBytes         *int64   `json:"limit_bytes,omitempty" jsonschema:"quota limit in bytes; -1 = unlimited"`
	Subjects           []string `json:"subjects,omitempty" jsonschema:"quota subjects, e.g. size:repos:all"`
	Groups             []string `json:"groups,omitempty" jsonschema:"quota group names; [] clears"`
	Subject            string   `json:"subject,omitempty" jsonschema:"quota subject to check"`
	Confirm            string   `json:"confirm,omitempty" jsonschema:"exact target name"`
	Sort               string   `json:"sort,omitempty" jsonschema:"users_list order: oldest|newest|alphabetically|reversealphabetically|recentupdate|leastupdate"`
	Page               int      `json:"page,omitempty"`
	Limit              int      `json:"limit,omitempty"`
}

const needsAdmin = "site-admin token with the admin scope"

// Register wiring lives in register.go; registerAdmin builds the Spec.
func registerAdmin(r *tools.Registry) {
	act := func(name string, mode tools.Mode, run func(*tools.Call, *adminParams) (*tools.Result, error), summary string, params, required []string, eps ...string) tools.Action {
		return tools.NewAction(name, mode, run,
			tools.Summary(summary),
			tools.Params(params...),
			tools.Required(required...),
			tools.Endpoints(eps...),
			tools.Needs(needsAdmin),
		)
	}
	paged := []string{"page", "limit"}
	userEdit := []string{"user", "email", "password", "full_name", "login_name", "source_id", "must_change_password", "admin", "active", "prohibit_login", "restricted", "visibility"}
	r.Add(tools.Spec{
		Name:        "admin",
		Toolset:     "admin",
		Description: "Site admin: users, orgs, emails, cron, system hooks, unadopted repos, quota.",
		Params:      adminParams{},
		Actions: []tools.Action{
			act("users_list", tools.Read, runUsersList, "list all users",
				append([]string{"source_id", "login_name", "sort"}, paged...), nil, "GET /admin/users"),
			act("users_create", tools.Write, runUsersCreate, "create a user (password never echoed)",
				append([]string{"full_name", "login_name", "source_id", "must_change_password", "admin", "active", "prohibit_login", "restricted", "visibility", "password"}, "user", "email"),
				[]string{"user", "email"}, "POST /admin/users"),
			act("users_edit", tools.Write, runUsersEdit, "edit a user",
				userEdit, []string{"user"}, "PATCH /admin/users/{username}"),
			act("users_delete", tools.Destructive, runUsersDelete, "delete a user (confirm=username)",
				[]string{"purge"}, []string{"user", "confirm"}, "DELETE /admin/users/{username}"),
			act("users_rename", tools.Write, runUsersRename, "rename a user",
				nil, []string{"user", "new_name"}, "POST /admin/users/{username}/rename"),
			act("orgs_list", tools.Read, runOrgsList, "list all organizations", paged, nil, "GET /admin/orgs"),
			act("emails_list", tools.Read, runEmailsList, "list all email addresses", paged, nil, "GET /admin/emails"),
			act("emails_search", tools.Read, runEmailsSearch, "search email addresses",
				paged, []string{"q"}, "GET /admin/emails/search"),
			act("cron_list", tools.Read, runCronList, "list cron tasks", paged, nil, "GET /admin/cron"),
			act("cron_run", tools.Write, runCronRun, "run a cron task now",
				nil, []string{"task"}, "POST /admin/cron/{task}"),
			act("unadopted_list", tools.Read, runUnadoptedList, "list unadopted repository directories",
				append([]string{"pattern"}, paged...), nil, "GET /admin/unadopted"),
			act("unadopted_adopt", tools.Write, runUnadoptedAdopt, "adopt files as a repository",
				nil, []string{"owner", "repo"}, "POST /admin/unadopted/{owner}/{repo}"),
			act("unadopted_delete", tools.Destructive, runUnadoptedDelete, "delete unadopted files (confirm=owner/repo)",
				nil, []string{"owner", "repo", "confirm"}, "DELETE /admin/unadopted/{owner}/{repo}"),
			act("quota_info", tools.Read, runQuotaInfo, "quota usage of me, a user (user=) or an org (org=)",
				[]string{"user", "org"}, nil, "GET /admin/users/{username}/quota"),
			act("quota_groups", tools.Read, runQuotaGroups, "list quota groups or show one (group=)",
				append([]string{"group"}, paged...), nil, "GET /admin/quota/groups", "GET /admin/quota/groups/{quotagroup}"),
			act("quota_group_set", tools.Write, runQuotaGroupSet, "create a quota group; with rule= add that rule to it",
				[]string{"rule"}, []string{"group"}, "POST /admin/quota/groups",
				"PUT /admin/quota/groups/{quotagroup}/rules/{quotarule}"),
			act("quota_group_delete", tools.Write, runQuotaGroupDelete, "delete a quota group; with rule= only remove that rule",
				[]string{"rule"}, []string{"group"}, "DELETE /admin/quota/groups/{quotagroup}",
				"DELETE /admin/quota/groups/{quotagroup}/rules/{quotarule}"),
			act("quota_rules", tools.Read, runQuotaRules, "list quota rules or show one (rule=)",
				append([]string{"rule"}, paged...), nil, "GET /admin/quota/rules", "GET /admin/quota/rules/{quotarule}"),
			act("quota_rule_set", tools.Write, runQuotaRuleSet, "create or update a quota rule",
				[]string{"limit_bytes", "subjects"}, []string{"rule"}, "POST /admin/quota/rules",
				"PATCH /admin/quota/rules/{quotarule}"),
			act("quota_rule_delete", tools.Write, runQuotaRuleDelete, "delete a quota rule",
				nil, []string{"rule"}, "DELETE /admin/quota/rules/{quotarule}"),
			act("quota_user_groups_set", tools.Write, runQuotaUserGroupsSet, "replace the quota groups of a user",
				[]string{"groups"}, []string{"user"}, "POST /admin/users/{username}/quota/groups"),
			act("quota_check", tools.Read, runQuotaCheck, "check a quota subject for me or an org (org=)",
				[]string{"org"}, []string{"subject"}, "GET /user/quota/check", "GET /orgs/{org}/quota/check"),
		},
	})
}

// listLines fetches one page of a server-paginated list and renders it with
// the standard header.
func listLines[T any](c *tools.Call, kind, path string, q url.Values, p *adminParams, line func(*T) string) (*tools.Result, error) {
	var items []T
	pg, err := c.Env.Client.List(c.Ctx, path, q, p.Page, p.Limit, &items)
	if err != nil {
		return nil, err
	}
	var b format.B
	b.Raw(format.ListHeader(kind, pg) + "\n")
	for i := range items {
		b.Raw(line(&items[i]) + "\n")
	}
	return &tools.Result{Text: c.Cap(b.String())}, nil
}

// window cuts an unpaginated list to one page (section 6.7: client-side windowing).
func window[T any](c *tools.Call, items []T, page, limit int) ([]T, forgejo.Page) {
	page = max(page, 1)
	if limit <= 0 {
		limit = c.Env.Cfg.DefaultLimit
	}
	limit = min(max(limit, 1), 100)
	total := len(items)
	from := min((page-1)*limit, total)
	to := min(from+limit, total)
	return items[from:to], forgejo.Page{Page: page, Limit: limit, Total: int64(total), Shown: to - from, More: to < total}
}

// windowed renders an unpaginated list with the standard header.
func windowed[T any](c *tools.Call, kind string, items []T, p *adminParams, line func(*T) string) *tools.Result {
	part, pg := window(c, items, p.Page, p.Limit)
	var b format.B
	b.Raw(format.ListHeader(kind, pg) + "\n")
	for i := range part {
		b.Raw(line(&part[i]) + "\n")
	}
	return &tools.Result{Text: c.Cap(b.String())}
}

func isStatus(err error, status int) bool {
	var ae *forgejo.APIError
	return errors.As(err, &ae) && ae.Status == status
}

// quotaErr turns the 404 of a quota route into `quota is disabled` when the
// server has no quota routes at all (the routes exist only with [quota]
// ENABLED); any other error is returned unchanged.
func quotaErr(c *tools.Call, err error) (*tools.Result, error) {
	if !isStatus(err, http.StatusNotFound) {
		return nil, err
	}
	if _, perr := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodGet, Path: "/user/quota"}); isStatus(perr, http.StatusNotFound) {
		return c.Fail("quota is disabled on this server"), nil
	}
	return nil, err
}

func invalid(c *tools.Call, f string, a ...any) *tools.Result {
	return c.Fail("invalid: %s", fmt.Sprintf(f, a...))
}

// okName reports whether s is usable as one path segment name.
func okName(s string) bool { return strings.TrimSpace(s) != "" && s != "." && s != ".." }
