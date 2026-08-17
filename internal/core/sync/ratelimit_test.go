package sync

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNewTokenBucket_RejectsBadInput(t *testing.T) {
	t.Parallel()
	if _, err := NewTokenBucket(0, 1); err == nil {
		t.Fatalf("rate=0 must fail")
	}
	if _, err := NewTokenBucket(1, 0); err == nil {
		t.Fatalf("capacity=0 must fail")
	}
}

// TestTokenBucket_BurstThenThrottle locks the clock so we can assert the
// classic shape of a token-bucket's output: the first `burst` requests
// finish without waiting; the next `rate` requests per virtual second do
// the same; nothing else gets through. With a real clock the throttle
// section is the flaky part on shared CI runners (a 1.0001s sleep would
// leak an extra token), so we drive the bucket from a frozen clock.
func TestTokenBucket_BurstThenThrottle(t *testing.T) {
	t.Parallel()
	const rate = 10
	const burst = 30
	now := time.Unix(0, 0).UTC()
	b, err := NewTokenBucket(rate, burst)
	if err != nil {
		t.Fatalf("NewTokenBucket: %v", err)
	}
	b.now = func() time.Time { return now }
	b.last = now
	b.tokens = float64(burst)

	// First burst: 30 immediate take()s succeed.
	for i := 0; i < burst; i++ {
		if wait, ok := b.take(); !ok {
			t.Fatalf("burst[%d]: expected immediate token, got wait=%v", i, wait)
		}
	}
	// Bucket now empty: take() should ask the caller to wait.
	if _, ok := b.take(); ok {
		t.Fatalf("bucket should be empty after %d takes", burst)
	}

	// Advance virtual time by exactly one second: rate tokens accrue.
	now = now.Add(time.Second)
	for i := 0; i < rate; i++ {
		if wait, ok := b.take(); !ok {
			t.Fatalf("after 1s refill[%d]: expected token, got wait=%v", i, wait)
		}
	}
	if _, ok := b.take(); ok {
		t.Fatalf("only %d tokens should accrue per second", rate)
	}

	// Advance by another two seconds: 2*rate tokens accrue but capacity is
	// not exceeded — the bucket caps at `burst`.
	now = now.Add(2 * time.Second)
	got := 0
	for {
		if _, ok := b.take(); !ok {
			break
		}
		got++
		if got > burst+1 {
			t.Fatalf("bucket capacity ignored, served %d > burst=%d", got, burst)
		}
	}
	if got != 2*rate {
		t.Fatalf("after +2s, got %d tokens, want %d", got, 2*rate)
	}
}

// TestTokenBucket_Wait_RespectsContext checks that a starved Wait returns
// ctx.Err() promptly when the caller cancels rather than waiting forever.
func TestTokenBucket_Wait_RespectsContext(t *testing.T) {
	t.Parallel()
	b, err := NewTokenBucket(0.001, 1) // ~one token every 1000s — effectively starved
	if err != nil {
		t.Fatalf("NewTokenBucket: %v", err)
	}
	// Drain the initial token.
	if _, ok := b.take(); !ok {
		t.Fatalf("initial token missing")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = b.Wait(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait should surface ctx error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("Wait did not honour ctx deadline (took %v)", elapsed)
	}
}

// TestTokenBucket_Wait_ServesQuickly confirms a fresh bucket grants a token
// immediately (no spurious sleep on the warm path).
func TestTokenBucket_Wait_ServesQuickly(t *testing.T) {
	t.Parallel()
	b, err := NewTokenBucket(10, 1)
	if err != nil {
		t.Fatalf("NewTokenBucket: %v", err)
	}
	start := time.Now()
	if err := b.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("warm Wait took too long: %v", elapsed)
	}
}
