package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/pgmac/lazytg/internal/core/domain"
)

// Repo is the SQLite-backed storage repository. It owns the *sql.DB and runs
// migrations during Open. Methods are safe for concurrent use; SQLite (in WAL
// mode) handles serialisation of writes for us.
type Repo struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at path, applies the WAL/foreign
// keys pragmas, and runs any pending migrations. Returns a ready-to-use Repo.
func Open(ctx context.Context, path string) (*Repo, error) {
	db, err := sql.Open(driverName, path)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", path, err)
	}
	if err := applyPragmas(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := RunMigrations(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}
	return &Repo{db: db}, nil
}

// Close releases the underlying database connection.
func (r *Repo) Close() error { return r.db.Close() }

// DB exposes the underlying *sql.DB. Used by tests and by code that needs to
// run ad-hoc statements (e.g. the FTS index builder in stage 3). Callers are
// expected not to close this handle.
func (r *Repo) DB() *sql.DB { return r.db }

// SaveChat upserts a chat row using SQLite's ON CONFLICT REPLACE semantics.
func (r *Repo) SaveChat(ctx context.Context, c domain.Chat) error {
	_, err := r.db.ExecContext(ctx, `
        INSERT INTO chats (id, type, title, username, last_message_date, unread_count, pinned)
        VALUES (?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
            type              = excluded.type,
            title             = excluded.title,
            username          = excluded.username,
            last_message_date = excluded.last_message_date,
            unread_count      = excluded.unread_count,
            pinned            = excluded.pinned
    `,
		c.ID,
		string(c.Type),
		c.Title,
		c.Username,
		nullableUnix(c.LastMessageDate),
		c.UnreadCount,
		boolToInt(c.Pinned),
	)
	if err != nil {
		return fmt.Errorf("save chat %d: %w", c.ID, err)
	}
	return nil
}

// GetChats returns all chats ordered by pinned-first, then last_message_date desc.
func (r *Repo) GetChats(ctx context.Context) ([]domain.Chat, error) {
	rows, err := r.db.QueryContext(ctx, `
        SELECT id, type, title, username, last_message_date, unread_count, pinned
        FROM chats
        ORDER BY pinned DESC, COALESCE(last_message_date, 0) DESC, id ASC
    `)
	if err != nil {
		return nil, fmt.Errorf("query chats: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Chat
	for rows.Next() {
		var (
			c        domain.Chat
			typ      string
			title    sql.NullString
			username sql.NullString
			lastDate sql.NullInt64
			pinned   int
		)
		if err := rows.Scan(&c.ID, &typ, &title, &username, &lastDate, &c.UnreadCount, &pinned); err != nil {
			return nil, fmt.Errorf("scan chat: %w", err)
		}
		c.Type = domain.ChatType(typ)
		c.Title = title.String
		c.Username = username.String
		if lastDate.Valid {
			c.LastMessageDate = time.Unix(lastDate.Int64, 0).UTC()
		}
		c.Pinned = pinned != 0
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chats: %w", err)
	}
	return out, nil
}

// SaveMessage inserts or replaces a message. The composite primary key is
// (chat_id, id), matching Telegram's per-chat message numbering.
func (r *Repo) SaveMessage(ctx context.Context, m domain.Message) error {
	if m.ChatID == 0 {
		return errors.New("message: chat_id is required")
	}
	_, err := r.db.ExecContext(ctx, `
        INSERT INTO messages (id, chat_id, from_id, date, text, reply_to, raw_blob)
        VALUES (?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(chat_id, id) DO UPDATE SET
            from_id  = excluded.from_id,
            date     = excluded.date,
            text     = excluded.text,
            reply_to = excluded.reply_to,
            raw_blob = excluded.raw_blob
    `,
		m.ID,
		m.ChatID,
		nullableInt64(m.FromID),
		m.Date.UTC().Unix(),
		m.Text,
		nullableInt64(m.ReplyTo),
		m.RawBlob,
	)
	if err != nil {
		return fmt.Errorf("save message %d/%d: %w", m.ChatID, m.ID, err)
	}
	return nil
}

// GetMessages returns up to limit messages from chatID ordered by date desc.
// Offset is applied after ordering. Pass limit <= 0 to get an empty slice.
func (r *Repo) GetMessages(ctx context.Context, chatID int64, limit, offset int) ([]domain.Message, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `
        SELECT id, chat_id, from_id, date, text, reply_to, raw_blob
        FROM messages
        WHERE chat_id = ?
        ORDER BY date DESC, id DESC
        LIMIT ? OFFSET ?
    `, chatID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("query messages chat=%d: %w", chatID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Message
	for rows.Next() {
		var (
			m       domain.Message
			fromID  sql.NullInt64
			text    sql.NullString
			replyTo sql.NullInt64
			date    int64
			raw     []byte
		)
		if err := rows.Scan(&m.ID, &m.ChatID, &fromID, &date, &text, &replyTo, &raw); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		m.FromID = fromID.Int64
		m.Text = text.String
		m.ReplyTo = replyTo.Int64
		m.Date = time.Unix(date, 0).UTC()
		m.RawBlob = raw
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}
	return out, nil
}

// nullableUnix returns a sql.NullInt64 for the Unix timestamp of t, or NULL if
// t is the zero value (so we can distinguish "never had a message" chats).
func nullableUnix(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UTC().Unix(), Valid: true}
}

// nullableInt64 returns NULL when v is 0 so callers can express "no value" via
// the zero int64 without introducing pointer fields in the domain types.
func nullableInt64(v int64) sql.NullInt64 {
	if v == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: v, Valid: true}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
