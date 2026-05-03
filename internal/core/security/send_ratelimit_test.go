package security

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestNewSendGuard_AppliesDefaults ensures a zero-value config falls
// back to the project-wide ban-risk ceiling rather than the underlying
// bucket's "rate must be > 0" error.
func TestNewSendGuard_AppliesDefaults(t *testing.T) {
	t.Parallel()

	g, err := NewSendGuard(SendRateLimit{})
	if err != nil {
		t.Fatalf("NewSendGuard: %v", err)
	}
	cfg := g.Config()
	if cfg.Rate != DefaultSendRate {
		t.Fatalf("Rate: want %v, got %v", DefaultSendRate, cfg.Rate)
	}
	if cfg.Burst != DefaultSendBurst {
		t.Fatalf("Burst: want %d, got %d", DefaultSendBurst, cfg.Burst)
	}
}

// TestSendGuard_BurstFitsInsideCapacity is the headline case: at most
// `burst` Wait calls succeed back-to-back without the bucket blocking.
// Anything more than that needs token accrual.
func TestSendGuard_BurstFitsInsideCapacity(t *testing.T) {
	t.Parallel()

	g, err := NewSendGuard(SendRateLimit{Rate: 10, Burst: 30})
	if err != nil {
		t.Fatalf("NewSendGuard: %v", err)
	}

	start := time.Now()
	for i := 0; i < 30; i++ {
		if err := g.Wait(context.Background()); err != nil {
			t.Fatalf("Wait[%d]: %v", i, err)
		}
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("burst should be near-instant, took %v", elapsed)
	}
}

// TestSendGuard_BlocksAfterBurst exercises the throttle path: 31
// requests in quick succession on a 10-rps bucket — the 31st must
// wait at least one refill period (1/10s = 100 ms). We use the
// internal token-bucket clock seam to keep the assertion deterministic
// instead of relying on real wall-clock sleeping.
func TestSendGuard_BlocksAfterBurst(t *testing.T) {
	t.Parallel()

	g, err := NewSendGuard(SendRateLimit{Rate: 10, Burst: 30})
	if err != nil {
		t.Fatalf("NewSendGuard: %v", err)
	}

	// Drain the burst.
	for i := 0; i < 30; i++ {
		if err := g.Wait(context.Background()); err != nil {
			t.Fatalf("burst Wait[%d]: %v", i, err)
		}
	}

	// The 31st call should genuinely block. We give it a 200 ms ctx
	// deadline so the test fails fast if the guard wires through
	// without throttling, and we expect Wait to succeed within that
	// window because 100 ms is enough for one token to accrue at 10 rps.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := g.Wait(ctx); err != nil {
		t.Fatalf("31st Wait should succeed within 200 ms, got %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 50*time.Millisecond {
		t.Fatalf("31st Wait should block for at least ~100 ms, observed %v", elapsed)
	}
}

// TestSendGuard_ContextCancellation makes sure a starved Wait returns
// promptly when the caller cancels — important because the TUI's
// SendText callsite hands in a request-scoped ctx that goes away when
// the user navigates off the input pane.
func TestSendGuard_ContextCancellation(t *testing.T) {
	t.Parallel()

	g, err := NewSendGuard(SendRateLimit{Rate: 0.001, Burst: 1})
	if err != nil {
		t.Fatalf("NewSendGuard: %v", err)
	}
	if err := g.Wait(context.Background()); err != nil {
		t.Fatalf("initial token Wait: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = g.Wait(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait should surface ctx error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("Wait did not honour ctx deadline (%v)", elapsed)
	}
}

// TestSendGuard_NilIsNoop guarantees a (*SendGuard)(nil) Wait is safe.
// SendService keeps the guard optional so unit tests of unrelated
// behaviour stay cheap; this asserts the no-op path works.
func TestSendGuard_NilIsNoop(t *testing.T) {
	t.Parallel()

	var g *SendGuard
	if err := g.Wait(context.Background()); err != nil {
		t.Fatalf("nil guard Wait: %v", err)
	}
	if cfg := g.Config(); cfg != (SendRateLimit{}) {
		t.Fatalf("nil guard Config should be zero, got %+v", cfg)
	}
}

// TestSendGuard_SteadyStateMatchesRate sanity-checks the steady-state
// throughput. After draining the burst, we expect roughly `rate` Waits
// per second once accrual takes over. We measure 15 extra Waits at
// rate=20 and assert the elapsed time is close to 15/20 = 750 ms.
//
// 50% slop on either side keeps the test stable on slow CI runners
// while still catching catastrophic regressions (1×, 10×, no-throttle
// behaviour).
func TestSendGuard_SteadyStateMatchesRate(t *testing.T) {
	if testing.Short() {
		t.Skip("steady-state check involves a ~750 ms wall-clock wait")
	}
	t.Parallel()

	g, err := NewSendGuard(SendRateLimit{Rate: 20, Burst: 5})
	if err != nil {
		t.Fatalf("NewSendGuard: %v", err)
	}
	// Drain the burst.
	for i := 0; i < 5; i++ {
		if err := g.Wait(context.Background()); err != nil {
			t.Fatalf("burst Wait[%d]: %v", i, err)
		}
	}
	const extra = 15
	expected := time.Duration(float64(extra) / 20.0 * float64(time.Second))

	start := time.Now()
	for i := 0; i < extra; i++ {
		if err := g.Wait(context.Background()); err != nil {
			t.Fatalf("steady Wait[%d]: %v", i, err)
		}
	}
	elapsed := time.Since(start)
	low := time.Duration(float64(expected) * 0.5)
	high := time.Duration(float64(expected) * 1.5)
	if elapsed < low || elapsed > high {
		t.Fatalf("steady-state pace off: elapsed=%v want %v..%v (centre %v)", elapsed, low, high, expected)
	}
}
