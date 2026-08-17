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
		// would silently drop most of them. We feed in chunks of 32
		// and yield to the consumer between chunks.
		batchSize     = 32
		p95Budget     = 500 * time.Millisecond
		drainDeadline = 10 * time.Second
	)

	bus := events.New()
	store := newInMemoryStore()
	logger := slog.New(slog.DiscardHandler)
	live := coresync.NewLiveService(store, bus, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := live.Start(ctx)

	// Drip-feed eventCount events through the bus. Each chunk is followed
	// by a yield so the bus's per-subscriber buffer (64 slots) drains
	// before the next batch lands. Without the yield, the bus drops the
	// overflow and the SLA assertion below would falsely look "fast"
	// because we counted events that never reached storage.
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
		// Yield to the consumer so the buffer drains before the next chunk.
		time.Sleep(time.Millisecond)
	}

	deadline := time.Now().Add(drainDeadline)
	for {
		store.mu.Lock()
		got := len(store.saved)
		store.mu.Unlock()
		if got >= eventCount {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("LiveService did not persist %d events within %s (got %d)",
				eventCount, drainDeadline, got)
		}
		time.Sleep(2 * time.Millisecond)
	}

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
