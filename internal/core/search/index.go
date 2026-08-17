package search

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// DefaultPerChatLimit is the lazy-index cap: only the most recent N messages
// per chat are folded into the FTS index by Backfill when no explicit limit
// is supplied. The number matches the product brief — heavy users opt into
// full reindex via `lazytg reindex --all`.
const DefaultPerChatLimit = 5000

// IndexStore is the narrow storage surface the indexer needs. *sqlite.Repo
// satisfies it via its DB() accessor, but tests can swap in an in-memory DB
// without dragging the whole repo behind them.
//
// Keeping the interface this thin (a single accessor) reflects how the
// indexer interacts with FTS5: the work is plain SQL bound to the messages
// and messages_fts tables, not a higher-level domain operation.
type IndexStore interface {
	DB() *sql.DB
}

// Indexer folds rows from the messages table into the messages_fts virtual
// table. The schema's AFTER INSERT/UPDATE/DELETE triggers (migration 0005)
// already keep the index in sync for every fresh write — Indexer only needs
// to be invoked when historic rows pre-date the FTS table (e.g. a database
// upgraded from a previous lazytg version that ran the spike but never the
// production migration) or when the user explicitly asks for a reindex.
type Indexer struct {
	store IndexStore
	log   *slog.Logger
}

// NewIndexer wires an Indexer. log may be nil — a discard handler is
// installed in that case to keep tests free of plumbing noise.
func NewIndexer(store IndexStore, log *slog.Logger) *Indexer {
	if log == nil {
		log = slog.New(discardHandler{})
	}
	return &Indexer{store: store, log: log}
}

// Backfill folds the most recent `limit` messages of chatID into messages_fts,
// skipping rows that are already present (the triggers may have inserted them
// when the message was first persisted). limit <= 0 falls back to
// DefaultPerChatLimit.
//
// Returns the number of rows newly inserted into messages_fts. A repeat call
// over the same chat is expected to return 0 — useful as an idempotency
// signal for the lazy trigger.
//
// Empty-text rows are excluded (FTS5 would index them as zero tokens, which
// is wasted I/O for service messages such as join/leave notifications).
func (i *Indexer) Backfill(ctx context.Context, chatID int64, limit int) (int, error) {
	if limit <= 0 {
		limit = DefaultPerChatLimit
	}
	db := i.store.DB()

	// FTS5 virtual tables do not honour database/sql's RowsAffected on the
	// modernc.org/sqlite driver — the result always reports 0 even for a
	// successful INSERT. Wrap the count+insert in a transaction and read
	// the candidate cardinality up-front so the returned figure is a
	// reliable idempotency signal for LazyTrigger.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("backfill chat=%d begin tx: %w", chatID, err)
	}
	defer func() { _ = tx.Rollback() }()

	const candidateCTE = `
        WITH candidates AS (
            SELECT rowid, text
            FROM messages
            WHERE chat_id = ?
              AND text IS NOT NULL
              AND text != ''
            ORDER BY date DESC, id DESC
            LIMIT ?
        )
    `

	var n int
	if err := tx.QueryRowContext(ctx, candidateCTE+`
        SELECT COUNT(*) FROM candidates
        WHERE rowid NOT IN (SELECT rowid FROM messages_fts)
    `, chatID, limit).Scan(&n); err != nil {
		return 0, fmt.Errorf("backfill chat=%d count: %w", chatID, err)
	}

	if n == 0 {
		if err := tx.Commit(); err != nil {
			return 0, fmt.Errorf("backfill chat=%d commit: %w", chatID, err)
		}
		return 0, nil
	}

	if _, err := tx.ExecContext(ctx, candidateCTE+`
        INSERT INTO messages_fts(rowid, text)
        SELECT rowid, text FROM candidates
        WHERE rowid NOT IN (SELECT rowid FROM messages_fts)
    `, chatID, limit); err != nil {
		return 0, fmt.Errorf("backfill chat=%d insert: %w", chatID, err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("backfill chat=%d commit: %w", chatID, err)
	}

	i.log.LogAttrs(ctx, slog.LevelInfo, "fts backfill",
		slog.Int64("chat_id", chatID),
		slog.Int("indexed", n),
		slog.Int("limit", limit),
	)
	return n, nil
}

// discardHandler is a slog.Handler that drops every record. Used as the
// fallback logger when callers pass nil so we don't have to import
// log/slog/internal helpers.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (h discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return h }
func (h discardHandler) WithGroup(string) slog.Handler           { return h }
