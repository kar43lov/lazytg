package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// OutgoingMessage mirrors the outgoing table. State is one of
// events.OutgoingStatePending / Sent / Failed; ServerID is meaningful only
// for state="sent" and Error for state="failed".
type OutgoingMessage struct {
	LocalID   string
	ChatID    int64
	Text      string
	ReplyTo   int64
	State     string
	ServerID  int64
	Error     string
	CreatedAt time.Time
}

// SaveOutgoing inserts a new optimistic record. CreatedAt defaults to
// time.Now in UTC when the caller leaves it as the zero value.
func (r *Repo) SaveOutgoing(ctx context.Context, m OutgoingMessage) error {
	if m.LocalID == "" {
		return errors.New("outgoing: local_id is required")
	}
	if m.ChatID == 0 {
		return errors.New("outgoing: chat_id is required")
	}
	if m.State == "" {
		return errors.New("outgoing: state is required")
	}
	created := m.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	_, err := r.db.ExecContext(ctx, `
        INSERT INTO outgoing (local_id, chat_id, text, reply_to, state, server_id, error, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)
    `,
		m.LocalID,
		m.ChatID,
		m.Text,
		m.ReplyTo,
		m.State,
		nullableInt64(m.ServerID),
		nullableString(m.Error),
		created.UTC().Unix(),
	)
	if err != nil {
		return fmt.Errorf("save outgoing %s: %w", m.LocalID, err)
	}
	return nil
}

// UpdateOutgoingState transitions an existing record. Pass serverID > 0
// when promoting to "sent"; pass errMsg for "failed". The function returns
// sql.ErrNoRows if no record matches localID — callers should treat this
// as a programming error (every transition follows a SaveOutgoing).
func (r *Repo) UpdateOutgoingState(ctx context.Context, localID, state string, serverID int64, errMsg string) error {
	if localID == "" {
		return errors.New("outgoing: local_id is required")
	}
	if state == "" {
		return errors.New("outgoing: state is required")
	}
	res, err := r.db.ExecContext(ctx, `
        UPDATE outgoing
           SET state     = ?,
               server_id = ?,
               error     = ?
         WHERE local_id  = ?
    `,
		state,
		nullableInt64(serverID),
		nullableString(errMsg),
		localID,
	)
	if err != nil {
		return fmt.Errorf("update outgoing %s: %w", localID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected outgoing %s: %w", localID, err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// GetOutgoing returns every outgoing record for chatID ordered by
// created_at ascending so the UI can render them in send order.
func (r *Repo) GetOutgoing(ctx context.Context, chatID int64) ([]OutgoingMessage, error) {
	rows, err := r.db.QueryContext(ctx, `
        SELECT local_id, chat_id, text, reply_to, state, server_id, error, created_at
          FROM outgoing
         WHERE chat_id = ?
      ORDER BY created_at ASC, local_id ASC
    `, chatID)
	if err != nil {
		return nil, fmt.Errorf("query outgoing chat=%d: %w", chatID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []OutgoingMessage
	for rows.Next() {
		var (
			m         OutgoingMessage
			serverID  sql.NullInt64
			errStr    sql.NullString
			createdAt int64
		)
		if err := rows.Scan(&m.LocalID, &m.ChatID, &m.Text, &m.ReplyTo, &m.State, &serverID, &errStr, &createdAt); err != nil {
			return nil, fmt.Errorf("scan outgoing: %w", err)
		}
		m.ServerID = serverID.Int64
		m.Error = errStr.String
		m.CreatedAt = time.Unix(createdAt, 0).UTC()
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate outgoing: %w", err)
	}
	return out, nil
}
