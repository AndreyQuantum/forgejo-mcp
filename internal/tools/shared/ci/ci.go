// Package ci holds the CI plumbing shared by the pr, checks, run, ci_failure
// and ci_wait tools (DESIGN.md sections 10.1-10.4). It never imports package
// tools.
package ci

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
)

// Run is the projection of an Actions run (section 6.9).
type Run struct {
	ID           int64  `json:"id"`
	Index        int64  `json:"index_in_repo"`
	WorkflowID   string `json:"workflow_id"`
	Title        string `json:"title"`
	Status       string `json:"status"`
	Event        string `json:"event"`
	PrettyRef    string `json:"prettyref"`
	CommitSHA    string `json:"commit_sha"`
	TriggerUser  string `json:"-"` // trigger_user.login, filled by UnmarshalJSON
	Started      string `json:"started"`
	Stopped      string `json:"stopped"`
	Duration     int64  `json:"duration"` // nanoseconds
	Created      string `json:"created"`
	NeedApproval bool   `json:"need_approval"`
	IsForkPR     bool   `json:"is_fork_pull_request"`
}

// UnmarshalJSON decodes the projected fields and `trigger_user.login`.
func (r *Run) UnmarshalJSON(b []byte) error {
	type plain Run
	aux := struct {
		*plain
		TriggerUser *struct {
			Login string `json:"login"`
		} `json:"trigger_user"`
	}{plain: (*plain)(r)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	if aux.TriggerUser != nil {
		r.TriggerUser = aux.TriggerUser.Login
	}
	return nil
}

// Job is the projection of an Actions job.
type Job struct {
	ID      int64    `json:"id"`
	RunID   int64    `json:"run_id"`
	Name    string   `json:"name"`
	Status  string   `json:"status"`
	Attempt int      `json:"attempt"`
	Needs   []string `json:"needs"`
	RunsOn  []string `json:"runs_on"`
	TaskID  int64    `json:"task_id"`
	Steps   []Step   `json:"steps"`
}

// Step is the projection of a job step.
type Step struct {
	Number  int    `json:"number"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Started string `json:"started"`
	Stopped string `json:"stopped"`
}

// IsDone reports a final run/job/step status.
func IsDone(status string) bool {
	switch status {
	case "success", "failure", "cancelled", "skipped":
		return true
	}
	return false
}

// IsUnfinished reports a status that can still change (waiting, running,
// blocked, unknown).
func IsUnfinished(status string) bool { return !IsDone(status) }

// RunLine renders `run 1234 no.56 failure | ci.yml | push main 8a1b2c3d | by alice | 1m03s | T`.
func RunLine(r Run) string {
	head := fmt.Sprintf("run %d", r.ID)
	if r.Index > 0 {
		head += fmt.Sprintf(" no.%d", r.Index)
	}
	if r.Status != "" {
		head += " " + r.Status
	}
	var trig []string
	for _, p := range []string{r.Event, r.PrettyRef, format.SHA(r.CommitSHA, 8)} {
		if p != "" {
			trig = append(trig, p)
		}
	}
	by := ""
	if r.TriggerUser != "" {
		by = "by " + r.TriggerUser
	}
	dur := ""
	if r.Duration > 0 {
		dur = format.Dur(time.Duration(r.Duration))
	}
	approval := ""
	if r.NeedApproval {
		approval = "approval-needed"
	}
	var b format.B
	b.Line(head, r.WorkflowID, strings.Join(trig, " "), by, dur, format.T(r.Created), approval)
	return strings.TrimRight(b.String(), "\n")
}

// JobLine renders `job 5678 build failure a1 needs:lint on:docker`.
func JobLine(j Job) string {
	s := fmt.Sprintf("job %d %s %s", j.ID, j.Name, j.Status)
	if j.Attempt > 0 {
		s += fmt.Sprintf(" a%d", j.Attempt)
	}
	if len(j.Needs) > 0 {
		s += " needs:" + strings.Join(j.Needs, ",")
	}
	if len(j.RunsOn) > 0 {
		s += " on:" + strings.Join(j.RunsOn, ",")
	}
	return s
}

// StepLine renders `  step 3 Run tests failure 12s`.
func StepLine(s Step) string {
	out := fmt.Sprintf("  step %d %s %s", s.Number, s.Name, s.Status)
	if d, ok := StepDuration(s); ok {
		out += " " + format.Dur(d)
	}
	return out
}

// StepDuration returns stopped-started when both timestamps parse.
func StepDuration(s Step) (time.Duration, bool) {
	if s.Started == "" || s.Stopped == "" {
		return 0, false
	}
	a, err1 := time.Parse(time.RFC3339, s.Started)
	b, err2 := time.Parse(time.RFC3339, s.Stopped)
	if err1 != nil || err2 != nil || b.Before(a) {
		return 0, false
	}
	return b.Sub(a), true
}

// JobSummary renders `1 failed (build), 1 ok (lint), 1 skipped (test)`.
func JobSummary(jobs []Job) string {
	order := []string{"failed", "running", "waiting", "blocked", "cancelled", "ok", "skipped", "unknown"}
	names := map[string][]string{}
	for _, j := range jobs {
		k := jobBucket(j.Status)
		names[k] = append(names[k], j.Name)
	}
	var parts []string
	for _, k := range order {
		if n := names[k]; len(n) > 0 {
			parts = append(parts, fmt.Sprintf("%d %s (%s)", len(n), k, strings.Join(n, ", ")))
		}
	}
	return strings.Join(parts, ", ")
}

func jobBucket(status string) string {
	switch status {
	case "failure":
		return "failed"
	case "success":
		return "ok"
	case "running", "waiting", "blocked", "cancelled", "skipped":
		return status
	}
	return "unknown"
}

// runsPath is `/repos/{o}/{r}/actions/runs`.
func runsPath(owner, repo string, more ...string) string {
	parts := append([]string{"repos", owner, repo, "actions", "runs"}, more...)
	return forgejo.Path(parts...)
}

// GetRun reads `GET R/actions/runs/{id}`.
func GetRun(ctx context.Context, c *forgejo.Client, owner, repo string, id int64) (*Run, error) {
	var r Run
	if _, err := c.GetJSON(ctx, runsPath(owner, repo, strconv.FormatInt(id, 10)), nil, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// ListJobs reads `GET R/actions/runs/{id}/jobs` (an unpaginated array; a
// {"jobs":[...]} wrapper is accepted too).
func ListJobs(ctx context.Context, c *forgejo.Client, owner, repo string, run int64) ([]Job, error) {
	path := runsPath(owner, repo, strconv.FormatInt(run, 10), "jobs")
	resp, err := c.Do(ctx, forgejo.Request{Method: "GET", Path: path})
	if err != nil {
		return nil, err
	}
	body := strings.TrimSpace(string(resp.Body))
	var jobs []Job
	switch {
	case body == "":
	case strings.HasPrefix(body, "["):
		err = json.Unmarshal(resp.Body, &jobs)
	default:
		var w struct {
			Jobs []Job `json:"jobs"`
		}
		err = json.Unmarshal(resp.Body, &w)
		jobs = w.Jobs
	}
	if err != nil {
		return nil, &forgejo.APIError{Message: "invalid JSON in response: " + err.Error(), Method: "GET", Path: path}
	}
	sort.SliceStable(jobs, func(i, j int) bool { return jobs[i].ID < jobs[j].ID })
	return jobs, nil
}

// GetJob reads `GET R/actions/jobs/{id}` (with steps).
func GetJob(ctx context.Context, c *forgejo.Client, owner, repo string, id int64) (*Job, error) {
	var j Job
	p := forgejo.Path("repos", owner, repo, "actions", "jobs", strconv.FormatInt(id, 10))
	if _, err := c.GetJSON(ctx, p, nil, &j); err != nil {
		return nil, err
	}
	return &j, nil
}

// RunsForSHA reads `GET R/actions/runs?head_sha=<sha>&limit=<n>` (no status
// filter, see section 10.4 step 1), newest first as the server returns them.
func RunsForSHA(ctx context.Context, c *forgejo.Client, owner, repo, sha string, limit int) ([]Run, error) {
	var body struct {
		WorkflowRuns []Run `json:"workflow_runs"`
	}
	q := url.Values{"head_sha": {sha}}
	if _, err := c.List(ctx, runsPath(owner, repo), q, 1, limit, &body); err != nil {
		return nil, err
	}
	return body.WorkflowRuns, nil
}

// NewestPerGroup keeps the newest run (highest id) of every `workflow_id|event`
// group, ordered by id ascending.
func NewestPerGroup(runs []Run) []Run {
	best := map[string]Run{}
	for _, r := range runs {
		k := r.WorkflowID + "|" + r.Event
		if cur, ok := best[k]; !ok || r.ID > cur.ID {
			best[k] = r
		}
	}
	out := make([]Run, 0, len(best))
	for _, r := range best {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ResolveSHA resolves a branch, tag or sha to a commit sha through the
// commit list (`GET R/commits?sha=<ref>&limit=1`).
func ResolveSHA(ctx context.Context, c *forgejo.Client, owner, repo, ref string) (string, error) {
	var commits []struct {
		SHA string `json:"sha"`
	}
	q := url.Values{"sha": {ref}, "stat": {"false"}, "files": {"false"}, "verification": {"false"}, "limit": {"1"}, "page": {"1"}}
	if _, err := c.GetJSON(ctx, forgejo.Path("repos", owner, repo, "commits"), q, &commits); err != nil {
		return "", err
	}
	if len(commits) == 0 || commits[0].SHA == "" {
		return "", &forgejo.APIError{Status: 404, Message: fmt.Sprintf("no commit found for ref %q", ref), Method: "GET", Path: "/repos/" + owner + "/" + repo + "/commits"}
	}
	return commits[0].SHA, nil
}
