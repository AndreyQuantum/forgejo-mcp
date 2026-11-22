package users

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// userLine renders `alice | Alice A | admin` (inactive accounts say so).
func userLine(c *tools.Call) func(account) string {
	return func(u account) string {
		var b format.B
		parts := []string{u.Login, format.OneLine(u.FullName, 60)}
		if u.IsAdmin {
			parts = append(parts, "admin")
		}
		if !u.Active && u.ID != 0 && activeVisible(c, u.Login) {
			parts = append(parts, "inactive")
		}
		b.Line(parts...)
		return strings.TrimRight(b.String(), "\n")
	}
}

// activeVisible reports whether the server sent a meaningful `active` flag
// for login: it does so only for site admins and for the user themselves
// (everyone else gets a false placeholder).
func activeVisible(c *tools.Call, login string) bool {
	if c == nil || c.Env == nil || c.Env.Client == nil {
		return false
	}
	me, err := c.Env.Client.Me(c.Ctx)
	if err != nil || me == nil {
		return false
	}
	return me.IsAdmin || strings.EqualFold(me.Login, login)
}

// realTime reports whether s is a real timestamp (the server sends the zero
// time or the Unix epoch for users that never logged in).
func realTime(s string) bool {
	if s == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return true
	}
	return t.Year() > 1970
}

// userObject renders the single-object view of a user.
func userObject(c *tools.Call, u account, self bool) string {
	var b format.B
	b.Raw("user " + u.Login + "\n")
	b.KV("id", u.ID)
	b.KV("name", u.FullName)
	b.KV("email", u.Email)
	if u.IsAdmin {
		b.KV("admin", "yes")
	}
	if !u.Active && (self || activeVisible(c, u.Login)) {
		b.KV("active", "no")
	}
	b.KV("visibility", u.Visibility)
	b.KV("location", u.Location)
	b.KV("website", u.Website)
	b.KV("created", format.T(u.Created))
	if realTime(u.LastLogin) {
		b.KV("last_login", format.T(u.LastLogin))
	}
	if u.Followers+u.Following+u.Starred > 0 {
		b.KV("counts", "followers:"+strconv.Itoa(u.Followers)+" following:"+strconv.Itoa(u.Following)+" starred:"+strconv.Itoa(u.Starred))
	}
	b.KV("bio", format.Trunc(u.Desc, 600, "see the profile page for the rest"))
	return strings.TrimRight(b.String(), "\n")
}

// repoLine renders `owner/name | private | fork,archived | stars:12 | upd T | desc`.
func repoLine(r repoItem) string {
	vis := "public"
	if r.Private {
		vis = "private"
	}
	var flags []string
	if r.Fork {
		flags = append(flags, "fork")
	}
	if r.Mirror {
		flags = append(flags, "mirror")
	}
	if r.Archived {
		flags = append(flags, "archived")
	}
	if r.Empty {
		flags = append(flags, "empty")
	}
	stars := ""
	if r.Stars > 0 {
		stars = "stars:" + strconv.Itoa(r.Stars)
	}
	upd := ""
	if r.Updated != "" {
		upd = "upd " + format.T(r.Updated)
	}
	var b format.B
	b.Line(r.FullName, vis, strings.Join(flags, ","), stars, upd, format.OneLine(r.Description, 80))
	return strings.TrimRight(b.String(), "\n")
}

// keyLine renders `id 4 | title | SHA256:... | ro`.
func keyLine(k sshKey) string {
	ro := ""
	if k.ReadOnly {
		ro = "ro"
	}
	var b format.B
	b.Line("id "+strconv.FormatInt(k.ID, 10), format.OneLine(k.Title, 80), k.Fingerprint, ro)
	return strings.TrimRight(b.String(), "\n")
}

// tokenLine renders `id 3 | name | scopes | ...last8 | repos:o/r | T`.
func tokenLine(t accessToken) string {
	scopes := strings.Join(t.Scopes, ",")
	if scopes == "" {
		scopes = "no-scopes"
	}
	last := ""
	if t.LastEight != "" {
		last = "..." + t.LastEight
	}
	var repos []string
	for _, r := range t.Repositories {
		repos = append(repos, r.FullName)
	}
	rs := ""
	if len(repos) > 0 {
		rs = "repos:" + strings.Join(repos, ",")
	}
	var b format.B
	b.Line("id "+strconv.FormatInt(t.ID, 10), t.Name, scopes, last, rs, format.T(t.Created))
	return strings.TrimRight(b.String(), "\n")
}

// orgLine renders `org | visibility | desc(80)`.
func orgLine(o org) string {
	var b format.B
	b.Line(o.login(), o.Visibility, format.OneLine(o.Description, 80))
	return strings.TrimRight(b.String(), "\n")
}

// orgObject renders the single-object view of an organization.
func orgObject(o org) string {
	var b format.B
	b.Raw("org " + o.login() + "\n")
	b.KV("id", o.ID)
	b.KV("name", o.FullName)
	b.KV("visibility", o.Visibility)
	b.KV("description", format.Trunc(o.Description, 600, "see the org page for the rest"))
	b.KV("website", o.Website)
	b.KV("location", o.Location)
	b.KV("email", o.Email)
	b.KV("created", format.T(o.Created))
	return strings.TrimRight(b.String(), "\n")
}

// unitsText renders a units map as `code:write,issues:read` (none omitted).
func unitsText(m map[string]string) string {
	var parts []string
	for k, v := range m {
		if v == "" || v == "none" {
			continue
		}
		parts = append(parts, strings.TrimPrefix(k, "repo.")+":"+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// teamLine renders `team 3 devs write | all-repos`.
func teamLine(t team) string {
	all := ""
	if t.IncludesAllRepositories {
		all = "all-repos"
	}
	var b format.B
	b.Line("team "+strconv.FormatInt(t.ID, 10)+" "+t.Name+" "+t.Permission, all)
	return strings.TrimRight(b.String(), "\n")
}

// teamObject renders the single-object view of a team.
func teamObject(t team) string {
	var b format.B
	b.Raw("team " + strconv.FormatInt(t.ID, 10) + "\n")
	b.KV("name", t.Name)
	if t.Organization != nil {
		b.KV("org", t.Organization.login())
	}
	b.KV("permission", t.Permission)
	if t.IncludesAllRepositories {
		b.KV("repos", "all")
	}
	if t.CanCreateOrgRepo {
		b.KV("can_create_org_repo", "yes")
	}
	b.KV("units", unitsText(t.UnitsMap))
	b.KV("description", format.Trunc(t.Description, 600, "see the team page for the rest"))
	return strings.TrimRight(b.String(), "\n")
}

// subjectNumber extracts the issue/PR number from a subject API URL.
func subjectNumber(typ, url string) int {
	if t := strings.ToLower(typ); t != "issue" && t != "pull" {
		return 0
	}
	i := strings.LastIndexByte(url, '/')
	if i < 0 {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(url[i+1:]))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// subjectKind maps the API subject type to the short word of the output.
func subjectKind(t string) string {
	switch strings.ToLower(t) {
	case "pull":
		return "pr"
	case "issue":
		return "issue"
	case "repository":
		return "repo"
	case "commit":
		return "commit"
	}
	return strings.ToLower(t)
}

// threadState renders `unread`, `read`, `unread pinned` or `read pinned`.
func threadState(t thread) string {
	state := "read"
	if t.Unread {
		state = "unread"
	}
	if t.Pinned {
		state += " pinned"
	}
	return state
}

// threadHead renders `n77 unread pinned owner/repo#12 pr open`.
func threadHead(t thread) string {
	parts := []string{"n" + strconv.FormatInt(t.ID, 10), threadState(t)}
	ref := ""
	if t.Repository != nil {
		ref = t.Repository.FullName
	}
	kind, sstate := "", ""
	if t.Subject != nil {
		if n := subjectNumber(t.Subject.Type, t.Subject.URL); n > 0 {
			ref += "#" + strconv.Itoa(n)
		}
		kind, sstate = subjectKind(t.Subject.Type), t.Subject.State
	}
	if ref != "" {
		parts = append(parts, ref)
	}
	if kind != "" {
		parts = append(parts, kind)
	}
	if sstate != "" {
		parts = append(parts, sstate)
	}
	return strings.Join(parts, " ")
}

// threadLine renders the list line `n77 unread owner/repo#12 pr open | title | T`.
func threadLine(t thread) string {
	title := ""
	if t.Subject != nil {
		title = format.OneLine(t.Subject.Title, 120)
		if strings.EqualFold(t.Subject.Type, "repository") && t.Repository != nil && title == t.Repository.FullName {
			title = ""
		}
	}
	var b format.B
	b.Line(threadHead(t), title, format.T(t.UpdatedAt))
	return strings.TrimRight(b.String(), "\n")
}

// threadObject renders the single-object view of a notification thread.
func threadObject(t thread) string {
	var b format.B
	b.Raw("notification " + strconv.FormatInt(t.ID, 10) + "\n")
	b.KV("status", threadState(t))
	if t.Repository != nil {
		b.KV("repo", t.Repository.FullName)
	}
	if t.Subject != nil {
		b.KV("title", format.OneLine(t.Subject.Title, 200))
		b.KV("subject", subjectKind(t.Subject.Type)+" "+t.Subject.State)
		if n := subjectNumber(t.Subject.Type, t.Subject.URL); n > 0 {
			b.KV("number", n)
		}
		b.KV("url", t.Subject.HTMLURL)
	}
	b.KV("updated", format.T(t.UpdatedAt))
	return strings.TrimRight(b.String(), "\n")
}
