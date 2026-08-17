package tg

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/kar43lov/lazytg/internal/core/events"
)

// DefaultPollingInterval is the cadence between successive history pulls
// when the polling fallback is engaged. Three seconds is a deliberate
// trade-off: faster than Telegram Desktop's typical reconnect backoff,
// slow enough to stay under the 10 req/sec rate-limit budget when
// covering a handful of active chats per tick.
const DefaultPollingInterval = 3 * time.Second

// PolledChat describes one chat the fallback should keep polling.
// LastSeenID acts as the message-id watermark so the fallback only
// publishes genuinely new messages even when Telegram returns the
// same window twice.
type PolledChat struct {
	ChatID     int64
	AccessHash int64
	Type       string
	LastSeenID int64
}

// PollingActiveSource is supplied to PollingFallback so the caller can
// rotate the active-chat list (open/close conversations) without
// recreating the fallback. The implementation must be safe for
// concurrent reads.
type PollingActiveSource interface {
	Active(ctx context.Context) ([]PolledChat, error)
}

// MessagePollingFetcher is the lightweight polling-friendly contract on
// top of HistoryFetcher: returns the latest message id and a small
// payload describing the message. The compact return type keeps the
// fallback from importing gotd's Message types into the polling loop.
type MessagePollingFetcher interface {
	Latest(ctx context.Context, chat PolledChat, sinceID int64) ([]events.MessageReceived, int64, error)
}

// PollingFallback drives a periodic pull of active chats when the
// updates.Manager is unavailable. It publishes new MessageReceived
// events onto the bus exactly like UpdatesDispatcher would, so
// consumers downstream do not need to know which path delivered the
// data.
//
// The fallback is a strict opt-in: enable it via the --polling CLI
// flag or as an automatic fallback after N consecutive Manager
// failures (the supervisor lives in internal/app/wire.go in Task 11).
type PollingFallback struct {
	source   PollingActiveSource
	fetcher  MessagePollingFetcher
	bus      *events.Bus
	log      *slog.Logger
	interval time.Duration

	mu        sync.Mutex
	watermark map[int64]int64
}

// NewPollingFallback wires a fallback. interval <= 0 falls back to
// DefaultPollingInterval; log nil yields a no-op logger.
func NewPollingFallback(source PollingActiveSource, fetcher MessagePollingFetcher, bus *events.Bus, log *slog.Logger, interval time.Duration) *PollingFallback {
	if interval <= 0 {
		interval = DefaultPollingInterval
	}
	if log == nil {
		log = slog.New(noopHandler{})
	}
	return &PollingFallback{
		source:    source,
		fetcher:   fetcher,
		bus:       bus,
		log:       log,
		interval:  interval,
		watermark: make(map[int64]int64),
	}
}

// Run blocks until ctx is cancelled, polling every interval. Errors are
// logged and the loop continues — a transient network blip should never
// take the fallback down.
func (p *PollingFallback) Run(ctx context.Context) error {
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			p.tick(ctx)
		}
	}
}

// tick performs one round: snapshot active chats, fetch each one,
// publish new messages. Per-chat failures are logged but do not abort
// the round.
func (p *PollingFallback) tick(ctx context.Context) {
	chats, err := p.source.Active(ctx)
	if err != nil {
		p.log.Warn("polling: active source failed", "err", err)
		return
	}
	for _, c := range chats {
		since := p.since(c)
		msgs, latest, err := p.fetcher.Latest(ctx, c, since)
		if err != nil {
			p.log.Warn("polling: fetch failed", "chat_id", c.ChatID, "err", err)
			continue
		}
		for _, m := range msgs {
			p.bus.Publish(m)
		}
		if latest > since {
			p.advance(c.ChatID, latest)
		}
	}
}

// since returns the highest message id we have seen for chat. Zero on
// first poll so the very first tick still publishes the latest batch.
func (p *PollingFallback) since(c PolledChat) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if w, ok := p.watermark[c.ChatID]; ok {
		return w
	}
	return c.LastSeenID
}

// advance bumps the watermark for chat to id. Concurrent callers are
// serialised on the same mutex as since().
func (p *PollingFallback) advance(chatID, id int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.watermark[chatID] = id
}
