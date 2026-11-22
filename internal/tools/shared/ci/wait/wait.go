// Package wait implements the server-side polling of section 10.1 (ci_wait
// and run dispatch wait=true). All sleeping goes through clock.Clock. WP6b
// fills the bodies.
package wait

import (
	"context"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/clock"
	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
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

// Snapshot is one poll result; equal snapshots are not reported as progress.
type Snapshot struct {
	Summary string // e.g. "run 1234 running: 2/3 jobs done (build ok, test running)"
	Key     string // comparison key
}

// StepFunc performs one poll.
type StepFunc func(ctx context.Context) (done bool, snap Snapshot, err error)

// PollOpts configures Poll.
type PollOpts struct {
	Clock    clock.Clock
	Timeout  time.Duration
	Progress func(msg string, done, total float64) // nil = none
}

// Poll polls step: first immediately, then 2 s, 3 s, 4.5 s, then 6 s (10 s
// when the same snapshot repeated 5 times); 3 consecutive step errors abort.
// timedOut is true when the deadline passed while not done.
func Poll(ctx context.Context, step StepFunc, o PollOpts) (last Snapshot, timedOut bool, err error) {
	panic("WP6b")
}

// MemoReader is the part of tools.Memo the waiter needs (shared packages
// never import package tools).
type MemoReader interface{ Has(key string) bool }

// Result is the outcome of WaitRun or WaitTarget.
type Result struct {
	Verdict string
	Waited  time.Duration
	Text    string // rendered verdict block (section 10.1 output)
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
func WaitRun(ctx context.Context, c *forgejo.Client, o RunOpts) (*Result, error) { panic("WP6b") }

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

// WaitTarget waits for the checks of a PR, ref or sha (target mode, verdict
// rules 1-6 of section 10.1 including the auto-merge follow-up).
func WaitTarget(ctx context.Context, c *forgejo.Client, o TargetOpts) (*Result, error) {
	panic("WP6b")
}
