package security

import (
	"context"
	"errors"
	"fmt"

	coresync "github.com/kar43lov/lazytg/internal/core/sync"
)

// Default send-rate guard parameters. The 10 msg/sec rate is the
// project-wide ban-risk ceiling agreed in the v0.1 plan: it sits well
// below Telegram's per-second flood thresholds while still feeling
// instant for any human-driven typing pattern (and large enough to
// burst through programmatic-paste UX without a perceptible pause).
//
// Capacity 30 lets the user empty a chat-thread queue (e.g. three
// replies typed in 200 ms) without the limiter ever activating. Token
// accrual is continuous, so 30 successive sends drains the bucket but
// the next msg only blocks once the +10/sec accrual cannot keep up
// with sustained traffic.
const (
	// DefaultSendRate is tokens per second for outgoing-message guard.
	DefaultSendRate float64 = 10
	// DefaultSendBurst is bucket capacity for outgoing-message guard.
	DefaultSendBurst int = 30
)

// SendRateLimit holds the tunables for the outbound send guard. Zero
// values fall back to the documented defaults — the wiring layer
// constructs SendRateLimit{} when no override is needed.
type SendRateLimit struct {
	// Rate is tokens per second. Defaults to DefaultSendRate when ≤ 0.
	Rate float64
	// Burst is the bucket capacity. Defaults to DefaultSendBurst when ≤ 0.
	Burst int
}

func (c SendRateLimit) withDefaults() SendRateLimit {
	if c.Rate <= 0 {
		c.Rate = DefaultSendRate
	}
	if c.Burst <= 0 {
		c.Burst = DefaultSendBurst
	}
	return c
}

// SendGuard wraps coresync.TokenBucket so callers (SendService) only
// have to depend on this small surface, and so we can tag rate-limit
// hits with a slog-friendly callback later if telemetry asks for it.
//
// The guard is safe for concurrent use because the underlying bucket
// is. We deliberately keep zero allocations in the hot path: Wait
// forwards directly without wrapping ctx or the result.
type SendGuard struct {
	bucket *coresync.TokenBucket
	cfg    SendRateLimit
}

// NewSendGuard constructs a guard with the documented send-side
// defaults. Returns the same error shape as coresync.NewTokenBucket so
// the wiring layer can refuse to start on misconfiguration (rate ≤ 0
// after defaulting indicates the caller passed something pathological).
func NewSendGuard(cfg SendRateLimit) (*SendGuard, error) {
	cfg = cfg.withDefaults()
	bucket, err := coresync.NewTokenBucket(cfg.Rate, cfg.Burst)
	if err != nil {
		return nil, fmt.Errorf("send guard: %w", err)
	}
	return &SendGuard{bucket: bucket, cfg: cfg}, nil
}

// Wait blocks until a token is available or ctx is cancelled. A nil
// guard is treated as "no guard installed" and returns immediately —
// this lets callers keep the guard optional in tests without forcing
// every callsite into a nil-check.
func (g *SendGuard) Wait(ctx context.Context) error {
	if g == nil {
		return nil
	}
	if g.bucket == nil {
		return errors.New("send guard: not initialised")
	}
	return g.bucket.Wait(ctx)
}

// Config returns the resolved (post-default) configuration so wiring
// and observability code can introspect what the guard is actually
// using without reaching into private fields.
func (g *SendGuard) Config() SendRateLimit {
	if g == nil {
		return SendRateLimit{}
	}
	return g.cfg
}
