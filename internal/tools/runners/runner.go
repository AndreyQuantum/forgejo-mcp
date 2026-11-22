package runners

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared"
)

// runnerParams are the parameters of the runner tool (section 8.7).
type runnerParams struct {
	Action      string    `json:"action"`
	Scope       string    `json:"scope,omitempty" jsonschema:"repo|org|user|admin (default repo)"`
	Repo        string    `json:"repo,omitempty" jsonschema:"owner/name for scope=repo; default: detected repo"`
	Org         string    `json:"org,omitempty" jsonschema:"organization for scope=org"`
	ID          int       `json:"id,omitempty" jsonschema:"runner id"`
	Name        string    `json:"name,omitempty" jsonschema:"runner name"`
	Description string    `json:"description,omitempty" jsonschema:"runner description"`
	Ephemeral   bool      `json:"ephemeral,omitempty" jsonschema:"single-use runner"`
	Visible     *bool     `json:"visible,omitempty" jsonschema:"include runners visible from this level (default true)"`
	Status      string    `json:"status,omitempty" jsonschema:"online|offline|idle|active, client-side filter"`
	Label       string    `json:"label,omitempty" jsonschema:"keep runners that have this label (client-side)"`
	Labels      labelList `json:"labels,omitempty" jsonschema:"array of labels, or one comma-separated string; queue filter, or labels printed in the .runner line"`
	Page        int       `json:"page,omitempty"`
	Limit       int       `json:"limit,omitempty"`
}

// labelList is a list of labels that decodes from a JSON array (the shared
// vocabulary of section 7.1) or from one comma-separated string (section 8.7).
type labelList []string

// UnmarshalJSON accepts ["a","b"], "a,b" and null.
func (l *labelList) UnmarshalJSON(b []byte) error {
	var arr []string
	if err := json.Unmarshal(b, &arr); err != nil {
		var one string
		if err2 := json.Unmarshal(b, &one); err2 != nil {
			return err
		}
		arr = []string{one}
	}
	var out []string
	for _, a := range arr {
		out = append(out, splitCSV(a)...)
	}
	*l = out
	return nil
}

var namePart = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

const (
	scopeRepo  = "repo"
	scopeOrg   = "org"
	scopeUser  = "user"
	scopeAdmin = "admin"

	needsText = "repo owner (scope=repo), org owner (scope=org), site admin (scope=admin)"
	maxJobs   = 20
)

// registerRunner registers the runner tool.
func registerRunner(r *tools.Registry) {
	t := &runnerTool{}
	pfx := []string{"scope", "repo", "org"}
	with := func(extra ...string) []string { return append(slices.Clone(pfx), extra...) }
	r.Add(tools.Spec{
		Name:        "runner",
		Toolset:     "runners",
		Description: "Runners at repo/org/user/admin scope: list, get, register, delete, queue, reg_token.",
		Params:      runnerParams{},
		Actions: []tools.Action{
			tools.NewAction("list", tools.Read, t.list,
				tools.Summary("runners visible from the scope; filters are client-side"),
				tools.Params(with("visible", "status", "label", "page", "limit")...),
				tools.Needs(needsText),
				tools.Endpoints(
					"GET /repos/{owner}/{repo}/actions/runners",
					"GET /orgs/{org}/actions/runners",
					"GET /user/actions/runners",
					"GET /admin/actions/runners",
				)),
			tools.NewAction("get", tools.Read, t.get,
				tools.Summary("one runner with uuid, labels, version, description"),
				tools.Params(with("id")...), tools.Required("id"),
				tools.Needs(needsText),
				tools.Endpoints(
					"GET /repos/{owner}/{repo}/actions/runners/{runner_id}",
					"GET /orgs/{org}/actions/runners/{runner_id}",
					"GET /user/actions/runners/{runner_id}",
					"GET /admin/actions/runners/{runner_id}",
				)),
			tools.NewAction("register", tools.Write, t.register,
				tools.Summary("create a runner; prints the token once and a ready .runner line"),
				tools.Params(with("name", "description", "ephemeral", "labels")...), tools.Required("name"),
				tools.Needs(needsText),
				tools.Endpoints(
					"POST /repos/{owner}/{repo}/actions/runners",
					"POST /orgs/{org}/actions/runners",
					"POST /user/actions/runners",
					"POST /admin/actions/runners",
				)),
			tools.NewAction("delete", tools.Write, t.delete,
				tools.Summary("delete a runner registered at exactly this level"),
				tools.Params(with("id")...), tools.Required("id"),
				tools.Needs(needsText),
				tools.Endpoints(
					"DELETE /repos/{owner}/{repo}/actions/runners/{runner_id}",
					"DELETE /orgs/{org}/actions/runners/{runner_id}",
					"DELETE /user/actions/runners/{runner_id}",
					"DELETE /admin/actions/runners/{runner_id}",
				)),
			tools.NewAction("queue", tools.Read, t.queue,
				tools.Summary("waiting and running jobs, optionally for labels"),
				tools.Params(with("labels")...),
				tools.Needs(needsText),
				tools.Endpoints(
					"GET /repos/{owner}/{repo}/actions/runners/jobs",
					"GET /orgs/{org}/actions/runners/jobs",
					"GET /user/actions/runners/jobs",
					"GET /admin/actions/runners/jobs",
				)),
			tools.NewAction("reg_token", tools.Write, t.regToken,
				tools.Summary("registration token (deprecated; creates one as a side effect)"),
				tools.Params(with()...),
				tools.Needs(needsText),
				tools.Endpoints(
					"GET /repos/{owner}/{repo}/actions/runners/registration-token",
					"GET /orgs/{org}/actions/runners/registration-token",
					"GET /user/actions/runners/registration-token",
					"GET /admin/actions/runners/registration-token",
				)),
		},
	})
}

type runnerTool struct{}

// target is the resolved level of a call.
type target struct {
	scope  string
	prefix string // escaped API path of the runner collection
	label  string // human text for messages
	note   string // "note: ignored params: ..." line, or empty
}

// done wraps the text of a successful call, appending the scope note.
func (t *target) done(text string) *tools.Result {
	if t.note != "" {
		text = strings.TrimRight(text, "\n") + "\n" + t.note
	}
	return &tools.Result{Text: text}
}

// resolve validates scope/repo/org and builds the path prefix.
func resolve(c *tools.Call, p *runnerParams) (*target, *tools.Result) {
	scope := strings.ToLower(strings.TrimSpace(p.Scope))
	if scope == "" {
		scope = scopeRepo
		if strings.TrimSpace(p.Org) != "" && strings.TrimSpace(p.Repo) == "" {
			scope = scopeOrg
		}
	}
	t, bad := resolveScope(c, p, scope)
	if t != nil {
		var ign []string
		if scope != scopeRepo && strings.TrimSpace(p.Repo) != "" {
			ign = append(ign, "repo")
		}
		if scope != scopeOrg && strings.TrimSpace(p.Org) != "" {
			ign = append(ign, "org")
		}
		if len(ign) > 0 {
			t.note = "note: ignored params: " + strings.Join(ign, ",") + " (scope=" + scope + ")"
		}
	}
	return t, bad
}

func resolveScope(c *tools.Call, p *runnerParams, scope string) (*target, *tools.Result) {
	switch scope {
	case scopeRepo:
		if c.Env.Repo == nil {
			return nil, c.Fail("invalid: no repository resolver configured")
		}
		o, n, _, err := c.Env.Repo.Resolve(p.Repo)
		if err != nil {
			return nil, c.Fail("%s", err.Error())
		}
		if _, _, err := shared.ParseOwnerRepo(o + "/" + n); err != nil {
			return nil, c.Fail("invalid: repo %q (owner and name allow letters, digits, . _ -)", o+"/"+n)
		}
		return &target{scope: scope, prefix: forgejo.Path("repos", o, n, "actions", "runners"), label: "repo scope"}, nil
	case scopeOrg:
		org := strings.TrimSpace(p.Org)
		if org == "" {
			return nil, c.Fail("invalid: missing param org for runner.%s with scope=org", c.Action)
		}
		if !namePart.MatchString(org) {
			return nil, c.Fail("invalid: org %q (letters, digits, . _ - only)", org)
		}
		return &target{scope: scope, prefix: forgejo.Path("orgs", org, "actions", "runners"), label: "org scope"}, nil
	case scopeUser:
		return &target{scope: scope, prefix: forgejo.Path("user", "actions", "runners"), label: "user scope"}, nil
	case scopeAdmin:
		return &target{scope: scope, prefix: forgejo.Path("admin", "actions", "runners"), label: "admin scope"}, nil
	}
	return nil, c.Fail("invalid: scope %q; valid: repo,org,user,admin", p.Scope)
}

// scopeNeeds is the permission text for 403 hints of a scope.
func scopeNeeds(scope string) string {
	switch scope {
	case scopeOrg:
		return "org owner"
	case scopeAdmin:
		return "site admin"
	case scopeUser:
		return "own account token"
	}
	return "repo owner"
}

// wrapErr adds scope-specific hints to API errors.
func wrapErr(err error, t *target, action string) error {
	var ae *forgejo.APIError
	if !errors.As(err, &ae) {
		return err
	}
	switch {
	case ae.Status == http.StatusForbidden && ae.Hint == "":
		ae.Hint = "needs: " + scopeNeeds(t.scope)
	case ae.Status == http.StatusNotFound && action == "delete" && strings.Contains(ae.Message, "No permission"):
		ae.Hint = "the runner belongs to another level; delete it with the scope it was registered at (list visible=false shows this level only)"
	}
	return err
}

func (runnerTool) list(c *tools.Call, p *runnerParams) (*tools.Result, error) {
	t, bad := resolve(c, p)
	if bad != nil {
		return bad, nil
	}
	status := strings.ToLower(strings.TrimSpace(p.Status))
	switch status {
	case "", "online", "offline", "idle", "active":
	default:
		return c.Fail("invalid: status %q; valid: online,offline,idle,active", p.Status), nil
	}
	visible := p.Visible == nil || *p.Visible
	q := url.Values{"visible": {strconv.FormatBool(visible)}}
	var rs []apiRunner
	pg, err := c.Env.Client.List(c.Ctx, t.prefix, q, p.Page, p.Limit, &rs)
	if err != nil {
		return nil, wrapErr(err, t, "list")
	}
	label := strings.ToLower(strings.TrimSpace(p.Label))
	var lines []string
	scanned := 0
	collect := func(rs []apiRunner) {
		scanned += len(rs)
		for _, r := range rs {
			if !statusMatch(status, r.Status) || !labelMatch(label, r.Labels) {
				continue
			}
			lines = append(lines, runnerLine(r))
		}
	}
	collect(rs)
	scope := "on this page"
	// Section 6.7: a post-filtered page that came out empty while the raw page
	// was full may hide matches on later pages; look at up to 3 more.
	for extra := 0; extra < 3 && (status != "" || label != "") && len(lines) == 0 && pg.More; extra++ {
		var more []apiRunner
		npg, err := c.Env.Client.List(c.Ctx, t.prefix, q, pg.Page+1, pg.Limit, &more)
		if err != nil {
			return nil, wrapErr(err, t, "list")
		}
		pg = npg
		scope = "scanned"
		collect(more)
	}
	var b format.B
	head := format.ListHeader("runners", pg) + " | visible=" + strconv.FormatBool(visible)
	b.Raw(head + "\n")
	if status != "" || label != "" {
		var f []string
		if status != "" {
			f = append(f, "status="+status)
		}
		if label != "" {
			f = append(f, "label="+label)
		}
		b.Raw(fmt.Sprintf("filter: %s (client-side, %d of %d %s)\n", strings.Join(f, " "), len(lines), scanned, scope))
	}
	for _, l := range lines {
		b.Raw(l + "\n")
	}
	return t.done(strings.TrimRight(b.String(), "\n")), nil
}

func statusMatch(want, got string) bool {
	switch want {
	case "":
		return true
	case "online":
		return got == "idle" || got == "active"
	}
	return want == got
}

func labelMatch(want string, labels []string) bool {
	if want == "" {
		return true
	}
	for _, l := range labels {
		l = strings.ToLower(l)
		name, _, _ := strings.Cut(l, ":")
		if l == want || name == want {
			return true
		}
	}
	return false
}

func (runnerTool) get(c *tools.Call, p *runnerParams) (*tools.Result, error) {
	t, bad := resolve(c, p)
	if bad != nil {
		return bad, nil
	}
	if p.ID <= 0 {
		return c.Fail("invalid: id must be a positive runner id"), nil
	}
	var r apiRunner
	if _, err := c.Env.Client.GetJSON(c.Ctx, t.prefix+"/"+strconv.Itoa(p.ID), nil, &r); err != nil {
		return nil, wrapErr(err, t, "get")
	}
	return t.done(strings.TrimRight(runnerBlock(r), "\n")), nil
}

func (runnerTool) register(c *tools.Call, p *runnerParams) (*tools.Result, error) {
	t, bad := resolve(c, p)
	if bad != nil {
		return bad, nil
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return c.Fail("invalid: missing param name for runner.register"), nil
	}
	body := map[string]any{"name": name, "description": p.Description, "ephemeral": p.Ephemeral}
	var out registerResponse
	if _, err := c.Env.Client.SendJSON(c.Ctx, http.MethodPost, t.prefix, nil, body, &out); err != nil {
		return nil, wrapErr(err, t, "register")
	}
	labels := []string(p.Labels)
	addr := c.Env.Client.BaseURL()
	var b format.B
	b.Raw(fmt.Sprintf("ok: registered runner %d %q (%s)\n", out.ID, name, t.label))
	b.Raw("uuid: " + out.UUID + "\n")
	b.Raw("token: " + out.Token + "\n")
	b.Raw("address: " + addr + "\n")
	b.Raw(".runner: " + renderRunnerFile(out.ID, out.UUID, name, out.Token, addr, labels) + "\n")
	b.Raw("start: write the .runner line to <dir>/.runner, set runner.file: .runner in config.yml, then run: forgejo-runner daemon --config config.yml\n")
	if len(labels) == 0 {
		b.Raw("note: pass labels=a,b to include labels in the snippet\n")
	}
	b.Raw("note: store the token now; shown once")
	return t.done(b.String()), nil
}

func (runnerTool) delete(c *tools.Call, p *runnerParams) (*tools.Result, error) {
	t, bad := resolve(c, p)
	if bad != nil {
		return bad, nil
	}
	if p.ID <= 0 {
		return c.Fail("invalid: id must be a positive runner id"), nil
	}
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodDelete, Path: t.prefix + "/" + strconv.Itoa(p.ID)}); err != nil {
		return nil, wrapErr(err, t, "delete")
	}
	return t.done(fmt.Sprintf("ok: deleted runner %d (%s)", p.ID, t.label)), nil
}

func (runnerTool) queue(c *tools.Call, p *runnerParams) (*tools.Result, error) {
	t, bad := resolve(c, p)
	if bad != nil {
		return bad, nil
	}
	q := url.Values{}
	if ls := []string(p.Labels); len(ls) > 0 {
		// Forgejo splits on "," without trimming, so send a normalised list.
		q.Set("labels", strings.Join(ls, ","))
	}
	var jobs []apiJob
	if _, err := c.Env.Client.GetJSON(c.Ctx, t.prefix+"/jobs", q, &jobs); err != nil {
		return nil, wrapErr(err, t, "queue")
	}
	var b format.B
	b.Raw(queueSummary(jobs))
	for i, j := range jobs {
		if i >= maxJobs {
			b.Raw(fmt.Sprintf("\n[truncated: %d of %d jobs; pass labels to narrow]", maxJobs, len(jobs)))
			break
		}
		b.Raw("\n" + jobLine(j))
	}
	return t.done(b.String()), nil
}

func (runnerTool) regToken(c *tools.Call, p *runnerParams) (*tools.Result, error) {
	t, bad := resolve(c, p)
	if bad != nil {
		return bad, nil
	}
	var out tokenResponse
	if _, err := c.Env.Client.GetJSON(c.Ctx, t.prefix+"/registration-token", nil, &out); err != nil {
		return nil, wrapErr(err, t, "reg_token")
	}
	return t.done("token: " + out.Token + "\nnote: deprecated; prefer register"), nil
}

// splitCSV splits a comma list, trimming blanks.
func splitCSV(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
