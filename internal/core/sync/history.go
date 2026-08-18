package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
)

// initialBatchSize is the default number of messages LoadInitial pulls in a
// single round-trip. 100 is the conservative ceiling Telegram applies to
// messages.getHistory before the server starts truncating; staying at the
// limit keeps the per-chat backfill round-trip count low without provoking
// FLOOD_WAIT on the first call.
const initialBatchSize = 100

// freshnessWindow is how long a "the local cache is current" verdict stands
// before the next chat open goes back to the server. 90 seconds is picked
// against the two failure modes it sits between: the burst seen in the live
// smoke was six getHistory calls inside half a minute (all of them re-focuses
// of the same two chats, so any window over ~30s removes them), while messages
// missed during a transport drop stay invisible for at most one window instead
// of until the next restart.
const freshnessWindow = 90 * time.Second

// HistoryProvider is the gotd-free contract HistoryService relies on. It is
// satisfied by *internal/tg.HistoryFetcher in production and by mocks in
// tests; declaring it here keeps internal/core free of MTProto types.
type HistoryProvider interface {
	Fetch(ctx context.Context, peerID, accessHash int64, peerType string, limit, offsetID int) ([]domain.Message, bool, error)
}

// PeerLookup resolves a chat's MTProto access metadata from its local id.
// Returning ErrPeerUnknown lets HistoryService decide whether to surface a
// hard error or skip silently — useful when a chat list entry exists but no
// peer row has been cached yet.
type PeerLookup interface {
	Lookup(ctx context.Context, chatID int64) (PeerInfo, error)
}

// PeerInfo is the minimal projection of a peer row that HistoryService needs
// to ask MTProto for messages in a chat.
type PeerInfo struct {
	AccessHash int64
	Type       string
}

// MessageStore is the storage surface HistoryService writes through. It is a
// subset of *sqlite.Repo so we can swap an in-memory fake during unit tests.
type MessageStore interface {
	SaveMessages(ctx context.Context, msgs []domain.Message) error
	// ChatHistoryFreshness returns the dialog list's last-message timestamp
	// and the newest message actually cached for the chat. Zero values mean
	// "unknown", which reads as "not current".
	ChatHistoryFreshness(ctx context.Context, chatID int64) (dialogNewest, localNewest time.Time, err error)
}

// ErrPeerUnknown is returned by PeerLookup.Lookup when no peer row exists.
var ErrPeerUnknown = errors.New("peer: unknown")

// HistoryService coordinates one-shot history backfills for a chat: it asks
// the provider for a batch, persists it through MessageStore, and announces
// the change on the event bus so the UI can refresh.
type HistoryService struct {
	provider HistoryProvider
	peers    PeerLookup
	store    MessageStore
	bus      *events.Bus
	log      *slog.Logger

	// now is injectable so the freshness window can be tested without
	// sleeping, following the same pattern as TokenBucket.
	now func() time.Time

	mu       sync.Mutex
	verified map[int64]time.Time
}

// NewHistoryService wires a HistoryService. log may be nil — a no-op logger
// is used in that case, which keeps unit tests free of plumbing noise.
func NewHistoryService(provider HistoryProvider, peers PeerLookup, store MessageStore, bus *events.Bus, log *slog.Logger) *HistoryService {
	if log == nil {
		log = slog.New(noopHandler{})
	}
	return &HistoryService{
		provider: provider,
		peers:    peers,
		store:    store,
		bus:      bus,
		log:      log,
		now:      time.Now,
		verified: make(map[int64]time.Time),
	}
}

// LoadInitial pulls the most recent initialBatchSize messages for chatID,
// persists them and emits DialogUpdated. Re-running the same call is a
// no-op for storage thanks to the (chat_id, id) UPSERT — the bus event is
// fired regardless so any UI consumer that missed the previous one can catch
// up.
func (s *HistoryService) LoadInitial(ctx context.Context, chatID int64) error {
	if s.cacheIsCurrent(ctx, chatID) {
		return nil
	}
	peer, err := s.peers.Lookup(ctx, chatID)
	if err != nil {
		return fmt.Errorf("history: lookup peer %d: %w", chatID, err)
	}
	msgs, _, err := s.provider.Fetch(ctx, chatID, peer.AccessHash, peer.Type, initialBatchSize, 0)
	if err != nil {
		return fmt.Errorf("history: fetch chat=%d: %w", chatID, err)
	}
	s.markVerified(chatID)
	if len(msgs) == 0 {
		s.log.Debug("history: empty batch", "chat_id", chatID)
		return nil
	}
	if err := s.store.SaveMessages(ctx, msgs); err != nil {
		return fmt.Errorf("history: persist chat=%d: %w", chatID, err)
	}
	if s.bus != nil {
		s.bus.Publish(events.DialogUpdated{ChatID: chatID})
	}
	s.log.Info("history: initial batch loaded", "chat_id", chatID, "messages", len(msgs))
	return nil
}

// cacheIsCurrent reports whether the local mirror already holds the newest
// message the dialog list knows about, in which case LoadInitial has nothing to
// ask Telegram for.
//
// Why this guard exists: the UI enqueues a backfill every time a chat is
// selected, and BackfillService only coalesces requests that are still in
// flight. Re-focusing a chat therefore fired a fresh messages.getHistory even
// though every message was already in SQLite — six calls for two chats inside
// half a minute during the first live smoke. For an unofficial client that is
// pure behavioural trace with nothing gained, and it delays the pane behind a
// round-trip it did not need.
//
// Comparing timestamps rather than remembering "already loaded this chat" is
// deliberate: a chat that gains messages while the process runs (live update
// persisted, or a dialog sync that moved last_message_date forward) compares as
// stale and is fetched again. A remembered-set would have skipped it until
// restart, which is exactly how a thread silently stops updating.
//
// Any probe failure means "fetch": a cache check that cannot answer must never
// be the reason history goes missing.
//
// The verdict is also bounded in time, and that bound is the important part.
// Both timestamps come from local storage, so they agree with each other even
// when both are stale: if the transport drops and updates are missed, the
// dialog row stops moving too, and the comparison keeps answering "current"
// for the rest of the process lifetime. v0.1 has no updates.Manager and no
// reliable drop signal — ConnectionStateChanged is published only by
// ReconnectManager — so nothing else would ever pull those messages in, and
// re-opening the chat used to be the accidental recovery path. Expiring the
// verdict after freshnessWindow restores that path at a rate of at most one
// getHistory per chat per window, instead of one per re-focus.
func (s *HistoryService) cacheIsCurrent(ctx context.Context, chatID int64) bool {
	if !s.verifiedRecently(chatID) {
		return false
	}
	dialogNewest, localNewest, err := s.store.ChatHistoryFreshness(ctx, chatID)
	if err != nil {
		s.log.Debug("history: freshness probe failed, fetching anyway", "chat_id", chatID, "err", err)
		return false
	}
	if dialogNewest.IsZero() || localNewest.IsZero() {
		return false
	}
	if localNewest.Before(dialogNewest) {
		return false
	}
	s.log.Debug("history: local cache already current, skipping fetch",
		"chat_id", chatID, "local_newest", localNewest, "dialog_newest", dialogNewest)
	return true
}

// verifiedRecently reports whether this chat has been confirmed against the
// server inside the last freshnessWindow. A chat never fetched in this process
// answers false, which is what makes the first open after a restart pull
// whatever arrived while lazytg was down.
func (s *HistoryService) verifiedRecently(chatID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.verified[chatID]
	return ok && s.now().Sub(at) < freshnessWindow
}

// markVerified records that the server has just answered for this chat.
func (s *HistoryService) markVerified(chatID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verified[chatID] = s.now()
}

// noopHandler discards all log records. Used as the default slog handler so
// constructors stay non-nil-clean without dragging slog setup into tests.
type noopHandler struct{}

func (noopHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (noopHandler) Handle(context.Context, slog.Record) error { return nil }
func (h noopHandler) WithAttrs([]slog.Attr) slog.Handler      { return h }
func (h noopHandler) WithGroup(string) slog.Handler           { return h }
