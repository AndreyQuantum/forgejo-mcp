package wait

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/clock"
	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
)

const (
	firstInterval   = 2 * time.Second
	maxInterval     = 6 * time.Second
	repeatedPause   = 10 * time.Second
	repeatsForPause = 5
	maxStepErrors   = 3
	defaultTimeout  = 50 * time.Second
	// ctxReserve is added to the timeout for the real-time bound of the
	// HTTP work (the last poll round, hints, digests); the fake clock only
	// drives the sleeps.
	ctxReserve = 4 * time.Second
	// minFinalPoll: no further poll round starts when less than this remains.
	minFinalPoll = 2 * time.Second
)

// Bound derives the context that bounds all HTTP work of one wait to the
// real time of timeout plus a small reserve (section 10.1): requests cannot
// outlive the call even when the server is slow.
func Bound(ctx context.Context, o PollOpts) (context.Context, context.CancelFunc) {
	t := o.Timeout
	if t <= 0 {
		t = defaultTimeout
	}
	return context.WithDeadline(ctx, time.Now().Add(t+ctxReserve))
}

// Snapshot is one poll result; equal snapshots are not reported as progress.
type Snapshot struct {
	Summary string // e.g. "run 1234 running: 2/3 jobs done (build ok, test running)"
	Key     string // comparison key
	// Wake, when > 0, caps the pause before the next poll (a verdict rule
	// that changes at a known time, e.g. the no-checks grace window).
	Wake time.Duration
}

func (s Snapshot) key() string {
	if s.Key != "" {
		return s.Key
	}
	return s.Summary
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
// timedOut is true when the deadline passed while not done. Every sleep and
// the deadline go through o.Clock.
func Poll(ctx context.Context, step StepFunc, o PollOpts) (last Snapshot, timedOut bool, err error) {
	clk := o.Clock
	if clk == nil {
		clk = clock.Real()
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	start := clk.Now()
	deadline := start.Add(timeout)
	pctx := ctx
	ctx, cancel := Bound(pctx, o)
	defer cancel()

	base := firstInterval
	var lastKey string
	havePrev := false
	repeats := 0
	errs := 0
	var wake time.Duration
	for {
		if err := pctx.Err(); err != nil {
			return last, false, err
		}
		if ctx.Err() != nil {
			return last, true, nil
		}
		done, snap, serr := step(ctx)
		if serr != nil {
			if pctx.Err() != nil {
				return last, false, pctx.Err()
			}
			if ctx.Err() != nil {
				return last, true, nil // the real-time bound ran out
			}
			errs++
			if errs >= maxStepErrors || fatal(serr) {
				return last, false, serr
			}
		} else {
			errs = 0
			k := snap.key()
			if havePrev && k == lastKey {
				repeats++
			} else {
				repeats = 1
				if o.Progress != nil {
					o.Progress(snap.Summary, clk.Now().Sub(start).Seconds(), timeout.Seconds())
				}
			}
			lastKey, havePrev, last, wake = k, true, snap, snap.Wake
			if done {
				return last, false, nil
			}
		}

		now := clk.Now()
		remaining := deadline.Sub(now)
		if remaining <= minFinalPoll {
			return last, true, nil
		}
		wait := base
		if repeats >= repeatsForPause {
			wait = repeatedPause
		}
		if wake > 0 {
			wait = min(wait, wake)
		}
		// leave room for one more poll round before the deadline
		wait = min(wait, remaining-minFinalPoll)
		if err := clk.Sleep(ctx, wait); err != nil {
			if pctx.Err() != nil {
				return last, false, pctx.Err()
			}
			return last, true, nil
		}
		base = min(base*3/2, maxInterval)
	}
}

// fatal reports errors that polling again cannot fix (client errors other
// than timeouts and rate limits).
func fatal(err error) bool {
	var ae *forgejo.APIError
	if !errors.As(err, &ae) {
		return false
	}
	if ae.Status >= 400 && ae.Status < 500 {
		return ae.Status != http.StatusRequestTimeout && ae.Status != http.StatusTooManyRequests
	}
	return false
}
