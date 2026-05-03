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

// DefaultJumpContext is the default ±N window applied by JumpContext when
// callers pass around <= 0. The number matches the Stage 3 plan: 5
// messages of context above and below the hit produce an 11-row window
// the user can scan at a glance without scrolling.
const DefaultJumpContext = 5

// JumpContext loads the messages around hit so the UI can scroll the
// thread pane to the hit with surrounding context. Returns up to
// (around*2 + 1) messages ordered oldest-first, plus the index of the
// target message inside that slice. The slice may be shorter than the
// theoretical maximum at the boundaries of a chat (the very first / last
// messages do not have a full ±around window). The target is always
// present unless the message is missing from the repo entirely (in
// which case the call returns ErrJumpTargetMissing).
//
// around <= 0 falls back to DefaultJumpContext so callers can omit the
// argument and get the documented behaviour.
func (s *Service) JumpContext(ctx context.Context, hit Hit, around int) ([]domain.Message, int, error) {
	if around <= 0 {
		around = DefaultJumpContext
	}
	db := s.store.DB()

	// Two windows joined around the target id keep the round-trip count
	// at 1: the "before" half pulls messages with id < target ordered
	// desc + LIMIT around, the "after" half pulls id >= target ordered
	// asc + LIMIT around+1 (so the target itself rides along). UNION ALL
	// preserves the ordering produced by each leg, then the outer
	// SELECT re-sorts the merged set ascending so the caller does not
	// have to.
	const windowSQL = `
        SELECT id, chat_id, from_id, date, text, reply_to, raw_blob FROM (
            SELECT id, chat_id, from_id, date, text, reply_to, raw_blob, 0 AS half
            FROM messages
            WHERE chat_id = ? AND id < ?
            ORDER BY id DESC
            LIMIT ?
        )
        UNION ALL
        SELECT id, chat_id, from_id, date, text, reply_to, raw_blob FROM (
            SELECT id, chat_id, from_id, date, text, reply_to, raw_blob, 1 AS half
            FROM messages
            WHERE chat_id = ? AND id >= ?
            ORDER BY id ASC
            LIMIT ?
        )
        ORDER BY id ASC
    `
	rows, err := db.QueryContext(ctx, windowSQL,
		hit.ChatID, hit.Message.ID, around,
		hit.ChatID, hit.Message.ID, around+1,
	)
	if err != nil {
		return nil, -1, fmt.Errorf("jump context chat=%d id=%d: %w", hit.ChatID, hit.Message.ID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Message
	target := -1
	for rows.Next() {
		var (
			m       domain.Message
			fromID  sql.NullInt64
			text    sql.NullString
			replyTo sql.NullInt64
			date    int64
			rawBlob []byte
		)
		if err := rows.Scan(&m.ID, &m.ChatID, &fromID, &date, &text, &replyTo, &rawBlob); err != nil {
			return nil, -1, fmt.Errorf("jump context scan: %w", err)
		}
		m.FromID = fromID.Int64
		m.Text = text.String
		m.ReplyTo = replyTo.Int64
		m.Date = time.Unix(date, 0).UTC()
		m.RawBlob = rawBlob
		if m.ID == hit.Message.ID {
			target = len(out)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, -1, fmt.Errorf("jump context iterate: %w", err)
	}
	if target < 0 {
		return nil, -1, ErrJumpTargetMissing
	}
	return out, target, nil
}

// ErrJumpTargetMissing is returned by JumpContext when the hit's
// message id is not present in the repo. In practice this only happens
// when the messages_fts row outlives the messages row (a delete that
// did not propagate), or when the test setup is incomplete.
var ErrJumpTargetMissing = errors.New("search: jump target not found")

// Search parses raw with the search grammar (operators, phrases,
// exclusions) and runs the resulting Query against messages_fts.
// Results are returned ordered by bm25 ascending (best first); limit
// <= 0 falls back to DefaultSearchLimit so UI code can omit it during
// early debugging.
//
// Stage 3 Task 3 routes raw through Parse + BuildSQL so the search
// service no longer hands user input to FTS5 verbatim — operators
// like from:@user / in:#chat / before: / after: now turn into
// parameterised SQL fragments and only the FTS5 MATCH expression
// itself stays string-substituted (it cannot be parameterised in
// modernc.org/sqlite without losing trigram tokenization).
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

	q, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	extraWhere, ftsMatch, extraArgs, err := BuildSQL(q)
	if err != nil {
		return nil, err
	}

	args := make([]any, 0, len(extraArgs)+2)
	args = append(args, ftsMatch)
	args = append(args, extraArgs...)
	args = append(args, limit)

	sqlText := `
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
        WHERE messages_fts MATCH ?` + extraWhere + `
        ORDER BY bm25(messages_fts)
        LIMIT ?
    `

	rows, err := s.store.DB().QueryContext(ctx, sqlText, args...)
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
