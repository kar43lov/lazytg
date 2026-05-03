package search

import (
	"context"
	"log/slog"
	"sync"
)

// LazyTrigger arranges for a single full reindex pass to run in the
// background the first time a search is issued. Subsequent calls are
// no-ops — the goroutine started on the first call carries the work
// through to completion (or until its context cancels) and the trigger
// stays "armed" forever. UI code calls EnsureIndexed unconditionally
// from the search code path; correctness depends on the triggered-once
// flag, not on caller ordering.
type LazyTrigger struct {
	reindex *ReindexService
	log     *slog.Logger

	mu        sync.Mutex
	triggered bool
	done      chan struct{}
}

// NewLazyTrigger wires a LazyTrigger. log nil installs a discard handler.
func NewLazyTrigger(reindex *ReindexService, log *slog.Logger) *LazyTrigger {
	if log == nil {
		log = slog.New(discardHandler{})
	}
	return &LazyTrigger{reindex: reindex, log: log, done: make(chan struct{})}
}

// EnsureIndexed runs reindex.RunAll in a goroutine the first time it is
// called and returns nil immediately. Subsequent calls are no-ops. The
// returned error is always nil — failures are reported via the bus
// events emitted by ReindexService and via the configured logger.
//
// The passed ctx scopes the background goroutine: cancelling ctx (e.g.
// app shutdown) unwinds the reindex pass cleanly. Pass an app-scoped
// context, not a request-scoped one.
func (l *LazyTrigger) EnsureIndexed(ctx context.Context) error {
	l.mu.Lock()
	if l.triggered {
		l.mu.Unlock()
		return nil
	}
	l.triggered = true
	l.mu.Unlock()

	go func() {
		defer close(l.done)
		if err := l.reindex.RunAll(ctx); err != nil {
			l.log.LogAttrs(ctx, slog.LevelWarn, "lazy reindex run failed",
				slog.String("err", err.Error()))
		}
	}()
	return nil
}

// Triggered reports whether EnsureIndexed has already started a background
// pass. Exported for tests; UI code does not need to call it.
func (l *LazyTrigger) Triggered() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.triggered
}

// Done returns a channel that closes when the background reindex pass
// (started by the first EnsureIndexed call) exits. Callers that care
// about completion timing — typically tests — can wait on it. Calling
// before EnsureIndexed returns a channel that never closes.
func (l *LazyTrigger) Done() <-chan struct{} { return l.done }
