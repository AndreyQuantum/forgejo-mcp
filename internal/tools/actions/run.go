// Package actions implements the `run` tool: Actions runs, jobs, logs,
// workflows, dispatch and artifacts (DESIGN.md sections 8.6 and 10.2). The
// heavy lifting (log reading, run/job renderers, waiting) lives in
// internal/tools/shared/ci; this package is the thin tool layer over it.
package actions

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
)

// stepArg is the `step` param: "N" or "failed". A JSON number is accepted
// too, because models often send step=3 as an integer.
type stepArg string

// UnmarshalJSON accepts a JSON string or number.
func (s *stepArg) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("null")) {
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*s = stepArg(strings.TrimSpace(v))
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return &json.UnmarshalTypeError{Value: string(b), Type: reflect.TypeOf(""), Field: "step"}
	}
	*s = stepArg(n.String())
	return nil
}

// runParams are the parameters of the run tool (section 8.6).
type runParams struct {
	Action     string         `json:"action"`
	Repo       string         `json:"repo,omitempty"`
	Run        int64          `json:"run,omitempty" jsonschema:"global run id"`
	Number     int64          `json:"number,omitempty" jsonschema:"per-repo run number (no.N)"`
	Job        int64          `json:"job,omitempty" jsonschema:"job id"`
	Name       string         `json:"name,omitempty" jsonschema:"job name with run, or artifact name filter"`
	Step       stepArg        `json:"step,omitempty" jsonschema:"step number or failed"`
	Tail       int            `json:"tail,omitempty" jsonschema:"last N log lines, default 80"`
	First      int            `json:"first,omitempty" jsonschema:"first N log lines instead of the tail"`
	Grep       string         `json:"grep,omitempty" jsonschema:"substring filter of log lines"`
	IgnoreCase bool           `json:"ignore_case,omitempty" jsonschema:"case-insensitive grep"`
	Attempt    int            `json:"attempt,omitempty" jsonschema:"job attempt, default latest"`
	MaxChars   int            `json:"max_chars,omitempty" jsonschema:"output size in chars"`
	Offset     *int64         `json:"offset,omitempty" jsonschema:"follow a log from this byte offset"`
	Status     []string       `json:"status,omitempty" jsonschema:"unknown,waiting,running,success,failure,cancelled,skipped,blocked"`
	Workflow   string         `json:"workflow,omitempty" jsonschema:"workflow file name, e.g. ci.yml"`
	Ref        string         `json:"ref,omitempty" jsonschema:"branch, tag or refs/...; default branch for dispatch"`
	SHA        string         `json:"sha,omitempty" jsonschema:"commit sha"`
	PR         int64          `json:"pr,omitempty" jsonschema:"pull request number"`
	Event      string         `json:"event,omitempty" jsonschema:"push, pull_request, workflow_dispatch, schedule"`
	Inputs     map[string]any `json:"inputs,omitempty" jsonschema:"workflow_dispatch inputs; values are stringified"`
	Wait       bool           `json:"wait,omitempty" jsonschema:"wait for the dispatched run to finish"`
	Timeout    int            `json:"timeout,omitempty" jsonschema:"wait seconds"`
	ID         int64          `json:"id,omitempty" jsonschema:"artifact id"`
	SaveTo     string         `json:"save_to,omitempty" jsonschema:"local file path for the download"`
	Include    []string       `json:"include,omitempty" jsonschema:"steps or jobs"`
	Verbose    bool           `json:"verbose,omitempty" jsonschema:"add second-tier fields"`
	Page       int            `json:"page,omitempty"`
	Limit      int            `json:"limit,omitempty"`
}

const (
	pathRuns     = "/repos/{owner}/{repo}/actions/runs"
	pathRun      = "/repos/{owner}/{repo}/actions/runs/{run_id}"
	pathJob      = "/repos/{owner}/{repo}/actions/jobs/{job_id}"
	pathArtifact = "/repos/{owner}/{repo}/actions/artifacts/{artifact_id}"
	actionsNeeds = "write access to Actions"

	defaultRunLimit = 10
)

// runSpec describes the `run` tool.
func runSpec() tools.Spec {
	t := &runTool{}
	runSel := []string{"run", "number"}
	return tools.Spec{
		Name:        "run",
		Toolset:     "actions",
		Description: "Actions runs and jobs: list, get, job, log (tail/grep/step), cancel, rerun, delete, dispatch, workflows, artifacts.",
		Params:      runParams{},
		RepoScoped:  true,
		Actions: []tools.Action{
			tools.NewAction("list", tools.Read, t.list,
				tools.Summary("list runs (newest first); filter by status, workflow, ref/pr, sha, event, number"),
				tools.Params("status", "workflow", "ref", "sha", "pr", "event", "number", "page", "limit"),
				tools.Endpoints("GET "+pathRuns),
			),
			tools.NewAction("get", tools.Read, t.get,
				tools.Summary("run with its jobs; include=steps adds steps of unfinished or failed jobs"),
				tools.Params("run", "number", "include", "verbose"),
				tools.Endpoints("GET "+pathRun, "GET "+pathRun+"/jobs", "GET "+pathJob),
			),
			tools.NewAction("job", tools.Read, t.job,
				tools.Summary("one job with its steps and durations"),
				tools.Params("job"), tools.Required("job"),
				tools.Endpoints("GET "+pathJob),
			),
			tools.NewAction("log", tools.Read, t.log,
				tools.Summary("job log: tail, first, grep or follow (offset); step=failed for the failing step"),
				tools.Params("job", "run", "number", "name", "step", "tail", "first", "grep", "ignore_case", "attempt", "max_chars", "offset"),
				tools.Endpoints("GET "+pathJob+"/logs", "GET "+pathJob, "GET "+pathRun+"/jobs"),
			),
			tools.NewAction("cancel", tools.Write, t.cancel,
				tools.Summary("cancel a run; reports the real status"),
				tools.Params(runSel...), tools.Needs(actionsNeeds),
				tools.Endpoints("POST "+pathRun+"/cancel"),
			),
			tools.NewAction("rerun", tools.Write, t.rerun,
				tools.Summary("rerun every job of a finished run (same run id)"),
				tools.Params(runSel...), tools.Needs(actionsNeeds),
				tools.Endpoints("POST "+pathRun+"/rerun"),
			),
			tools.NewAction("rerun_job", tools.Write, t.rerunJob,
				tools.Summary("rerun one finished job and the jobs that depend on it"),
				tools.Params("job"), tools.Required("job"), tools.Needs(actionsNeeds),
				tools.Endpoints("POST "+pathJob+"/rerun"),
			),
			tools.NewAction("delete", tools.Write, t.delete,
				tools.Summary("delete a finished run"),
				tools.Params(runSel...), tools.Needs("repo admin"),
				tools.Hint500("run still active"),
				tools.Endpoints("DELETE "+pathRun),
			),
			tools.NewAction("dispatch", tools.Write, t.dispatch,
				tools.Summary("start a workflow_dispatch run; wait=true waits for the verdict"),
				tools.Params("workflow", "ref", "inputs", "wait", "timeout"), tools.Required("workflow"),
				tools.Needs(actionsNeeds), tools.Hint500("too many inputs"),
				tools.Endpoints("POST /repos/{owner}/{repo}/actions/workflows/{workflowfilename}/dispatches"),
			),
			tools.NewAction("workflows", tools.Read, t.workflows,
				tools.Summary("workflow files with triggers and dispatch inputs"),
				tools.Params("ref", "verbose"),
			),
			tools.NewAction("artifacts", tools.Read, t.artifacts,
				tools.Summary("artifacts of a run, or of the whole repo"),
				tools.Params("run", "number", "name", "page", "limit"),
				tools.Endpoints("GET /repos/{owner}/{repo}/actions/artifacts", "GET "+pathRun+"/artifacts"),
			),
			tools.NewAction("artifact_download", tools.Read, t.artifactDownload,
				tools.Summary("save an artifact zip to a local file"),
				tools.Params("id", "save_to"), tools.Required("id", "save_to"),
				tools.Endpoints("GET "+pathArtifact+"/zip"),
			),
			tools.NewAction("artifact_delete", tools.Write, t.artifactDelete,
				tools.Summary("delete an artifact"),
				tools.Params("id"), tools.Required("id"), tools.Needs(actionsNeeds),
				tools.Endpoints("DELETE "+pathArtifact),
			),
		},
	}
}

// runTool carries the action methods.
type runTool struct{}
