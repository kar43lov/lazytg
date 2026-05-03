package search

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/pgmac/lazytg/internal/core/domain"
)

// Hit is a single search result. Snippet is the FTS5-rendered fragment with
// <b>...</b> markers around matched windows; the UI maps those to its bold
// style on render. Score is FTS5's bm25 — lower is better.
type Hit struct {
	Message domain.Message
	Snippet string
	ChatID  int64
	Score   float64
}

// Service serves search queries against the FTS5 index. It composes
// IndexStore (the *sql.DB used for the JOIN query) and an optional
// LazyTrigger so that the first search request kicks off a background
// reindex pass. At Stage 3 Task 2 the query syntax is intentionally
// minimal: callers pass a free-text string forwarded to FTS5 MATCH
// verbatim. Task 3 plugs in the parser that turns user-typed strings
// into a structured Query and Task 4 wires the result through the UI
// overlay.
type Service struct {
	store IndexStore
	lazy  *LazyTrigger
	log   *slog.Logger
}

// NewService wires the search service. lazy may be nil — callers that
// don't want lazy reindex (CLI `lazytg reindex`, tests) pass nil and
// get a plain "search whatever's in the index" service.
func NewService(store IndexStore, lazy *LazyTrigger, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(discardHandler{})
	}
	return &Service{store: store, lazy: lazy, log: log}
}

// DefaultSearchLimit is the fallback when callers pass limit <= 0. Picked
// to match the search overlay's visible-rows budget.
const DefaultSearchLimit = 50

// Search runs raw against messages_fts MATCH and returns up to limit hits
// ordered by bm25 ascending (best first). limit <= 0 falls back to
// DefaultSearchLimit so UI code can omit it during early debugging.
//
// raw is forwarded to FTS5 MATCH verbatim at Stage 3 Task 2 — Task 3
// wraps this method behind a Query+parser layer that escapes user
// input. Callers must not pass untrusted raw strings here yet.
func (s *Service) Search(ctx context.Context, raw string, limit int) ([]Hit, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("search: empty query")
	}
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	if s.lazy != nil {
		_ = s.lazy.EnsureIndexed(ctx)
	}

	rows, err := s.store.DB().QueryContext(ctx, `
        SELECT m.id,
               m.chat_id,
               m.from_id,
               m.date,
               m.text,
               m.reply_to,
               m.raw_blob,
               snippet(messages_fts, 0, '<b>', '</b>', '...', 16) AS snippet,
               bm25(messages_fts) AS score
        FROM messages_fts
        JOIN messages m ON m.rowid = messages_fts.rowid
        WHERE messages_fts MATCH ?
        ORDER BY bm25(messages_fts)
        LIMIT ?
    `, raw, limit)
	if err != nil {
		return nil, fmt.Errorf("search %q: %w", raw, err)
	}
	defer func() { _ = rows.Close() }()

	var hits []Hit
	for rows.Next() {
		var (
			m       domain.Message
			fromID  sql.NullInt64
			text    sql.NullString
			replyTo sql.NullInt64
			date    int64
			rawBlob []byte
			snippet string
			score   float64
		)
		if err := rows.Scan(&m.ID, &m.ChatID, &fromID, &date, &text, &replyTo, &rawBlob, &snippet, &score); err != nil {
			return nil, fmt.Errorf("search scan: %w", err)
		}
		m.FromID = fromID.Int64
		m.Text = text.String
		m.ReplyTo = replyTo.Int64
		m.Date = time.Unix(date, 0).UTC()
		m.RawBlob = rawBlob
		hits = append(hits, Hit{
			Message: m,
			Snippet: snippet,
			ChatID:  m.ChatID,
			Score:   score,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search iterate: %w", err)
	}
	return hits, nil
}
