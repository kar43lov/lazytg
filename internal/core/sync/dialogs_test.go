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

// fakeDialogsProvider replays one page per call and records the cursors it was
// asked with, so paging can be asserted without a network.
type fakeDialogsProvider struct {
	mu      sync.Mutex
	pages   []DialogPage
	errs    []error
	cursors []DialogCursor
	calls   int
}

func (f *fakeDialogsProvider) FetchDialogs(_ context.Context, _ int, cursor DialogCursor) (DialogPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := f.calls
	f.calls++
	f.cursors = append(f.cursors, cursor)
	if idx < len(f.errs) && f.errs[idx] != nil {
		return DialogPage{}, f.errs[idx]
	}
	if idx < len(f.pages) {
		return f.pages[idx], nil
	}
	return DialogPage{}, nil
}

type fakeChatStore struct {
	mu       sync.Mutex
	saved    []domain.Chat
	failOn   map[int64]error
	pruned   [][]int64
	pruneErr error
}

// DeleteChatsExcept records the keep-set every prune was called with, so a
// test can assert both that a complete walk prunes and that a capped one
// does not.
func (f *fakeChatStore) DeleteChatsExcept(_ context.Context, keep []int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pruned = append(f.pruned, append([]int64(nil), keep...))
	return 0, f.pruneErr
}

// prunedSnapshot returns the recorded prune calls.
func (f *fakeChatStore) prunedSnapshot() [][]int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]int64(nil), f.pruned...)
}

func (f *fakeChatStore) SaveChat(_ context.Context, c domain.Chat) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.failOn[c.ID]; ok {
		return err
	}
	f.saved = append(f.saved, c)
	return nil
}

func (f *fakeChatStore) ids() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int64, 0, len(f.saved))
	for _, c := range f.saved {
		out = append(out, c.ID)
	}
	return out
}

type fakePeerStore struct {
	mu    sync.Mutex
	saved []domain.Peer
	err   error
}

func (f *fakePeerStore) Save(_ context.Context, p domain.Peer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.saved = append(f.saved, p)
	return nil
}

func (f *fakePeerStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.saved)
}

// newTestService builds a service with the page delay collapsed — the real
// 300ms spacing is a ban-risk measure, not something to wait out per test.
func newTestService(t *testing.T, p DialogsProvider, chats ChatStore, peers PeerStore, bus *events.Bus, cfg DialogsConfig) *DialogsService {
	t.Helper()
	if cfg.PageDelay == 0 {
		cfg.PageDelay = time.Microsecond
	}
	svc, err := NewDialogsService(p, chats, peers, bus, nil, cfg)
	if err != nil {
		t.Fatalf("NewDialogsService: %v", err)
	}
	return svc
}

func chatPage(hasMore bool, next DialogCursor, ids ...int64) DialogPage {
	page := DialogPage{Next: next, HasMore: hasMore}
	for _, id := range ids {
		page.Chats = append(page.Chats, domain.Chat{ID: id, Type: domain.ChatTypePrivate, Title: "chat"})
		page.Peers = append(page.Peers, domain.Peer{ID: id, Type: domain.ChatTypePrivate, AccessHash: id * 10})
	}
	return page
}

func TestDialogsService_RequiresDependencies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		provider DialogsProvider
		chats    ChatStore
		peers    PeerStore
	}{
		{"no provider", nil, &fakeChatStore{}, &fakePeerStore{}},
		{"no chat store", &fakeDialogsProvider{}, nil, &fakePeerStore{}},
		{"no peer store", &fakeDialogsProvider{}, &fakeChatStore{}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewDialogsService(tc.provider, tc.chats, tc.peers, nil, nil, DialogsConfig{}); err == nil {
				t.Fatalf("want an error when a dependency is missing")
			}
		})
	}
}

func TestDialogsService_Sync_PersistsChatsAndPeers(t *testing.T) {
	t.Parallel()

	provider := &fakeDialogsProvider{pages: []DialogPage{chatPage(false, DialogCursor{}, 1, 2, 3)}}
	chats := &fakeChatStore{}
	peers := &fakePeerStore{}
	svc := newTestService(t, provider, chats, peers, nil, DialogsConfig{})

	stored, err := svc.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if stored != 3 {
		t.Fatalf("stored = %d, want 3", stored)
	}
	if got := chats.ids(); len(got) != 3 {
		t.Fatalf("chats saved = %v, want 3", got)
	}
	if peers.count() != 3 {
		t.Fatalf("peers saved = %d, want 3", peers.count())
	}
}

// Pagination must carry the cursor forward. Repeating a zero cursor would walk
// the first page over and over until the page cap stops it.
func TestDialogsService_Sync_FollowsCursorAcrossPages(t *testing.T) {
	t.Parallel()

	second := DialogCursor{Date: 100, ID: 10, PeerID: 1, PeerType: string(domain.ChatTypePrivate)}
	third := DialogCursor{Date: 200, ID: 20, PeerID: 2, PeerType: string(domain.ChatTypePrivate)}
	provider := &fakeDialogsProvider{pages: []DialogPage{
		chatPage(true, second, 1),
		chatPage(true, third, 2),
		chatPage(false, DialogCursor{}, 3),
	}}
	chats := &fakeChatStore{}
	svc := newTestService(t, provider, chats, &fakePeerStore{}, nil, DialogsConfig{})

	stored, err := svc.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if stored != 3 {
		t.Fatalf("stored = %d, want 3", stored)
	}
	want := []DialogCursor{{}, second, third}
	if len(provider.cursors) != len(want) {
		t.Fatalf("cursors = %+v, want %d entries", provider.cursors, len(want))
	}
	for i, w := range want {
		if provider.cursors[i] != w {
			t.Errorf("cursor[%d] = %+v, want %+v", i, provider.cursors[i], w)
		}
	}
}

// The page cap is a ban-risk guard: an unbounded crawl over a huge account is
// exactly the machine-like traffic pattern the project promises to avoid.
func TestDialogsService_Sync_StopsAtMaxPages(t *testing.T) {
	t.Parallel()

	// Always claims another page exists, with a non-zero cursor.
	endless := make([]DialogPage, 10)
	for i := range endless {
		endless[i] = chatPage(true, DialogCursor{Date: i + 1, ID: i + 1, PeerID: int64(i + 1), PeerType: string(domain.ChatTypePrivate)}, int64(i+1))
	}
	provider := &fakeDialogsProvider{pages: endless}
	svc := newTestService(t, provider, &fakeChatStore{}, &fakePeerStore{}, nil, DialogsConfig{MaxPages: 3})

	stored, err := svc.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if provider.calls != 3 {
		t.Fatalf("provider calls = %d, want exactly MaxPages (3)", provider.calls)
	}
	if stored != 3 {
		t.Fatalf("stored = %d, want 3", stored)
	}
}

func TestDialogsService_Sync_StopsWhenServerSaysNoMore(t *testing.T) {
	t.Parallel()

	provider := &fakeDialogsProvider{pages: []DialogPage{chatPage(false, DialogCursor{}, 1)}}
	svc := newTestService(t, provider, &fakeChatStore{}, &fakePeerStore{}, nil, DialogsConfig{MaxPages: 5})

	if _, err := svc.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls)
	}
}

// HasMore with an empty cursor would loop on the same page forever; treat the
// missing cursor as the end of the list.
func TestDialogsService_Sync_StopsOnMissingCursorDespiteHasMore(t *testing.T) {
	t.Parallel()

	provider := &fakeDialogsProvider{pages: []DialogPage{chatPage(true, DialogCursor{}, 1)}}
	svc := newTestService(t, provider, &fakeChatStore{}, &fakePeerStore{}, nil, DialogsConfig{MaxPages: 5})

	if _, err := svc.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1 — a zero cursor ends the walk", provider.calls)
	}
}

// A mid-walk fetch failure must keep whatever already landed and report the
// count, so the caller can tell the user the list is partial rather than empty.
// A cursor that repeats means the next request would be identical. The page cap
// bounds it, but five identical round-trips are pure behavioural footprint —
// the thing the inter-page pacing exists to minimise.
func TestDialogsService_Sync_StopsOnNonAdvancingCursor(t *testing.T) {
	t.Parallel()

	stuck := DialogCursor{Date: 100, ID: 10, PeerID: 1, PeerType: string(domain.ChatTypePrivate)}
	provider := &fakeDialogsProvider{pages: []DialogPage{
		chatPage(true, stuck, 1),
		chatPage(true, stuck, 2), // server hands back the same position
		chatPage(true, stuck, 3),
	}}
	svc := newTestService(t, provider, &fakeChatStore{}, &fakePeerStore{}, nil, DialogsConfig{MaxPages: 5})

	if _, err := svc.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls = %d, want 2 — the repeat must end the walk", provider.calls)
	}
}

func TestDialogsService_Sync_ReportsPartialProgressOnFetchError(t *testing.T) {
	t.Parallel()

	boom := errors.New("flood wait")
	provider := &fakeDialogsProvider{
		pages: []DialogPage{chatPage(true, DialogCursor{Date: 1, ID: 1, PeerID: 1, PeerType: "private"}, 1, 2)},
		errs:  []error{nil, boom},
	}
	chats := &fakeChatStore{}
	svc := newTestService(t, provider, chats, &fakePeerStore{}, nil, DialogsConfig{})

	stored, err := svc.Sync(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("want the fetch error wrapped, got %v", err)
	}
	if stored != 2 {
		t.Fatalf("stored = %d, want the 2 chats from page one kept", stored)
	}
	if len(chats.ids()) != 2 {
		t.Fatalf("page-one chats must stay persisted, got %v", chats.ids())
	}
}

// One bad chat should not cost the user the rest of the list.
func TestDialogsService_Sync_SkipsUnwritableChat(t *testing.T) {
	t.Parallel()

	provider := &fakeDialogsProvider{pages: []DialogPage{chatPage(false, DialogCursor{}, 1, 2, 3)}}
	chats := &fakeChatStore{failOn: map[int64]error{2: errors.New("constraint failed")}}
	svc := newTestService(t, provider, chats, &fakePeerStore{}, nil, DialogsConfig{})

	stored, err := svc.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync must not fail on a single bad row: %v", err)
	}
	if stored != 2 {
		t.Fatalf("stored = %d, want 2 (the writable ones)", stored)
	}
	got := chats.ids()
	if len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Fatalf("saved ids = %v, want [1 3]", got)
	}
}

// A peer-store failure is logged, not fatal: the chat is still worth showing,
// it just cannot be opened until the peer resolves.
func TestDialogsService_Sync_SurvivesPeerStoreFailure(t *testing.T) {
	t.Parallel()

	provider := &fakeDialogsProvider{pages: []DialogPage{chatPage(false, DialogCursor{}, 1)}}
	chats := &fakeChatStore{}
	svc := newTestService(t, provider, chats, &fakePeerStore{err: errors.New("disk full")}, nil, DialogsConfig{})

	stored, err := svc.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if stored != 1 {
		t.Fatalf("stored = %d, want 1", stored)
	}
}

func TestDialogsService_Sync_PublishesDialogUpdated(t *testing.T) {
	t.Parallel()

	bus := events.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := bus.Subscribe(ctx)

	provider := &fakeDialogsProvider{pages: []DialogPage{chatPage(false, DialogCursor{}, 7, 8)}}
	svc := newTestService(t, provider, &fakeChatStore{}, &fakePeerStore{}, bus, DialogsConfig{})

	if _, err := svc.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	seen := map[int64]bool{}
	deadline := time.After(2 * time.Second)
	for len(seen) < 2 {
		select {
		case ev := <-sub:
			if upd, ok := ev.(events.DialogUpdated); ok {
				seen[upd.ChatID] = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for DialogUpdated, saw %v", seen)
		}
	}
	if !seen[7] || !seen[8] {
		t.Fatalf("want events for chats 7 and 8, saw %v", seen)
	}
}

func TestDialogsService_Sync_StopsOnCancelledContext(t *testing.T) {
	t.Parallel()

	provider := &fakeDialogsProvider{pages: []DialogPage{chatPage(true, DialogCursor{Date: 1, ID: 1, PeerID: 1, PeerType: "private"}, 1)}}
	svc := newTestService(t, provider, &fakeChatStore{}, &fakePeerStore{}, nil, DialogsConfig{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := svc.Sync(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if provider.calls != 0 {
		t.Fatalf("a cancelled context must not reach the network, got %d calls", provider.calls)
	}
}

func TestDialogCursor_IsZero(t *testing.T) {
	t.Parallel()
	if !(DialogCursor{}).IsZero() {
		t.Fatalf("empty cursor must be zero")
	}
	if (DialogCursor{ID: 1}).IsZero() {
		t.Fatalf("populated cursor must not be zero")
	}
}

// TestDialogsSync_PrunesChatsDeletedElsewhere covers the only path by which a
// chat deleted from another device leaves the mirror. The walk upserts, so
// without this the row survives every sync forever — observed live on
// 19.08.2026, where a chat deleted in Telegram Desktop stayed in the list
// while the sync itself reported one chat fewer.
func TestDialogsSync_PrunesChatsDeletedElsewhere(t *testing.T) {
	chats := &fakeChatStore{}
	svc, err := NewDialogsService(
		&fakeDialogsProvider{pages: []DialogPage{{
			Chats: []domain.Chat{
				{ID: 1, Type: domain.ChatTypePrivate, Title: "kept"},
				{ID: 2, Type: domain.ChatTypePrivate, Title: "also kept"},
			},
			HasMore: false,
		}}},
		chats, &fakePeerStore{}, nil, nil, DialogsConfig{},
	)
	if err != nil {
		t.Fatalf("NewDialogsService: %v", err)
	}

	if _, err := svc.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	pruned := chats.prunedSnapshot()
	if len(pruned) != 1 {
		t.Fatalf("prune calls = %d, want 1 after a complete walk", len(pruned))
	}
	if len(pruned[0]) != 2 {
		t.Fatalf("keep set = %v, want both chats the server listed", pruned[0])
	}
}

// TestDialogsSync_DoesNotPruneAfterAPartialWalk is the guard that matters far
// more than the feature. The walk stops after maxPages by design — a
// ban-risk decision — and for an account past that cap most chats are simply
// beyond the pages fetched. Pruning there would delete the larger part of the
// mirror on every sync.
func TestDialogsSync_DoesNotPruneAfterAPartialWalk(t *testing.T) {
	chats := &fakeChatStore{}
	// Cursors advance, so the walk is stopped by the page cap rather than by
	// the "cursor did not advance" guard — the cap is what this is about.
	pages := make([]DialogPage, 0, 3)
	for i := 1; i <= 3; i++ {
		pages = append(pages, DialogPage{
			Chats:   []domain.Chat{{ID: int64(i), Type: domain.ChatTypePrivate, Title: "one of many"}},
			Next:    DialogCursor{Date: 1000 + i, ID: i},
			HasMore: true,
		})
	}
	svc, err := NewDialogsService(
		&fakeDialogsProvider{pages: pages},
		chats, &fakePeerStore{}, nil, nil,
		DialogsConfig{MaxPages: 2, PageDelay: time.Millisecond},
	)
	if err != nil {
		t.Fatalf("NewDialogsService: %v", err)
	}

	if _, err := svc.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if pruned := chats.prunedSnapshot(); len(pruned) != 0 {
		t.Fatalf("a capped walk pruned %d time(s) — every chat past the cap would be deleted", len(pruned))
	}
}

// selfAwareProvider is a fakeDialogsProvider that also knows the account's
// own chat, the way the production fetcher does.
type selfAwareProvider struct {
	fakeDialogsProvider
	self      domain.Chat
	selfPeer  domain.Peer
	selfErr   error
	selfCalls int
}

func (p *selfAwareProvider) SelfDialog(context.Context) (domain.Chat, domain.Peer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.selfCalls++
	return p.self, p.selfPeer, p.selfErr
}

// Telegram lists the chat with yourself only once something was written
// there; every official client shows it regardless. When the server leaves
// it out, the sync adds it — and the prune that follows must keep it.
func TestDialogsService_Sync_AddsSavedMessagesTheServerLeftOut(t *testing.T) {
	t.Parallel()

	provider := &selfAwareProvider{
		fakeDialogsProvider: fakeDialogsProvider{pages: []DialogPage{chatPage(false, DialogCursor{}, 1, 2)}},
		self:                domain.Chat{ID: 777, Type: domain.ChatTypePrivate, Title: "Saved Messages"},
		selfPeer:            domain.Peer{ID: 777, Type: domain.ChatTypePrivate, AccessHash: 5},
	}
	chats := &fakeChatStore{}
	peers := &fakePeerStore{}
	svc := newTestService(t, provider, chats, peers, nil, DialogsConfig{})

	stored, err := svc.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if stored != 3 {
		t.Fatalf("stored = %d, want the two listed plus Saved Messages", stored)
	}
	var found bool
	for _, id := range chats.ids() {
		if id == 777 {
			found = true
		}
	}
	if !found {
		t.Fatalf("chats saved = %v, Saved Messages missing", chats.ids())
	}
	pruned := chats.prunedSnapshot()
	if len(pruned) != 1 {
		t.Fatalf("prune ran %d times, want once", len(pruned))
	}
	var kept bool
	for _, id := range pruned[0] {
		if id == 777 {
			kept = true
		}
	}
	if !kept {
		t.Fatalf("prune keep-list %v would delete Saved Messages", pruned[0])
	}
}

// A row the server did list carries its date and unread count; rewriting it
// from the bare user record would zero both. So when the list has it, the
// self record is left alone.
func TestDialogsService_Sync_LeavesAListedSavedMessagesAlone(t *testing.T) {
	t.Parallel()

	provider := &selfAwareProvider{
		fakeDialogsProvider: fakeDialogsProvider{pages: []DialogPage{chatPage(false, DialogCursor{}, 777, 2)}},
		self:                domain.Chat{ID: 777, Type: domain.ChatTypePrivate, Title: "Saved Messages"},
		selfPeer:            domain.Peer{ID: 777, Type: domain.ChatTypePrivate, AccessHash: 5},
	}
	chats := &fakeChatStore{}
	svc := newTestService(t, provider, chats, &fakePeerStore{}, nil, DialogsConfig{})

	stored, err := svc.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if stored != 2 {
		t.Fatalf("stored = %d, want just the two listed", stored)
	}
}

func TestDialogsService_Sync_SurvivesASelfLookupFailure(t *testing.T) {
	t.Parallel()

	provider := &selfAwareProvider{
		fakeDialogsProvider: fakeDialogsProvider{pages: []DialogPage{chatPage(false, DialogCursor{}, 1)}},
		selfErr:             errors.New("boom"),
	}
	chats := &fakeChatStore{}
	svc := newTestService(t, provider, chats, &fakePeerStore{}, nil, DialogsConfig{})

	if _, err := svc.Sync(context.Background()); err != nil {
		t.Fatalf("Sync failed on the optional lookup: %v", err)
	}
	if got := chats.ids(); len(got) != 1 {
		t.Fatalf("chats saved = %v, want the one listed", got)
	}
}

// A chat that arrives with a draft announces it: the draft is not stored,
// so the event is the only way the composer learns of it.
func TestDialogsService_Sync_PublishesDrafts(t *testing.T) {
	t.Parallel()

	bus := events.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := bus.Subscribe(ctx)

	page := chatPage(false, DialogCursor{}, 7, 8)
	page.Chats[0].Draft = "half a sentence"
	provider := &fakeDialogsProvider{pages: []DialogPage{page}}
	svc := newTestService(t, provider, &fakeChatStore{}, &fakePeerStore{}, bus, DialogsConfig{})
	if _, err := svc.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	var drafts []events.DraftChanged
	deadline := time.After(2 * time.Second)
	for updates := 0; updates < 2; {
		select {
		case ev := <-sub:
			switch typed := ev.(type) {
			case events.DialogUpdated:
				updates++
			case events.DraftChanged:
				drafts = append(drafts, typed)
			}
		case <-deadline:
			t.Fatal("timed out waiting for the sync events")
		}
	}
	if len(drafts) != 1 || drafts[0].ChatID != 7 || drafts[0].Text != "half a sentence" {
		t.Fatalf("drafts = %+v", drafts)
	}
}
