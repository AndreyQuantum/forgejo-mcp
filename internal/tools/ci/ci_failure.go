package ci

import (
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared"
	sci "github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared/ci"
)

// failureParams are the parameters of ci_failure (section 8.6).
type failureParams struct {
	Repo             string `json:"repo,omitempty" jsonschema:"owner/name; default from git remote or FORGEJO_REPO"`
	Run              int64  `json:"run,omitempty" jsonschema:"run id"`
	Number           int64  `json:"number,omitempty" jsonschema:"per-repo run number (no.N)"`
	PR               int64  `json:"pr,omitempty" jsonschema:"pull request number"`
	Ref              string `json:"ref,omitempty" jsonschema:"branch or tag"`
	SHA              string `json:"sha,omitempty" jsonschema:"commit sha"`
	Tail             int    `json:"tail,omitempty" jsonschema:"log lines kept per failing step (default 30)"`
	MaxJobs          int    `json:"max_jobs,omitempty" jsonschema:"failed jobs detailed per run (default 5)"`
	MaxChars         int    `json:"max_chars,omitempty" jsonschema:"output size (default 12000)"`
	IncludeCancelled bool   `json:"include_cancelled,omitempty" jsonschema:"also report cancelled runs and jobs"`
}

func registerCIFailure(r *tools.Registry) {
	t := &failureTool{}
	r.Add(tools.Spec{
		Name:        "ci_failure",
		Toolset:     "actions",
		Description: "One call: why did CI fail. Failed jobs with the tail of the failing step's log, for a run, PR, ref or sha.",
		Params:      failureParams{},
		RepoScoped:  true,
		Single:      true,
		Actions: []tools.Action{
			tools.NewAction("run", tools.Read, t.run,
				tools.Summary("failed jobs with the tail of the failing step"),
				tools.Params("run", "number", "pr", "ref", "sha", "tail", "max_jobs", "max_chars", "include_cancelled"),
				tools.Needs("repo read access to code and Actions"),
				tools.Endpoints(
					"GET /repos/{owner}/{repo}/pulls",
					"GET /repos/{owner}/{repo}/pulls/{index}",
					"GET /repos/{owner}/{repo}/branches/{branch}",
					"GET /repos/{owner}/{repo}",
					"GET /repos/{owner}/{repo}/commits",
					"GET /repos/{owner}/{repo}/git/commits/{sha}",
					"GET /repos/{owner}/{repo}/actions/runs",
					"GET /repos/{owner}/{repo}/actions/runs/{run}",
					"GET /repos/{owner}/{repo}/actions/runs/{run}/jobs",
					"GET /repos/{owner}/{repo}/actions/jobs/{job_id}",
					"GET /repos/{owner}/{repo}/actions/jobs/{job_id}/logs",
					"GET /repos/{owner}/{repo}/commits/{ref}/status",
				),
			),
		},
	})
}

type failureTool struct{}

func (failureTool) run(c *tools.Call, p *failureParams) (*tools.Result, error) {
	sel := selector{Run: p.Run, Number: p.Number, PR: p.PR, Ref: p.Ref, SHA: p.SHA}
	if err := sel.check(); err != nil {
		return nil, err
	}
	if p.Tail < 0 || p.MaxJobs < 0 {
		return nil, errf("invalid: tail and max_jobs must not be negative")
	}
	o := sci.DigestOpts{
		Owner: c.Owner, Repo: c.Repo,
		Tail: p.Tail, MaxJobs: p.MaxJobs,
		MaxChars:      c.Chars(p.MaxChars, 12000),
		IncludeCancel: p.IncludeCancelled,
	}
	switch {
	case sel.Run > 0:
		o.Run = sel.Run
	case sel.Number > 0:
		ref, err := shared.ResolveRun(c.Ctx, c.Env.Client, c.Owner, c.Repo, shared.RunSelector{Number: sel.Number})
		if err != nil {
			return nil, err
		}
		o.Run = ref.ID
	case sel.PR > 0:
		o.PR = sel.PR
	case sel.SHA != "":
		sha, err := fullCommit(c, sel.SHA)
		if err != nil {
			return nil, err
		}
		o.SHA = sha
	case sel.Ref != "":
		o.Ref = sel.Ref
	default:
		t, err := defaultTarget(c.Ctx, c)
		if err != nil {
			return nil, err
		}
		o.TargetLabel = t.Label
		if t.PR > 0 {
			o.PR = t.PR
		} else {
			o.Ref = t.Branch
		}
	}
	text, _, err := sci.FailureDigest(c.Ctx, c.Env.Client, o)
	if err != nil {
		return nil, err
	}
	return &tools.Result{Text: c.CapTail(text)}, nil
}
