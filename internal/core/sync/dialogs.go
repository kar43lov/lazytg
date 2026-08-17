package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
)

// Dialog-sync tuning. Telegram caps messages.getDialogs at 100 entries per
// call; asking for exactly that keeps the round-trip count minimal.
//
// defaultMaxDialogPages bounds the walk at 500 chats. That covers the vast
// majority of accounts, and an unbounded crawl is precisely the "machine-like"
// access pattern the project promises not to exhibit (see CLAUDE.md ban-risk).
// Users past the cap still reach older chats through search and the palette.
//
// defaultPageDelay spaces the pages out. Without it a 500-chat account issues
// five getDialogs calls back-to-back within a few hundred milliseconds, which
// looks nothing like a human opening an app.
const (
	defaultDialogBatchSize = 100
	defaultMaxDialogPages  = 5
	defaultPageDelay       = 300 * time.Millisecond
)

// DialogCursor is the paging position inside the dialog list. Telegram pages
// getDialogs by the *last returned dialog* rather than a numeric offset, so all
// four fields have to travel together; a zero cursor means "start at the top".
type DialogCursor struct {
	Date           int
	ID             int
	PeerID         int64
	PeerAccessHash int64
	PeerType       string
}

// IsZero reports whether the cursor is the initial position.
func (c DialogCursor) IsZero() bool { return c == DialogCursor{} }

// DialogPage is one response worth of dialogs. Peers travels alongside Chats
// because a chat is useless without its access_hash: every later call —
// history, send, download — needs it to build an InputPeer, and getDialogs is
// the only place it arrives in bulk.
type DialogPage struct {
	Chats   []domain.Chat
	Peers   []domain.Peer
	Next    DialogCursor
	HasMore bool
}

// DialogsProvider is the gotd-free contract DialogsService relies on,
// satisfied by *internal/tg.DialogsFetcher in production.
type DialogsProvider interface {
	FetchDialogs(ctx context.Context, limit int, cursor DialogCursor) (DialogPage, error)
}

// ChatStore is the storage surface for the chat list — a subset of
// *sqlite.Repo.
type ChatStore interface {
	SaveChat(ctx context.Context, c domain.Chat) error
}

// PeerStore persists MTProto access metadata — a subset of *sqlite.PeerRepo.
type PeerStore interface {
	Save(ctx context.Context, p domain.Peer) error
}

// DialogsConfig overrides the paging defaults. Zero fields fall back to the
// package defaults, so callers can pass an empty struct.
type DialogsConfig struct {
	BatchSize int
	MaxPages  int
	PageDelay time.Duration
}

// DialogsService fills the local chat list from Telegram. Until it runs the
// chats table stays empty and the TUI has nothing to show, no matter how
// healthy the connection is — it is the entry point of the whole read path.
type DialogsService struct {
	provider DialogsProvider
	chats    ChatStore
	peers    PeerStore
	bus      *events.Bus
	log      *slog.Logger

	batchSize int
	maxPages  int
	pageDelay time.Duration
}

// NewDialogsService wires a DialogsService. log may be nil (a discard logger
// is substituted). provider, chats and peers are mandatory.
func NewDialogsService(provider DialogsProvider, chats ChatStore, peers PeerStore, bus *events.Bus, log *slog.Logger, cfg DialogsConfig) (*DialogsService, error) {
	if provider == nil {
		return nil, errors.New("dialogs: provider is required")
	}
	if chats == nil {
		return nil, errors.New("dialogs: chat store is required")
	}
	if peers == nil {
		return nil, errors.New("dialogs: peer store is required")
	}
	if log == nil {
		log = slog.New(noopHandler{})
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultDialogBatchSize
	}
	if cfg.MaxPages <= 0 {
		cfg.MaxPages = defaultMaxDialogPages
	}
	if cfg.PageDelay <= 0 {
		cfg.PageDelay = defaultPageDelay
	}
	return &DialogsService{
		provider:  provider,
		chats:     chats,
		peers:     peers,
		bus:       bus,
		log:       log,
		batchSize: cfg.BatchSize,
		maxPages:  cfg.MaxPages,
		pageDelay: cfg.PageDelay,
	}, nil
}

// Sync walks the dialog list and persists every chat it finds, returning the
// number stored. It is safe to re-run: both stores upsert by id.
//
// A per-chat write failure is logged and skipped rather than aborting the
// walk — one unparseable chat should not cost the user their whole chat list.
// A fetch failure does stop the walk, but chats already persisted stay, and
// the count returned reflects them, so the caller can report partial success.
func (s *DialogsService) Sync(ctx context.Context) (int, error) {
	var cursor DialogCursor
	stored := 0

	for page := 0; page < s.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return stored, err
		}

		p, err := s.provider.FetchDialogs(ctx, s.batchSize, cursor)
		if err != nil {
			return stored, fmt.Errorf("dialogs: fetch page %d: %w", page, err)
		}
		if len(p.Chats) == 0 {
			break
		}

		stored += s.persist(ctx, p)

		if !p.HasMore || p.Next.IsZero() {
			// Worth stating why the walk ended. A page that came back full but
			// carries no usable cursor means the list was truncated (the last
			// dialog had no date, or none of them decoded) — the user sees a
			// short chat list and would otherwise have no way to tell that from
			// "this is all the chats there are".
			if len(p.Chats) >= s.batchSize {
				s.log.Warn("dialogs: list truncated — page was full but carried no usable cursor",
					"page", page, "chats_in_page", len(p.Chats))
			} else {
				s.log.Debug("dialogs: reached the end of the list", "page", page)
			}
			break
		}
		// A cursor that has not advanced means the next request would repeat
		// this one verbatim. The page cap keeps that finite, but it still costs
		// identical round-trips whose only effect is a worse behavioural
		// footprint — exactly what the pacing above exists to avoid.
		if p.Next == cursor {
			s.log.Warn("dialogs: cursor did not advance, stopping walk", "page", page)
			break
		}
		cursor = p.Next

		select {
		case <-ctx.Done():
			return stored, ctx.Err()
		case <-time.After(s.pageDelay):
		}
	}

	s.log.Info("dialogs: sync finished", "chats", stored)
	return stored, nil
}

// persist writes one page and returns how many chats landed in storage.
//
// Peers are written before chats deliberately: the UI reacts to DialogUpdated
// by reloading the list and may immediately ask for history, which needs the
// access_hash, so publishing the chat first would open a window where the chat
// is visible but unusable.
//
// The ordering is best-effort, not an invariant. When a peer write fails the
// chat is still saved — a visible chat that cannot open yet beats a chat the
// user cannot see at all, and the next sync retries the peer. The warning says
// which chats are in that state, because the symptom (history silently failing
// to load for one chat) is otherwise unattributable.
func (s *DialogsService) persist(ctx context.Context, p DialogPage) int {
	for _, peer := range p.Peers {
		if err := s.peers.Save(ctx, peer); err != nil {
			s.log.Warn("dialogs: save peer failed — chat will be listed but cannot load history",
				"peer_id", peer.ID, "err", err)
		}
	}

	stored := 0
	for _, c := range p.Chats {
		if err := s.chats.SaveChat(ctx, c); err != nil {
			s.log.Warn("dialogs: save chat failed", "chat_id", c.ID, "err", err)
			continue
		}
		stored++
		if s.bus != nil {
			s.bus.Publish(events.DialogUpdated{ChatID: c.ID})
		}
	}
	return stored
}
