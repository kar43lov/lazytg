package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/gotd/td/telegram/updates"
)

// StateRepo persists the gotd updates.Manager state in SQLite. It implements
// the updates.StateStorage interface so reconnects can fetch only the
// difference since the last seen pts/qts/date/seq instead of re-downloading
// the entire history. The accountID is the Telegram self-user id (which
// matches accounts.id in our schema) so a single database can host multiple
// accounts without clobbering each other's pts.
//
// The implementation is intentionally SQL-light: every method touches one
// row and every write goes through a small UPSERT. updates.Manager is the
// only writer in production; concurrent updates within a single account are
// serialised by the manager itself, so we don't need transactions here.
type StateRepo struct {
	db *sql.DB
}

// NewStateRepo wraps an existing *sql.DB. Callers pass repo.DB() so the
// state storage lives in the same SQLite file (and the same migrations
// stack) as messages and chats.
func NewStateRepo(db *sql.DB) *StateRepo {
	return &StateRepo{db: db}
}

// Compile-time assertion that StateRepo satisfies the gotd contract.
var _ updates.StateStorage = (*StateRepo)(nil)

// errStateMissing is returned by methods documented to require an existing
// row (SetPts/SetQts/SetDate/SetSeq/SetDateSeq) when the user state row
// has not been persisted yet via SetState. Mirrors gotd's storage_mem
// behaviour so updates.Manager can detect the "first auth" case.
var errStateMissing = errors.New("state: row not found")

// GetState returns the persisted user state and whether it existed.
func (r *StateRepo) GetState(ctx context.Context, userID int64) (updates.State, bool, error) {
	row := r.db.QueryRowContext(ctx, `
        SELECT pts, qts, date, seq FROM state WHERE account_id = ?
    `, userID)
	var (
		pts, qts, date, seq sql.NullInt64
	)
	if err := row.Scan(&pts, &qts, &date, &seq); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return updates.State{}, false, nil
		}
		return updates.State{}, false, fmt.Errorf("query state user=%d: %w", userID, err)
	}
	return updates.State{
		Pts:  int(pts.Int64),
		Qts:  int(qts.Int64),
		Date: int(date.Int64),
		Seq:  int(seq.Int64),
	}, true, nil
}

// SetState upserts the full state. The row is created on first call and
// updated atomically afterwards.
func (r *StateRepo) SetState(ctx context.Context, userID int64, state updates.State) error {
	_, err := r.db.ExecContext(ctx, `
        INSERT INTO state (account_id, pts, qts, date, seq)
        VALUES (?, ?, ?, ?, ?)
        ON CONFLICT(account_id) DO UPDATE SET
            pts  = excluded.pts,
            qts  = excluded.qts,
            date = excluded.date,
            seq  = excluded.seq
    `, userID, state.Pts, state.Qts, state.Date, state.Seq)
	if err != nil {
		return fmt.Errorf("set state user=%d: %w", userID, err)
	}
	return nil
}

// SetPts updates only the pts column. Returns errStateMissing if no row
// exists yet — gotd requires SetState to have run first.
func (r *StateRepo) SetPts(ctx context.Context, userID int64, pts int) error {
	return r.updateField(ctx, userID, "pts", pts)
}

// SetQts updates only the qts column.
func (r *StateRepo) SetQts(ctx context.Context, userID int64, qts int) error {
	return r.updateField(ctx, userID, "qts", qts)
}

// SetDate updates only the date column.
func (r *StateRepo) SetDate(ctx context.Context, userID int64, date int) error {
	return r.updateField(ctx, userID, "date", date)
}

// SetSeq updates only the seq column.
func (r *StateRepo) SetSeq(ctx context.Context, userID int64, seq int) error {
	return r.updateField(ctx, userID, "seq", seq)
}

// SetDateSeq updates date and seq atomically.
func (r *StateRepo) SetDateSeq(ctx context.Context, userID int64, date, seq int) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE state SET date = ?, seq = ? WHERE account_id = ?`,
		date, seq, userID)
	if err != nil {
		return fmt.Errorf("set date+seq user=%d: %w", userID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected user=%d: %w", userID, err)
	}
	if n == 0 {
		return errStateMissing
	}
	return nil
}

// GetChannelPts returns the per-channel pts and whether the row existed.
func (r *StateRepo) GetChannelPts(ctx context.Context, userID, channelID int64) (int, bool, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT pts FROM channel_state WHERE account_id = ? AND channel_id = ?`,
		userID, channelID)
	var pts int
	if err := row.Scan(&pts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("query channel_state user=%d channel=%d: %w", userID, channelID, err)
	}
	return pts, true, nil
}

// SetChannelPts upserts the per-channel pts.
func (r *StateRepo) SetChannelPts(ctx context.Context, userID, channelID int64, pts int) error {
	_, err := r.db.ExecContext(ctx, `
        INSERT INTO channel_state (account_id, channel_id, pts)
        VALUES (?, ?, ?)
        ON CONFLICT(account_id, channel_id) DO UPDATE SET pts = excluded.pts
    `, userID, channelID, pts)
	if err != nil {
		return fmt.Errorf("set channel_state user=%d channel=%d: %w", userID, channelID, err)
	}
	return nil
}

// ForEachChannels invokes f for every persisted channel pts row for the
// given account. The callback's error short-circuits the iteration; rows
// already iterated are not rolled back.
func (r *StateRepo) ForEachChannels(ctx context.Context, userID int64, f func(ctx context.Context, channelID int64, pts int) error) error {
	rows, err := r.db.QueryContext(ctx,
		`SELECT channel_id, pts FROM channel_state WHERE account_id = ?`, userID)
	if err != nil {
		return fmt.Errorf("query channel_state user=%d: %w", userID, err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var channelID int64
		var pts int
		if err := rows.Scan(&channelID, &pts); err != nil {
			return fmt.Errorf("scan channel_state: %w", err)
		}
		if err := f(ctx, channelID, pts); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate channel_state: %w", err)
	}
	return nil
}

// updateField applies a single-column update, surfacing errStateMissing
// when the row hasn't been initialised yet. The query is selected from a
// fixed allowlist so gosec can verify there is no untrusted SQL
// interpolation; new fields require an explicit case here.
func (r *StateRepo) updateField(ctx context.Context, userID int64, col string, val int) error {
	var q string
	switch col {
	case "pts":
		q = `UPDATE state SET pts = ? WHERE account_id = ?`
	case "qts":
		q = `UPDATE state SET qts = ? WHERE account_id = ?`
	case "date":
		q = `UPDATE state SET date = ? WHERE account_id = ?`
	case "seq":
		q = `UPDATE state SET seq = ? WHERE account_id = ?`
	default:
		return fmt.Errorf("state: unsupported column %q", col)
	}
	res, err := r.db.ExecContext(ctx, q, val, userID)
	if err != nil {
		return fmt.Errorf("set %s user=%d: %w", col, userID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected user=%d: %w", userID, err)
	}
	if n == 0 {
		return errStateMissing
	}
	return nil
}
