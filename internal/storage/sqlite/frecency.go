package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

// frecencyHalfLifeDays is the time-decay constant used by RecordVisit when
// it folds the new visit into the persisted score column. With λ = 1/30
// days a chat the user has not visited for a month contributes ~37 % of
// its raw visit count to the ranking; at three months it is down to ~5 %.
// This matches the muscle-memory horizon documented for the L1 palette
// (top-50 chats by frecency) — recent traffic dominates, but a stable
// long-running thread is not flushed out by a single noisy day.
const frecencyHalfLifeDays = 30.0

// RecordVisit logs a chat visit and refreshes the cached frecency score.
//
// The score is precomputed here (not at TopFrecency time) so the hot
// palette-open path is a sequential index scan ordered by score DESC and
// nothing more. The trade-off is that the score is "fresh as of the last
// visit" — relative ordering only changes when the user actually visits
// a chat, which is exactly the moment the order should be re-considered.
//
// Algorithm:
//  1. UPSERT visit_count = visit_count + 1, last_visit = now
//  2. score = visit_count * exp(-(now - last_visit_before_this) / 30 days)
//
// Because the score is computed AFTER the UPSERT (so visit_count already
// includes this visit), step 2 runs as a second UPDATE on the same row.
// SQLite serialises both inside the implicit transaction the *sql.DB
// connection holds for the call, but we wrap them explicitly to be safe
// against driver pool quirks.
func (r *Repo) RecordVisit(ctx context.Context, chatID int64, now time.Time) error {
	if r.readOnly.Load() {
		return ErrReadOnly
	}
	if chatID == 0 {
		return errors.New("frecency: chat_id is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	nowUnix := now.UTC().Unix()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("frecency begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Read previous last_visit so we can compute decay against the prior
	// visit moment rather than "now" (which would always give exp(0) = 1
	// and turn score into pure visit_count). For first-time visits the
	// row does not yet exist, sql.ErrNoRows → prior = now → decay = 1
	// which is the correct behaviour (1 visit, fully fresh).
	var priorLastVisit int64
	err = tx.QueryRowContext(ctx, `SELECT last_visit FROM chat_frecency WHERE chat_id = ?`, chatID).Scan(&priorLastVisit)
	if errors.Is(err, sql.ErrNoRows) {
		priorLastVisit = nowUnix
	} else if err != nil {
		return fmt.Errorf("frecency read prior: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
        INSERT INTO chat_frecency (chat_id, visit_count, last_visit, score)
        VALUES (?, 1, ?, 0)
        ON CONFLICT(chat_id) DO UPDATE SET
            visit_count = chat_frecency.visit_count + 1,
            last_visit  = excluded.last_visit
    `, chatID, nowUnix); err != nil {
		return fmt.Errorf("frecency upsert: %w", err)
	}

	var visitCount int64
	if err := tx.QueryRowContext(ctx, `SELECT visit_count FROM chat_frecency WHERE chat_id = ?`, chatID).Scan(&visitCount); err != nil {
		return fmt.Errorf("frecency read count: %w", err)
	}

	ageDays := float64(nowUnix-priorLastVisit) / 86400.0
	if ageDays < 0 {
		ageDays = 0
	}
	score := float64(visitCount) * math.Exp(-ageDays/frecencyHalfLifeDays)

	if _, err := tx.ExecContext(ctx, `UPDATE chat_frecency SET score = ? WHERE chat_id = ?`, score, chatID); err != nil {
		return fmt.Errorf("frecency update score: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("frecency commit: %w", err)
	}
	return nil
}

// TopFrecency returns up to limit chat_ids ordered by score DESC. Used by
// the L1 palette to seed its candidate list before fuzzy filtering kicks
// in. Limit ≤ 0 is normalised to 1 so a misconfigured caller cannot drop
// the underlying SQL into an unbounded scan.
func (r *Repo) TopFrecency(ctx context.Context, limit int) ([]int64, error) {
	if limit <= 0 {
		limit = 1
	}
	rows, err := r.db.QueryContext(ctx, `
        SELECT chat_id
        FROM chat_frecency
        ORDER BY score DESC, last_visit DESC
        LIMIT ?
    `, limit)
	if err != nil {
		return nil, fmt.Errorf("frecency query top: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("frecency scan id: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("frecency iterate: %w", err)
	}
	return out, nil
}
