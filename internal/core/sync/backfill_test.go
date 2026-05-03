package sync

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
)

// scriptedProvider returns a FloodWait on the first call (with a 50ms wait
// so the test stays fast) and the actual batch on the second.
type scriptedProvider struct {
	first  atomic.Bool
	called atomic.Int32
}

func (p *scriptedProvider) Fetch(_ context.Context, peerID, _ int64, _ string, _, _ int) ([]domain.Message, bool, error) {
	p.called.Add(1)
	if p.first.CompareAndSwap(false, true) {
		return nil, false, &FloodWaitError{RetryAfter: 50 * time.Millisecond}
	}
	return sampleMessages(peerID, 1, 2), false, nil
}

func TestBackfillService_RetriesAfterFloodWait(t *testing.T) {
	t.Parallel()
	const chatID = 11
	provider := &scriptedProvider{}
	peers := &stubPeers{known: map[int64]PeerInfo{
		chatID: {AccessHash: 1, Type: string(domain.ChatTypePrivate)},
	}}
	store := newMemStore()
	history := NewHistoryService(provider, peers, store, events.New(), nil)
	bf, err := NewBackfillService(history, nil, BackfillConfig{Rate: 1000, Burst: 10, QueueSize: 4})
	if err != nil {
		t.Fatalf("NewBackfillService: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := bf.Start(ctx)

	bf.Enqueue(chatID)
	deadline := time.After(time.Second)
	for store.count(chatID) != 2 {
		select {
		case <-deadline:
			t.Fatalf("messages did not appear within deadline (calls=%d, count=%d)",
				provider.called.Load(), store.count(chatID))
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	if got := provider.called.Load(); got != 2 {
		t.Fatalf("provider calls: want 2 (first floodwait, then success), got %d", got)
	}
	cancel()
	<-done
}

// TestBackfillService_DedupesEnqueue ensures Enqueue collapses repeats while
// a backfill is in flight. We block the provider until the test releases it,
// so multiple Enqueue calls all hit the inflight map together.
type blockingProvider struct {
	release chan struct{}
	called  atomic.Int32
}

func (p *blockingProvider) Fetch(_ context.Context, peerID, _ int64, _ string, _, _ int) ([]domain.Message, bool, error) {
	p.called.Add(1)
	<-p.release
	return sampleMessages(peerID, 1), false, nil
}

func TestBackfillService_DedupesEnqueue(t *testing.T) {
	t.Parallel()
	const chatID = 22
	provider := &blockingProvider{release: make(chan struct{})}
	peers := &stubPeers{known: map[int64]PeerInfo{
		chatID: {AccessHash: 1, Type: string(domain.ChatTypePrivate)},
	}}
	store := newMemStore()
	history := NewHistoryService(provider, peers, store, events.New(), nil)
	bf, err := NewBackfillService(history, nil, BackfillConfig{Rate: 1000, Burst: 10, QueueSize: 16})
	if err != nil {
		t.Fatalf("NewBackfillService: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := bf.Start(ctx)

	bf.Enqueue(chatID)
	// Wait until provider is in-flight so we know the inflight slot is held.
	deadline := time.After(time.Second)
	for provider.called.Load() == 0 {
		select {
		case <-deadline:
			t.Fatalf("provider was never called")
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
	for i := 0; i < 5; i++ {
		bf.Enqueue(chatID)
	}
	// Release the in-flight call. Subsequent Enqueues should be no-ops.
	close(provider.release)

	deadline = time.After(time.Second)
	for store.count(chatID) == 0 {
		select {
		case <-deadline:
			t.Fatalf("store never received a message")
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
	cancel()
	<-done

	if got := provider.called.Load(); got != 1 {
		t.Fatalf("provider should have been called exactly once due to inflight dedup, got %d", got)
	}
}

func TestBackfillService_StopsOnContextCancel(t *testing.T) {
	t.Parallel()
	provider := &stubProvider{batches: map[int][]domain.Message{0: nil}}
	peers := &stubPeers{known: map[int64]PeerInfo{}}
	history := NewHistoryService(provider, peers, newMemStore(), events.New(), nil)
	bf, err := NewBackfillService(history, nil, BackfillConfig{})
	if err != nil {
		t.Fatalf("NewBackfillService: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := bf.Start(ctx)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Start goroutine did not exit on ctx cancel")
	}
}

func TestNewBackfillService_DefaultsAreSet(t *testing.T) {
	t.Parallel()
	bf, err := NewBackfillService(nil, nil, BackfillConfig{})
	if err != nil {
		t.Fatalf("NewBackfillService: %v", err)
	}
	if cap(bf.queue) != 64 {
		t.Fatalf("queue size default = %d, want 64", cap(bf.queue))
	}
	if bf.limiter.rate != 10 {
		t.Fatalf("rate default = %v, want 10", bf.limiter.rate)
	}
	if bf.limiter.capacity != 30 {
		t.Fatalf("burst default = %v, want 30", bf.limiter.capacity)
	}
}
