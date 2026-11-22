package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared/ci"
)

var runStatuses = []string{"unknown", "waiting", "running", "success", "failure", "cancelled", "skipped", "blocked"}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func runsPath(c *tools.Call, more ...string) string {
	parts := append([]string{"repos", c.Owner, c.Repo, "actions", "runs"}, more...)
	return forgejo.Path(parts...)
}

func jobPath(c *tools.Call, job int64, more ...string) string {
	parts := append([]string{"repos", c.Owner, c.Repo, "actions", "jobs", itoa(job)}, more...)
	return forgejo.Path(parts...)
}

// runDoc is a run plus the fields ci.Run does not project.
type runDoc struct {
	ci.Run
	URL          string
	TriggerEvent string
	Updated      string
}

// decodeRun decodes an ActionRun twice: ci.Run has a custom UnmarshalJSON, so
// the extra fields come from a second pass. Forgejo 17 leaves `event` empty on
// workflow_dispatch runs; `trigger_event` then carries the real value.
func decodeRun(b []byte) (*runDoc, error) {
	d := &runDoc{}
	if err := json.Unmarshal(b, &d.Run); err != nil {
		return nil, err
	}
	var extra struct {
		HTMLURL      string `json:"html_url"`
		TriggerEvent string `json:"trigger_event"`
		Updated      string `json:"updated"`
	}
	if err := json.Unmarshal(b, &extra); err == nil {
		d.URL, d.TriggerEvent, d.Updated = extra.HTMLURL, extra.TriggerEvent, extra.Updated
	}
	if d.Event == "" {
		d.Event = d.TriggerEvent
	}
	return d, nil
}

// getRun reads one run (a single request).
func getRun(ctx context.Context, c *tools.Call, id int64) (*runDoc, error) {
	path := runsPath(c, itoa(id))
	resp, err := c.Env.Client.Do(ctx, forgejo.Request{Method: http.MethodGet, Path: path})
	if err != nil {
		return nil, err
	}
	d, err := decodeRun(resp.Body)
	if err != nil {
		return nil, &forgejo.APIError{Message: "invalid JSON in response: " + err.Error(), Method: http.MethodGet, Path: path}
	}
	if d.ID == 0 {
		d.ID = id
	}
	return d, nil
}

// pickRun resolves `run` or `number` to a run read from the server.
func pickRun(c *tools.Call, p *runParams) (*runDoc, error) {
	switch {
	case p.Run != 0 && p.Number != 0:
		return nil, errors.New("invalid: give only one of run, number")
	case p.Run != 0:
		return getRun(c.Ctx, c, p.Run)
	case p.Number != 0:
		ref, err := shared.ResolveRun(c.Ctx, c.Env.Client, c.Owner, c.Repo, shared.RunSelector{Number: p.Number})
		if err != nil {
			return nil, err
		}
		return getRun(c.Ctx, c, ref.ID)
	}
	return nil, errors.New("invalid: pass run (id) or number (no.N)")
}

// ---- list ----------------------------------------------------------------

func (t *runTool) list(c *tools.Call, p *runParams) (*tools.Result, error) {
	q := url.Values{}
	for _, s := range p.Status {
		s = strings.ToLower(strings.TrimSpace(s))
		if !slices.Contains(runStatuses, s) {
			return c.Fail("invalid: status %q; valid: %s", s, strings.Join(runStatuses, ",")), nil
		}
		q.Add("status", s)
	}
	if p.Workflow != "" {
		q.Set("workflow_id", p.Workflow)
	}
	if p.Event != "" {
		q.Set("event", p.Event)
	}
	if p.Number != 0 {
		q.Set("run_number", itoa(p.Number))
	}
	if p.Ref != "" && p.PR != 0 {
		return c.Fail("invalid: give only one of ref, pr"), nil
	}
	bareTag := ""
	switch {
	case p.PR != 0:
		q.Set("ref", shared.PullRef(p.PR))
	case p.Ref != "":
		full, err := shared.FullRunRef(p.Ref)
		if err != nil {
			return c.Fail("%s", err.Error()), nil
		}
		q.Set("ref", full)
		if full == "refs/heads/"+strings.TrimSpace(p.Ref) {
			bareTag = "refs/tags/" + strings.TrimSpace(p.Ref)
		}
	}
	if p.SHA != "" {
		sha, err := shared.ExpandSHA(c.Ctx, c.Env.Client, c.Owner, c.Repo, p.SHA)
		if err != nil {
			return nil, err
		}
		q.Set("head_sha", sha)
	}
	limit := p.Limit
	if limit <= 0 {
		limit = defaultRunLimit
	}
	fetch := func() ([]ci.Run, forgejo.Page, error) {
		var body struct {
			Runs []json.RawMessage `json:"workflow_runs"`
		}
		pg, err := c.Env.Client.List(c.Ctx, runsPath(c), q, p.Page, limit, &body)
		if err != nil {
			return nil, pg, err
		}
		runs := make([]ci.Run, 0, len(body.Runs))
		for _, raw := range body.Runs {
			d, err := decodeRun(raw)
			if err != nil {
				return nil, pg, &forgejo.APIError{Message: "invalid JSON in response: " + err.Error(), Method: http.MethodGet, Path: runsPath(c)}
			}
			runs = append(runs, d.Run)
		}
		return runs, pg, nil
	}
	runs, pg, err := fetch()
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 && bareTag != "" && p.Page <= 1 {
		// a bare name with no runs as a branch is retried once as a tag
		q.Set("ref", bareTag)
		if runs, pg, err = fetch(); err != nil {
			return nil, err
		}
	}
	var b format.B
	b.Raw(format.ListHeader("runs", pg) + "\n")
	for _, r := range runs {
		b.Raw(ci.RunLine(r) + "\n")
	}
	return &tools.Result{Text: strings.TrimRight(b.String(), "\n")}, nil
}

// ---- get -----------------------------------------------------------------

func (t *runTool) get(c *tools.Call, p *runParams) (*tools.Result, error) {
	steps := false
	for _, inc := range p.Include {
		switch strings.ToLower(strings.TrimSpace(inc)) {
		case "steps":
			steps = true
		case "jobs":
		default:
			return c.Fail("invalid: include %q; valid: steps,jobs", inc), nil
		}
	}
	d, err := pickRun(c, p)
	if err != nil {
		return nil, err
	}
	jobs, err := ci.ListJobs(c.Ctx, c.Env.Client, c.Owner, c.Repo, d.ID)
	if err != nil {
		return nil, err
	}
	detail := map[int64]*ci.Job{}
	if steps {
		detail, err = jobDetails(c, jobs)
		if err != nil {
			return nil, err
		}
	}
	var b format.B
	b.Raw(runBlock(d, p.Verbose))
	for _, j := range jobs {
		b.Raw(ci.JobLine(j) + "\n")
		if full := detail[j.ID]; full != nil {
			for _, s := range full.Steps {
				b.Raw(ci.StepLine(s) + "\n")
			}
		}
	}
	return &tools.Result{Text: strings.TrimRight(b.String(), "\n")}, nil
}

// jobDetails reads the steps of every job that is not successful and has a
// task (a job that never started has no steps), four at a time.
func jobDetails(c *tools.Call, jobs []ci.Job) (map[int64]*ci.Job, error) {
	out := map[int64]*ci.Job{}
	var mu sync.Mutex
	g, ctx := errgroup.WithContext(c.Ctx)
	g.SetLimit(4)
	for _, j := range jobs {
		if j.Status == "success" || j.TaskID == 0 {
			continue
		}
		g.Go(func() error {
			full, err := ci.GetJob(ctx, c.Env.Client, c.Owner, c.Repo, j.ID)
			if err != nil {
				return err
			}
			mu.Lock()
			out[j.ID] = full
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// runBlock renders the header lines of a run (section 8.6 example).
func runBlock(d *runDoc, verbose bool) string {
	var b format.B
	head := fmt.Sprintf("run %d", d.ID)
	if d.Index > 0 {
		head += fmt.Sprintf(" no.%d", d.Index)
	}
	if d.Status != "" {
		head += " " + d.Status
	}
	wf := d.WorkflowID
	if d.Title != "" {
		wf = strings.TrimSpace(wf + " " + strconv.Quote(format.OneLine(d.Title, 120)))
	}
	b.Line(head, wf)
	trig := strings.TrimSpace(strings.Join(nonEmpty(d.Event, d.PrettyRef, format.SHA(d.CommitSHA, 8)), " "))
	if d.TriggerUser != "" {
		trig += " by " + d.TriggerUser
	}
	dur := ""
	if d.Duration > 0 {
		dur = format.Dur(time.Duration(d.Duration))
	}
	flags := ""
	switch {
	case d.NeedApproval:
		flags = "approval-needed"
	case d.IsForkPR:
		flags = "fork-pr"
	}
	b.Line(strings.TrimSpace(trig), format.T(d.Created), dur, flags)
	b.KV("url", d.URL)
	if verbose {
		b.KV("sha", d.CommitSHA)
		b.KV("started", format.T(d.Started))
		b.KV("stopped", format.T(d.Stopped))
		b.KV("updated", format.T(d.Updated))
		if d.TriggerEvent != "" && d.TriggerEvent != d.Event {
			b.KV("trigger_event", d.TriggerEvent)
		}
	}
	return b.String()
}

// ---- job -----------------------------------------------------------------

func (t *runTool) job(c *tools.Call, p *runParams) (*tools.Result, error) {
	j, err := ci.GetJob(c.Ctx, c.Env.Client, c.Owner, c.Repo, p.Job)
	if err != nil {
		return nil, err
	}
	var b format.B
	b.Raw(ci.JobLine(*j) + "\n")
	if j.RunID != 0 {
		b.Raw(fmt.Sprintf("run: %d\n", j.RunID))
	}
	for _, s := range j.Steps {
		b.Raw(ci.StepLine(s) + "\n")
	}
	if len(j.Steps) == 0 && j.TaskID == 0 {
		b.Raw("(no steps: job not started)\n")
	}
	return &tools.Result{Text: strings.TrimRight(b.String(), "\n")}, nil
}

// ---- log -----------------------------------------------------------------

func (t *runTool) log(c *tools.Call, p *runParams) (*tools.Result, error) {
	o := ci.LogOpts{
		Owner: c.Owner, Repo: c.Repo,
		Job: p.Job, Name: p.Name, Step: string(p.Step),
		Attempt: p.Attempt, Tail: p.Tail, First: p.First,
		Grep: p.Grep, IgnoreCase: p.IgnoreCase, Offset: p.Offset,
		MaxChars: c.Chars(p.MaxChars, 8000),
	}
	if st := strings.ToLower(o.Step); st != "" && st != "failed" {
		if n, err := strconv.Atoi(st); err != nil || n < 0 {
			return c.Fail("invalid: step %q; use a step number or failed", o.Step), nil
		}
		o.Step = st
	} else {
		o.Step = st
	}
	if p.Job == 0 {
		if p.Name == "" || (p.Run == 0 && p.Number == 0) {
			return c.Fail("invalid: pass job, or run (or number) with name"), nil
		}
		if p.Run != 0 && p.Number != 0 {
			return c.Fail("invalid: give only one of run, number"), nil
		}
		o.Run = p.Run
		if o.Run == 0 {
			ref, err := shared.ResolveRun(c.Ctx, c.Env.Client, c.Owner, c.Repo, shared.RunSelector{Number: p.Number})
			if err != nil {
				return nil, err
			}
			o.Run = ref.ID
		}
	}
	res, err := ci.FetchJobLog(c.Ctx, c.Env.Client, o)
	if err != nil {
		return nil, err
	}
	return &tools.Result{Text: res.Render()}, nil
}

// ---- cancel / rerun / rerun_job / delete ---------------------------------

func (t *runTool) cancel(c *tools.Call, p *runParams) (*tools.Result, error) {
	d, err := pickRun(c, p)
	if err != nil {
		return nil, err
	}
	if ci.IsDone(d.Status) {
		return &tools.Result{Text: noOpCancel(d.ID, d.Status)}, nil
	}
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodPost, Path: runsPath(c, itoa(d.ID), "cancel")}); err != nil {
		return nil, err
	}
	now, err := getRun(c.Ctx, c, d.ID)
	if err != nil {
		return nil, err
	}
	switch {
	case now.Status == "cancelled":
		return &tools.Result{Text: fmt.Sprintf("ok: run %d cancelled", d.ID)}, nil
	case ci.IsDone(now.Status):
		return &tools.Result{Text: noOpCancel(d.ID, now.Status)}, nil
	}
	return &tools.Result{Text: fmt.Sprintf("ok: cancel requested for run %d (status now %s)", d.ID, now.Status)}, nil
}

func noOpCancel(id int64, status string) string {
	return fmt.Sprintf("ok: run %d already %s (cancel was a no-op)", id, status)
}

func (t *runTool) rerun(c *tools.Call, p *runParams) (*tools.Result, error) {
	d, err := pickRun(c, p)
	if err != nil {
		return nil, err
	}
	if !ci.IsDone(d.Status) {
		return c.Fail("run %d is %s; wait for it to finish or cancel it first", d.ID, d.Status), nil
	}
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodPost, Path: runsPath(c, itoa(d.ID), "rerun")}); err != nil {
		return nil, err
	}
	now, err := getRun(c.Ctx, c, d.ID)
	if err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: rerun started: run %d %s", d.ID, now.Status)}, nil
}

func (t *runTool) rerunJob(c *tools.Call, p *runParams) (*tools.Result, error) {
	j, err := ci.GetJob(c.Ctx, c.Env.Client, c.Owner, c.Repo, p.Job)
	if err != nil {
		return nil, err
	}
	if !ci.IsDone(j.Status) {
		return c.Fail("job %d is %s; only finished jobs can be rerun", j.ID, j.Status), nil
	}
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodPost, Path: jobPath(c, p.Job, "rerun")}); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: rerun started: job %d %s (and the jobs that need it)", j.ID, strconv.Quote(j.Name))}, nil
}

func (t *runTool) delete(c *tools.Call, p *runParams) (*tools.Result, error) {
	d, err := pickRun(c, p)
	if err != nil {
		return nil, err
	}
	if !ci.IsDone(d.Status) {
		// the server maps this to a 500
		return c.Fail("run %d is %s; only finished runs can be deleted (cancel it first)", d.ID, d.Status), nil
	}
	if _, err := c.Env.Client.Do(c.Ctx, forgejo.Request{Method: http.MethodDelete, Path: runsPath(c, itoa(d.ID))}); err != nil {
		return nil, err
	}
	return &tools.Result{Text: fmt.Sprintf("ok: deleted run %d", d.ID)}, nil
}

func nonEmpty(xs ...string) []string {
	var out []string
	for _, x := range xs {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}
