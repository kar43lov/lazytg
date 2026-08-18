package tg

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/events"
)

type fakeActiveSource struct {
	mu    sync.Mutex
	chats []PolledChat
	err   error
	calls int
}

func (s *fakeActiveSource) Active(_ context.Context) ([]PolledChat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	out := make([]PolledChat, len(s.chats))
	copy(out, s.chats)
	return out, nil
}

type fakeFetcher struct {
	mu      sync.Mutex
	calls   []PolledChat
	answers map[int64][]events.MessageReceived
	latest  map[int64]int64
	err     error
}

func (f *fakeFetcher) Latest(_ context.Context, c PolledChat, since int64) ([]events.MessageReceived, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	if f.err != nil {
		return nil, 0, f.err
	}
	all := f.answers[c.ChatID]
	out := all[:0:0]
	for _, m := range all {
		if m.MessageID > since {
			out = append(out, m)
		}
	}
	return out, f.latest[c.ChatID], nil
}

func TestPollingFallback_PublishesNewMessages(t *testing.T) {
	t.Parallel()

	bus := events.New()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := bus.Subscribe(ctx)

	source := &fakeActiveSource{chats: []PolledChat{{ChatID: 11, Type: "private", LastSeenID: 0}}}
	fetcher := &fakeFetcher{
		answers: map[int64][]events.MessageReceived{
			11: {{ChatID: 11, MessageID: 1, Text: "first"}, {ChatID: 11, MessageID: 2, Text: "second"}},
		},
		latest: map[int64]int64{11: 2},
	}
	fb := NewPollingFallback(source, fetcher, bus, nil, 10*time.Millisecond)

	go func() { _ = fb.Run(ctx) }()

	got := drainEvents(t, ch, 2, time.Second)
	ids := []int64{got[0].(events.MessageReceived).MessageID, got[1].(events.MessageReceived).MessageID}
	if (ids[0] != 1 && ids[0] != 2) || ids[0]+ids[1] != 3 {
		t.Fatalf("expected ids {1,2}, got %v", ids)
	}
}

func TestPollingFallback_RespectsWatermark(t *testing.T) {
	t.Parallel()

	bus := events.New()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := bus.Subscribe(ctx)

	source := &fakeActiveSource{chats: []PolledChat{{ChatID: 7}}}
	fetcher := &fakeFetcher{
		answers: map[int64][]events.MessageReceived{
			7: {{ChatID: 7, MessageID: 5, Text: "msg5"}},
		},
		latest: map[int64]int64{7: 5},
	}
	fb := NewPollingFallback(source, fetcher, bus, nil, 5*time.Millisecond)
	go func() { _ = fb.Run(ctx) }()

	first := drainEvents(t, ch, 1, time.Second)
	if first[0].(events.MessageReceived).MessageID != 5 {
		t.Fatalf("first event mismatch: %+v", first[0])
	}

	// Wait for several ticks so the same response would have been
	// re-published if the watermark were not honoured.
	time.Sleep(50 * time.Millisecond)

	select {
	case ev := <-ch:
		t.Fatalf("watermark broken: re-published %+v", ev)
	default:
	}
}

func TestPollingFallback_KeepsRunningOnFetcherError(t *testing.T) {
	t.Parallel()

	bus := events.New()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	source := &fakeActiveSource{chats: []PolledChat{{ChatID: 1}}}
	fetcher := &fakeFetcher{err: errors.New("network down")}
	fb := NewPollingFallback(source, fetcher, bus, nil, 5*time.Millisecond)

	go func() { _ = fb.Run(ctx) }()

	deadline := time.After(time.Second)
	for {
		fetcher.mu.Lock()
		got := len(fetcher.calls)
		fetcher.mu.Unlock()
		if got >= 3 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("fetcher only called %d times, expected loop to keep ticking", got)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestPollingFallback_DefaultsInterval(t *testing.T) {
	t.Parallel()
	fb := NewPollingFallback(nil, nil, events.New(), nil, 0)
	if fb.interval != DefaultPollingInterval {
		t.Fatalf("interval = %v, want %v", fb.interval, DefaultPollingInterval)
	}
}

func drainEvents(t *testing.T, ch <-chan events.Event, n int, total time.Duration) []events.Event {
	t.Helper()
	out := make([]events.Event, 0, n)
	deadline := time.After(total)
	for len(out) < n {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed early; got %d/%d", len(out), n)
			}
			out = append(out, ev)
		case <-deadline:
			t.Fatalf("timed out after %v; got %d/%d events", total, len(out), n)
		}
	}
	return out
}

// TestPollingFallback_HonoursTheStoredWatermark covers the two update paths
// running at once. The fallback polls and lands on message 8, so its own
// watermark is 8. The live dispatcher then delivers message 9 and it gets
// persisted, which makes the source report 9 as LastSeenID on the next tick.
// Preferring the in-memory watermark there republishes message 9 — the
// fallback generating duplicates of exactly the traffic it exists to back up,
// once per tick for as long as that chat stays quiet.
func TestPollingFallback_HonoursTheStoredWatermark(t *testing.T) {
	t.Parallel()

	bus := events.New()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := bus.Subscribe(ctx)

	source := &fakeActiveSource{chats: []PolledChat{{ChatID: 7}}}
	fetcher := &fakeFetcher{
		answers: map[int64][]events.MessageReceived{
			7: {{ChatID: 7, MessageID: 8, Text: "found by polling"}},
		},
		latest: map[int64]int64{7: 8},
	}
	fb := NewPollingFallback(source, fetcher, bus, nil, 5*time.Millisecond)
	go func() { _ = fb.Run(ctx) }()

	// Tick one: the fallback finds message 8 itself and sets its watermark.
	first := drainEvents(t, ch, 1, time.Second)
	if got := first[0].(events.MessageReceived).MessageID; got != 8 {
		t.Fatalf("first event = %d want 8", got)
	}

	// The live path now delivers message 9 and stores it, so the source —
	// which reads the mirror — starts reporting it as the newest seen.
	source.mu.Lock()
	source.chats = []PolledChat{{ChatID: 7, LastSeenID: 9}}
	source.mu.Unlock()
	fetcher.mu.Lock()
	fetcher.answers[7] = append(fetcher.answers[7],
		events.MessageReceived{ChatID: 7, MessageID: 9, Text: "delivered by the dispatcher"})
	fetcher.latest[7] = 9
	fetcher.mu.Unlock()

	time.Sleep(60 * time.Millisecond)
	select {
	case ev := <-ch:
		t.Fatalf("republished a message the live path had already stored: %+v", ev)
	default:
	}
}

// countingHandler counts slog records by level so a test can assert on log
// volume rather than on log text.
type countingHandler struct {
	mu     sync.Mutex
	counts map[slog.Level]int
}

func newCountingHandler() *countingHandler {
	return &countingHandler{counts: make(map[slog.Level]int)}
}

func (h *countingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *countingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.counts[r.Level]++
	return nil
}

func (h *countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *countingHandler) WithGroup(string) slog.Handler      { return h }

func (h *countingHandler) count(l slog.Level) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.counts[l]
}

// TestPollingFallback_DoesNotFloodTheLogDuringAnOutage covers what a dropped
// connection does to a fallback that keeps ticking. Every poll fails for
// every polled chat, so warning on each one is 60 lines a minute — enough to
// rotate the log file and carry off the diagnostics that explain the outage.
// One warn opens the streak, the rest are debug.
func TestPollingFallback_DoesNotFloodTheLogDuringAnOutage(t *testing.T) {
	t.Parallel()

	handler := newCountingHandler()
	source := &fakeActiveSource{chats: []PolledChat{{ChatID: 7}}}
	fetcher := &fakeFetcher{err: errors.New("network down")}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	fb := NewPollingFallback(source, fetcher, events.New(), slog.New(handler), time.Millisecond)
	go func() { _ = fb.Run(ctx) }()

	time.Sleep(80 * time.Millisecond)
	cancel()

	if got := handler.count(slog.LevelWarn); got != 1 {
		t.Fatalf("warn lines = %d want exactly 1 for one failure streak", got)
	}
	if handler.count(slog.LevelDebug) == 0 {
		t.Fatal("no debug lines — the repeated failures went unrecorded entirely")
	}
}
