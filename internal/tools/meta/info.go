package meta

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// infoParams are the parameters of forgejo_info (section 8.1).
type infoParams struct {
	Verbose bool `json:"verbose,omitempty" jsonschema:"also token scopes, quota and Actions probes"`
	Refresh bool `json:"refresh,omitempty" jsonschema:"bypass the cached version, limits and identity"`
}

// repoSettings mirrors GET /settings/repository (only the flags we print).
type repoSettings struct {
	ForksDisabled      bool `json:"forks_disabled"`
	HTTPGitDisabled    bool `json:"http_git_disabled"`
	LFSDisabled        bool `json:"lfs_disabled"`
	MigrationsDisabled bool `json:"migrations_disabled"`
	MirrorsDisabled    bool `json:"mirrors_disabled"`
	StarsDisabled      bool `json:"stars_disabled"`
	TimeTrackDisabled  bool `json:"time_tracking_disabled"`
}

func (s repoSettings) disabled() []string {
	var out []string
	for _, f := range []struct {
		off  bool
		name string
	}{
		{s.ForksDisabled, "forks"},
		{s.HTTPGitDisabled, "http_git"},
		{s.LFSDisabled, "lfs"},
		{s.MigrationsDisabled, "migrations"},
		{s.MirrorsDisabled, "mirrors"},
		{s.StarsDisabled, "stars"},
		{s.TimeTrackDisabled, "time_tracking"},
	} {
		if f.off {
			out = append(out, f.name)
		}
	}
	return out
}

// registerInfo registers forgejo_info.
func registerInfo(r *tools.Registry) {
	i := &infoTool{reg: r}
	r.Add(tools.Spec{
		Name:        "forgejo_info",
		Toolset:     "meta",
		Description: "Server version, API limits, who am I, default repo, enabled toolsets. Call first when unsure.",
		Params:      infoParams{},
		Single:      true,
		Actions: []tools.Action{
			tools.NewAction("run", tools.Read, i.run,
				tools.Summary("server, identity, repo and toolset overview"),
				tools.Params("verbose", "refresh"),
				tools.Endpoints(
					"GET /version",
					"GET /settings/api",
					"GET /settings/repository",
					"GET /user/quota",
				),
			),
		},
	})
}

type infoTool struct{ reg *tools.Registry }

func (t *infoTool) run(c *tools.Call, p *infoParams) (*tools.Result, error) {
	cl := c.Env.Client
	if p.Refresh {
		cl.Cache().Invalidate(cl.SharedCacheKey("version"))
		cl.Cache().Invalidate(cl.SharedCacheKey("settings/api"))
		cl.Cache().Invalidate(cl.CacheKey("me"))
	}
	var b format.B

	// line 1: version, limits, URL
	ver, verr := cl.ServerVersion(c.Ctx)
	if verr != nil {
		return nil, verr
	}
	def, maxItems := cl.Limits(c.Ctx)
	b.Raw(fmt.Sprintf("forgejo %d.%d.%d (limits: default %d, max %d) %s\n",
		ver.Major, ver.Minor, ver.Patch, def, maxItems, cl.BaseURL()))

	// line 2: identity
	var me *forgejo.User
	meLine := "me: anonymous"
	if cl.Token() != "" {
		u, err := cl.Me(c.Ctx)
		if err == nil {
			me = u
			meLine = fmt.Sprintf("me: %s (id %d", u.Login, u.ID)
			if u.IsAdmin {
				meLine += ", admin"
			}
			meLine += ")"
		} else {
			var ae *forgejo.APIError
			if errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
				meLine = "me: unauthenticated (token rejected)"
			} else {
				meLine = "me: unknown (" + firstLine(err) + ")"
			}
		}
	}
	if c.Env.Cfg != nil && c.Env.Cfg.Sudo != "" {
		meLine += " | sudo: " + c.Env.Cfg.Sudo
	}
	if p.Verbose && me != nil {
		meLine += " | token scopes: " + t.scopes(c, me)
	}
	b.Raw(meLine + "\n")

	// line 3: repository
	owner, name := "", ""
	if c.Env.Repo != nil {
		o, n, src, err := c.Env.Repo.Resolve("")
		if err != nil {
			b.Raw("repo: none (" + format.OneLine(err.Error(), 120) + ")\n")
		} else {
			owner, name = o, n
			line := fmt.Sprintf("repo: %s/%s (%s)", o, n, src)
			if gOwner, gn, ok := c.Env.Repo.GitRepo(); ok && gOwner == o && gn == n {
				if br := c.Env.Repo.CurrentBranch(); br != "" {
					line += " branch: " + br
				}
			}
			b.Raw(line + "\n")
		}
	} else {
		b.Raw("repo: none\n")
	}

	// line 4: toolsets, read-only, tool count
	on := t.reg.EnabledToolsets()
	var sets []string
	for _, ts := range tools.Toolsets {
		if ts != "meta" && on[ts] {
			sets = append(sets, ts)
		}
	}
	ro := "no"
	if c.Env.Cfg != nil && c.Env.Cfg.ReadOnly {
		ro = "yes"
	}
	b.Raw(fmt.Sprintf("toolsets: %s | read-only: %s | %d tools\n", strings.Join(sets, ","), ro, len(t.reg.Enabled())))

	// line 5: features disabled on the server
	var rs repoSettings
	if _, err := cl.GetJSON(c.Ctx, "/settings/repository", nil, &rs); err == nil {
		if d := rs.disabled(); len(d) > 0 {
			b.Raw("disabled on server: " + strings.Join(d, ", ") + "\n")
		}
	}
	if ver.Major < 15 {
		b.Raw(fmt.Sprintf("warn: server major %d < 15, some actions unsupported\n", ver.Major))
	}
	if cl.Token() == "" {
		b.Raw("warn: no token; anonymous access is read-only on public repos\n")
	}

	if p.Verbose {
		t.probes(c, &b, owner, name)
	}
	return &tools.Result{Text: c.Cap(strings.TrimRight(b.String(), "\n"))}, nil
}

// scopes finds our token in GET /users/{me}/tokens by its last 8 characters
// (best effort; errors are ignored).
func (t *infoTool) scopes(c *tools.Call, me *forgejo.User) string {
	tok := c.Env.Client.Token()
	if len(tok) < 8 {
		return "unknown"
	}
	var list []struct {
		TokenLastEight string   `json:"token_last_eight"`
		Scopes         []string `json:"scopes"`
	}
	if _, err := c.Env.Client.GetJSON(c.Ctx, forgejo.Path("users", me.Login, "tokens"), nil, &list); err != nil {
		return "unknown"
	}
	last8 := tok[len(tok)-8:]
	for _, e := range list {
		if e.TokenLastEight == last8 {
			if len(e.Scopes) == 0 {
				return "none"
			}
			sc := append([]string(nil), e.Scopes...)
			sort.Strings(sc)
			return strings.Join(sc, ",")
		}
	}
	return "unknown"
}

// probes adds the verbose-only lines: quota and Actions availability.
func (t *infoTool) probes(c *tools.Call, b *format.B, owner, name string) {
	cl := c.Env.Client
	if _, err := cl.Do(c.Ctx, forgejo.Request{Method: http.MethodGet, Path: "/user/quota"}); err != nil {
		var ae *forgejo.APIError
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
			b.Raw("quota: disabled\n")
		} else {
			b.Raw("quota: unknown (" + firstLine(err) + ")\n")
		}
	} else {
		b.Raw("quota: enabled\n")
	}
	if owner == "" {
		return
	}
	q := url.Values{"limit": {"1"}}
	_, err := cl.Do(c.Ctx, forgejo.Request{Method: http.MethodGet, Path: forgejo.Path("repos", owner, name, "actions", "runs"), Query: q})
	var ae *forgejo.APIError
	switch {
	case err == nil:
		b.Raw("actions: enabled\n")
	case errors.As(err, &ae) && (ae.Status == http.StatusNotFound || ae.Status == http.StatusForbidden):
		b.Raw("actions: disabled or no access\n")
	default:
		b.Raw("actions: unknown (" + firstLine(err) + ")\n")
	}
}

func firstLine(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimPrefix(s, "error: ")
}
