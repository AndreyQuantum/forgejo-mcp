package wait

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/AndreyQuantum/forgejo-mcp/internal/clock"
	"github.com/AndreyQuantum/forgejo-mcp/internal/forgejo"
)

// recClock is a fake clock whose Sleep advances time immediately and records
// the requested durations.
type recClock struct {
	*clock.Fake
	mu     sync.Mutex
	sleeps []time.Duration
}

func newRecClock() *recClock { return &recClock{Fake: clock.NewFake(time.Time{})} }

func (c *recClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.sleeps = append(c.sleeps, d)
	c.mu.Unlock()
	c.Advance(d)
	return nil
}

func TestPollIntervals(t *testing.T) {
	clk := newRecClock()
	n := 0
	step := func(ctx context.Context) (bool, Snapshot, error) {
		n++
		return false, Snapshot{Summary: "s", Key: fmt.Sprint(n)}, nil // always different
	}
	_, timedOut, err := Poll(t.Context(), step, PollOpts{Clock: clk, Timeout: 20 * time.Second})
	if err != nil || !timedOut {
		t.Fatalf("timedOut=%v err=%v", timedOut, err)
	}
	want := []time.Duration{2 * time.Second, 3 * time.Second, 4500 * time.Millisecond, 6 * time.Second, 2500 * time.Millisecond}
	if fmt.Sprint(clk.sleeps) != fmt.Sprint(want) {
		t.Fatalf("sleeps %v want %v", clk.sleeps, want)
	}
	// the last poll leaves minFinalPoll before the deadline
	if got := clk.Now().Sub(time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)); got != 18*time.Second {
		t.Fatalf("elapsed %v", got)
	}
}

func TestPollRepeatedSnapshotSlowsDown(t *testing.T) {
	clk := newRecClock()
	step := func(ctx context.Context) (bool, Snapshot, error) {
		return false, Snapshot{Key: "same"}, nil
	}
	if _, timedOut, err := Poll(t.Context(), step, PollOpts{Clock: clk, Timeout: 60 * time.Second}); err != nil || !timedOut {
		t.Fatal(timedOut, err)
	}
	// polls 1..5 are the same snapshot; from the 5th repeat on the pause is 10 s
	want := []time.Duration{2 * time.Second, 3 * time.Second, 4500 * time.Millisecond, 6 * time.Second, 10 * time.Second}
	for i, w := range want {
		if clk.sleeps[i] != w {
			t.Fatalf("sleep %d = %v want %v (all %v)", i, clk.sleeps[i], w, clk.sleeps)
		}
	}
}

func TestPollDoneAndProgress(t *testing.T) {
	clk := newRecClock()
	var msgs []string
	var secs []float64
	n := 0
	step := func(ctx context.Context) (bool, Snapshot, error) {
		n++
		key := "a"
		if n >= 3 {
			key = "b"
		}
		return n == 4, Snapshot{Summary: "state " + key, Key: key}, nil
	}
	last, timedOut, err := Poll(t.Context(), step, PollOpts{Clock: clk, Timeout: 50 * time.Second, Progress: func(m string, done, total float64) {
		msgs = append(msgs, m)
		secs = append(secs, done)
		if total != 50 {
			t.Errorf("total %v", total)
		}
	}})
	if err != nil || timedOut || last.Summary != "state b" {
		t.Fatalf("%v %v %+v", err, timedOut, last)
	}
	if fmt.Sprint(msgs) != "[state a state b]" || secs[0] != 0 || secs[1] != 5 {
		t.Fatalf("progress %v %v", msgs, secs)
	}
}

func TestPollErrors(t *testing.T) {
	transient := &forgejo.APIError{Status: 502, Message: "bad gateway"}
	t.Run("three consecutive abort", func(t *testing.T) {
		n := 0
		_, _, err := Poll(t.Context(), func(ctx context.Context) (bool, Snapshot, error) {
			n++
			return false, Snapshot{}, transient
		}, PollOpts{Clock: newRecClock(), Timeout: time.Minute})
		if !errors.Is(err, transient) || n != 3 {
			t.Fatalf("n=%d err=%v", n, err)
		}
	})
	t.Run("recovery resets the count", func(t *testing.T) {
		n := 0
		_, timedOut, err := Poll(t.Context(), func(ctx context.Context) (bool, Snapshot, error) {
			n++
			if n%3 != 0 {
				return false, Snapshot{}, transient
			}
			return n == 9, Snapshot{Key: fmt.Sprint(n)}, nil
		}, PollOpts{Clock: newRecClock(), Timeout: time.Hour})
		if err != nil || timedOut || n != 9 {
			t.Fatalf("n=%d timedOut=%v err=%v", n, timedOut, err)
		}
	})
	t.Run("client error aborts at once", func(t *testing.T) {
		n := 0
		_, _, err := Poll(t.Context(), func(ctx context.Context) (bool, Snapshot, error) {
			n++
			return false, Snapshot{}, &forgejo.APIError{Status: http.StatusNotFound}
		}, PollOpts{Clock: newRecClock(), Timeout: time.Minute})
		if err == nil || n != 1 {
			t.Fatalf("n=%d err=%v", n, err)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		n := 0
		_, timedOut, err := Poll(ctx, func(ctx context.Context) (bool, Snapshot, error) {
			n++
			cancel()
			return false, Snapshot{Key: "x"}, nil
		}, PollOpts{Clock: newRecClock(), Timeout: time.Minute})
		if !errors.Is(err, context.Canceled) || timedOut || n != 1 {
			t.Fatalf("n=%d timedOut=%v err=%v", n, timedOut, err)
		}
	})
}

// TestPollFakeClockSleeps checks that Poll parks in clock.Sleep (no real time).
func TestPollFakeClockSleeps(t *testing.T) {
	fc := clock.NewFake(time.Time{})
	n := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = Poll(t.Context(), func(ctx context.Context) (bool, Snapshot, error) {
			n++
			return n == 2, Snapshot{Key: fmt.Sprint(n)}, nil
		}, PollOpts{Clock: fc, Timeout: time.Minute})
	}()
	if !fc.BlockUntil(t.Context(), 1) {
		t.Fatal("poller never slept")
	}
	fc.Advance(2 * time.Second)
	<-done
	if n != 2 {
		t.Fatalf("polls %d", n)
	}
}
