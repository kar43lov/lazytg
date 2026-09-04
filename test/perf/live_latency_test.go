// Package perf hosts SLA-bearing benchmarks for the lazytg TUI.
// Tests in this package gate Stage 2 acceptance: live-update latency
// must stay under 500ms p95 from MTProto-side bus emit to repo write.
//
// Each benchmark intentionally re-builds the full pipeline (fake gotd
// dispatcher → events.Bus → coresync.LiveService → in-memory repo) so a
// regression in any layer surfaces here rather than hiding behind a
// component-level micro-benchmark.
package perf

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
	coresync "github.com/kar43lov/lazytg/internal/core/sync"
)

// inMemoryStore is a minimal LiveStore that records each Save's
// completion time so the benchmark can compute end-to-end latency
// without sharing state with the producer goroutine. Each saved
// message carries its emit timestamp via the FromID-encoded sequence
// number — the producer stores `time.Now().UnixNano()` of the emit in
// a side map keyed by message id, and SaveMessage looks it up to
// record the emit→save delta. This is the only honest way to measure
// the bus → drain dispatch delay end-to-end since the producer and
// consumer goroutines do not share a synchronous code path.
type inMemoryStore struct {
	mu        sync.Mutex
	saved     []domain.Message
	durations []time.Duration
	emits     map[int64]time.Time
	clock     func() time.Time
}

func newInMemoryStore() *inMemoryStore {
	return &inMemoryStore{
		emits: make(map[int64]time.Time),
		clock: time.Now,
	}
}

// recordEmit is called by the producer right before bus.Publish so
// SaveMessage can compute end-to-end latency without sharing the emit
// instant via a side channel.
func (s *inMemoryStore) recordEmit(messageID int64, at time.Time) {
	s.mu.Lock()
	s.emits[messageID] = at
	s.mu.Unlock()
}

// EnsureChat satisfies coresync.LiveStore. The fakes carry no chats
// table, so there is no parent row to create.
// DeleteMessages satisfies coresync.LiveStore. The benchmark never
// deletes, so this records nothing.
func (s *inMemoryStore) DeleteMessages(_ context.Context, _ int64, ids []int64) (int64, error) {
	return int64(len(ids)), nil
}

func (s *inMemoryStore) SetReactions(context.Context, int64, int64, []domain.Reaction) error {
	return nil
}

func (s *inMemoryStore) IncrementUnread(_ context.Context, _ int64) error { return nil }

func (s *inMemoryStore) EnsureChat(_ context.Context, _ int64, _ domain.ChatType, _ time.Time) (bool, error) {
	return false, nil
}

func (s *inMemoryStore) SaveMessage(_ context.Context, m domain.Message) error {
	now := s.clock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved = append(s.saved, m)
	if emit, ok := s.emits[m.ID]; ok {
		s.durations = append(s.durations, now.Sub(emit))
		delete(s.emits, m.ID)
	}
	return nil
}

// savedCount reports how many events the consumer has persisted so far.
func (s *inMemoryStore) savedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.saved)
}

// waitForSaved blocks until the store holds at least want events and returns
// the count, or fails the test when budget expires.
//
// A shortfall here means events were dropped rather than delayed, so the
// message says so: Publish is non-blocking and the bus discards for a
// subscriber whose 64-slot buffer is full (see internal/core/events). Once an
// event is dropped it is gone — no larger deadline recovers it, and a test
// that merely waits longer turns a lost-event bug into a slow failure.
func waitForSaved(t *testing.T, store *inMemoryStore, want int, budget time.Duration) int {
	t.Helper()
	if want <= 0 {
		return store.savedCount()
	}
	deadline := time.Now().Add(budget)
	for {
		got := store.savedCount()
		if got >= want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("consumer persisted %d of %d events within %s — the bus drops events for a subscriber whose buffer is full, so the shortfall is lost, not late",
				got, want, budget)
		}
		time.Sleep(time.Millisecond)
	}
}

// p95Latency returns the 95th-percentile of d. The slice is sorted in
// place; callers should pass a copy if they need the original order.
func p95Latency(d []time.Duration) time.Duration {
	if len(d) == 0 {
		return 0
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	idx := (len(d) * 95) / 100
	if idx >= len(d) {
		idx = len(d) - 1
	}
	return d[idx]
}

// TestLiveUpdateLatencySLA is the SLA gate: emit 1000 MessageReceived
// events through the bus, wait for the LiveService drain to persist
// every one, then assert p95 of the per-event latency is under 500ms.
//
// We expose this as a Test (not a Benchmark) because Go's bench harness
// expects a `b.N` loop and our actual gate is a percentile, not
// allocations or wall-clock. The plan calls for both a benchmark and a
// gate; the benchmark below (BenchmarkLiveUpdateLatency) reports
// throughput, while this Test fails the build when latency regresses.
func TestLiveUpdateLatencySLA(t *testing.T) {
	const (
		eventCount = 1000
		// drip-feed batch size: bus.Subscribe uses a 64-slot buffer so
		// emitting 1000 events back-to-back from a single goroutine
		// would silently drop most of them. We feed in chunks of 32.
		batchSize = 32
		// maxInFlight bounds published-but-not-yet-persisted events, which
		// keeps the bus buffer below its 64-slot capacity by construction.
		//
		// This used to be a fixed `time.Sleep(time.Millisecond)` between
		// chunks, on the assumption that a millisecond is always enough for
		// the consumer to drain. On a loaded CI runner under -race it is
		// not: the consumer goroutine can miss two chunks in a row, the
		// buffer fills, and Publish — which is non-blocking by design —
		// discards the overflow. The test then waited out its full drain
		// deadline for events that no longer existed (observed as
		// "persisted 981 of 1000"), which looks like a latency regression
		// and is not one.
		//
		// A bound of 32 still leaves a real queue for the latency
		// percentile to measure; it only stops the producer from running
		// off the end of the buffer.
		maxInFlight = 32
		p95Budget   = 500 * time.Millisecond
		// feedDeadline covers the whole drip-feed, drainDeadline the tail
		// after the last chunk. Both are generous on purpose: the failure
		// they exist to catch is a stalled or dropping pipeline, not a slow
		// runner.
		feedDeadline  = 60 * time.Second
		drainDeadline = 10 * time.Second
	)

	bus := events.New()
	store := newInMemoryStore()
	logger := slog.New(slog.DiscardHandler)
	live := coresync.NewLiveService(store, bus, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := live.Start(ctx)

	// Drip-feed eventCount events through the bus. Each chunk waits for the
	// consumer to come within maxInFlight of the producer, so the
	// per-subscriber buffer cannot overflow. Without that backpressure the
	// bus drops the overflow and the assertions below either fail on a
	// count that can never be reached or, worse, report a flattering p95
	// computed only over the events that survived.
	for i := 0; i < eventCount; i += batchSize {
		end := i + batchSize
		if end > eventCount {
			end = eventCount
		}
		for j := i; j < end; j++ {
			messageID := int64(j) + 1
			// Stamp the emit instant before Publish so SaveMessage can
			// compute the bus → drain delta. Without this, durations
			// stays empty and the percentile assertion below is a no-op.
			store.recordEmit(messageID, time.Now())
			bus.Publish(events.MessageReceived{
				ChatID:    int64(j%10) + 1,
				MessageID: messageID,
				Text:      fmt.Sprintf("event %d", j),
				FromID:    int64(j) + 100,
				Date:      time.Unix(1700000000+int64(j), 0).UTC(),
			})
		}
		// Let the consumer catch up to within maxInFlight before publishing
		// the next chunk.
		waitForSaved(t, store, end-maxInFlight, feedDeadline)
	}

	waitForSaved(t, store, eventCount, drainDeadline)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("LiveService did not exit within 2s after cancel")
	}

	// Per-event latency: distance between when we emitted on the bus
	// and the LastIngestLatency snapshot at the time the corresponding
	// SaveMessage returned. The LiveService writes
	// LastIngestLatency on every event; computing the percentile from
	// that monotonic series gives us the right summary even when the
	// drain coalesces.
	store.mu.Lock()
	saved := append([]domain.Message(nil), store.saved...)
	store.mu.Unlock()
	if len(saved) != eventCount {
		t.Fatalf("expected %d saved events, got %d", eventCount, len(saved))
	}

	// Compute p95 from per-event durations recorded by SaveMessage —
	// each entry is the wall-clock delta between the producer's
	// recordEmit (just before bus.Publish) and the consumer's Save
	// completion. This is the bus → drain dispatch delay, which is what
	// the SLA actually polices.
	store.mu.Lock()
	durations := append([]time.Duration(nil), store.durations...)
	store.mu.Unlock()
	if len(durations) != eventCount {
		t.Fatalf("expected %d duration samples, got %d (some events missed the emit→save round-trip)",
			eventCount, len(durations))
	}
	p95 := p95Latency(durations)
	t.Logf("p95 latency over %d events: %s (budget %s)", eventCount, p95, p95Budget)
	if p95 > p95Budget {
		t.Fatalf("p95 latency %s exceeds budget %s", p95, p95Budget)
	}
}

// BenchmarkLiveUpdateLatency measures throughput of the bus → live
// drain pipeline. Useful for tracking regressions across refactors;
// the SLA gate lives in TestLiveUpdateLatencySLA above.
func BenchmarkLiveUpdateLatency(b *testing.B) {
	bus := events.New()
	store := newInMemoryStore()
	logger := slog.New(slog.DiscardHandler)
	live := coresync.NewLiveService(store, bus, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := live.Start(ctx)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bus.Publish(events.MessageReceived{
			ChatID:    1,
			MessageID: int64(i) + 1,
			Text:      "bench",
			FromID:    1,
			Date:      time.Unix(1700000000, 0).UTC(),
		})
		if i%32 == 31 {
			// Yield so the bus buffer (64-slot per subscriber) does
			// not drop events during the bench loop.
			time.Sleep(time.Microsecond)
		}
	}
	b.StopTimer()

	cancel()
	<-done
	b.ReportMetric(float64(live.LastIngestLatency().Microseconds()), "last_us/op")
}
