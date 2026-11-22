package ci

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	sci "github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared/ci"
)

// checksParams are the parameters of checks (section 8.6).
type checksParams struct {
	Action      string `json:"action" jsonschema:"action to run"`
	Repo        string `json:"repo,omitempty" jsonschema:"owner/name; default from git remote or FORGEJO_REPO"`
	Ref         string `json:"ref,omitempty" jsonschema:"branch or tag"`
	SHA         string `json:"sha,omitempty" jsonschema:"commit sha"`
	State       string `json:"state,omitempty" jsonschema:"status state: pending, success, error, failure, warning, skipped"`
	Context     string `json:"context,omitempty" jsonschema:"status context, e.g. ci/external"`
	Description string `json:"description,omitempty" jsonschema:"status description"`
	TargetURL   string `json:"target_url,omitempty" jsonschema:"status link"`
	Verbose     bool   `json:"verbose,omitempty" jsonschema:"also list the Actions runs of the commit"`
	Page        int    `json:"page,omitempty" jsonschema:"page number from 1"`
	Limit       int    `json:"limit,omitempty" jsonschema:"items per page"`
}

var statusStates = []string{"pending", "success", "error", "failure", "warning", "skipped"}

func registerChecks(r *tools.Registry) {
	t := &checksTool{}
	r.Add(tools.Spec{
		Name:        "checks",
		Toolset:     "actions",
		Description: "Commit statuses and combined CI state for a ref/PR; set a status.",
		Params:      checksParams{},
		RepoScoped:  true,
		Actions: []tools.Action{
			tools.NewAction("get", tools.Read, t.get,
				tools.Summary("combined CI state of a ref or sha (default: current branch or PR)"),
				tools.Params("ref", "sha", "verbose"),
				tools.Endpoints(
					"GET /repos/{owner}/{repo}/commits/{ref}/status",
					"GET /repos/{owner}/{repo}/commits",
					"GET /repos/{owner}/{repo}/git/commits/{sha}",
					"GET /repos/{owner}/{repo}/actions/runs",
					"GET /repos/{owner}/{repo}/pulls",
					"GET /repos/{owner}/{repo}/branches/{branch}",
					"GET /repos/{owner}/{repo}",
				),
			),
			tools.NewAction("list", tools.Read, t.list,
				tools.Summary("full status history of a ref or sha"),
				tools.Params("ref", "sha", "state", "page", "limit"),
				tools.Endpoints(
					"GET /repos/{owner}/{repo}/commits/{ref}/statuses",
					"GET /repos/{owner}/{repo}/pulls",
					"GET /repos/{owner}/{repo}/branches/{branch}",
					"GET /repos/{owner}/{repo}",
				),
			),
			tools.NewAction("set", tools.Write, t.set,
				tools.Summary("set a commit status"),
				tools.Params("ref", "sha", "state", "context", "description", "target_url"),
				tools.Required("state", "context"),
				tools.Needs("repo write access"),
				tools.Endpoints(
					"POST /repos/{owner}/{repo}/statuses/{sha}",
					"GET /repos/{owner}/{repo}/commits",
					"GET /repos/{owner}/{repo}/git/commits/{sha}",
				),
			),
		},
	})
}

type checksTool struct{}

// refTarget returns the ref to read statuses for and the optional first
// output line: sha | ref | the target rule of 3.1 step 4.
func (checksTool) refTarget(c *tools.Call, p *checksParams) (ref, label string, err error) {
	switch {
	case p.SHA != "" && p.Ref != "":
		return "", "", errf("invalid: give only one of sha, ref")
	case p.SHA != "":
		return p.SHA, "", nil
	case p.Ref != "":
		return p.Ref, "", nil
	}
	t, err := defaultTarget(c.Ctx, c)
	if err != nil {
		return "", "", err
	}
	return t.Branch, t.Label, nil
}

// fullSHA resolves the target of a status read or write to a full commit id.
func fullSHA(c *tools.Call, p *checksParams, ref string) (string, error) {
	if p.SHA != "" {
		return fullCommit(c, p.SHA)
	}
	return sci.ResolveSHA(c.Ctx, c.Env.Client, c.Owner, c.Repo, ref)
}

// statusLine renders `failure ci / build (push) | Failing after 12s | url`.
func statusLine(state, ctxName, desc, target string) string {
	var b format.B
	b.Line(state+" "+ctxName, format.OneLine(desc, 120), target)
	return strings.TrimRight(b.String(), "\n")
}

func (t checksTool) get(c *tools.Call, p *checksParams) (*tools.Result, error) {
	ref, label, err := t.refTarget(c, p)
	if err != nil {
		return nil, err
	}
	sha, err := fullSHA(c, p, ref)
	if err != nil {
		return nil, err
	}
	cs, err := sci.CombinedStatus(c.Ctx, c.Env.Client, c.Owner, c.Repo, sha)
	if err != nil {
		return nil, err
	}
	var b format.B
	if label != "" {
		b.Raw(label + "\n")
	}
	head := "state: " + cs.State
	if cs.Total > 0 {
		head += fmt.Sprintf(" (%d/%d ok)", cs.OK, cs.Total)
	}
	b.Raw(head + " sha " + format.SHA(sha, 8) + "\n")
	for _, f := range cs.Failing {
		b.Raw(statusLine(f.State, f.Context, f.Description, f.TargetURL) + "\n")
	}
	for _, name := range cs.Pending {
		b.Raw("pending " + name + "\n")
	}
	if p.Verbose {
		runs, rerr := sci.RunsForSHA(c.Ctx, c.Env.Client, c.Owner, c.Repo, sha, 20)
		switch {
		case rerr != nil:
			b.Raw("runs: unavailable (" + format.OneLine(strings.TrimPrefix(rerr.Error(), "error: "), 120) + ")\n")
		case len(runs) == 0:
			b.Raw("runs: none\n")
		default:
			for _, r := range sci.NewestPerGroup(runs) {
				b.Raw(sci.RunLine(r) + "\n")
			}
		}
	}
	return &tools.Result{Text: c.Cap(strings.TrimRight(b.String(), "\n"))}, nil
}

func (t checksTool) list(c *tools.Call, p *checksParams) (*tools.Result, error) {
	if p.State != "" && !slices.Contains(statusStates, p.State) {
		return nil, errf("invalid: state %q; valid: %s", p.State, strings.Join(statusStates, ","))
	}
	ref, label, err := t.refTarget(c, p)
	if err != nil {
		return nil, err
	}
	page, limit := pageArgs(c, p.Page, p.Limit)
	// The default order (created_unix DESC) is unstable for statuses posted
	// within the same second. "leastindex" is Forgejo's name for index DESC,
	// i.e. newest first with a strict tie-break.
	q := url.Values{"sort": {"leastindex"}}
	if p.State != "" {
		q.Set("state", p.State)
	}
	var items []forgejo.Status
	pg, err := c.Env.Client.List(c.Ctx, forgejo.Path("repos", c.Owner, c.Repo, "commits", ref, "statuses"), q, page, limit, &items)
	if err != nil {
		return nil, err
	}
	var b format.B
	if label != "" {
		b.Raw(label + "\n")
	}
	b.Raw(format.ListHeader("statuses", pg) + "\n")
	for _, s := range items {
		b.Raw(statusLine(s.Status, s.Context, s.Description, s.TargetURL) + "\n")
	}
	return &tools.Result{Text: c.Cap(strings.TrimRight(b.String(), "\n"))}, nil
}

func (t checksTool) set(c *tools.Call, p *checksParams) (*tools.Result, error) {
	if !slices.Contains(statusStates, p.State) {
		return nil, errf("invalid: state %q; valid: %s", p.State, strings.Join(statusStates, ","))
	}
	if strings.TrimSpace(p.Context) == "" {
		return nil, errf("invalid: context must not be empty")
	}
	if p.SHA == "" && p.Ref == "" {
		return nil, errf("invalid: give sha or ref")
	}
	if p.SHA != "" && p.Ref != "" {
		return nil, errf("invalid: give only one of sha, ref")
	}
	sha, err := fullSHA(c, p, p.Ref)
	if err != nil {
		return nil, err
	}
	body := map[string]string{"state": p.State, "context": p.Context}
	if p.Description != "" {
		body["description"] = p.Description
	}
	if p.TargetURL != "" {
		body["target_url"] = p.TargetURL
	}
	if _, err := c.Env.Client.SendJSON(c.Ctx, "POST", forgejo.Path("repos", c.Owner, c.Repo, "statuses", sha), nil, body, nil); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: status %s=%s on %s", p.Context, p.State, format.SHA(sha, 8))}, nil
}

// pageArgs applies the defaults of section 6.7 (the client clamps limit to
// the server maximum).
func pageArgs(c *tools.Call, page, limit int) (int, int) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
		if c.Env.Cfg != nil && c.Env.Cfg.DefaultLimit > 0 {
			limit = c.Env.Cfg.DefaultLimit
		}
	}
	return page, min(limit, 100)
}
