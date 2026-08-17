package tg

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/gotd/td/tgerr"

	coresync "github.com/kar43lov/lazytg/internal/core/sync"
)

// DefaultFloodWaitMaxRetries caps how many FLOOD_WAIT cycles a single RPC
// is allowed to absorb before we give up. Telegram occasionally answers
// with second/third FLOOD_WAITs in a row when a client is rate-limited
// hard; without a cap we would loop indefinitely on a stuck account.
const DefaultFloodWaitMaxRetries = 3

// FloodWaiter retries fn while the server returns FLOOD_WAIT, waiting the
// suggested retry duration between attempts. It is the gotd-aware sibling
// of internal/core/sync's reactive FloodWaitError handling: every wait is
// logged at warn level so the debug-bundle reproducer captures the cadence.
//
// On context cancellation the Waiter returns the ctx error as-is. On
// non-FLOOD_WAIT errors it propagates the original error untouched. After
// MaxRetries consecutive FLOOD_WAITs it surfaces a *coresync.FloodWaitError
// so callers in core/ keep their existing AsFloodWait inspection working
// without importing gotd themselves.
type FloodWaiter struct {
	MaxRetries int
	Log        *slog.Logger
}

// NewFloodWaiter returns a Waiter with sensible defaults: 3 retries and a
// no-op logger when log is nil. Tests can override either field directly.
func NewFloodWaiter(log *slog.Logger) *FloodWaiter {
	if log == nil {
		log = slog.New(noopHandler{})
	}
	return &FloodWaiter{
		MaxRetries: DefaultFloodWaitMaxRetries,
		Log:        log,
	}
}

// Do invokes fn and re-runs it whenever the server reports FLOOD_WAIT.
// The label is included in every log line so tests and prod operators can
// tell which RPC tripped the limiter.
func (w *FloodWaiter) Do(ctx context.Context, label string, fn func(ctx context.Context) error) error {
	for attempt := 0; ; attempt++ {
		err := fn(ctx)
		if err == nil {
			return nil
		}
		d, ok := tgerr.AsFloodWait(err)
		if !ok {
			return err
		}
		if attempt >= w.MaxRetries {
			w.Log.Warn("flood wait: max retries exceeded",
				"rpc", label, "retry_after", d, "max_retries", w.MaxRetries)
			return &coresync.FloodWaitError{RetryAfter: d}
		}
		w.Log.Warn("flood wait: sleeping",
			"rpc", label, "retry_after", d, "attempt", attempt+1, "max_retries", w.MaxRetries)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}
}

// noopHandler discards all log records. Mirrors internal/core/sync so the
// tg package stays self-contained for test wiring.
type noopHandler struct{}

func (noopHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (noopHandler) Handle(context.Context, slog.Record) error { return nil }
func (h noopHandler) WithAttrs([]slog.Attr) slog.Handler      { return h }
func (h noopHandler) WithGroup(string) slog.Handler           { return h }

// AsFloodWaitDuration peeks at err and returns the FLOOD_WAIT retry-after
// duration if the server signalled one. The helper exists so tests can
// build the same gotd error shape we react to without re-importing tgerr.
func AsFloodWaitDuration(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	if d, ok := tgerr.AsFloodWait(err); ok {
		return d, true
	}
	var fw *coresync.FloodWaitError
	if errors.As(err, &fw) {
		return fw.RetryAfter, true
	}
	return 0, false
}
