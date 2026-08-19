package sync

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
)

// latencyStore records the wall-clock duration between event publish and
// SaveMessage entry. Storage itself is a no-op so the measurement reflects
// only the bus + dispatch path — the slowest possible storage backend
// would only add to this number, so a passing benchmark here is a
// necessary (not sufficient) condition for the SLA.
type latencyStore struct {
	mu        sync.Mutex
	latencies []time.Duration
}

// stamps maps message IDs to publish times so the SaveMessage hook can
// look up the right publish moment without serialising with the
// bus-driven goroutine.
type stamps struct {
	mu sync.Mutex
	m  map[int64]time.Time
}

func (s *stamps) put(id int64, t time.Time) {
	s.mu.Lock()
	s.m[id] = t
	s.mu.Unlock()
}

func (s *stamps) take(id int64) time.Time {
	s.mu.Lock()
	t := s.m[id]
	delete(s.m, id)
	s.mu.Unlock()
	return t
}

type stampedStore struct {
	stamps *stamps
	out    *latencyStore
}

// EnsureChat satisfies coresync.LiveStore. The fakes carry no chats
// table, so there is no parent row to create.
func (s *stampedStore) EnsureChat(_ context.Context, _ int64, _ domain.ChatType, _ time.Time) error {
	return nil
}

func (s *stampedStore) SaveMessage(_ context.Context, m domain.Message) error {
	emit := s.stamps.take(m.ID)
	s.out.mu.Lock()
	s.out.latencies = append(s.out.latencies, time.Since(emit))
	s.out.mu.Unlock()
	return nil
}

// BenchmarkLiveUpdateLatency drives 1000 events through the bus into a
// LiveService and asserts the p95 ingest latency stays below the 500ms
// SLA captured in the Stage 2 acceptance criteria.
//
// Events are published at a sustainable rate so the bus's 64-deep
// per-subscriber buffer never overflows; production updates arrive
// even more spread out (Telegram pushes one update at a time, not
// 1000 at once), so the benchmark is a worst-case for the path we
// actually ship without measuring an artificial drop scenario.
func BenchmarkLiveUpdateLatency(b *testing.B) {
	const eventsCount = 1000
	const sla = 500 * time.Millisecond

	bus := events.New()
	st := &stamps{m: make(map[int64]time.Time, eventsCount)}
	rec := &latencyStore{}
	svc := NewLiveService(&stampedStore{stamps: st, out: rec}, bus, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = svc.Start(ctx)

	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		rec.mu.Lock()
		rec.latencies = rec.latencies[:0]
		rec.mu.Unlock()
		st.mu.Lock()
		clear(st.m)
		st.mu.Unlock()
		idBase := int64(n) * eventsCount
		base := time.Now()
		for i := 1; i <= eventsCount; i++ {
			id := idBase + int64(i)
			when := base.Add(time.Duration(i) * time.Microsecond)
			st.put(id, time.Now())
			bus.Publish(events.MessageReceived{ChatID: 1, MessageID: id, Date: when})
			// Drip-feed: every 32 events, give the consumer a chance
			// to drain so the bounded bus buffer never overflows.
			if i%32 == 0 {
				waitForDrain(b, rec, i)
			}
		}
		waitForDrain(b, rec, eventsCount)
		rec.mu.Lock()
		got := append([]time.Duration(nil), rec.latencies...)
		rec.mu.Unlock()
		sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
		p95 := got[(95*len(got)/100)-1]
		if p95 > sla {
			b.Fatalf("p95 latency = %v, SLA = %v", p95, sla)
		}
	}
}

// waitForDrain blocks until at least target latencies have been recorded
// or the deadline expires. Used as a back-pressure valve in the
// benchmark loop.
func waitForDrain(b *testing.B, rec *latencyStore, target int) {
	b.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec.mu.Lock()
		got := len(rec.latencies)
		rec.mu.Unlock()
		if got >= target {
			return
		}
		if time.Now().After(deadline) {
			b.Fatalf("timed out waiting for events to drain (got %d/%d)", got, target)
		}
		time.Sleep(100 * time.Microsecond)
	}
}
