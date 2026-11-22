package ci

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
)

// DigestOpts configures FailureDigest (section 10.4).
type DigestOpts struct {
	Owner, Repo   string
	Run           int64
	PR            int64
	SHA           string
	Ref           string
	Tail          int // default 30 (ci_wait uses 10)
	MaxJobs       int // default 5
	MaxChars      int // default 12000
	IncludeCancel bool
	TargetLabel   string // first output line when the target was implicit
	DefaultWindow time.Duration
	// NoHeaders skips the run line and the `jobs:` summary of every failed run
	// (callers such as ci_wait print their own verdict and run summary).
	NoHeaders bool
}

// ValidSHA reports a full commit id (40 hex chars, 64 for sha256 repos). The
// head_sha filter of the runs list needs the full id.
func ValidSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

const (
	digestMatches   = 12
	digestMatchLen  = 160
	digestMinBudget = 600
	digestParallel  = 4
)

type digestRun struct {
	run    Run
	jobs   []Job
	failed []Job // failed jobs to detail (at most MaxJobs)
	more   []Job // failed jobs not detailed
}

type jobReport struct {
	job     Job
	step    Step
	hasStep bool
	log     *JobLog
	err     error
	noStart bool
}

// FailureDigest builds the `ci_failure` output of section 10.4. It returns the
// text and whether any failed run was found.
func FailureDigest(ctx context.Context, c *forgejo.Client, o DigestOpts) (text string, failed bool, err error) {
	if o.Tail <= 0 {
		o.Tail = 30
	}
	if o.MaxJobs <= 0 {
		o.MaxJobs = 5
	}
	if o.MaxChars <= 0 {
		o.MaxChars = 12000
	}

	var runs []Run
	sha := o.SHA
	if sha != "" && !ValidSHA(sha) {
		return "", false, errors.New("invalid: sha must be the full commit id (40 hex chars)")
	}
	switch {
	case o.Run > 0:
		r, err := GetRun(ctx, c, o.Owner, o.Repo, o.Run)
		if err != nil {
			return "", false, err
		}
		runs = []Run{*r}
	default:
		if sha == "" {
			if sha, err = resolveDigestSHA(ctx, c, o); err != nil {
				return "", false, err
			}
		}
		all, err := RunsForSHA(ctx, c, o.Owner, o.Repo, sha, 20)
		if err != nil {
			return "", false, err
		}
		runs = NewestPerGroup(all)
	}

	var b format.B
	if o.TargetLabel != "" {
		b.Raw(o.TargetLabel + "\n")
	}

	if len(runs) == 0 {
		return digestExternal(ctx, c, o, sha, &b)
	}

	// classify
	var okNames, pendingNames, cancelledNames []string
	var failedRuns []*digestRun
	for _, r := range runs {
		name := r.WorkflowID
		if name == "" {
			name = fmt.Sprintf("run %d", r.ID)
		}
		if r.Status == "success" || r.Status == "skipped" {
			okNames = append(okNames, name)
			continue
		}
		// A run with a failed and a cancelled job aggregates to `cancelled`,
		// so cancelled runs are always inspected at the job level.
		jobs, err := ListJobs(ctx, c, o.Owner, o.Repo, r.ID)
		if err != nil {
			return "", false, err
		}
		jobFailed := false
		for _, j := range jobs {
			if j.Status == "failure" {
				jobFailed = true
				break
			}
		}
		isFailed := r.Status == "failure" || (r.Status == "cancelled" && o.IncludeCancel) || jobFailed
		if !isFailed {
			if r.Status == "cancelled" {
				cancelledNames = append(cancelledNames, name)
			} else {
				pendingNames = append(pendingNames, name)
			}
			continue
		}
		dr := &digestRun{run: r, jobs: jobs}
		for _, j := range jobs {
			if j.Status == "failure" || (o.IncludeCancel && j.Status == "cancelled") {
				if len(dr.failed) < o.MaxJobs {
					dr.failed = append(dr.failed, j)
				} else {
					dr.more = append(dr.more, j)
				}
			}
		}
		failedRuns = append(failedRuns, dr)
	}

	// fetch the failing step logs of every failed job (parallel)
	nJobs := 0
	for _, dr := range failedRuns {
		nJobs += len(dr.failed)
	}
	budget := o.MaxChars
	if nJobs > 0 {
		budget = max(digestMinBudget, o.MaxChars/nJobs)
	}
	reports := make([][]jobReport, len(failedRuns))
	var g errgroup.Group
	g.SetLimit(digestParallel)
	for i, dr := range failedRuns {
		reports[i] = make([]jobReport, len(dr.failed))
		for k, j := range dr.failed {
			g.Go(func() error {
				reports[i][k] = fetchJobReport(ctx, c, o, j, budget)
				return nil
			})
		}
	}
	_ = g.Wait()
	if ctx.Err() != nil {
		return "", false, ctx.Err()
	}

	seen := map[string]int64{} // tail fingerprint -> first job id
	for i, dr := range failedRuns {
		if !o.NoHeaders {
			b.Raw(RunLine(dr.run) + "\n")
			if s := JobSummary(dr.jobs); s != "" {
				b.Raw("jobs: " + s + "\n")
			}
		}
		for _, rep := range reports[i] {
			renderJobReport(&b, rep, o.Tail, budget, seen)
		}
		if len(dr.more) > 0 {
			names := make([]string, len(dr.more))
			for k, j := range dr.more {
				names[k] = fmt.Sprintf("%s (%d)", j.Name, j.ID)
			}
			b.Raw(fmt.Sprintf("+%d more failed jobs: %s\n", len(dr.more), strings.Join(names, ", ")))
		}
	}
	if len(failedRuns) == 0 {
		b.Raw("no failed run\n")
		if len(runs) == 1 {
			b.Raw(RunLine(runs[0]) + "\n")
		}
	}
	if len(okNames) > 0 {
		b.Raw("ok: " + strings.Join(okNames, ", ") + "\n")
	}
	if len(pendingNames) > 0 {
		b.Raw("pending: " + strings.Join(pendingNames, ", ") + "\n")
	}
	if len(cancelledNames) > 0 {
		b.Raw("cancelled: " + strings.Join(cancelledNames, ", ") + "\n")
	}
	return strings.TrimRight(b.String(), "\n"), len(failedRuns) > 0, nil
}

func resolveDigestSHA(ctx context.Context, c *forgejo.Client, o DigestOpts) (string, error) {
	switch {
	case o.PR > 0:
		var pr struct {
			Head struct {
				SHA string `json:"sha"`
			} `json:"head"`
		}
		p := forgejo.Path("repos", o.Owner, o.Repo, "pulls", strconv.FormatInt(o.PR, 10))
		if _, err := c.GetJSON(ctx, p, nil, &pr); err != nil {
			return "", err
		}
		if pr.Head.SHA == "" {
			return "", fmt.Errorf("pr #%d has no head sha", o.PR)
		}
		return pr.Head.SHA, nil
	case o.Ref != "":
		return ResolveSHA(ctx, c, o.Owner, o.Repo, o.Ref)
	}
	var repo struct {
		DefaultBranch string `json:"default_branch"`
	}
	if _, err := c.GetJSON(ctx, forgejo.Path("repos", o.Owner, o.Repo), nil, &repo); err != nil {
		return "", err
	}
	if repo.DefaultBranch == "" {
		return "", errors.New("invalid: no run, pr, ref or sha given and the repo has no default branch")
	}
	return ResolveSHA(ctx, c, o.Owner, o.Repo, repo.DefaultBranch)
}

// digestExternal is the fallback of step 5: no Actions run for the target.
func digestExternal(ctx context.Context, c *forgejo.Client, o DigestOpts, sha string, b *format.B) (string, bool, error) {
	cs, err := CombinedStatus(ctx, c, o.Owner, o.Repo, sha)
	if err != nil {
		return "", false, err
	}
	head := "no Actions run for " + digestTarget(o, sha) + "; latest: "
	if latest := latestRun(ctx, c, o); latest != nil {
		head += RunLine(*latest)
	} else {
		head += "none"
	}
	b.Raw(head + "\n")
	if len(cs.Failing) == 0 {
		b.Raw("no failing checks\n")
		if len(cs.Pending) > 0 {
			b.Raw("pending: " + strings.Join(cs.Pending, ", ") + "\n")
		}
		return strings.TrimRight(b.String(), "\n"), false, nil
	}
	b.Raw("external checks failing:\n")
	for _, f := range cs.Failing {
		b.Line("  "+f.State+" "+f.Context, format.OneLine(f.Description, 120), f.TargetURL)
	}
	return strings.TrimRight(b.String(), "\n"), true, nil
}

func digestTarget(o DigestOpts, sha string) string {
	switch {
	case o.PR > 0:
		return fmt.Sprintf("pr #%d (%s)", o.PR, format.SHA(sha, 8))
	case o.Ref != "":
		return fmt.Sprintf("ref %s (%s)", o.Ref, format.SHA(sha, 8))
	}
	return "sha " + format.SHA(sha, 8)
}

// latestRun is the newest run of the repository (one limit=1 query); nil when
// there is none or it cannot be read.
func latestRun(ctx context.Context, c *forgejo.Client, o DigestOpts) *Run {
	var body struct {
		WorkflowRuns []Run `json:"workflow_runs"`
	}
	if _, err := c.List(ctx, runsPath(o.Owner, o.Repo), nil, 1, 1, &body); err != nil || len(body.WorkflowRuns) == 0 {
		return nil
	}
	return &body.WorkflowRuns[0]
}

func fetchJobReport(ctx context.Context, c *forgejo.Client, o DigestOpts, listed Job, budget int) jobReport {
	rep := jobReport{job: listed}
	job, err := GetJob(ctx, c, o.Owner, o.Repo, listed.ID)
	if err != nil {
		rep.err = err
		return rep
	}
	rep.job = *job
	if job.TaskID == 0 && listed.TaskID == 0 && len(job.Steps) == 0 {
		rep.noStart = true
		return rep
	}
	step, ok := failingStep(job)
	rep.step, rep.hasStep = step, ok
	spec := ""
	if ok {
		spec = strconv.Itoa(step.Number)
	}
	log, err := FetchJobLog(ctx, c, LogOpts{
		Owner: o.Owner, Repo: o.Repo, Job: job.ID, Known: job,
		Step: spec, Tail: o.Tail, MaxChars: budget,
	})
	rep.log, rep.err = log, err
	return rep
}

// failingStep is the first step with status failure, else the last step whose
// status is not success (section 10.4 step 3).
func failingStep(job *Job) (Step, bool) {
	for _, s := range job.Steps {
		if s.Status == "failure" {
			return s, true
		}
	}
	for i := len(job.Steps) - 1; i >= 0; i-- {
		if job.Steps[i].Status != "success" {
			return job.Steps[i], true
		}
	}
	return Step{}, false
}

func renderJobReport(b *format.B, rep jobReport, tail, budget int, seen map[string]int64) {
	head := fmt.Sprintf("failed job %d %q", rep.job.ID, rep.job.Name)
	if rep.hasStep {
		head += fmt.Sprintf(" step %d %q", rep.step.Number, rep.step.Name)
		if d, ok := StepDuration(rep.step); ok {
			head += " (" + format.Dur(d) + ")"
		}
	}
	b.Raw(head + "\n")
	switch {
	case rep.noStart:
		b.Raw("  no log (job not started)\n")
		return
	case rep.err != nil:
		b.Raw("  log unavailable: " + format.OneLine(strings.TrimPrefix(rep.err.Error(), "error: "), 200) + "\n")
		return
	case rep.log == nil:
		return
	case rep.log.NoLog != "":
		b.Raw("  " + rep.log.NoLog + "\n")
		return
	}

	var matches []string
	for _, m := range format.ErrorMatches(rep.log.Numbered, digestMatches) {
		matches = append(matches, fmt.Sprintf("%d: %s", m.N, format.OneLine(m.Text, digestMatchLen)))
	}
	tailLines := format.TailLines(rep.log.Numbered, tail)
	texts := make([]string, len(tailLines))
	for i, l := range tailLines {
		texts[i] = l.Text
	}
	tailText := strings.Join(texts, "\n")
	key := strings.Join(matches, "\n") + "\x00" + tailText
	if tailText != "" || len(matches) > 0 {
		if first, ok := seen[key]; ok {
			b.Raw(fmt.Sprintf("  same as job %d\n", first))
			return
		}
		seen[key] = rep.job.ID
	}
	matchLine := ""
	if len(matches) > 0 {
		matchLine = "  matches: " + strings.Join(matches, " | ")
		b.Raw(matchLine + "\n")
	}
	if tailText == "" {
		b.Raw("  (empty log)\n")
		return
	}
	b.Raw(fmt.Sprintf("  --- tail (last %d lines) ---\n", len(tailLines)))
	room := max(200, budget-len(matchLine)-120)
	tailText = format.CapTail(tailText, room)
	b.Raw(indent(tailText, "  ") + "\n")
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
