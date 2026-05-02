package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/pgmac/lazytg/internal/core/domain"
	"github.com/pgmac/lazytg/internal/core/events"
)

// initialBatchSize is the default number of messages LoadInitial pulls in a
// single round-trip. 100 is the conservative ceiling Telegram applies to
// messages.getHistory before the server starts truncating; staying at the
// limit keeps the per-chat backfill round-trip count low without provoking
// FLOOD_WAIT on the first call.
const initialBatchSize = 100

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
	}
}

// LoadInitial pulls the most recent initialBatchSize messages for chatID,
// persists them and emits DialogUpdated. Re-running the same call is a
// no-op for storage thanks to the (chat_id, id) UPSERT — the bus event is
// fired regardless so any UI consumer that missed the previous one can catch
// up.
func (s *HistoryService) LoadInitial(ctx context.Context, chatID int64) error {
	peer, err := s.peers.Lookup(ctx, chatID)
	if err != nil {
		return fmt.Errorf("history: lookup peer %d: %w", chatID, err)
	}
	msgs, _, err := s.provider.Fetch(ctx, chatID, peer.AccessHash, peer.Type, initialBatchSize, 0)
	if err != nil {
		return fmt.Errorf("history: fetch chat=%d: %w", chatID, err)
	}
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

// noopHandler discards all log records. Used as the default slog handler so
// constructors stay non-nil-clean without dragging slog setup into tests.
type noopHandler struct{}

func (noopHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (noopHandler) Handle(context.Context, slog.Record) error { return nil }
func (h noopHandler) WithAttrs([]slog.Attr) slog.Handler      { return h }
func (h noopHandler) WithGroup(string) slog.Handler           { return h }
