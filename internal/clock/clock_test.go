package clock

import (
	"context"
	"testing"
	"time"
)

func TestFakeSleepAdvance(t *testing.T) {
	f := NewFake(time.Time{})
	start := f.Now()
	done := make(chan error, 1)
	go func() { done <- f.Sleep(context.Background(), 5*time.Second) }()
	if !f.BlockUntil(context.Background(), 1) {
		t.Fatal("sleeper never parked")
	}
	f.Advance(4 * time.Second)
	select {
	case <-done:
		t.Fatal("woke too early")
	case <-time.After(20 * time.Millisecond):
	}
	f.Advance(time.Second)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := f.Now().Sub(start); got != 5*time.Second {
		t.Fatalf("advanced %v", got)
	}
	if f.Pending() != 0 {
		t.Fatalf("pending %d", f.Pending())
	}
}

func TestFakeSleepCancel(t *testing.T) {
	f := NewFake(time.Time{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.Sleep(ctx, time.Hour); err == nil {
		t.Fatal("expected context error")
	}
}

func TestRealSleep(t *testing.T) {
	if err := Real().Sleep(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Real().Sleep(ctx, time.Hour); err == nil {
		t.Fatal("expected context error")
	}
}
