package sync

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// TokenBucket is a classic token-bucket limiter. Tokens accrue at `rate`
// per second up to `capacity`; each Wait consumes one token, blocking until
// one is available or ctx is cancelled.
//
// We use it instead of golang.org/x/time/rate to keep the dependency
// footprint small and to make the refill clock injectable for tests
// (otherwise the burst-vs-throttle assertion in the rate-limit test
// becomes flaky on shared CI runners). The implementation is intentionally
// simple — single-request-per-Wait — because the only callsites
// (history backfill, send) request one token at a time.
type TokenBucket struct {
	rate     float64
	capacity float64
	now      func() time.Time

	mu     sync.Mutex
	tokens float64
	last   time.Time
}

// NewTokenBucket constructs a bucket that refills at `rate` tokens per
// second up to `capacity` tokens. Both must be > 0; the bucket starts full
// so that warm-up requests are served without an artificial first delay.
func NewTokenBucket(rate float64, capacity int) (*TokenBucket, error) {
	if rate <= 0 {
		return nil, fmt.Errorf("rate must be > 0, got %v", rate)
	}
	if capacity <= 0 {
		return nil, fmt.Errorf("capacity must be > 0, got %d", capacity)
	}
	return &TokenBucket{
		rate:     rate,
		capacity: float64(capacity),
		now:      time.Now,
		tokens:   float64(capacity),
		last:     time.Now(),
	}, nil
}

// Wait blocks until a token is available or ctx is cancelled.
//
// The timer is allocated via time.NewTimer + Stop on every iteration
// so a ctx-cancellation does not leave a dangling timer in the runtime
// heap until it fires (the time.After form keeps the timer pinned for
// up to ~1 s after each cancelled wait).
func (b *TokenBucket) Wait(ctx context.Context) error {
	for {
		wait, ok := b.take()
		if ok {
			return nil
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// take attempts to consume one token. If a token was available it returns
// (0, true). Otherwise it returns the duration the caller should sleep
// before retrying, computed from the current refill rate.
func (b *TokenBucket) take() (time.Duration, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * b.rate
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return 0, true
	}
	missing := 1 - b.tokens
	wait := time.Duration(missing / b.rate * float64(time.Second))
	if wait <= 0 {
		wait = time.Millisecond
	}
	return wait, false
}
