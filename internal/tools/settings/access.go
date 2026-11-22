package settings

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

var accessPerms = []string{"read", "write", "admin"}

// accessParams are the flat parameters of the access tool (section 8.8).
type accessParams struct {
	Action     string `json:"action"`
	Repo       string `json:"repo,omitempty" jsonschema:"owner/name; default repo"`
	Kind       string `json:"kind,omitempty" jsonschema:"collaborator or key (deploy key)"`
	User       string `json:"user,omitempty" jsonschema:"collaborator login"`
	Permission string `json:"permission,omitempty" jsonschema:"read, write (default) or admin"`
	ID         int64  `json:"id,omitempty" jsonschema:"deploy key id"`
	Title      string `json:"title,omitempty" jsonschema:"deploy key title"`
	Key        string `json:"key,omitempty" jsonschema:"public SSH key text"`
	ReadOnly   *bool  `json:"read_only,omitempty" jsonschema:"deploy key read-only; default true"`
	Page       int    `json:"page,omitempty" jsonschema:"page, from 1"`
	Limit      int    `json:"limit,omitempty" jsonschema:"items per page, 1..100"`
}

func registerAccess(r *tools.Registry) {
	r.Add(tools.Spec{
		Name:        "access",
		Toolset:     "settings",
		Description: "Collaborators and deploy keys: list, add, remove, permission.",
		Params:      accessParams{},
		RepoScoped:  true,
		Actions: []tools.Action{
			tools.NewAction("list", tools.Read, accessList,
				tools.Summary("list collaborators or deploy keys"),
				tools.Params("kind", "page", "limit"),
				tools.Required("kind"),
				tools.Needs("read access (keys: repo admin)"),
				tools.Endpoints("GET /repos/{owner}/{repo}/collaborators", "GET /repos/{owner}/{repo}/keys"),
			),
			tools.NewAction("permission", tools.Read, accessPermission,
				tools.Summary("permission of a collaborator (kind=collaborator)"),
				tools.Params("kind", "user"),
				tools.Required("kind", "user"),
				tools.Needs("repo admin, or the user themself"),
				tools.Endpoints("GET /repos/{owner}/{repo}/collaborators/{collaborator}/permission"),
			),
			tools.NewAction("add", tools.Write, accessAdd,
				tools.Summary("add a collaborator (user, permission) or deploy key (title, key, read_only)"),
				tools.Params("kind", "user", "permission", "title", "key", "read_only"),
				tools.Required("kind"),
				tools.Needs("repo admin"),
				tools.Endpoints("PUT /repos/{owner}/{repo}/collaborators/{collaborator}", "POST /repos/{owner}/{repo}/keys"),
			),
			tools.NewAction("remove", tools.Write, accessRemove,
				tools.Summary("remove a collaborator (user) or deploy key (id)"),
				tools.Params("kind", "user", "id"),
				tools.Required("kind"),
				tools.Needs("repo admin"),
				tools.Endpoints("DELETE /repos/{owner}/{repo}/collaborators/{collaborator}", "DELETE /repos/{owner}/{repo}/keys/{id}"),
			),
		},
	})
}

func (p *accessParams) checkKind(c *tools.Call) *tools.Result {
	switch p.Kind {
	case "collaborator", "key":
		return nil
	}
	return c.Fail("invalid: kind %q; valid: collaborator,key", p.Kind)
}

func (p *accessParams) needUser(c *tools.Call) *tools.Result {
	p.User = strings.TrimSpace(p.User)
	if p.User == "" {
		return c.Fail("invalid: missing param user for access.%s kind=collaborator", c.Action)
	}
	if strings.ContainsAny(p.User, "/\\") {
		return c.Fail("invalid: user %q", p.User)
	}
	return nil
}

func repoBase(c *tools.Call) string { return forgejo.Path("repos", c.Owner, c.Repo) }

func accessList(c *tools.Call, p *accessParams) (*tools.Result, error) {
	if bad := p.checkKind(c); bad != nil {
		return bad, nil
	}
	page, limit := pageArgs(c, p.Page, p.Limit)
	var b format.B
	if p.Kind == "collaborator" {
		var users []collaborator
		pg, err := c.Env.Client.List(c.Ctx, repoBase(c)+"/collaborators", nil, page, limit, &users)
		if err != nil {
			return nil, err
		}
		b.Raw(format.ListHeader("collaborators", pg) + "\n")
		for _, u := range users {
			b.Raw(u.Login + "\n")
		}
	} else {
		var keys []deployKey
		pg, err := c.Env.Client.List(c.Ctx, repoBase(c)+"/keys", nil, page, limit, &keys)
		if err != nil {
			return nil, err
		}
		b.Raw(format.ListHeader("keys", pg) + "\n")
		for _, k := range keys {
			b.Raw(keyLine(k) + "\n")
		}
	}
	return &tools.Result{Text: strings.TrimRight(b.String(), "\n")}, nil
}

func accessPermission(c *tools.Call, p *accessParams) (*tools.Result, error) {
	if bad := p.checkKind(c); bad != nil {
		return bad, nil
	}
	if p.Kind != "collaborator" {
		return c.Fail("invalid: permission works for kind=collaborator only"), nil
	}
	if bad := p.needUser(c); bad != nil {
		return bad, nil
	}
	var perm collabPermission
	path := repoBase(c) + "/collaborators/" + forgejo.Seg(p.User) + "/permission"
	if _, err := c.Env.Client.GetJSON(c.Ctx, path, nil, &perm); err != nil {
		return nil, err
	}
	text := p.User + ": " + perm.Permission
	if perm.RoleName != "" && perm.RoleName != perm.Permission {
		text += " (" + perm.RoleName + ")"
	}
	return &tools.Result{Text: text}, nil
}

func accessAdd(c *tools.Call, p *accessParams) (*tools.Result, error) {
	if bad := p.checkKind(c); bad != nil {
		return bad, nil
	}
	repo := c.Owner + "/" + c.Repo
	if p.Kind == "collaborator" {
		if bad := p.needUser(c); bad != nil {
			return bad, nil
		}
		perm := p.Permission
		if perm == "" {
			perm = "write"
		}
		if !slices.Contains(accessPerms, perm) {
			return c.Fail("invalid: permission %q; valid: read,write,admin", perm), nil
		}
		path := repoBase(c) + "/collaborators/" + forgejo.Seg(p.User)
		if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPut, path, nil, map[string]string{"permission": perm}, nil); err != nil {
			return nil, err
		}
		return &tools.Result{Text: "ok: added collaborator " + p.User + " (" + perm + ") to " + repo}, nil
	}
	var missing []string
	if strings.TrimSpace(p.Title) == "" {
		missing = append(missing, "title")
	}
	if strings.TrimSpace(p.Key) == "" {
		missing = append(missing, "key")
	}
	if len(missing) > 0 {
		return c.Fail("invalid: missing param %s for access.add kind=key", strings.Join(missing, ", ")), nil
	}
	readOnly := true // the server default is read-write; a deploy key should not push unless asked to
	if p.ReadOnly != nil {
		readOnly = *p.ReadOnly
	}
	body := map[string]any{"title": p.Title, "key": strings.TrimSpace(p.Key), "read_only": readOnly}
	var k deployKey
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPost, repoBase(c)+"/keys", nil, body, &k); err != nil {
		return nil, err
	}
	mode := "read-write"
	if readOnly {
		mode = "ro"
	}
	return &tools.Result{Text: "ok: added deploy key " + strconv.FormatInt(k.ID, 10) + " " +
		format.OneLine(p.Title, 80) + " (" + mode + ") to " + repo}, nil
}

func accessRemove(c *tools.Call, p *accessParams) (*tools.Result, error) {
	if bad := p.checkKind(c); bad != nil {
		return bad, nil
	}
	repo := c.Owner + "/" + c.Repo
	if p.Kind == "collaborator" {
		if bad := p.needUser(c); bad != nil {
			return bad, nil
		}
		path := repoBase(c) + "/collaborators/" + forgejo.Seg(p.User)
		if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodDelete, path, nil, nil, nil); err != nil {
			return nil, err
		}
		return &tools.Result{Text: "ok: removed collaborator " + p.User + " from " + repo}, nil
	}
	if p.ID <= 0 {
		return c.Fail("invalid: missing param id for access.remove kind=key"), nil
	}
	path := repoBase(c) + "/keys/" + strconv.FormatInt(p.ID, 10)
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodDelete, path, nil, nil, nil); err != nil {
		return nil, err
	}
	return &tools.Result{Text: "ok: removed deploy key " + strconv.FormatInt(p.ID, 10) + " from " + repo}, nil
}
