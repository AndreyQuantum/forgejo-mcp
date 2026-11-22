package ci

import (
	"fmt"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared/ci/wait"
)

// waitParams are the parameters of ci_wait (section 8.6, 10.1).
type waitParams struct {
	Repo        string `json:"repo,omitempty" jsonschema:"owner/name; default from git remote or FORGEJO_REPO"`
	Run         int64  `json:"run,omitempty" jsonschema:"run id"`
	Number      int64  `json:"number,omitempty" jsonschema:"per-repo run number (no.N)"`
	PR          int64  `json:"pr,omitempty" jsonschema:"pull request number"`
	Ref         string `json:"ref,omitempty" jsonschema:"branch or tag"`
	SHA         string `json:"sha,omitempty" jsonschema:"commit sha"`
	Timeout     int    `json:"timeout,omitempty" jsonschema:"seconds to wait (default 50, capped by FORGEJO_WAIT_MAX)"`
	FailFast    *bool  `json:"fail_fast,omitempty" jsonschema:"return as soon as a check fails (default true)"`
	WithFailure *bool  `json:"with_failure,omitempty" jsonschema:"append the failure digest when CI fails (default true)"`
}

func registerCIWait(r *tools.Registry) {
	t := &waitTool{}
	r.Add(tools.Spec{
		Name:        "ci_wait",
		Toolset:     "actions",
		Description: "Block server-side until a run or PR/ref checks finish or timeout; returns the verdict.",
		Params:      waitParams{},
		RepoScoped:  true,
		Single:      true,
		Actions: []tools.Action{
			tools.NewAction("run", tools.Read, t.run,
				tools.Summary("wait for a run or for PR/ref checks"),
				tools.Params("run", "number", "pr", "ref", "sha", "timeout", "fail_fast", "with_failure"),
				tools.Needs("repo read access to code and Actions"),
				tools.Endpoints(
					"GET /repos/{owner}/{repo}/pulls",
					"GET /repos/{owner}/{repo}/pulls/{index}",
					"GET /repos/{owner}/{repo}/branches/{branch}",
					"GET /repos/{owner}/{repo}",
					"GET /repos/{owner}/{repo}/commits",
					"GET /repos/{owner}/{repo}/git/commits/{sha}",
					"GET /repos/{owner}/{repo}/commits/{ref}/status",
					"GET /repos/{owner}/{repo}/actions/runs",
					"GET /repos/{owner}/{repo}/actions/runs/{run}",
					"GET /repos/{owner}/{repo}/actions/runs/{run}/jobs",
					"GET /repos/{owner}/{repo}/actions/jobs/{job_id}",
					"GET /repos/{owner}/{repo}/actions/jobs/{job_id}/logs",
					"GET /repos/{owner}/{repo}/actions/runners",
					"GET /repos/{owner}/{repo}/issues/{index}/timeline",
					"GET /repos/{owner}/{repo}/branch_protections/{name}",
					"GET /repos/{owner}/{repo}/pulls/{index}/reviews",
				),
			),
		},
	})
}

type waitTool struct{}

func (waitTool) run(c *tools.Call, p *waitParams) (*tools.Result, error) {
	sel := selector{Run: p.Run, Number: p.Number, PR: p.PR, Ref: p.Ref, SHA: p.SHA}
	if err := sel.check(); err != nil {
		return nil, err
	}
	if p.Timeout < 0 {
		return nil, errf("invalid: timeout must not be negative")
	}
	failFast := p.FailFast == nil || *p.FailFast
	withFailure := p.WithFailure == nil || *p.WithFailure
	poll := wait.PollOpts{Clock: c.Env.Clock, Timeout: waitBounds(c, p.Timeout), Progress: c.Progress}
	cl := c.Env.Client

	var res *wait.Result
	var err error
	switch {
	case sel.Run > 0 || sel.Number > 0:
		id, resume := sel.Run, fmt.Sprintf("ci_wait run=%d", sel.Run)
		if sel.Number > 0 {
			ref, rerr := shared.ResolveRun(c.Ctx, cl, c.Owner, c.Repo, shared.RunSelector{Number: sel.Number})
			if rerr != nil {
				return nil, rerr
			}
			id, resume = ref.ID, fmt.Sprintf("ci_wait number=%d", sel.Number)
		}
		res, err = wait.WaitRun(c.Ctx, cl, wait.RunOpts{
			Owner: c.Owner, Repo: c.Repo, Run: id,
			FailFast: failFast, WithFailure: withFailure, Poll: poll, Resume: resume,
		})
	default:
		o := wait.TargetOpts{
			Owner: c.Owner, Repo: c.Repo, PR: sel.PR, Ref: sel.Ref,
			FailFast: failFast, WithFailure: withFailure, Poll: poll, Memo: c.Env.Memo,
		}
		switch {
		case sel.SHA != "":
			given := sel.SHA
			full, serr := fullCommit(c, given)
			if serr != nil {
				return nil, serr
			}
			o.SHA = full
			if !strings.EqualFold(full, given) {
				o.Resume = "ci_wait sha=" + given
			}
		case sel.PR > 0 || sel.Ref != "":
		default:
			t, terr := defaultTarget(c.Ctx, c)
			if terr != nil {
				return nil, terr
			}
			o.TargetLabel = t.Label
			if t.PR > 0 {
				o.PR = t.PR
			} else {
				o.Ref = t.Branch
			}
		}
		res, err = wait.WaitTarget(c.Ctx, cl, o)
	}
	if err != nil {
		return nil, err
	}
	return &tools.Result{Text: c.CapTail(res.Text)}, nil
}
