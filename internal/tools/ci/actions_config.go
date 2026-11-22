package ci

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// configParams are the parameters of actions_config (section 8.6).
type configParams struct {
	Action string `json:"action" jsonschema:"action to run"`
	Repo   string `json:"repo,omitempty" jsonschema:"owner/name; default from git remote or FORGEJO_REPO"`
	Org    string `json:"org,omitempty" jsonschema:"organization (selects org scope)"`
	Scope  string `json:"scope,omitempty" jsonschema:"repo, org or user (default repo; org when org is given)"`
	Kind   string `json:"kind,omitempty" jsonschema:"secret or variable"`
	Name   string `json:"name,omitempty" jsonschema:"secret or variable name"`
	Value  string `json:"value,omitempty" jsonschema:"secret or variable value"`
	Page   int    `json:"page,omitempty" jsonschema:"page number from 1"`
	Limit  int    `json:"limit,omitempty" jsonschema:"items per page"`
}

const configNeeds = "repo owner (repo scope), org owner (org scope) or the user themself (user scope)"

func registerActionsConfig(r *tools.Registry) {
	t := &configTool{}
	common := []string{"repo", "org", "scope", "kind"}
	r.Add(tools.Spec{
		Name:        "actions_config",
		Toolset:     "actions",
		Description: "Actions secrets and variables at repo, org or user scope: list, get, set, delete.",
		Params:      configParams{},
		Actions: []tools.Action{
			tools.NewAction("list", tools.Read, t.list,
				tools.Summary("list secret names or variables"),
				tools.Params(append(common, "page", "limit")...),
				tools.Required("kind"), tools.Needs(configNeeds),
				tools.Endpoints(
					"GET /repos/{owner}/{repo}/actions/secrets", "GET /orgs/{org}/actions/secrets", "GET /user/actions/secrets",
					"GET /repos/{owner}/{repo}/actions/variables", "GET /orgs/{org}/actions/variables", "GET /user/actions/variables",
				),
			),
			tools.NewAction("get", tools.Read, t.get,
				tools.Summary("read a variable (secrets are write-only)"),
				tools.Params(append(common, "name")...),
				tools.Required("kind", "name"), tools.Needs(configNeeds),
				tools.Endpoints(
					"GET /repos/{owner}/{repo}/actions/variables/{variablename}",
					"GET /orgs/{org}/actions/variables/{variablename}",
					"GET /user/actions/variables/{variablename}",
				),
			),
			tools.NewAction("set", tools.Write, t.set,
				tools.Summary("create or update a secret or variable"),
				tools.Params(append(common, "name", "value")...),
				tools.Required("kind", "name", "value"), tools.Needs(configNeeds),
				tools.Endpoints(
					"PUT /repos/{owner}/{repo}/actions/secrets/{secretname}", "PUT /orgs/{org}/actions/secrets/{secretname}", "PUT /user/actions/secrets/{secretname}",
					"GET /repos/{owner}/{repo}/actions/variables/{variablename}", "GET /orgs/{org}/actions/variables/{variablename}", "GET /user/actions/variables/{variablename}",
					"POST /repos/{owner}/{repo}/actions/variables/{variablename}", "POST /orgs/{org}/actions/variables/{variablename}", "POST /user/actions/variables/{variablename}",
					"PUT /repos/{owner}/{repo}/actions/variables/{variablename}", "PUT /orgs/{org}/actions/variables/{variablename}", "PUT /user/actions/variables/{variablename}",
				),
			),
			tools.NewAction("delete", tools.Write, t.delete,
				tools.Summary("delete a secret or variable"),
				tools.Params(append(common, "name")...),
				tools.Required("kind", "name"), tools.Needs(configNeeds),
				tools.Endpoints(
					"DELETE /repos/{owner}/{repo}/actions/secrets/{secretname}", "DELETE /orgs/{org}/actions/secrets/{secretname}", "DELETE /user/actions/secrets/{secretname}",
					"DELETE /repos/{owner}/{repo}/actions/variables/{variablename}", "DELETE /orgs/{org}/actions/variables/{variablename}", "DELETE /user/actions/variables/{variablename}",
				),
			),
		},
	})
}

type configTool struct{}

var ownerRepoPart = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// target is the resolved scope of one call.
type configTarget struct {
	prefix string // e.g. /repos/o/r/actions
	scope  string // repo|org|user
	label  string // shown in ok lines
}

// resolve picks the API prefix: scope repo (default), org (when org is given
// or scope=org) or user.
func (configTool) resolve(c *tools.Call, p *configParams) (configTarget, error) {
	scope := p.Scope
	if scope == "" {
		scope = "repo"
		if p.Org != "" {
			scope = "org"
		}
	}
	switch scope {
	case "repo":
		if c.Env.Repo == nil {
			return configTarget{}, errors.New("invalid: no repository resolver configured")
		}
		o, n, _, err := c.Env.Repo.Resolve(p.Repo)
		if err != nil {
			return configTarget{}, err
		}
		if !ownerRepoPart.MatchString(o) || !ownerRepoPart.MatchString(n) {
			return configTarget{}, fmt.Errorf("invalid: repo %q (owner and name allow letters, digits, . _ -)", o+"/"+n)
		}
		return configTarget{prefix: forgejo.Path("repos", o, n, "actions"), scope: scope, label: o + "/" + n}, nil
	case "org":
		if strings.TrimSpace(p.Org) == "" {
			return configTarget{}, errors.New("invalid: scope=org needs org")
		}
		return configTarget{prefix: forgejo.Path("orgs", p.Org, "actions"), scope: scope, label: "org " + p.Org}, nil
	case "user":
		return configTarget{prefix: "/user/actions", scope: scope, label: "user"}, nil
	}
	return configTarget{}, fmt.Errorf("invalid: scope %q; valid: repo,org,user", scope)
}

func kindOf(p *configParams) (plural string, secret bool, err error) {
	switch p.Kind {
	case "secret":
		return "secrets", true, nil
	case "variable":
		return "variables", false, nil
	}
	return "", false, fmt.Errorf("invalid: kind %q; valid: secret,variable", p.Kind)
}

// varEntry is the projection of an Actions variable.
type varEntry struct {
	Name string `json:"name"`
	Data string `json:"data"`
}

func (v varEntry) line() string { return v.Name + "=" + format.OneLine(v.Data, 200) }

func (t configTool) list(c *tools.Call, p *configParams) (*tools.Result, error) {
	plural, secret, err := kindOf(p)
	if err != nil {
		return nil, err
	}
	tg, err := t.resolve(c, p)
	if err != nil {
		return nil, err
	}
	page, limit := pageArgs(c, p.Page, p.Limit)
	var b format.B
	var pg forgejo.Page
	if secret {
		var items []struct {
			Name string `json:"name"`
		}
		if pg, err = c.Env.Client.List(c.Ctx, tg.prefix+"/secrets", nil, page, limit, &items); err != nil {
			return nil, err
		}
		b.Raw(format.ListHeader(plural, pg) + "\n")
		for _, it := range items {
			b.Raw(it.Name + "\n")
		}
	} else {
		var items []varEntry
		if pg, err = c.Env.Client.List(c.Ctx, tg.prefix+"/variables", nil, page, limit, &items); err != nil {
			return nil, err
		}
		b.Raw(format.ListHeader(plural, pg) + "\n")
		for _, it := range items {
			b.Raw(it.line() + "\n")
		}
	}
	return &tools.Result{Text: c.Cap(strings.TrimRight(b.String(), "\n"))}, nil
}

func (t configTool) get(c *tools.Call, p *configParams) (*tools.Result, error) {
	_, secret, err := kindOf(p)
	if err != nil {
		return nil, err
	}
	if secret {
		return nil, errors.New("invalid: secrets are write-only; use kind=variable, or list kind=secret for names")
	}
	tg, err := t.resolve(c, p)
	if err != nil {
		return nil, err
	}
	var v varEntry
	if _, err := c.Env.Client.GetJSON(c.Ctx, tg.prefix+"/variables/"+forgejo.Seg(p.Name), nil, &v); err != nil {
		return nil, err
	}
	if v.Name == "" {
		v.Name = strings.ToUpper(p.Name)
	}
	return &tools.Result{Text: c.Cap(v.line())}, nil
}

func (t configTool) set(c *tools.Call, p *configParams) (*tools.Result, error) {
	_, secret, err := kindOf(p)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Name) == "" || p.Value == "" {
		return nil, errors.New("invalid: name and value must not be empty")
	}
	tg, err := t.resolve(c, p)
	if err != nil {
		return nil, err
	}
	cl := c.Env.Client
	name := strings.ToUpper(p.Name) // the server stores names uppercased
	seg := forgejo.Seg(p.Name)
	if secret {
		resp, err := cl.Do(c.Ctx, forgejo.Request{Method: http.MethodPut, Path: tg.prefix + "/secrets/" + seg, JSON: map[string]string{"data": p.Value}})
		if err != nil {
			return nil, err
		}
		verb := "updated"
		if resp.Status == http.StatusCreated {
			verb = "created"
		}
		return &tools.Result{Text: fmt.Sprintf("ok: secret %s set (%s, %s)", name, verb, tg.label)}, nil
	}

	vpath := tg.prefix + "/variables/" + seg
	method, verb := http.MethodPost, "created"
	if _, err := cl.Do(c.Ctx, forgejo.Request{Method: http.MethodGet, Path: vpath}); err == nil {
		method, verb = http.MethodPut, "updated"
	} else {
		var ae *forgejo.APIError
		if !errors.As(err, &ae) || ae.Status != http.StatusNotFound {
			return nil, err
		}
	}
	if _, err := cl.Do(c.Ctx, forgejo.Request{Method: method, Path: vpath, JSON: map[string]string{"value": p.Value}}); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: variable %s=%s %s (%s)", name, format.OneLine(p.Value, 40), verb, tg.label)}, nil
}

func (t configTool) delete(c *tools.Call, p *configParams) (*tools.Result, error) {
	plural, secret, err := kindOf(p)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Name) == "" {
		return nil, errors.New("invalid: name must not be empty")
	}
	tg, err := t.resolve(c, p)
	if err != nil {
		return nil, err
	}
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodDelete, Path: tg.prefix + "/" + plural + "/" + forgejo.Seg(p.Name)}); err != nil {
		return nil, err
	}
	kind := "variable"
	if secret {
		kind = "secret"
	}
	return &tools.Result{Text: fmt.Sprintf("ok: deleted %s %s (%s)", kind, strings.ToUpper(p.Name), tg.label)}, nil
}
