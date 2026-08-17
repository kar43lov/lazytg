package sync

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
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

	// freshness scripts ChatHistoryFreshness per chat as {dialogNewest,
	// localNewest}. Unset chats report the zero pair, which means "unknown" and
	// therefore "fetch" — the default every pre-existing test relies on.
	freshness  map[int64][2]time.Time
	freshErr   error
	freshCalls int
}

func newMemStore() *memStore {
	return &memStore{
		saved:     make(map[int64]map[int64]domain.Message),
		freshness: make(map[int64][2]time.Time),
	}
}

func (s *memStore) ChatHistoryFreshness(_ context.Context, chatID int64) (time.Time, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.freshCalls++
	if s.freshErr != nil {
		return time.Time{}, time.Time{}, s.freshErr
	}
	pair := s.freshness[chatID]
	return pair[0], pair[1], nil
}

// setFreshness declares what the store reports for a chat: the dialog list's
// last-message time and the newest message cached locally.
func (s *memStore) setFreshness(chatID int64, dialogNewest, localNewest time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.freshness[chatID] = [2]time.Time{dialogNewest, localNewest}
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

// TestHistoryService_LoadInitial_SkipsFetchWhenCacheIsCurrent is the fix for
// what the first live smoke showed in the log: six messages.getHistory calls for
// two chats inside half a minute, one per chat re-focus, with every message
// already in SQLite. For an unofficial client those calls are pure behavioural
// trace, and they put a network round-trip in front of a pane that had nothing
// to learn from it.
func TestHistoryService_LoadInitial_SkipsFetchWhenCacheIsCurrent(t *testing.T) {
	t.Parallel()

	newest := time.Unix(1_700_000_000, 0).UTC()
	provider := &stubProvider{batches: map[int][]domain.Message{0: sampleMessages(7, 1, 2)}}
	store := newMemStore()
	store.setFreshness(7, newest, newest)
	bus := events.New()
	svc := NewHistoryService(provider, &stubPeers{known: map[int64]PeerInfo{7: {AccessHash: 1, Type: "user"}}}, store, bus, nil)

	// The first open always goes to the server — the guard only suppresses the
	// re-focuses that follow it, which is what the burst consisted of.
	for i := range 4 {
		if err := svc.LoadInitial(context.Background(), 7); err != nil {
			t.Fatalf("LoadInitial #%d: %v", i+1, err)
		}
	}
	if provider.calls != 1 {
		t.Errorf("provider calls = %d across four opens of a chat whose cache is current, want 1", provider.calls)
	}
	if store.calls != 1 {
		t.Errorf("store writes = %d, want 1 (only the first batch had anything to persist)", store.calls)
	}
}

// TestHistoryService_LoadInitial_RefetchesAfterFreshnessWindow pins the bound on
// that guard. Both timestamps it compares live in local storage, so a transport
// drop freezes them together and the comparison keeps answering "current" — with
// no updates.Manager in v0.1, the messages missed during the drop would stay
// invisible until the next restart. Expiring the verdict is what still pulls
// them in.
func TestHistoryService_LoadInitial_RefetchesAfterFreshnessWindow(t *testing.T) {
	t.Parallel()

	newest := time.Unix(1_700_000_000, 0).UTC()
	provider := &stubProvider{batches: map[int][]domain.Message{0: sampleMessages(7, 1, 2)}}
	store := newMemStore()
	store.setFreshness(7, newest, newest)
	svc := NewHistoryService(provider, &stubPeers{known: map[int64]PeerInfo{7: {AccessHash: 1, Type: "user"}}}, store, events.New(), nil)

	clock := time.Unix(1_700_000_000, 0).UTC()
	svc.now = func() time.Time { return clock }

	if err := svc.LoadInitial(context.Background(), 7); err != nil {
		t.Fatalf("first LoadInitial: %v", err)
	}
	clock = clock.Add(freshnessWindow - time.Second)
	if err := svc.LoadInitial(context.Background(), 7); err != nil {
		t.Fatalf("LoadInitial inside the window: %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d inside the window, want 1", provider.calls)
	}

	clock = clock.Add(2 * time.Second) // now past freshnessWindow
	if err := svc.LoadInitial(context.Background(), 7); err != nil {
		t.Fatalf("LoadInitial after the window: %v", err)
	}
	if provider.calls != 2 {
		t.Errorf("provider calls = %d after the window expired, want 2", provider.calls)
	}
}

// TestHistoryService_LoadInitial_FetchesWhenCacheIsBehind is the other half:
// the guard must not turn into "never fetch again". A chat whose dialog row
// moved forward — a message arrived while the process was not watching — is
// stale and has to be pulled.
func TestHistoryService_LoadInitial_FetchesWhenCacheIsBehind(t *testing.T) {
	t.Parallel()

	local := time.Unix(1_700_000_000, 0).UTC()
	provider := &stubProvider{batches: map[int][]domain.Message{0: sampleMessages(7, 1, 2)}}
	store := newMemStore()
	store.setFreshness(7, local.Add(time.Minute), local)
	svc := NewHistoryService(provider, &stubPeers{known: map[int64]PeerInfo{7: {AccessHash: 1, Type: "user"}}}, store, events.New(), nil)

	if err := svc.LoadInitial(context.Background(), 7); err != nil {
		t.Fatalf("LoadInitial: %v", err)
	}
	if provider.calls != 1 {
		t.Errorf("provider calls = %d, want 1 for a chat that is behind", provider.calls)
	}
}

// TestHistoryService_LoadInitial_FetchesWhenFreshnessUnknown covers the first
// open of a chat: no dialog date, or nothing cached, means the guard has no
// grounds to skip.
func TestHistoryService_LoadInitial_FetchesWhenFreshnessUnknown(t *testing.T) {
	t.Parallel()

	known := time.Unix(1_700_000_000, 0).UTC()
	cases := []struct {
		name          string
		dialog, local time.Time
	}{
		{"nothing known at all", time.Time{}, time.Time{}},
		{"dialog dated, nothing cached", known, time.Time{}},
		{"cached, dialog undated", time.Time{}, known},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider := &stubProvider{batches: map[int][]domain.Message{0: sampleMessages(7, 1)}}
			store := newMemStore()
			store.setFreshness(7, tc.dialog, tc.local)
			svc := NewHistoryService(provider, &stubPeers{known: map[int64]PeerInfo{7: {AccessHash: 1, Type: "user"}}}, store, events.New(), nil)

			if err := svc.LoadInitial(context.Background(), 7); err != nil {
				t.Fatalf("LoadInitial: %v", err)
			}
			if provider.calls != 1 {
				t.Errorf("provider calls = %d, want 1", provider.calls)
			}
		})
	}
}

// TestHistoryService_LoadInitial_FetchesWhenProbeFails pins the fail-open rule:
// a cache probe that errors must never be the reason history goes missing.
func TestHistoryService_LoadInitial_FetchesWhenProbeFails(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{batches: map[int][]domain.Message{0: sampleMessages(7, 1)}}
	store := newMemStore()
	store.freshErr = errors.New("probe exploded")
	svc := NewHistoryService(provider, &stubPeers{known: map[int64]PeerInfo{7: {AccessHash: 1, Type: "user"}}}, store, events.New(), nil)

	if err := svc.LoadInitial(context.Background(), 7); err != nil {
		t.Fatalf("LoadInitial: %v", err)
	}
	if provider.calls != 1 {
		t.Errorf("provider calls = %d, want 1 — a failed probe must fall through to fetching", provider.calls)
	}
}
