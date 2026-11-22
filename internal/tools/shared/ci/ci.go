// Package ci holds the CI plumbing shared by the pr, checks, run, ci_failure
// and ci_wait tools (DESIGN.md sections 10.1-10.4). It never imports package
// tools. WP0 defines signatures; WP6b fills them.
package ci

import (
	"context"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
)

// Check is one failing commit status.
type Check struct {
	Context     string
	State       string
	Description string
	TargetURL   string
}

// Combined is the combined commit status over all pages.
type Combined struct {
	State   string // success|warning|pending|failure|error|none
	Total   int
	OK      int
	Failing []Check
	Pending []string
}

// CombinedStatus reads `GET R/commits/{ref}/status?limit=<server max>`, fetches
// further pages (at most 5) when a page is full and recomputes the state
// client-side as the worst one (failure|error > pending > warning > success,
// latest status per context wins).
func CombinedStatus(ctx context.Context, c *forgejo.Client, owner, repo, ref string) (*Combined, error) {
	panic("WP6b")
}

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
	TriggerUser  string `json:"-"`
	Started      string `json:"started"`
	Stopped      string `json:"stopped"`
	Duration     int64  `json:"duration"` // nanoseconds
	Created      string `json:"created"`
	NeedApproval bool   `json:"need_approval"`
	IsForkPR     bool   `json:"is_fork_pull_request"`
}

// Job is the projection of an Actions job.
type Job struct {
	ID      int64    `json:"id"`
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

// RunLine renders `run 1234 no.56 failure | ci.yml | push main 8a1b2c3d | by alice | 1m03s | T`.
func RunLine(r Run) string { panic("WP6b") }

// JobLine renders `job 5678 build failure a1 needs:lint on:docker`.
func JobLine(j Job) string { panic("WP6b") }

// StepLine renders `  step 3 Run tests failure 12s`.
func StepLine(s Step) string { panic("WP6b") }

// LogOpts selects what FetchJobLog reads and how it is cut (section 10.2).
type LogOpts struct {
	Owner, Repo string
	Job         int64
	Run         int64  // with Name: select the job by name
	Name        string // job name (exact, then case-insensitive prefix)
	Step        string // "", "failed" or a number
	Attempt     int
	Tail        int // default 80
	First       int
	Grep        string
	IgnoreCase  bool
	Offset      *int64
	MaxChars    int // body budget, already clamped by Call.Chars
}

// JobLog is the result of FetchJobLog.
type JobLog struct {
	Header     string   // `job 5678 "build" attempt 1 step 3 "Run tests" [failure] | ...`
	Text       string   // cleaned, capped text (without the header)
	Lines      []string // cleaned lines of the fetched range (for digests)
	NextOffset int64
	NoLog      string // non-empty: `no log: <message>` (expired / not executed)
}

// FetchJobLog implements section 10.2.
func FetchJobLog(ctx context.Context, c *forgejo.Client, o LogOpts) (*JobLog, error) {
	panic("WP6b")
}

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
}

// FailureDigest builds the `ci_failure` output of section 10.4. It returns the
// text and whether any failed run was found.
func FailureDigest(ctx context.Context, c *forgejo.Client, o DigestOpts) (text string, failed bool, err error) {
	panic("WP6b")
}
