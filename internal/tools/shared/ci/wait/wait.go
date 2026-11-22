// Package wait implements the server-side polling of section 10.1 (ci_wait
// and run dispatch wait=true). All sleeping goes through clock.Clock.
package wait

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/clock"
	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
	"github.com/AndreyQuantum/forgejo-mcp/internal/format"
	"github.com/AndreyQuantum/forgejo-mcp/internal/tools/shared/ci"
)

// Verdict values.
const (
	VerdictSuccess   = "success"
	VerdictFailure   = "failure"
	VerdictCancelled = "cancelled"
	VerdictSkipped   = "skipped"
	VerdictTimeout   = "timeout"
	VerdictGreen     = "green"
	VerdictFailed    = "failed"
	VerdictPending   = "pending"
	VerdictNoChecks  = "no_checks"
	VerdictMerged    = "merged"
	VerdictClosed    = "closed"
)

const (
	noChecksGrace   = 20 * time.Second
	autoMergeFollow = 30 * time.Second
	failureTail     = 10
	failureChars    = 8000
)

// MemoReader is the part of tools.Memo the waiter needs (shared packages
// never import package tools).
type MemoReader interface{ Has(key string) bool }

// Result is the outcome of WaitRun or WaitTarget.
type Result struct {
	Verdict string
	Waited  time.Duration
	Text    string // rendered verdict block (section 10.1 output)
}

func clockOf(p PollOpts) clock.Clock {
	if p.Clock != nil {
		return p.Clock
	}
	return clock.Real()
}

// RunOpts configures WaitRun.
type RunOpts struct {
	Owner, Repo string
	Run         int64
	FailFast    bool
	WithFailure bool
	Poll        PollOpts
	Resume      string // original selector for the `resume:` line, e.g. "ci_wait run=1236"
}

// WaitRun waits for one run (run mode of section 10.1).
func WaitRun(ctx context.Context, c *forgejo.Client, o RunOpts) (*Result, error) {
	clk := clockOf(o.Poll)
	o.Poll.Clock = clk
	start := clk.Now()
	// everything after the poll (hint, digest) stays inside the same
	// real-time bound
	pctx, cancel := Bound(ctx, o.Poll)
	defer cancel()

	var run *ci.Run
	var jobs []ci.Job
	step := func(ctx context.Context) (bool, Snapshot, error) {
		r, err := ci.GetRun(ctx, c, o.Owner, o.Repo, o.Run)
		if err != nil {
			return false, Snapshot{}, err
		}
		j, err := ci.ListJobs(ctx, c, o.Owner, o.Repo, o.Run)
		if err != nil {
			return false, Snapshot{}, err
		}
		run, jobs = r, j
		done := ci.IsDone(r.Status) || (o.FailFast && anyFailed(j))
		return done, runSnapshot(r, j), nil
	}
	_, timedOut, err := Poll(ctx, step, o.Poll)
	if err != nil {
		return nil, err
	}
	waited := clk.Now().Sub(start)
	if run == nil {
		if !timedOut {
			return nil, errors.New("run not read")
		}
		resume := o.Resume
		if resume == "" {
			resume = fmt.Sprintf("ci_wait run=%d", o.Run)
		}
		text := fmt.Sprintf("verdict: %s | waited %s | run %d (not readable in time)\nresume: %s", VerdictTimeout, format.Dur(waited), o.Run, resume)
		return &Result{Verdict: VerdictTimeout, Waited: waited, Text: text}, nil
	}

	verdict := VerdictTimeout
	switch {
	case timedOut:
	case ci.IsDone(run.Status):
		verdict = run.Status
	default:
		verdict = VerdictFailure // fail-fast on a failed job of a still running run
	}

	var b format.B
	subject := fmt.Sprintf("run %d no.%d", run.ID, run.Index)
	if verdict == VerdictTimeout {
		subject += " " + run.Status + " (" + jobDetail(jobs) + ")"
	} else {
		subject += " (" + joinNonEmpty(", ", run.WorkflowID, strings.TrimSpace(run.Event+" "+run.PrettyRef)) + ")"
	}
	b.Line("verdict: "+verdict, "waited "+format.Dur(waited), subject)
	if s := ci.JobSummary(jobs); s != "" {
		b.Raw("jobs: " + s + "\n")
	}
	if verdict == VerdictFailure && o.WithFailure {
		if pctx.Err() != nil {
			b.Raw("digest unavailable: time budget exhausted\n")
		} else {
			text, _, derr := ci.FailureDigest(pctx, c, ci.DigestOpts{Owner: o.Owner, Repo: o.Repo, Run: run.ID, Tail: failureTail, MaxChars: failureChars, NoHeaders: true})
			if derr != nil {
				b.Raw("digest unavailable: " + format.OneLine(strings.TrimPrefix(derr.Error(), "error: "), 200) + "\n")
			} else {
				b.Raw(text + "\n")
			}
		}
	}
	if verdict == VerdictTimeout {
		resume := o.Resume
		if resume == "" {
			resume = fmt.Sprintf("ci_wait run=%d", run.ID)
		}
		b.Raw("resume: " + resume + "\n")
		if pctx.Err() == nil {
			if h := stuckHint(pctx, c, o.Owner, o.Repo, []ci.Run{*run}, jobs); h != "" {
				b.Raw("hint: " + h + "\n")
			}
		}
	}
	return &Result{Verdict: verdict, Waited: waited, Text: strings.TrimRight(b.String(), "\n")}, nil
}

func anyFailed(jobs []ci.Job) bool {
	for _, j := range jobs {
		if j.Status == "failure" {
			return true
		}
	}
	return false
}

func runSnapshot(r *ci.Run, jobs []ci.Job) Snapshot {
	done := 0
	var keys []string
	for _, j := range jobs {
		if ci.IsDone(j.Status) {
			done++
		}
		keys = append(keys, j.Name+"="+j.Status)
	}
	return Snapshot{
		Summary: fmt.Sprintf("run %d %s: %d/%d jobs done (%s)", r.ID, r.Status, done, len(jobs), jobDetail(jobs)),
		Key:     r.Status + "|" + strings.Join(keys, ","),
	}
}

// jobDetail renders `build ok, test running`.
func jobDetail(jobs []ci.Job) string {
	parts := make([]string, len(jobs))
	for i, j := range jobs {
		s := j.Status
		switch s {
		case "success":
			s = "ok"
		case "failure":
			s = "failed"
		}
		parts[i] = j.Name + " " + s
	}
	return strings.Join(parts, ", ")
}

func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

// TargetOpts configures WaitTarget.
type TargetOpts struct {
	Owner, Repo string
	PR          int64
	Ref         string
	SHA         string
	FailFast    bool
	WithFailure bool
	Poll        PollOpts
	Memo        MemoReader
	Resume      string
	TargetLabel string
}

type prInfo struct {
	State          string `json:"state"`
	Merged         bool   `json:"merged"`
	Draft          bool   `json:"draft"`
	Mergeable      bool   `json:"mergeable"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	Head           struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// targetState is what the poll step learned; it is read after Poll returns.
type targetState struct {
	verdict     string
	sha         string
	firstSHA    string
	movedTo     string
	pr          *prInfo
	combined    *ci.Combined
	runs        []ci.Run
	autoPending bool
	greenWait   bool // green, following a scheduled auto-merge
	pendingList []string
}

// WaitTarget waits for the checks of a PR, ref or sha (target mode, verdict
// rules 1-6 of section 10.1 including the auto-merge follow-up).
func WaitTarget(ctx context.Context, c *forgejo.Client, o TargetOpts) (*Result, error) {
	if o.PR <= 0 && o.Ref == "" && o.SHA == "" {
		return nil, errors.New("invalid: pr, ref or sha required")
	}
	if o.SHA != "" && !ci.ValidSHA(o.SHA) {
		return nil, errors.New("invalid: sha must be the full commit id (40 hex chars); expand a short sha first")
	}
	clk := clockOf(o.Poll)
	o.Poll.Clock = clk
	start := clk.Now()
	// everything after the poll (mergeability, digest, hint) stays inside the
	// same real-time bound
	pctx, cancel := Bound(ctx, o.Poll)
	defer cancel()
	st := &targetState{}

	var greenAt time.Time
	autoChecked, autoMerge := false, false

	step := func(ctx context.Context) (bool, Snapshot, error) {
		sha := o.SHA
		switch {
		case o.PR > 0:
			pr, err := getPR(ctx, c, o.Owner, o.Repo, o.PR)
			if err != nil {
				return false, Snapshot{}, err
			}
			st.pr = pr
			if pr.Merged {
				st.verdict = VerdictMerged
				return true, Snapshot{Summary: fmt.Sprintf("pr #%d merged", o.PR), Key: "merged"}, nil
			}
			if pr.State == "closed" {
				st.verdict = VerdictClosed
				return true, Snapshot{Summary: fmt.Sprintf("pr #%d closed", o.PR), Key: "closed"}, nil
			}
			sha = pr.Head.SHA
		case sha == "":
			var err error
			if sha, err = ci.ResolveSHA(ctx, c, o.Owner, o.Repo, o.Ref); err != nil {
				return false, Snapshot{}, err
			}
		}
		if st.firstSHA == "" {
			st.firstSHA = sha
		}
		if sha != st.firstSHA && o.SHA == "" {
			st.movedTo = sha
		}
		if sha != st.sha {
			greenAt = time.Time{}
		}
		st.sha = sha
		st.greenWait = false

		cs, err := ci.CombinedStatus(ctx, c, o.Owner, o.Repo, sha)
		if err != nil {
			return false, Snapshot{}, err
		}
		runs, err := ci.RunsForSHA(ctx, c, o.Owner, o.Repo, sha, 10)
		if err != nil {
			var ae *forgejo.APIError
			if !errors.As(err, &ae) || (ae.Status != http.StatusNotFound && ae.Status != http.StatusForbidden) {
				return false, Snapshot{}, err
			}
			runs = nil
		}
		runs = ci.NewestPerGroup(runs)
		st.combined, st.runs = cs, runs

		failedRun, pendingRun := false, false
		for _, r := range runs {
			switch r.Status {
			case "failure", "cancelled":
				failedRun = true
			case "success", "skipped":
			default:
				pendingRun = true
			}
		}
		statusFail := cs.State == "failure" || cs.State == "error"
		pending := pendingRun || cs.State == "pending" || len(cs.Pending) > 0
		hasFailure := statusFail || (cs.Total == 0 && failedRun)
		st.pendingList = pendingNames(cs, runs)
		snap := targetSnapshot(o, st)

		switch {
		case hasFailure && (o.FailFast || !pending):
			st.verdict = VerdictFailed
			return true, snap, nil
		case pending:
			greenAt = time.Time{}
			snap.Summary += " pending"
			return false, snap, nil
		case cs.Total == 0 && len(runs) == 0:
			if left := noChecksGrace - clk.Now().Sub(start); left > 0 {
				snap.Summary += " waiting for checks to appear"
				snap.Wake = left
				return false, snap, nil
			}
			st.verdict = VerdictNoChecks
			return true, snap, nil
		}

		// green
		if o.PR > 0 {
			if !autoChecked {
				autoChecked = true
				autoMerge = autoMergeKnown(ctx, c, o)
			}
			if autoMerge {
				if greenAt.IsZero() {
					greenAt = clk.Now()
				}
				if left := autoMergeFollow - clk.Now().Sub(greenAt); left > 0 {
					st.greenWait = true
					snap.Summary += " green, waiting for auto-merge"
					snap.Wake = left
					return false, snap, nil
				}
				st.autoPending = true
			}
		}
		st.verdict = VerdictGreen
		return true, snap, nil
	}

	_, timedOut, err := Poll(ctx, step, o.Poll)
	if err != nil {
		return nil, err
	}
	waited := clk.Now().Sub(start)
	verdict := st.verdict
	if timedOut {
		if st.greenWait {
			verdict, st.autoPending = VerdictGreen, true
		} else {
			verdict = VerdictTimeout
		}
	}
	if verdict == "" {
		verdict = VerdictTimeout
	}

	var b format.B
	if o.TargetLabel != "" {
		b.Raw(o.TargetLabel + "\n")
	}
	subject := targetSubject(o, st)
	switch verdict {
	case VerdictMerged:
		subject = fmt.Sprintf("pr #%d merged", o.PR)
		if st.pr != nil && st.pr.MergeCommitSHA != "" {
			subject += " as " + format.SHA(st.pr.MergeCommitSHA, 8)
		}
	case VerdictClosed:
		subject = fmt.Sprintf("pr #%d closed", o.PR)
	case VerdictTimeout:
		if len(st.pendingList) > 0 {
			subject += " pending (" + strings.Join(st.pendingList, ", ") + ")"
		}
	}
	vparts := []string{"verdict: " + verdict, "waited " + format.Dur(waited), subject}
	if st.movedTo != "" {
		vparts = append(vparts, fmt.Sprintf("head moved %s->%s", format.SHA(st.firstSHA, 8), format.SHA(st.movedTo, 8)))
	}
	if verdict == VerdictGreen && st.combined != nil && st.combined.State == "warning" {
		vparts = append(vparts, "warning")
	}
	if st.autoPending {
		vparts = append(vparts, "auto-merge pending")
	}
	b.Line(vparts...)

	switch verdict {
	case VerdictMerged, VerdictClosed:
	default:
		if st.combined != nil {
			b.Raw(ciLine(st.combined, st.runs) + "\n")
		}
	}
	if verdict == VerdictGreen && o.PR > 0 && st.pr != nil && !st.autoPending && pctx.Err() == nil {
		b.Raw(mergeabilityLine(pctx, c, o, st.pr) + "\n")
	}
	if verdict == VerdictFailed && o.WithFailure {
		if pctx.Err() != nil {
			b.Raw("digest unavailable: time budget exhausted\n")
		} else {
			text, _, derr := ci.FailureDigest(pctx, c, ci.DigestOpts{Owner: o.Owner, Repo: o.Repo, SHA: st.sha, Tail: failureTail, MaxChars: failureChars, NoHeaders: true})
			if derr != nil {
				b.Raw("digest unavailable: " + format.OneLine(strings.TrimPrefix(derr.Error(), "error: "), 200) + "\n")
			} else {
				b.Raw(text + "\n")
			}
		}
	}
	if verdict == VerdictTimeout {
		resume := o.Resume
		if resume == "" {
			resume = resumeSelector(o)
		}
		b.Raw("resume: " + resume + "\n")
		if pctx.Err() == nil {
			if h := targetHint(pctx, c, o, st.runs); h != "" {
				b.Raw("hint: " + h + "\n")
			}
		}
	}
	return &Result{Verdict: verdict, Waited: waited, Text: strings.TrimRight(b.String(), "\n")}, nil
}

func resumeSelector(o TargetOpts) string {
	switch {
	case o.PR > 0:
		return fmt.Sprintf("ci_wait pr=%d", o.PR)
	case o.Ref != "":
		return "ci_wait ref=" + o.Ref
	}
	return "ci_wait sha=" + o.SHA
}

func targetSubject(o TargetOpts, st *targetState) string {
	sha8 := format.SHA(st.sha, 8)
	switch {
	case o.PR > 0:
		return fmt.Sprintf("pr #%d head %s", o.PR, sha8)
	case o.Ref != "":
		return fmt.Sprintf("ref %s head %s", o.Ref, sha8)
	}
	return "sha " + sha8
}

func targetSnapshot(o TargetOpts, st *targetState) Snapshot {
	cs := st.combined
	var keys []string
	for _, r := range st.runs {
		keys = append(keys, strconv.FormatInt(r.ID, 10)+"="+r.Status)
	}
	key := fmt.Sprintf("%s|%s|%d/%d|%s", st.sha, cs.State, cs.OK, cs.Total, strings.Join(keys, ","))
	sum := targetSubject(o, st) + " ci " + cs.State
	if cs.Total > 0 {
		sum += fmt.Sprintf(" %d/%d", cs.OK, cs.Total)
	}
	return Snapshot{Summary: sum, Key: key}
}

func pendingNames(cs *ci.Combined, runs []ci.Run) []string {
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, n := range cs.Pending {
		add(n)
	}
	for _, r := range runs {
		if ci.IsUnfinished(r.Status) {
			add(r.WorkflowID)
		}
	}
	return out
}

func ciLine(cs *ci.Combined, runs []ci.Run) string {
	var parts []string
	if cs.Total == 0 {
		parts = append(parts, "ci: none")
	} else {
		parts = append(parts, fmt.Sprintf("ci: %s %d/%d", cs.State, cs.OK, cs.Total))
	}
	if len(cs.Failing) > 0 {
		f := make([]string, len(cs.Failing))
		for i, ch := range cs.Failing {
			f[i] = ch.Context
			if ch.Description != "" {
				f[i] += " (" + format.OneLine(ch.Description, 60) + ")"
			}
		}
		parts = append(parts, "failing: "+strings.Join(f, ", "))
	}
	if len(cs.Pending) > 0 {
		parts = append(parts, "pending: "+strings.Join(cs.Pending, ", "))
	}
	if len(runs) > 0 {
		r := make([]string, len(runs))
		for i, run := range runs {
			r[i] = fmt.Sprintf("run %d no.%d %s", run.ID, run.Index, run.Status)
			if run.Duration > 0 {
				r[i] += " " + format.Dur(time.Duration(run.Duration))
			}
		}
		parts = append(parts, "runs: "+strings.Join(r, ", "))
	}
	return strings.Join(parts, " | ")
}

func getPR(ctx context.Context, c *forgejo.Client, owner, repo string, n int64) (*prInfo, error) {
	var pr prInfo
	if _, err := c.GetJSON(ctx, forgejo.Path("repos", owner, repo, "pulls", strconv.FormatInt(n, 10)), nil, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// timelineMaxPages bounds the timeline scan of autoMergeKnown.
const timelineMaxPages = 40

// autoMergeKnown reports a scheduled auto-merge: remembered by `pr merge`
// (Memo key `automerge:o/r#n`) or visible as the newest scheduling event of
// the PR timeline. The timeline drops some entries after paging (inline
// review comments), so pages can be short while later pages still exist: the
// scan stops only on an empty page (section 6.7, unknown-total rule).
func autoMergeKnown(ctx context.Context, c *forgejo.Client, o TargetOpts) bool {
	if o.Memo != nil && o.Memo.Has(fmt.Sprintf("automerge:%s/%s#%d", o.Owner, o.Repo, o.PR)) {
		return true
	}
	scheduled := false
	path := forgejo.Path("repos", o.Owner, o.Repo, "issues", strconv.FormatInt(o.PR, 10), "timeline")
	for page := 1; page <= timelineMaxPages; page++ {
		var events []struct {
			Type string `json:"type"`
		}
		if _, err := c.List(ctx, path, nil, page, 50, &events); err != nil {
			return scheduled
		}
		if len(events) == 0 {
			break
		}
		for _, e := range events {
			switch e.Type {
			case "pull_scheduled_merge":
				scheduled = true
			case "pull_cancel_scheduled_merge":
				scheduled = false
			}
		}
	}
	return scheduled
}

// mergeabilityLine renders `mergeable: yes | ready: yes` or
// `mergeable: yes | blocked_by: approvals:1/2`.
func mergeabilityLine(ctx context.Context, c *forgejo.Client, o TargetOpts, pr *prInfo) string {
	var blocked []string
	mergeable := "yes"
	if pr.Draft {
		blocked = append(blocked, "draft")
	}
	if !pr.Mergeable {
		mergeable = "conflict"
		blocked = append(blocked, "conflict")
	}
	rules := branchRules(ctx, c, o, pr.Base.Ref)
	if rules.required > 0 || rules.blockRejected {
		approved, changes := reviewCounts(ctx, c, o)
		if rules.required > 0 && approved < rules.required {
			blocked = append(blocked, fmt.Sprintf("approvals:%d/%d", approved, rules.required))
		}
		if rules.blockRejected && changes > 0 {
			blocked = append(blocked, "changes_requested")
		}
	}
	if len(blocked) == 0 {
		return "mergeable: " + mergeable + " | ready: yes"
	}
	return "mergeable: " + mergeable + " | blocked_by: " + strings.Join(blocked, ",")
}

type mergeRules struct {
	required      int
	blockRejected bool
}

// branchRules reads the approval rules of the base branch: required_approvals
// from the branch, block_on_rejected_reviews from its protection (readable by
// admins only; unreadable counts as not blocking).
func branchRules(ctx context.Context, c *forgejo.Client, o TargetOpts, base string) mergeRules {
	var r mergeRules
	if base == "" {
		return r
	}
	var br struct {
		RequiredApprovals int    `json:"required_approvals"`
		Protection        string `json:"effective_branch_protection_name"`
	}
	if _, err := c.GetJSON(ctx, forgejo.Path("repos", o.Owner, o.Repo, "branches", forgejo.Wild(base)), nil, &br); err != nil {
		return r
	}
	r.required = br.RequiredApprovals
	if br.Protection != "" {
		var bp struct {
			Block bool `json:"block_on_rejected_reviews"`
		}
		if _, err := c.GetJSON(ctx, forgejo.Path("repos", o.Owner, o.Repo, "branch_protections", br.Protection), nil, &bp); err == nil {
			r.blockRejected = bp.Block
		}
	}
	return r
}

// reviewCountMaxPages bounds the review scan of reviewCounts.
const reviewCountMaxPages = 20

// reviewCounts returns the official approvals and the open official change
// requests among the latest review of every reviewer. Only official reviews
// count toward the merge rules; all pages are read until an empty one (the
// server drops pending reviews of other users after paging, so pages can be
// short).
func reviewCounts(ctx context.Context, c *forgejo.Client, o TargetOpts) (approved, changes int) {
	type review struct {
		State     string `json:"state"`
		Dismissed bool   `json:"dismissed"`
		Official  bool   `json:"official"`
		User      *struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	path := forgejo.Path("repos", o.Owner, o.Repo, "pulls", strconv.FormatInt(o.PR, 10), "reviews")
	latest := map[string]review{}
	for page := 1; page <= reviewCountMaxPages; page++ {
		var reviews []review
		if _, err := c.List(ctx, path, nil, page, 50, &reviews); err != nil {
			return 0, 0
		}
		if len(reviews) == 0 {
			break
		}
		for _, r := range reviews {
			if r.Dismissed || (r.State != "APPROVED" && r.State != "REQUEST_CHANGES") || r.User == nil {
				continue
			}
			latest[r.User.Login] = r
		}
	}
	for _, r := range latest {
		if !r.Official {
			continue
		}
		if r.State == "APPROVED" {
			approved++
		} else {
			changes++
		}
	}
	return approved, changes
}

// hintMaxRuns bounds the job lookups of targetHint.
const hintMaxRuns = 5

// targetHint builds the stuck-queue hint of a timeout from every unfinished
// run (waiting, blocked, running): a running run can still hold a job that
// waits for a runner.
func targetHint(ctx context.Context, c *forgejo.Client, o TargetOpts, runs []ci.Run) string {
	var open []ci.Run
	for _, r := range runs {
		if ci.IsUnfinished(r.Status) && len(open) < hintMaxRuns {
			open = append(open, r)
		}
	}
	var jobs []ci.Job
	for _, r := range open {
		if ctx.Err() != nil {
			break
		}
		j, err := ci.ListJobs(ctx, c, o.Owner, o.Repo, r.ID)
		if err == nil {
			jobs = append(jobs, j...)
		}
	}
	return stuckHint(ctx, c, o.Owner, o.Repo, open, jobs)
}

// stuckHint explains why jobs stay in `waiting`: a run that needs approval, no
// online runner, or no single online runner covering all runs-on labels of a
// job. It returns "" when nothing is waiting, the runner list is not readable
// (403) or the runners cover the labels.
func stuckHint(ctx context.Context, c *forgejo.Client, owner, repo string, runs []ci.Run, jobs []ci.Job) string {
	for _, r := range runs {
		if r.NeedApproval && ci.IsUnfinished(r.Status) {
			return fmt.Sprintf("run %d needs approval before it can start", r.ID)
		}
	}
	var waiting []ci.Job
	for _, j := range jobs {
		if j.Status == "waiting" {
			waiting = append(waiting, j)
		}
	}
	if len(waiting) == 0 {
		return ""
	}
	var runners []struct {
		Name   string   `json:"name"`
		Status string   `json:"status"`
		Labels []string `json:"labels"`
	}
	q := url.Values{"visible": {"true"}}
	if _, err := c.List(ctx, forgejo.Path("repos", owner, repo, "actions", "runners"), q, 1, 50, &runners); err != nil {
		return ""
	}
	var sets []map[string]bool // label set of every online runner
	seenLabel := map[string]bool{}
	var onlineLabels []string
	for _, r := range runners {
		if r.Status == "offline" {
			continue
		}
		set := map[string]bool{}
		for _, l := range r.Labels {
			l = labelName(l)
			set[l] = true
			if !seenLabel[l] {
				seenLabel[l] = true
				onlineLabels = append(onlineLabels, l)
			}
		}
		sets = append(sets, set)
	}
	if len(sets) == 0 {
		return "all runners offline"
	}
	covered := func(runsOn []string) bool {
		for _, set := range sets {
			ok := true
			for _, l := range runsOn {
				if !set[l] {
					ok = false
					break
				}
			}
			if ok {
				return true
			}
		}
		return false
	}
	var missing []string
	seen := map[string]bool{}
	for _, j := range waiting {
		if len(j.RunsOn) == 0 || covered(j.RunsOn) {
			continue
		}
		k := strings.Join(j.RunsOn, ",")
		if !seen[k] {
			seen[k] = true
			missing = append(missing, "["+k+"]")
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf("no online runner covers runs-on %s; online runners: %d [%s]", strings.Join(missing, " "), len(sets), strings.Join(onlineLabels, ","))
}

// labelName strips the `:docker://image` part of a runner label.
func labelName(l string) string {
	if i := strings.IndexByte(l, ':'); i > 0 {
		return l[:i]
	}
	return l
}
