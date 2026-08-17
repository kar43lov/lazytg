package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
)

// ChatLister is the narrow interface ReindexService.RunAll needs to enumerate
// every chat that should be folded into the index. *sqlite.Repo satisfies it
// via its GetChats method, but tests can pass a fake without bringing the
// whole repo behind them.
type ChatLister interface {
	GetChats(ctx context.Context) ([]domain.Chat, error)
}

// EventPublisher narrows *events.Bus down to the Publish call so tests can
// supply a recording fake instead of spinning up a full bus + subscriber.
// *events.Bus satisfies the interface implicitly.
type EventPublisher interface {
	Publish(events.Event)
}

// ReindexService walks every chat (or a caller-provided subset) through
// Indexer.Backfill, emitting a ReindexProgress event after each chat. Cancellation
// of ctx makes Run/RunAll return promptly with the wrapped context error;
// the FTS index never gets left in a half-applied state because each
// Indexer.Backfill call is transactional in itself — a cancel mid-write
// rolls the in-progress transaction back without polluting the table.
type ReindexService struct {
	indexer      *Indexer
	chats        ChatLister
	bus          EventPublisher
	log          *slog.Logger
	perChatLimit int
}

// NewReindexService wires a ReindexService. perChatLimit <= 0 falls back to
// DefaultPerChatLimit. log nil installs a discard handler. bus and chats may
// also be nil — Run still works without them; RunAll requires chats.
func NewReindexService(indexer *Indexer, chats ChatLister, bus EventPublisher, log *slog.Logger, perChatLimit int) *ReindexService {
	if log == nil {
		log = slog.New(discardHandler{})
	}
	if perChatLimit <= 0 {
		perChatLimit = DefaultPerChatLimit
	}
	return &ReindexService{
		indexer:      indexer,
		chats:        chats,
		bus:          bus,
		log:          log,
		perChatLimit: perChatLimit,
	}
}

// Run folds the chats listed in chatIDs into the FTS index sequentially.
// After every chat completes (or is skipped because it was already up to
// date) a ReindexProgress event is published with the running counters; the
// final event in the pass carries Done=true so consumers can drop the
// "indexing…" indicator without counting chats themselves. Returning the
// wrapped context error on cancel is treated as a normal control-flow
// signal — callers should not log it as an internal failure.
func (r *ReindexService) Run(ctx context.Context, chatIDs []int64) error {
	if len(chatIDs) == 0 {
		if r.bus != nil {
			r.bus.Publish(events.ReindexProgress{Done: true})
		}
		return nil
	}

	var total int
	for i, chatID := range chatIDs {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("reindex cancelled at chat %d/%d: %w", i, len(chatIDs), err)
		}
		n, err := r.indexer.Backfill(ctx, chatID, r.perChatLimit)
		if err != nil {
			// Cancellation surfaced from inside Backfill (the database/sql
			// Tx APIs honour ctx) is reported as a context error so the
			// caller can branch on it and stay quiet in logs.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return fmt.Errorf("reindex cancelled at chat %d (%d/%d): %w", chatID, i, len(chatIDs), err)
			}
			return fmt.Errorf("reindex chat %d: %w", chatID, err)
		}
		total += n
		last := i == len(chatIDs)-1
		if r.bus != nil {
			r.bus.Publish(events.ReindexProgress{
				ChatID:  chatID,
				Indexed: n,
				Total:   total,
				Done:    last,
			})
		}
		r.log.LogAttrs(ctx, slog.LevelDebug, "reindex chat",
			slog.Int64("chat_id", chatID),
			slog.Int("indexed", n),
			slog.Int("total", total),
			slog.Bool("done", last),
		)
	}
	return nil
}

// RunAll resolves the chat list via the configured ChatLister and forwards
// to Run. Returns nil when there are no chats yet (a fresh install).
func (r *ReindexService) RunAll(ctx context.Context) error {
	if r.chats == nil {
		return errors.New("reindex: no chat lister configured")
	}
	chats, err := r.chats.GetChats(ctx)
	if err != nil {
		return fmt.Errorf("reindex: list chats: %w", err)
	}
	if len(chats) == 0 {
		if r.bus != nil {
			r.bus.Publish(events.ReindexProgress{Done: true})
		}
		return nil
	}
	ids := make([]int64, len(chats))
	for i, c := range chats {
		ids[i] = c.ID
	}
	return r.Run(ctx, ids)
}
