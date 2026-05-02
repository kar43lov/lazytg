package sync

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pgmac/lazytg/internal/core/domain"
	"github.com/pgmac/lazytg/internal/core/events"
)

// stubProvider is a minimal HistoryProvider that returns scripted batches
// keyed by the requested offsetID. Mocking at this level keeps the test
// free of gotd while still exercising the LoadInitial happy path.
type stubProvider struct {
	mu       sync.Mutex
	calls    int
	batches  map[int][]domain.Message // by offsetID
	hasMore  bool
	err      error
	gotPeer  []int64
	gotLimit []int
}

func (p *stubProvider) Fetch(_ context.Context, peerID, _ int64, _ string, limit, offsetID int) ([]domain.Message, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.gotPeer = append(p.gotPeer, peerID)
	p.gotLimit = append(p.gotLimit, limit)
	if p.err != nil {
		return nil, false, p.err
	}
	return p.batches[offsetID], p.hasMore, nil
}

// stubPeers resolves a fixed PeerInfo for every chatID. Returning
// ErrPeerUnknown for an unset chat lets us cover the not-found branch.
type stubPeers struct {
	known map[int64]PeerInfo
}

func (p *stubPeers) Lookup(_ context.Context, chatID int64) (PeerInfo, error) {
	if peer, ok := p.known[chatID]; ok {
		return peer, nil
	}
	return PeerInfo{}, ErrPeerUnknown
}

// memStore is an in-memory MessageStore that records every SaveMessages
// call and de-duplicates by (ChatID, ID) — same semantics as the real
// SQLite repo's UPSERT, but without any sql plumbing in the test.
type memStore struct {
	mu       sync.Mutex
	saved    map[int64]map[int64]domain.Message
	calls    int
	saveErr  error
	totalIns int
}

func newMemStore() *memStore {
	return &memStore{saved: make(map[int64]map[int64]domain.Message)}
}

func (s *memStore) SaveMessages(_ context.Context, msgs []domain.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.saveErr != nil {
		return s.saveErr
	}
	for _, m := range msgs {
		bucket, ok := s.saved[m.ChatID]
		if !ok {
			bucket = make(map[int64]domain.Message)
			s.saved[m.ChatID] = bucket
		}
		if _, exists := bucket[m.ID]; !exists {
			s.totalIns++
		}
		bucket[m.ID] = m
	}
	return nil
}

func (s *memStore) count(chatID int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.saved[chatID])
}

func sampleMessages(chatID int64, ids ...int64) []domain.Message {
	out := make([]domain.Message, 0, len(ids))
	now := time.Unix(1_700_000_000, 0).UTC()
	for _, id := range ids {
		out = append(out, domain.Message{
			ID:     id,
			ChatID: chatID,
			Date:   now.Add(time.Duration(id) * time.Minute),
			Text:   "msg",
		})
	}
	return out
}

func TestHistoryService_LoadInitial_PersistsAndAnnounces(t *testing.T) {
	t.Parallel()
	const chatID = 42
	provider := &stubProvider{
		batches: map[int][]domain.Message{
			0: sampleMessages(chatID, 1, 2, 3, 4, 5),
		},
	}
	peers := &stubPeers{known: map[int64]PeerInfo{
		chatID: {AccessHash: 0xCAFE, Type: string(domain.ChatTypePrivate)},
	}}
	store := newMemStore()
	bus := events.New()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := bus.Subscribe(ctx)

	svc := NewHistoryService(provider, peers, store, bus, nil)
	if err := svc.LoadInitial(ctx, chatID); err != nil {
		t.Fatalf("LoadInitial: %v", err)
	}
	if got := store.count(chatID); got != 5 {
		t.Fatalf("store count chat=%d: want 5, got %d", chatID, got)
	}
	if provider.gotLimit[0] != initialBatchSize {
		t.Fatalf("provider limit: want %d, got %d", initialBatchSize, provider.gotLimit[0])
	}

	select {
	case ev := <-sub:
		du, ok := ev.(events.DialogUpdated)
		if !ok || du.ChatID != chatID {
			t.Fatalf("unexpected event %T %+v", ev, ev)
		}
	case <-time.After(time.Second):
		t.Fatalf("no DialogUpdated event published")
	}
}

func TestHistoryService_LoadInitial_DeduplicatesAcrossCalls(t *testing.T) {
	t.Parallel()
	const chatID = 7
	msgs := sampleMessages(chatID, 1, 2, 3)
	provider := &stubProvider{batches: map[int][]domain.Message{0: msgs}}
	peers := &stubPeers{known: map[int64]PeerInfo{
		chatID: {AccessHash: 1, Type: string(domain.ChatTypeGroup)},
	}}
	store := newMemStore()

	svc := NewHistoryService(provider, peers, store, events.New(), nil)
	if err := svc.LoadInitial(context.Background(), chatID); err != nil {
		t.Fatalf("first LoadInitial: %v", err)
	}
	if err := svc.LoadInitial(context.Background(), chatID); err != nil {
		t.Fatalf("second LoadInitial: %v", err)
	}
	if got := store.count(chatID); got != 3 {
		t.Fatalf("after two loads with identical batch: want 3 unique rows, got %d", got)
	}
	if store.totalIns != 3 {
		t.Fatalf("dedup failed: %d INSERTs produced new rows, want 3", store.totalIns)
	}
}

func TestHistoryService_LoadInitial_PropagatesPeerErr(t *testing.T) {
	t.Parallel()
	provider := &stubProvider{}
	peers := &stubPeers{known: map[int64]PeerInfo{}}
	svc := NewHistoryService(provider, peers, newMemStore(), events.New(), nil)

	err := svc.LoadInitial(context.Background(), 999)
	if err == nil || !errors.Is(err, ErrPeerUnknown) {
		t.Fatalf("want ErrPeerUnknown, got %v", err)
	}
	if provider.calls != 0 {
		t.Fatalf("provider should not be called when peer is unknown, calls=%d", provider.calls)
	}
}

func TestHistoryService_LoadInitial_EmptyBatchSilent(t *testing.T) {
	t.Parallel()
	provider := &stubProvider{batches: map[int][]domain.Message{0: nil}}
	peers := &stubPeers{known: map[int64]PeerInfo{
		1: {Type: string(domain.ChatTypePrivate)},
	}}
	store := newMemStore()
	svc := NewHistoryService(provider, peers, store, events.New(), nil)

	if err := svc.LoadInitial(context.Background(), 1); err != nil {
		t.Fatalf("LoadInitial empty: %v", err)
	}
	if store.calls != 0 {
		t.Fatalf("store should not be called on empty batch, calls=%d", store.calls)
	}
}

func TestAsFloodWait(t *testing.T) {
	t.Parallel()
	if _, ok := AsFloodWait(errors.New("boom")); ok {
		t.Fatalf("plain error should not be flood wait")
	}
	d, ok := AsFloodWait(&FloodWaitError{RetryAfter: 5 * time.Second})
	if !ok || d != 5*time.Second {
		t.Fatalf("AsFloodWait: ok=%v d=%v", ok, d)
	}
}
