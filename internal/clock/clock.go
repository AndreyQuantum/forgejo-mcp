// Package clock abstracts time so polling and retry code can be tested
// without real sleeps. It depends on the standard library only.
package clock

import (
	"context"
	"sync"
	"time"
)

// Clock is the time source used by retries, backoff and waits.
type Clock interface {
	Now() time.Time
	// After returns a channel that receives the time once d has elapsed.
	After(d time.Duration) <-chan time.Time
	// Sleep blocks for d or until ctx is done (then it returns ctx.Err()).
	Sleep(ctx context.Context, d time.Duration) error
}

type realClock struct{}

// Real returns the wall-clock implementation.
func Real() Clock { return realClock{} }

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Fake is a manually advanced clock. The zero value is not usable; use NewFake.
type Fake struct {
	mu      sync.Mutex
	now     time.Time
	waiters []*waiter
	changed chan struct{} // closed and replaced whenever a waiter is added
}

type waiter struct {
	at time.Time
	ch chan time.Time
}

// NewFake returns a Fake starting at start (a fixed date when zero).
func NewFake(start time.Time) *Fake {
	if start.IsZero() {
		start = time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	}
	return &Fake{now: start, changed: make(chan struct{})}
}

// Now returns the fake time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// After registers a timer that fires when the fake time reaches now+d.
func (f *Fake) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- f.now
		return ch
	}
	f.waiters = append(f.waiters, &waiter{at: f.now.Add(d), ch: ch})
	close(f.changed)
	f.changed = make(chan struct{})
	return ch
}

// Sleep blocks until the fake time advances by d or ctx is done.
func (f *Fake) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	select {
	case <-f.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Advance moves the fake time forward and fires every timer that became due.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	kept := f.waiters[:0]
	for _, w := range f.waiters {
		if !w.at.After(f.now) {
			w.ch <- f.now
			continue
		}
		kept = append(kept, w)
	}
	f.waiters = kept
}

// Pending returns the number of timers that have not fired yet.
func (f *Fake) Pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.waiters)
}

// BlockUntil waits until at least n timers are pending (test synchronisation:
// it lets a test know the code under test is parked in Sleep/After before it
// calls Advance). It returns false when ctx ends first.
func (f *Fake) BlockUntil(ctx context.Context, n int) bool {
	for {
		f.mu.Lock()
		ok := len(f.waiters) >= n
		ch := f.changed
		f.mu.Unlock()
		if ok {
			return true
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return false
		}
	}
}
