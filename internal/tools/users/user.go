package users

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// userParams are the parameters of the user tool (section 8.9).
type userParams struct {
	Action   string   `json:"action"`
	User     string   `json:"user,omitempty" jsonschema:"username; default me where allowed"`
	Q        string   `json:"q,omitempty" jsonschema:"search text"`
	Sort     string   `json:"sort,omitempty" jsonschema:"oldest|newest|alphabetically|reversealphabetically|recentupdate|leastupdate"`
	ID       int64    `json:"id,omitempty" jsonschema:"key id or token id"`
	Title    string   `json:"title,omitempty" jsonschema:"key title"`
	Key      string   `json:"key,omitempty" jsonschema:"public SSH key text"`
	ReadOnly bool     `json:"read_only,omitempty" jsonschema:"key read-only flag; the server may ignore it for user keys"`
	Name     string   `json:"name,omitempty" jsonschema:"token name"`
	Scopes   []string `json:"scopes,omitempty" jsonschema:"token scopes, e.g. read:repository, write:issue, all"`
	Repos    []string `json:"repos,omitempty" jsonschema:"limit token to owner/name repos"`
	Page     int      `json:"page,omitempty"`
	Limit    int      `json:"limit,omitempty"`
}

const tokenNeedsAdmin = "creating tokens needs a site-admin token or the web UI (the API requires the account password, which this server never accepts)"

var reScope = regexp.MustCompile(`^(read|write):(activitypub|admin|issue|misc|notification|organization|package|repository|user)$`)

// validScope accepts `read|write:<category>`, `all` and `public-only`.
func validScope(s string) bool {
	return s == "all" || s == "public-only" || reScope.MatchString(s)
}

func registerUser(r *tools.Registry) {
	t := &userTool{}
	pg := []string{"page", "limit"}
	withUser := append([]string{"user"}, pg...)
	r.Add(tools.Spec{
		Name:        "user",
		Toolset:     "users",
		Description: "Users: me, get, search, followers, following, starred, orgs, keys, tokens.",
		Params:      userParams{},
		Actions: []tools.Action{
			tools.NewAction("me", tools.Read, t.me,
				tools.Summary("the authenticated account"),
				tools.Endpoints("GET /user")),
			tools.NewAction("get", tools.Read, t.get,
				tools.Summary("one user or organization profile"),
				tools.Required("user"),
				tools.Endpoints("GET /users/{username}")),
			tools.NewAction("search", tools.Read, t.search,
				tools.Summary("find users by name, full name or email"),
				tools.Params(append([]string{"q", "sort"}, pg...)...),
				tools.Endpoints("GET /users/search")),
			tools.NewAction("followers", tools.Read, t.followers,
				tools.Summary("followers of user (default me)"),
				tools.Params(withUser...),
				tools.Endpoints("GET /users/{username}/followers", "GET /user/followers")),
			tools.NewAction("following", tools.Read, t.following,
				tools.Summary("accounts user (default me) follows"),
				tools.Params(withUser...),
				tools.Endpoints("GET /users/{username}/following", "GET /user/following")),
			tools.NewAction("starred", tools.Read, t.starred,
				tools.Summary("repos starred by user (default me)"),
				tools.Params(withUser...),
				tools.Endpoints("GET /users/{username}/starred", "GET /user/starred")),
			tools.NewAction("orgs", tools.Read, t.orgs,
				tools.Summary("organizations of user (default me)"),
				tools.Params(withUser...),
				tools.Endpoints("GET /users/{username}/orgs", "GET /user/orgs")),
			tools.NewAction("keys_list", tools.Read, t.keysList,
				tools.Summary("SSH keys of user (default me)"),
				tools.Params(withUser...),
				tools.Endpoints("GET /user/keys", "GET /users/{username}/keys")),
			tools.NewAction("keys_add", tools.Write, t.keysAdd,
				tools.Summary("add an SSH key to my account"),
				tools.Required("title", "key"),
				tools.Params("read_only"),
				tools.Endpoints("POST /user/keys")),
			tools.NewAction("keys_delete", tools.Write, t.keysDelete,
				tools.Summary("remove one of my SSH keys"),
				tools.Required("id"),
				tools.Endpoints("DELETE /user/keys/{id}")),
			tools.NewAction("tokens_list", tools.Read, t.tokensList,
				tools.Summary("access tokens of user (default me), no secrets"),
				tools.Params(withUser...),
				tools.Needs("self or site admin"),
				tools.Endpoints("GET /users/{username}/tokens")),
			tools.NewAction("tokens_create", tools.Write, t.tokensCreate,
				tools.Summary("create an access token (needs a site-admin token); secret shown once"),
				tools.Required("name", "scopes"),
				tools.Params("user", "repos"),
				tools.Needs("site admin"),
				tools.Endpoints("POST /admin/users/{username}/tokens")),
			tools.NewAction("tokens_delete", tools.Write, t.tokensDelete,
				tools.Summary("delete an access token by id (needs a site-admin token)"),
				tools.Required("id"),
				tools.Params("user"),
				tools.Needs("site admin"),
				tools.Endpoints("DELETE /admin/users/{username}/tokens/{token}")),
		},
	})
}

type userTool struct{}

func (userTool) me(c *tools.Call, _ *userParams) (*tools.Result, error) {
	var u account
	if _, err := c.Env.Client.GetJSON(c.Ctx, "/user", nil, &u); err != nil {
		return nil, err
	}
	return &tools.Result{Text: c.Cap(userObject(c, u, true))}, nil
}

func (userTool) get(c *tools.Call, p *userParams) (*tools.Result, error) {
	var u account
	if _, err := c.Env.Client.GetJSON(c.Ctx, forgejo.Path("users", p.User), nil, &u); err != nil {
		return nil, err
	}
	return &tools.Result{Text: c.Cap(userObject(c, u, false))}, nil
}

func (userTool) search(c *tools.Call, p *userParams) (*tools.Result, error) {
	q := url.Values{}
	if p.Q != "" {
		q.Set("q", p.Q)
	}
	if p.Sort != "" {
		q.Set("sort", p.Sort)
	}
	var out struct {
		OK    bool      `json:"ok"`
		Error string    `json:"error"`
		Data  []account `json:"data"`
	}
	pg, err := c.Env.Client.List(c.Ctx, "/users/search", q, p.Page, defLimit(c, p.Limit), &out)
	if err != nil {
		return nil, err
	}
	return &tools.Result{Text: c.Cap(renderList("users", pg, out.Data, userLine(c)))}, nil
}

// selfOr returns `/user/<what>` or `/users/<user>/<what>`.
func selfOr(user, what string) string {
	if user != "" {
		return forgejo.Path("users", user, what)
	}
	return forgejo.Path("user", what)
}

func (userTool) followers(c *tools.Call, p *userParams) (*tools.Result, error) {
	return listPage(c, selfOr(p.User, "followers"), nil, p.Page, p.Limit, "followers", userLine(c))
}

func (userTool) following(c *tools.Call, p *userParams) (*tools.Result, error) {
	return listPage(c, selfOr(p.User, "following"), nil, p.Page, p.Limit, "following", userLine(c))
}

func (userTool) starred(c *tools.Call, p *userParams) (*tools.Result, error) {
	return listPage(c, selfOr(p.User, "starred"), nil, p.Page, p.Limit, "repos", repoLine)
}

func (userTool) orgs(c *tools.Call, p *userParams) (*tools.Result, error) {
	return listPage(c, selfOr(p.User, "orgs"), nil, p.Page, p.Limit, "orgs", orgLine)
}

func (userTool) keysList(c *tools.Call, p *userParams) (*tools.Result, error) {
	return listPage(c, selfOr(p.User, "keys"), nil, p.Page, p.Limit, "keys", keyLine)
}

func (userTool) keysAdd(c *tools.Call, p *userParams) (*tools.Result, error) {
	body := map[string]any{"title": p.Title, "key": strings.TrimSpace(p.Key), "read_only": p.ReadOnly}
	var k sshKey
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPost, "/user/keys", nil, body, &k); err != nil {
		return nil, err
	}
	ro := ""
	if k.ReadOnly {
		ro = " (read-only)"
	}
	return ok("added key id %d %q %s%s", k.ID, k.Title, k.Fingerprint, ro), nil
}

func (userTool) keysDelete(c *tools.Call, p *userParams) (*tools.Result, error) {
	path := forgejo.Path("user", "keys", strconv.FormatInt(p.ID, 10))
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodDelete, path, nil, nil, nil); err != nil {
		return nil, err
	}
	return ok("deleted key id %d", p.ID), nil
}

func (userTool) tokensList(c *tools.Call, p *userParams) (*tools.Result, error) {
	u, err := userOrMe(c, p.User)
	if err != nil {
		return nil, err
	}
	return listPage(c, forgejo.Path("users", u, "tokens"), nil, p.Page, p.Limit, "tokens", tokenLine)
}

// adminOnlyToken turns the auth failures of the admin token endpoints into
// the explanatory error of section 8.9.
func adminOnlyToken(err error) error {
	var ae *forgejo.APIError
	if errors.As(err, &ae) && (ae.Status == http.StatusUnauthorized || ae.Status == http.StatusForbidden) &&
		!strings.Contains(ae.Message, "scope") {
		// a missing token scope keeps its own message and hint
		return errors.New(tokenNeedsAdmin)
	}
	return err
}

// requireAdmin returns the explanatory error when the caller is known not to
// be a site admin (saves a request that cannot succeed).
func requireAdmin(c *tools.Call) error {
	if c.Env.Client.Token() == "" {
		return errors.New(tokenNeedsAdmin)
	}
	if u, err := c.Env.Client.Me(c.Ctx); err == nil && !u.IsAdmin {
		return errors.New(tokenNeedsAdmin)
	}
	return nil
}

func (userTool) tokensCreate(c *tools.Call, p *userParams) (*tools.Result, error) {
	for _, s := range p.Scopes {
		if !validScope(s) {
			return c.Fail("invalid: scope %q; valid: read|write:<activitypub,admin,issue,misc,notification,organization,package,repository,user>, all, public-only", s), nil
		}
	}
	var repos []map[string]string
	for _, r := range p.Repos {
		o, n, found := strings.Cut(strings.TrimSpace(r), "/")
		if !found || o == "" || n == "" || strings.Contains(n, "/") {
			return c.Fail("invalid: repos entry %q: want owner/name", r), nil
		}
		repos = append(repos, map[string]string{"owner": o, "name": n})
	}
	if err := requireAdmin(c); err != nil {
		return nil, err
	}
	u, err := userOrMe(c, p.User)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"name": p.Name, "scopes": p.Scopes}
	if len(repos) > 0 {
		body["repositories"] = repos
	}
	var t accessToken
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPost, forgejo.Path("admin", "users", u, "tokens"), nil, body, &t); err != nil {
		return nil, adminOnlyToken(err)
	}
	var b strings.Builder
	b.WriteString("ok: created token id " + strconv.FormatInt(t.ID, 10) + " " + strconv.Quote(t.Name) + " for " + u + " scopes " + strings.Join(t.Scopes, ","))
	if len(t.Repositories) > 0 {
		var rs []string
		for _, r := range t.Repositories {
			rs = append(rs, r.FullName)
		}
		b.WriteString(" repos " + strings.Join(rs, ","))
	}
	b.WriteString("\ntoken: " + t.Sha1 + "\nnote: the secret is shown once; store it now")
	return &tools.Result{Text: b.String()}, nil
}

func (userTool) tokensDelete(c *tools.Call, p *userParams) (*tools.Result, error) {
	if err := requireAdmin(c); err != nil {
		return nil, err
	}
	u, err := userOrMe(c, p.User)
	if err != nil {
		return nil, err
	}
	path := forgejo.Path("admin", "users", u, "tokens", strconv.FormatInt(p.ID, 10))
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodDelete, path, nil, nil, nil); err != nil {
		return nil, adminOnlyToken(err)
	}
	return ok("deleted token id %d of %s", p.ID, u), nil
}
