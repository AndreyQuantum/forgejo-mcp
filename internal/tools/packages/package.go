// Package packages implements the `package` tool (DESIGN.md section 8.10).
package packages

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// packageParams are the parameters of the package tool.
type packageParams struct {
	Action  string `json:"action"`
	Owner   string `json:"owner,omitempty" jsonschema:"user or org; default: owner of the default repo"`
	Type    string `json:"type,omitempty" jsonschema:"alpine|cargo|chef|composer|conan|conda|container|cran|debian|generic|go|helm|maven|npm|nuget|pub|pypi|rpm|rubygems|swift|vagrant"`
	Name    string `json:"name,omitempty" jsonschema:"package name"`
	Version string `json:"version,omitempty" jsonschema:"package version"`
	Q       string `json:"q,omitempty" jsonschema:"name filter"`
	Repo    string `json:"repo,omitempty" jsonschema:"link: target repo (same owner); required for link"`
	Confirm string `json:"confirm,omitempty" jsonschema:"owner/type/name/version"`
	Page    int    `json:"page,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

var packageTypes = []string{
	"alpine", "cargo", "chef", "composer", "conan", "conda", "container", "cran", "debian", "generic", "go",
	"helm", "maven", "npm", "nuget", "pub", "pypi", "rpm", "rubygems", "swift", "vagrant",
}

// registerPackage registers the package tool.
func registerPackage(r *tools.Registry) {
	const needs = "package owner or write access to the owner"
	r.Add(tools.Spec{
		Name:        "package",
		Toolset:     "packages",
		Description: "Packages: list, get, files, delete, link, unlink.",
		Params:      packageParams{},
		Actions: []tools.Action{
			tools.NewAction("list", tools.Read, runList,
				tools.Summary("list packages of an owner"),
				tools.Params("owner", "type", "q", "page", "limit"),
				tools.Endpoints("GET /packages/{owner}"),
			),
			tools.NewAction("get", tools.Read, runGet,
				tools.Summary("one package version"),
				tools.Params("owner"),
				tools.Required("type", "name", "version"),
				tools.Endpoints("GET /packages/{owner}/{type}/{name}/{version}"),
			),
			tools.NewAction("files", tools.Read, runFiles,
				tools.Summary("files of a package version"),
				tools.Params("owner", "page", "limit"),
				tools.Required("type", "name", "version"),
				tools.Endpoints("GET /packages/{owner}/{type}/{name}/{version}/files"),
			),
			tools.NewAction("delete", tools.Destructive, runDelete,
				tools.Summary("delete a package version (confirm=owner/type/name/version)"),
				tools.Params("owner"),
				tools.Required("type", "name", "version", "confirm"),
				tools.Endpoints("DELETE /packages/{owner}/{type}/{name}/{version}"),
				tools.Needs(needs),
			),
			tools.NewAction("link", tools.Write, runLink,
				tools.Summary("link a package to a repository of the same owner"),
				tools.Params("owner"),
				tools.Required("type", "name", "repo"),
				tools.Endpoints("POST /packages/{owner}/{type}/{name}/-/link/{repo_name}"),
				tools.Needs(needs),
			),
			tools.NewAction("unlink", tools.Write, runUnlink,
				tools.Summary("remove the repository link of a package"),
				tools.Params("owner"),
				tools.Required("type", "name"),
				tools.Endpoints("POST /packages/{owner}/{type}/{name}/-/unlink"),
				tools.Needs(needs),
			),
		},
	})
}

// owner returns the explicit owner, else the owner of the default repo, else
// the authenticated user.
var ownerRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// okSegment reports whether s is usable as one path segment name.
func okSegment(s string) bool { return strings.TrimSpace(s) != "" && s != "." && s != ".." }

func owner(c *tools.Call, explicit string) (string, error) {
	if o := strings.TrimSpace(explicit); o != "" {
		if !ownerRe.MatchString(o) {
			return "", fmt.Errorf("invalid: owner %q", o)
		}
		return o, nil
	}
	if c.Env.Repo != nil {
		if o, _, _, err := c.Env.Repo.Resolve(""); err == nil && o != "" {
			return o, nil
		}
	}
	if c.Env.Client.Token() != "" {
		if u, err := c.Env.Client.Me(c.Ctx); err == nil && u.Login != "" {
			return u.Login, nil
		}
	}
	return "", fmt.Errorf("invalid: missing param owner (no default repo to take it from)")
}

func checkType(t string) error {
	if slices.Contains(packageTypes, t) {
		return nil
	}
	return fmt.Errorf("invalid: type %q; valid: %s", t, strings.Join(packageTypes, ","))
}

// pkgPath builds /packages/{owner}/{type}/{name}[/{version}]; every segment is escaped.
func pkgPath(o, typ, name, version string, rest ...string) string {
	parts := []string{"packages", o, typ, name}
	if version != "" {
		parts = append(parts, version)
	}
	return forgejo.Path(append(parts, rest...)...)
}

// target validates owner, type and (optionally) version.
func target(c *tools.Call, p *packageParams, needVersion bool) (string, *tools.Result) {
	o, err := owner(c, p.Owner)
	if err != nil {
		return "", c.Fail("%s", err.Error())
	}
	if err := checkType(p.Type); err != nil {
		return "", c.Fail("%s", err.Error())
	}
	if !okSegment(p.Name) {
		return "", c.Fail("invalid: name %q", p.Name)
	}
	if needVersion && !okSegment(p.Version) {
		return "", c.Fail("invalid: version %q", p.Version)
	}
	return o, nil
}

func runList(c *tools.Call, p *packageParams) (*tools.Result, error) {
	o, err := owner(c, p.Owner)
	if err != nil {
		return c.Fail("%s", err.Error()), nil
	}
	q := url.Values{}
	if p.Type != "" {
		if err := checkType(p.Type); err != nil {
			return c.Fail("%s", err.Error()), nil
		}
		q.Set("type", p.Type)
	}
	if p.Q != "" {
		q.Set("q", p.Q)
	}
	var items []pkg
	pg, err := c.Env.Client.List(c.Ctx, forgejo.Path("packages", o), q, p.Page, p.Limit, &items)
	if err != nil {
		return nil, err
	}
	var b format.B
	b.Raw(format.ListHeader("packages", pg) + "\n")
	for i := range items {
		b.Raw(listLine(&items[i]) + "\n")
	}
	return &tools.Result{Text: c.Cap(b.String())}, nil
}

func runGet(c *tools.Call, p *packageParams) (*tools.Result, error) {
	o, res := target(c, p, true)
	if res != nil {
		return res, nil
	}
	var out pkg
	if _, err := c.Env.Client.GetJSON(c.Ctx, pkgPath(o, p.Type, p.Name, p.Version), nil, &out); err != nil {
		return nil, err
	}
	return &tools.Result{Text: c.Cap(renderOne(&out))}, nil
}

// runFiles lists the files of a version; the endpoint is not paginated, so the
// list is windowed client-side (section 6.7).
func runFiles(c *tools.Call, p *packageParams) (*tools.Result, error) {
	o, res := target(c, p, true)
	if res != nil {
		return res, nil
	}
	var items []pkgFile
	if _, err := c.Env.Client.GetJSON(c.Ctx, pkgPath(o, p.Type, p.Name, p.Version, "files"), nil, &items); err != nil {
		return nil, err
	}
	page, limit := max(p.Page, 1), p.Limit
	if limit <= 0 {
		limit = c.Env.Cfg.DefaultLimit
	}
	limit = min(max(limit, 1), 100)
	total := len(items)
	from := min((page-1)*limit, total)
	to := min(from+limit, total)
	pg := forgejo.Page{Page: page, Limit: limit, Total: int64(total), Shown: to - from, More: to < total}
	var b format.B
	b.Raw(format.ListHeader("files", pg) + "\n")
	for i := from; i < to; i++ {
		b.Raw(fileLine(&items[i]) + "\n")
	}
	return &tools.Result{Text: c.Cap(b.String())}, nil
}

func runDelete(c *tools.Call, p *packageParams) (*tools.Result, error) {
	o, res := target(c, p, true)
	if res != nil {
		return res, nil
	}
	want := strings.Join([]string{o, p.Type, p.Name, p.Version}, "/")
	if r := c.Confirm(p.Confirm, want); r != nil {
		return r, nil
	}
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: "DELETE", Path: pkgPath(o, p.Type, p.Name, p.Version)}); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: deleted package %s %s %s of %s", p.Type, p.Name, p.Version, o)}, nil
}

// repoTarget strips an optional owner prefix: the API links within the owner's namespace.
func repoTarget(o, repo string) (string, error) {
	repo = strings.TrimSpace(repo)
	if i := strings.Index(repo, "/"); i >= 0 {
		if !strings.EqualFold(repo[:i], o) {
			return "", fmt.Errorf("invalid: repo %q must belong to %s (packages link within one owner)", repo, o)
		}
		repo = repo[i+1:]
	}
	if !okSegment(repo) || strings.ContainsAny(repo, "/ ") {
		return "", fmt.Errorf("invalid: repo %q", repo)
	}
	return repo, nil
}

func runLink(c *tools.Call, p *packageParams) (*tools.Result, error) {
	o, res := target(c, p, false)
	if res != nil {
		return res, nil
	}
	if strings.TrimSpace(p.Repo) == "" {
		return c.Fail("invalid: missing param repo"), nil
	}
	repo, err := repoTarget(o, p.Repo)
	if err != nil {
		return c.Fail("%s", err.Error()), nil
	}
	path := forgejo.Path("packages", o, p.Type, p.Name, "-", "link", repo)
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: "POST", Path: path}); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: linked package %s %s to %s/%s", p.Type, p.Name, o, repo)}, nil
}

func runUnlink(c *tools.Call, p *packageParams) (*tools.Result, error) {
	o, res := target(c, p, false)
	if res != nil {
		return res, nil
	}
	path := forgejo.Path("packages", o, p.Type, p.Name, "-", "unlink")
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: "POST", Path: path}); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: unlinked package %s %s from its repository", p.Type, p.Name)}, nil
}
