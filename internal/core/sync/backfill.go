package sync

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// FloodWaitError is the gotd-free representation of FLOOD_WAIT signals from
// MTProto. The internal/tg layer translates *tgerr.Error into this concrete
// type so the backfill loop can react to rate limits without importing gotd.
type FloodWaitError struct {
	RetryAfter time.Duration
}

// Error satisfies the error interface.
func (e *FloodWaitError) Error() string {
	return "flood wait: retry after " + e.RetryAfter.String()
}

// AsFloodWait extracts a *FloodWaitError from err, if any. Returning the
// duration as a second value keeps the call sites (`if d, ok := AsFloodWait(...)`)
// terse while still letting errors.As walk wrapped chains.
func AsFloodWait(err error) (time.Duration, bool) {
	var fw *FloodWaitError
	if errors.As(err, &fw) {
		return fw.RetryAfter, true
	}
	return 0, false
}

// BackfillService is a small worker pool wrapping HistoryService. It owns
// a request queue (chatID channel) and a token-bucket gate so that a UI
// asking to backfill many chats in quick succession does not produce a
// FLOOD_WAIT-storm.
//
// The concurrency model is deliberately minimal: one consumer goroutine
// drains the queue. With the default 10 req/sec rate-limit, parallel
// fetches would not buy us throughput anyway and would only complicate
// FLOOD_WAIT bookkeeping.
type BackfillService struct {
	history *HistoryService
	limiter *TokenBucket
	log     *slog.Logger
	queue   chan int64

	// inflight tracks chatIDs currently queued or in-flight so duplicate
	// Enqueue calls collapse into a single backfill — useful when the UI
	// requests a refresh on every focus change.
	mu       sync.Mutex
	inflight map[int64]struct{}
}

// BackfillConfig groups optional knobs so future additions don't churn the
// constructor signature. Zero values are sensible defaults.
type BackfillConfig struct {
	// QueueSize bounds the number of pending chat backfills. Defaults to
	// 64 — enough to absorb a "select all chats" UI action without
	// allocating per-Enqueue, while still bounding memory.
	QueueSize int
	// Rate is tokens per second. Default 10.
	Rate float64
	// Burst is the bucket capacity. Default 30.
	Burst int
}

// NewBackfillService constructs a backfill worker. cfg may be the zero value;
// any zero field is replaced with the documented default.
func NewBackfillService(history *HistoryService, log *slog.Logger, cfg BackfillConfig) (*BackfillService, error) {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 64
	}
	if cfg.Rate <= 0 {
		cfg.Rate = 10
	}
	if cfg.Burst <= 0 {
		cfg.Burst = 30
	}
	if log == nil {
		log = slog.New(noopHandler{})
	}
	limiter, err := NewTokenBucket(cfg.Rate, cfg.Burst)
	if err != nil {
		return nil, err
	}
	return &BackfillService{
		history:  history,
		limiter:  limiter,
		log:      log,
		queue:    make(chan int64, cfg.QueueSize),
		inflight: make(map[int64]struct{}),
	}, nil
}

// Enqueue schedules a backfill for chatID. Repeat calls for the same id
// while a backfill is still pending are coalesced. Calls block briefly only
// when the queue is full; that backpressure is intentional — it keeps the
// caller's goroutine alive on the path so a runaway producer notices and
// slows down rather than ballooning memory.
func (b *BackfillService) Enqueue(chatID int64) {
	b.mu.Lock()
	if _, exists := b.inflight[chatID]; exists {
		b.mu.Unlock()
		return
	}
	b.inflight[chatID] = struct{}{}
	b.mu.Unlock()
	b.queue <- chatID
}

// Start spawns the consumer goroutine and returns a channel closed when
// the loop exits (after ctx cancellation). The channel lets call sites in
// internal/app/wire.go wait for clean shutdown without exporting the
// underlying goroutine.
func (b *BackfillService) Start(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case chatID := <-b.queue:
				b.process(ctx, chatID)
			}
		}
	}()
	return done
}

// process executes one backfill: rate-limit, fetch, retry on FLOOD_WAIT,
// release the inflight slot. We log each step at info level so the
// debug-bundle reproducer captures the backfill cadence.
func (b *BackfillService) process(ctx context.Context, chatID int64) {
	defer func() {
		b.mu.Lock()
		delete(b.inflight, chatID)
		b.mu.Unlock()
	}()
	for attempt := 0; ; attempt++ {
		if err := b.limiter.Wait(ctx); err != nil {
			b.log.Debug("backfill: limiter wait cancelled", "chat_id", chatID, "err", err)
			return
		}
		err := b.history.LoadInitial(ctx, chatID)
		if err == nil {
			return
		}
		if d, ok := AsFloodWait(err); ok {
			b.log.Warn("backfill: flood wait", "chat_id", chatID, "retry_after", d, "attempt", attempt)
			select {
			case <-ctx.Done():
				return
			case <-time.After(d):
			}
			continue
		}
		// Non-recoverable error: log and drop. A retry policy for transient
		// network errors lives in ReconnectManager (Stage 2 / Task 4) so
		// each layer keeps a single responsibility.
		b.log.Error("backfill: load initial failed", "chat_id", chatID, "err", err)
		return
	}
}
