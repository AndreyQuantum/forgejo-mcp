package users

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared"
)

// orgParams are the parameters of the org tool (section 8.9).
type orgParams struct {
	Action           string         `json:"action"`
	Org              string         `json:"org,omitempty" jsonschema:"organization name"`
	User             string         `json:"user,omitempty" jsonschema:"member username"`
	TeamID           int64          `json:"team_id,omitempty" jsonschema:"team id (see teams)"`
	Name             string         `json:"name,omitempty" jsonschema:"org username (create) or team name"`
	FullName         string         `json:"full_name,omitempty" jsonschema:"display name"`
	Description      string         `json:"description,omitempty"`
	Visibility       string         `json:"visibility,omitempty" jsonschema:"public|limited|private"`
	Website          string         `json:"website,omitempty"`
	Location         string         `json:"location,omitempty"`
	Email            string         `json:"email,omitempty"`
	Permission       string         `json:"permission,omitempty" jsonschema:"team permission: read|write|admin"`
	UnitsMap         map[string]any `json:"units_map,omitempty" jsonschema:"team unit access, e.g. {code: write, issues: read}"`
	IncludesAllRepos *bool          `json:"includes_all_repositories,omitempty" jsonschema:"team covers every org repo"`
	CanCreateOrgRepo *bool          `json:"can_create_org_repo,omitempty" jsonschema:"team may create repos"`
	Repo             string         `json:"repo,omitempty" jsonschema:"owner/name of the repo"`
	NewName          string         `json:"new_name,omitempty" jsonschema:"new organization name"`
	All              bool           `json:"all,omitempty" jsonschema:"every org on the server, not just mine"`
	Confirm          string         `json:"confirm,omitempty" jsonschema:"exact org name (delete) or team name (team_delete)"`
	Page             int            `json:"page,omitempty"`
	Limit            int            `json:"limit,omitempty"`
}

var (
	validVisibility = []string{"public", "limited", "private"}
	validTeamPerm   = []string{"read", "write", "admin"}
	validUnitKeys   = []string{"repo.code", "repo.issues", "repo.ext_issues", "repo.pulls", "repo.wiki", "repo.ext_wiki", "repo.releases", "repo.projects", "repo.packages", "repo.actions"}
	validUnitAccess = []string{"none", "read", "write", "admin", "owner"}
	// defaultUnits is the unit set of a team created without units_map (the
	// server answers 500 "units permission should not be empty" otherwise).
	defaultUnits = []string{"repo.code", "repo.issues", "repo.pulls", "repo.releases", "repo.wiki", "repo.projects", "repo.packages", "repo.actions"}
)

func registerOrg(r *tools.Registry) {
	t := &orgTool{}
	pg := []string{"page", "limit"}
	profile := []string{"full_name", "description", "visibility", "website", "location", "email"}
	teamFields := []string{"description", "permission", "units_map", "includes_all_repositories", "can_create_org_repo"}
	r.Add(tools.Spec{
		Name:        "org",
		Toolset:     "users",
		Description: "Organizations and teams: get, list, create, edit, delete, members, team_*.",
		Params:      orgParams{},
		Actions: []tools.Action{
			tools.NewAction("list", tools.Read, t.list,
				tools.Summary("my organizations (all=true: every org)"),
				tools.Params(append([]string{"all"}, pg...)...),
				tools.Endpoints("GET /user/orgs", "GET /orgs")),
			tools.NewAction("get", tools.Read, t.get,
				tools.Summary("one organization"),
				tools.Required("org"),
				tools.Endpoints("GET /orgs/{org}")),
			tools.NewAction("create", tools.Write, t.create,
				tools.Summary("create an organization (name = its username)"),
				tools.Required("name"),
				tools.Params(profile...),
				tools.Needs("permission to create organizations"),
				tools.Endpoints("POST /orgs")),
			tools.NewAction("edit", tools.Write, t.edit,
				tools.Summary("edit organization profile fields"),
				tools.Required("org"),
				tools.Params(profile...),
				tools.Needs("org owner"),
				tools.Endpoints("PATCH /orgs/{org}")),
			tools.NewAction("delete", tools.Destructive, t.delete,
				tools.Summary("delete an organization (confirm = org name)"),
				tools.Required("org"),
				tools.Params("confirm"),
				tools.Needs("org owner"),
				tools.Hint500("the org still owns repositories or packages; delete or transfer them first"),
				tools.Endpoints("DELETE /orgs/{org}")),
			tools.NewAction("rename", tools.Write, t.rename,
				tools.Summary("rename an organization"),
				tools.Required("org", "new_name"),
				tools.Needs("org owner"),
				tools.Endpoints("POST /orgs/{org}/rename")),
			tools.NewAction("members", tools.Read, t.members,
				tools.Summary("organization members"),
				tools.Required("org"),
				tools.Params(pg...),
				tools.Endpoints("GET /orgs/{org}/members")),
			tools.NewAction("member_remove", tools.Write, t.memberRemove,
				tools.Summary("remove a user from the organization"),
				tools.Required("org", "user"),
				tools.Needs("org owner"),
				tools.Endpoints("DELETE /orgs/{org}/members/{username}")),
			tools.NewAction("teams", tools.Read, t.teams,
				tools.Summary("teams of an organization"),
				tools.Required("org"),
				tools.Params(pg...),
				tools.Endpoints("GET /orgs/{org}/teams")),
			tools.NewAction("team_get", tools.Read, t.teamGet,
				tools.Summary("one team with its unit access"),
				tools.Required("team_id"),
				tools.Endpoints("GET /teams/{id}")),
			tools.NewAction("team_create", tools.Write, t.teamCreate,
				tools.Summary("create a team (permission default read, all units)"),
				tools.Required("org", "name"),
				tools.Params(teamFields...),
				tools.Needs("org owner"),
				tools.Endpoints("POST /orgs/{org}/teams")),
			tools.NewAction("team_edit", tools.Write, t.teamEdit,
				tools.Summary("edit a team; unset fields keep their value"),
				tools.Required("team_id"),
				tools.Params(append([]string{"name"}, teamFields...)...),
				tools.Needs("org owner"),
				tools.Endpoints("PATCH /teams/{id}")),
			tools.NewAction("team_delete", tools.Destructive, t.teamDelete,
				tools.Summary("delete a team (confirm = team name)"),
				tools.Required("team_id"),
				tools.Params("confirm"),
				tools.Needs("org owner"),
				tools.Endpoints("DELETE /teams/{id}")),
			tools.NewAction("team_members", tools.Read, t.teamMembers,
				tools.Summary("members of a team"),
				tools.Required("team_id"),
				tools.Params(pg...),
				tools.Endpoints("GET /teams/{id}/members")),
			tools.NewAction("team_add_member", tools.Write, t.teamAddMember,
				tools.Summary("add a user to a team (may send an email invite instead)"),
				tools.Required("team_id", "user"),
				tools.Needs("org owner"),
				tools.Endpoints("PUT /teams/{id}/members/{username}")),
			tools.NewAction("team_remove_member", tools.Write, t.teamRemoveMember,
				tools.Summary("remove a user from a team"),
				tools.Required("team_id", "user"),
				tools.Needs("org owner"),
				tools.Endpoints("DELETE /teams/{id}/members/{username}")),
			tools.NewAction("team_repos", tools.Read, t.teamRepos,
				tools.Summary("repositories of a team"),
				tools.Required("team_id"),
				tools.Params(pg...),
				tools.Endpoints("GET /teams/{id}/repos")),
			tools.NewAction("team_add_repo", tools.Write, t.teamAddRepo,
				tools.Summary("add a repo (owner/name) to a team"),
				tools.Required("team_id", "repo"),
				tools.Needs("repo admin"),
				tools.Endpoints("PUT /teams/{id}/repos/{org}/{repo}")),
			tools.NewAction("team_remove_repo", tools.Write, t.teamRemoveRepo,
				tools.Summary("remove a repo (owner/name) from a team"),
				tools.Required("team_id", "repo"),
				tools.Needs("repo admin"),
				tools.Endpoints("DELETE /teams/{id}/repos/{org}/{repo}")),
		},
	})
}

type orgTool struct{}

// profileBody collects the non-empty org profile fields and their names.
func profileBody(p *orgParams) (map[string]any, []string) {
	body := map[string]any{}
	var names []string
	for _, f := range []struct{ key, val string }{
		{"full_name", p.FullName},
		{"description", p.Description},
		{"visibility", p.Visibility},
		{"website", p.Website},
		{"location", p.Location},
		{"email", p.Email},
	} {
		if f.val != "" {
			body[f.key] = f.val
			names = append(names, f.key)
		}
	}
	return body, names
}

func checkEnum(c *tools.Call, name, val string, valid []string) *tools.Result {
	if val == "" || slices.Contains(valid, val) {
		return nil
	}
	return c.Fail("invalid: %s %q; valid: %s", name, val, strings.Join(valid, "|"))
}

func (orgTool) list(c *tools.Call, p *orgParams) (*tools.Result, error) {
	path := "/user/orgs"
	if p.All {
		path = "/orgs"
	}
	return listPage(c, path, nil, p.Page, p.Limit, "orgs", orgLine)
}

func (orgTool) get(c *tools.Call, p *orgParams) (*tools.Result, error) {
	var o org
	if _, err := c.Env.Client.GetJSON(c.Ctx, forgejo.Path("orgs", p.Org), nil, &o); err != nil {
		return nil, err
	}
	return &tools.Result{Text: c.Cap(orgObject(o))}, nil
}

func (orgTool) create(c *tools.Call, p *orgParams) (*tools.Result, error) {
	if r := checkEnum(c, "visibility", p.Visibility, validVisibility); r != nil {
		return r, nil
	}
	body, _ := profileBody(p)
	body["username"] = p.Name
	var o org
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPost, "/orgs", nil, body, &o); err != nil {
		return nil, err
	}
	return ok("created org %s (%s) id %d", o.login(), o.Visibility, o.ID), nil
}

func (orgTool) edit(c *tools.Call, p *orgParams) (*tools.Result, error) {
	if r := checkEnum(c, "visibility", p.Visibility, validVisibility); r != nil {
		return r, nil
	}
	body, names := profileBody(p)
	if len(body) == 0 {
		return c.Fail("invalid: nothing to change; pass at least one of full_name, description, visibility, website, location, email"), nil
	}
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPatch, forgejo.Path("orgs", p.Org), nil, body, nil); err != nil {
		return nil, err
	}
	return ok("updated org %s: %s", p.Org, strings.Join(names, ",")), nil
}

func (orgTool) delete(c *tools.Call, p *orgParams) (*tools.Result, error) {
	if r := c.Confirm(p.Confirm, p.Org); r != nil {
		return r, nil
	}
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodDelete, forgejo.Path("orgs", p.Org), nil, nil, nil); err != nil {
		return nil, err
	}
	return ok("deleted org %s", p.Org), nil
}

func (orgTool) rename(c *tools.Call, p *orgParams) (*tools.Result, error) {
	body := map[string]any{"new_name": p.NewName}
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPost, forgejo.Path("orgs", p.Org, "rename"), nil, body, nil); err != nil {
		return nil, err
	}
	return ok("renamed org %s -> %s", p.Org, p.NewName), nil
}

func (orgTool) members(c *tools.Call, p *orgParams) (*tools.Result, error) {
	return listPage(c, forgejo.Path("orgs", p.Org, "members"), nil, p.Page, p.Limit, "members", userLine(c))
}

func (orgTool) memberRemove(c *tools.Call, p *orgParams) (*tools.Result, error) {
	path := forgejo.Path("orgs", p.Org, "members", p.User)
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodDelete, path, nil, nil, nil); err != nil {
		return nil, err
	}
	return ok("removed @%s from org %s", p.User, p.Org), nil
}

func (orgTool) teams(c *tools.Call, p *orgParams) (*tools.Result, error) {
	return listPage(c, forgejo.Path("orgs", p.Org, "teams"), nil, p.Page, p.Limit, "teams", teamLine)
}

func teamPath(id int64, rest ...string) string {
	return forgejo.Path(append([]string{"teams", strconv.FormatInt(id, 10)}, rest...)...)
}

func getTeam(c *tools.Call, id int64) (*team, error) {
	var t team
	if _, err := c.Env.Client.GetJSON(c.Ctx, teamPath(id), nil, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (orgTool) teamGet(c *tools.Call, p *orgParams) (*tools.Result, error) {
	t, err := getTeam(c, p.TeamID)
	if err != nil {
		return nil, err
	}
	return &tools.Result{Text: c.Cap(teamObject(*t))}, nil
}

// unitsBody validates units_map and stringifies its values; short keys such
// as `code` get the `repo.` prefix.
func unitsBody(c *tools.Call, m map[string]any) (map[string]string, *tools.Result) {
	out := make(map[string]string, len(m))
	for k, v := range m {
		access := strings.ToLower(strings.TrimSpace(fmt.Sprint(v)))
		if !slices.Contains(validUnitAccess, access) {
			return nil, c.Fail("invalid: units_map[%s]=%q; valid: %s", k, access, strings.Join(validUnitAccess, "|"))
		}
		key := strings.TrimSpace(k)
		if !strings.Contains(key, ".") {
			key = "repo." + key
		}
		if !slices.Contains(validUnitKeys, key) {
			return nil, c.Fail("invalid: units_map key %q; valid: %s", k, strings.Join(validUnitKeys, ", "))
		}
		out[key] = access
	}
	return out, nil
}

func (orgTool) teamCreate(c *tools.Call, p *orgParams) (*tools.Result, error) {
	if r := checkEnum(c, "permission", p.Permission, validTeamPerm); r != nil {
		return r, nil
	}
	perm := p.Permission
	if perm == "" {
		perm = "read"
	}
	body := map[string]any{"name": p.Name, "permission": perm}
	if p.Description != "" {
		body["description"] = p.Description
	}
	if p.IncludesAllRepos != nil {
		body["includes_all_repositories"] = *p.IncludesAllRepos
	}
	if p.CanCreateOrgRepo != nil {
		body["can_create_org_repo"] = *p.CanCreateOrgRepo
	}
	if len(p.UnitsMap) > 0 {
		um, r := unitsBody(c, p.UnitsMap)
		if r != nil {
			return r, nil
		}
		body["units_map"] = um
	} else if perm != "admin" {
		um := make(map[string]string, len(defaultUnits))
		for _, u := range defaultUnits {
			um[u] = perm
		}
		body["units_map"] = um
	}
	var t team
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPost, forgejo.Path("orgs", p.Org, "teams"), nil, body, &t); err != nil {
		return nil, err
	}
	return ok("created team %d %s in %s (%s)", t.ID, t.Name, p.Org, t.Permission), nil
}

func (orgTool) teamEdit(c *tools.Call, p *orgParams) (*tools.Result, error) {
	if r := checkEnum(c, "permission", p.Permission, validTeamPerm); r != nil {
		return r, nil
	}
	var names []string
	body := map[string]any{}
	set := func(key string, v any) {
		body[key] = v
		names = append(names, key)
	}
	if p.Description != "" {
		set("description", p.Description)
	}
	if p.Permission != "" {
		set("permission", p.Permission)
	}
	if len(p.UnitsMap) > 0 {
		um, r := unitsBody(c, p.UnitsMap)
		if r != nil {
			return r, nil
		}
		set("units_map", um)
	}
	permUnits := p.Permission == "read" || p.Permission == "write"
	if p.IncludesAllRepos != nil {
		set("includes_all_repositories", *p.IncludesAllRepos)
	}
	if p.CanCreateOrgRepo != nil {
		set("can_create_org_repo", *p.CanCreateOrgRepo)
	}
	if p.Name != "" {
		set("name", p.Name)
	}
	if len(body) == 0 {
		return c.Fail("invalid: nothing to change; pass at least one of name, description, permission, units_map, includes_all_repositories, can_create_org_repo"), nil
	}
	// The swagger marks `name` required, and the server applies
	// includes_all_repositories only together with a permission: resend both
	// from the current state.
	cur, err := getTeam(c, p.TeamID)
	if err != nil {
		return nil, err
	}
	if _, has := body["name"]; !has {
		body["name"] = cur.Name
	}
	if _, has := body["permission"]; !has && p.IncludesAllRepos != nil && slices.Contains(validTeamPerm, cur.Permission) {
		body["permission"] = cur.Permission
	}
	// The server only replaces team units when units_map is sent, and repo
	// access comes from the units: derive them from the new permission.
	if _, has := body["units_map"]; !has && permUnits {
		keys := defaultUnits
		if len(cur.UnitsMap) > 0 {
			keys = keys[:0:0]
			for k := range cur.UnitsMap {
				keys = append(keys, k)
			}
			sort.Strings(keys)
		}
		um := make(map[string]string, len(keys))
		for _, k := range keys {
			um[k] = p.Permission
		}
		body["units_map"] = um
	}
	sort.Strings(names)
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPatch, teamPath(p.TeamID), nil, body, nil); err != nil {
		return nil, err
	}
	return ok("updated team %d %s: %s", p.TeamID, cur.Name, strings.Join(names, ",")), nil
}

func (orgTool) teamDelete(c *tools.Call, p *orgParams) (*tools.Result, error) {
	cur, err := getTeam(c, p.TeamID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Confirm) != strconv.FormatInt(p.TeamID, 10) {
		if r := c.Confirm(p.Confirm, cur.Name); r != nil {
			return r, nil
		}
	}
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodDelete, teamPath(p.TeamID), nil, nil, nil); err != nil {
		return nil, err
	}
	return ok("deleted team %d %s", p.TeamID, cur.Name), nil
}

func (orgTool) teamMembers(c *tools.Call, p *orgParams) (*tools.Result, error) {
	return listPage(c, teamPath(p.TeamID, "members"), nil, p.Page, p.Limit, "members", userLine(c))
}

func (orgTool) teamAddMember(c *tools.Call, p *orgParams) (*tools.Result, error) {
	path := teamPath(p.TeamID, "members", p.User)
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPut, path, nil, nil, nil); err != nil {
		return nil, err
	}
	// The server sends an email invite instead of adding a user who is not
	// yet a member of the organization; check which one happened.
	if _, err := c.Env.Client.GetJSON(c.Ctx, path, nil, new(account)); err == nil {
		return ok("added @%s to team %d", p.User, p.TeamID), nil
	}
	return ok("accepted @%s for team %d; not a member yet (the server may have sent an email invite instead of adding)", p.User, p.TeamID), nil
}

func (orgTool) teamRemoveMember(c *tools.Call, p *orgParams) (*tools.Result, error) {
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodDelete, teamPath(p.TeamID, "members", p.User), nil, nil, nil); err != nil {
		return nil, err
	}
	return ok("removed @%s from team %d", p.User, p.TeamID), nil
}

func (orgTool) teamRepos(c *tools.Call, p *orgParams) (*tools.Result, error) {
	return listPage(c, teamPath(p.TeamID, "repos"), nil, p.Page, p.Limit, "repos", repoLine)
}

func teamRepoPath(c *tools.Call, p *orgParams) (string, string, *tools.Result) {
	o, n, err := shared.ParseOwnerRepo(p.Repo)
	if err != nil {
		return "", "", c.Fail("invalid: repo %q: want owner/name", p.Repo)
	}
	return teamPath(p.TeamID, "repos", o, n), o + "/" + n, nil
}

func (orgTool) teamAddRepo(c *tools.Call, p *orgParams) (*tools.Result, error) {
	path, full, r := teamRepoPath(c, p)
	if r != nil {
		return r, nil
	}
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPut, path, nil, nil, nil); err != nil {
		return nil, err
	}
	return ok("added %s to team %d", full, p.TeamID), nil
}

func (orgTool) teamRemoveRepo(c *tools.Call, p *orgParams) (*tools.Result, error) {
	path, full, r := teamRepoPath(c, p)
	if r != nil {
		return r, nil
	}
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodDelete, path, nil, nil, nil); err != nil {
		return nil, err
	}
	return ok("removed %s from team %d", full, p.TeamID), nil
}
