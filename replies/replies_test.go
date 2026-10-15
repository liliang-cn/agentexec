package replies

import (
	"context"
	"testing"
	"time"
)

func TestTryTakeReturnsParkedReply(t *testing.T) {
	q := New()
	q.Push("s1", "do the thing")

	if got := q.TryTake("s1"); got != "do the thing" {
		t.Fatalf("TryTake = %q, want %q", got, "do the thing")
	}
	if got := q.TryTake("s1"); got != "" {
		t.Fatalf("second TryTake = %q, want empty", got)
	}
}

func TestTryTakeIsPerSession(t *testing.T) {
	q := New()
	q.Push("s1", "one")
	q.Push("s2", "two")

	if got := q.TryTake("s2"); got != "two" {
		t.Fatalf("TryTake(s2) = %q, want %q", got, "two")
	}
	if got := q.TryTake("s1"); got != "one" {
		t.Fatalf("TryTake(s1) = %q, want %q", got, "one")
	}
}

func TestWaitReceivesLaterPush(t *testing.T) {
	q := New()
	go func() {
		time.Sleep(20 * time.Millisecond)
		q.Push("s1", "late answer")
	}()

	if got := q.Wait(context.Background(), "s1", time.Second); got != "late answer" {
		t.Fatalf("Wait = %q, want %q", got, "late answer")
	}
}

func TestWaitTimesOut(t *testing.T) {
	q := New()
	if got := q.Wait(context.Background(), "s1", 20*time.Millisecond); got != "" {
		t.Fatalf("Wait = %q, want empty on timeout", got)
	}
	// A timed-out waiter must not keep a slot that swallows the next reply.
	q.Push("s1", "after timeout")
	if got := q.TryTake("s1"); got != "after timeout" {
		t.Fatalf("TryTake after timeout = %q, want %q", got, "after timeout")
	}
}

func TestWaitZeroTimeoutDoesNotBlock(t *testing.T) {
	q := New()
	q.Push("s1", "parked")
	if got := q.Wait(context.Background(), "s1", 0); got != "" {
		t.Fatalf("Wait with zero timeout = %q, want empty", got)
	}
}

func TestWaitingReportsBlockedHook(t *testing.T) {
	q := New()
	if q.Waiting("s1") {
		t.Fatal("Waiting = true before anyone waits")
	}

	waiting := make(chan struct{})
	go func() {
		close(waiting)
		q.Wait(context.Background(), "s1", 500*time.Millisecond)
	}()
	<-waiting

	deadline := time.Now().Add(time.Second)
	for !q.Waiting("s1") {
		if time.Now().After(deadline) {
			t.Fatal("Waiting never became true while a hook was blocked")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPushIgnoresEmpty(t *testing.T) {
	q := New()
	q.Push("", "text")
	q.Push("s1", "")
	if got := q.TryTake("s1"); got != "" {
		t.Fatalf("TryTake = %q, want empty", got)
	}
}

func TestCancelledContextStopsWait(t *testing.T) {
	q := New()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if got := q.Wait(ctx, "s1", 5*time.Second); got != "" {
		t.Fatalf("Wait = %q, want empty", got)
	}
	if time.Since(start) > time.Second {
		t.Fatal("Wait ignored context cancellation")
	}
}
