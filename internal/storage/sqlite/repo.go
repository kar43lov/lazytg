package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pgmac/lazytg/internal/core/domain"
)

// ErrReadOnly is returned by every write method when the repo has been
// marked read-only by DegradationDetector. Callers should check via
// errors.Is and surface a friendly "storage is read-only" message in the
// UI rather than retrying.
var ErrReadOnly = errors.New("repo: read-only mode")

// Repo is the SQLite-backed storage repository. It owns the *sql.DB and runs
// migrations during Open. Methods are safe for concurrent use; SQLite (in WAL
// mode) handles serialisation of writes for us.
//
// readOnly toggles the soft read-only mode set by DegradationDetector when
// the underlying file rejects writes (chmod 0444, disk full, SQLITE_READONLY).
// Read paths keep working so the UI can still browse cached history while
// writes return ErrReadOnly. The flag is an atomic.Bool because the detector
// goroutine flips it concurrently with read/write traffic.
type Repo struct {
	db       *sql.DB
	readOnly atomic.Bool

	// dbPath is the on-disk path passed to Open — used by DBSizeMonitor
	// (which os.Stats the file directly) and by debug-bundle (which
	// summarises file size in db_stats.txt). Empty for in-memory or
	// file:-URI databases that do not map to a single file.
	dbPath string
}

// Open opens (or creates) the SQLite database at path, applies the WAL/foreign
// keys pragmas, and runs any pending migrations. Returns a ready-to-use Repo.
//
// The pragmas are encoded into the DSN so they apply to every connection in
// the pool. PRAGMA foreign_keys is connection-scoped: setting it via
// db.Exec only takes effect on whichever connection the driver hands back,
// and the rest of the pool would silently ignore foreign-key constraints.
func Open(ctx context.Context, path string) (*Repo, error) {
	dsn := buildDSN(path)
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping %q: %w", path, err)
	}
	// SECURITY.md and the threat model promise the DB file lives at 0600
	// (it stores account phones, chat titles and message text — same threat
	// model as the secrets file). modernc.org/sqlite respects the process
	// umask, which on stock Unix is 022 and would land the file at 0644.
	// Force 0600 here so the documented invariant holds regardless of umask.
	// Skip in-memory and file: URI forms (the URI form may carry mode=ro and
	// pragma query strings that we shouldn't shove at os.Chmod).
	if !strings.HasPrefix(path, ":memory:") && !strings.HasPrefix(path, "file:") {
		if err := os.Chmod(path, 0o600); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("chmod %q: %w", path, err)
		}
	}
	if err := RunMigrations(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}
	return &Repo{db: db, dbPath: filePath(path)}, nil
}

// filePath narrows the path argument to the single on-disk file backing
// the repo. Empty when the input does not map to a regular file
// (":memory:" databases, file: URIs that may carry mode=ro / cache=
// query strings). Callers that os.Stat the result must treat the empty
// string as "not applicable".
func filePath(path string) string {
	if path == ":memory:" || strings.HasPrefix(path, "file:") {
		return ""
	}
	return path
}

// DBPath returns the on-disk path of the SQLite file backing this repo,
// or an empty string when the repo was opened from an in-memory or file:
// URI source. Used by the DB-size monitor (which os.Stats the file in a
// loop) and by the debug-bundle producer (which records file size in
// db_stats.txt).
func (r *Repo) DBPath() string { return r.dbPath }

// buildDSN encodes path-level pragmas so every pooled connection inherits
// them. modernc.org/sqlite parses the _pragma query parameter and runs each
// statement on connect.
//
// Bare paths are wrapped into a file: URI through net/url so reserved
// characters (e.g. '?' or '#' anywhere in the path) cannot bleed into the
// query string and break pragma parsing.
func buildDSN(path string) string {
	if path == ":memory:" || strings.HasPrefix(path, "file:") {
		// callers passing a file: URI take responsibility for their own
		// pragma string; for the in-memory case the connection pool is
		// effectively a single connection so the legacy ExecContext form
		// would also work, but we keep the call site uniform.
		sep := "?"
		if strings.ContainsRune(path, '?') {
			sep = "&"
		}
		return path + sep + pragmaQuery
	}
	u := url.URL{Scheme: "file", Opaque: (&url.URL{Path: path}).EscapedPath(), RawQuery: pragmaQuery}
	return u.String()
}

// pragmaQuery is the URL-encoded set of PRAGMAs that must run on every
// connection in the pool. Order matches what applyPragmas used to do.
const pragmaQuery = "_pragma=journal_mode(wal)&_pragma=foreign_keys(1)&_pragma=synchronous(normal)"

// Close releases the underlying database connection.
func (r *Repo) Close() error { return r.db.Close() }

// IsReadOnly reports whether the repo is currently in soft read-only mode.
// Set asynchronously by DegradationDetector when writes fail; cleared again
// when access is restored.
func (r *Repo) IsReadOnly() bool { return r.readOnly.Load() }

// SetReadOnly toggles the soft read-only flag. DegradationDetector is the
// only legitimate writer in production; tests use it directly to assert
// the gating behaviour without touching filesystem permissions.
func (r *Repo) SetReadOnly(v bool) { r.readOnly.Store(v) }

// ProbeWrite verifies the database file accepts writes without polluting
// any user-visible table. The probe acquires a dedicated connection,
// issues BEGIN IMMEDIATE (which requires the write lock — SQLite returns
// SQLITE_READONLY here when the file is on a read-only mount or chmoded
// 0444), then rolls back. DegradationDetector translates a non-nil error
// into StorageStateChanged{read-only}.
//
// The probe deliberately bypasses the soft read-only flag so the detector
// can re-arm rw mode once the underlying issue is resolved.
//
// A dedicated *sql.Conn is used because BEGIN/ROLLBACK on a pooled
// connection would leak transaction state to whichever goroutine the
// pool hands the connection to next.
func (r *Repo) ProbeWrite(ctx context.Context) error {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("probe conn: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("probe begin immediate: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		return fmt.Errorf("probe rollback: %w", err)
	}
	return nil
}

// DB exposes the underlying *sql.DB. Used by tests and by code that needs to
// run ad-hoc statements (e.g. the FTS index builder in stage 3). Callers are
// expected not to close this handle.
func (r *Repo) DB() *sql.DB { return r.db }

// SaveAccount upserts an account row by phone. CreatedAt is preserved on
// updates so the original login moment survives alias changes. When the
// supplied Alias is empty we keep whatever alias the row already has — that
// way `lazytg login` re-running on an existing account does not silently
// clear an alias the user (or a future `accounts rename` command) set.
func (r *Repo) SaveAccount(ctx context.Context, a domain.Account) error {
	if r.readOnly.Load() {
		return ErrReadOnly
	}
	if a.Phone == "" {
		return errors.New("account: phone is required")
	}
	created := a.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	_, err := r.db.ExecContext(ctx, `
        INSERT INTO accounts (id, phone, alias, created_at)
        VALUES (?, ?, ?, ?)
        ON CONFLICT(phone) DO UPDATE SET
            alias = COALESCE(excluded.alias, accounts.alias)
    `,
		nullableInt64(a.ID),
		a.Phone,
		nullableString(a.Alias),
		created.UTC().Unix(),
	)
	if err != nil {
		return fmt.Errorf("save account %q: %w", a.Phone, err)
	}
	return nil
}

// GetAccounts returns every persisted account ordered by created_at ascending
// so the oldest one is shown first (handy for "active by default" semantics).
func (r *Repo) GetAccounts(ctx context.Context) ([]domain.Account, error) {
	rows, err := r.db.QueryContext(ctx, `
        SELECT id, phone, alias, created_at
        FROM accounts
        ORDER BY created_at ASC, phone ASC
    `)
	if err != nil {
		return nil, fmt.Errorf("query accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Account
	for rows.Next() {
		var (
			a       domain.Account
			id      sql.NullInt64
			alias   sql.NullString
			created int64
		)
		if err := rows.Scan(&id, &a.Phone, &alias, &created); err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		a.ID = id.Int64
		a.Alias = alias.String
		a.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate accounts: %w", err)
	}
	return out, nil
}

// DeleteAccount removes an account row by phone. Returns nil if the account
// does not exist so logout flows are idempotent.
func (r *Repo) DeleteAccount(ctx context.Context, phone string) error {
	if r.readOnly.Load() {
		return ErrReadOnly
	}
	if phone == "" {
		return errors.New("account: phone is required")
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM accounts WHERE phone = ?`, phone); err != nil {
		return fmt.Errorf("delete account %q: %w", phone, err)
	}
	return nil
}

// SaveChat upserts a chat row using SQLite's ON CONFLICT REPLACE semantics.
func (r *Repo) SaveChat(ctx context.Context, c domain.Chat) error {
	if r.readOnly.Load() {
		return ErrReadOnly
	}
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
	if r.readOnly.Load() {
		return ErrReadOnly
	}
	if m.ChatID == 0 {
		return errors.New("message: chat_id is required")
	}
	if m.ID == 0 {
		return errors.New("message: id is required")
	}
	// time.Time{}.UTC().Unix() == -62135596800 (year 1 BCE in Unix epoch),
	// which would poison every downstream consumer (UI ordering, FTS5
	// before:/after: filters, the lazy-index "last 5000" cap). Guard here so
	// the row is rejected before it lands in the column.
	if m.Date.IsZero() {
		return errors.New("message: date is required")
	}
	args := messageInsertArgs(m)
	_, err := r.db.ExecContext(ctx, messageUpsertSQL, args...)
	if err != nil {
		return fmt.Errorf("save message %d/%d: %w", m.ChatID, m.ID, err)
	}
	return nil
}

// messageUpsertSQL is the canonical INSERT … ON CONFLICT statement used by
// SaveMessage / SaveMessages. Media columns (added in migration 0008)
// participate in the upsert so a re-fetch of the same message updates a
// stale file_reference (which expires every ~1h) without needing a
// dedicated UPDATE path.
const messageUpsertSQL = `
        INSERT INTO messages (
            id, chat_id, from_id, date, text, reply_to, raw_blob,
            media_kind, media_id, media_access_hash, media_file_reference,
            media_dc, media_filename, media_size, media_mime_type, media_thumb_size
        )
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(chat_id, id) DO UPDATE SET
            from_id              = excluded.from_id,
            date                 = excluded.date,
            text                 = excluded.text,
            reply_to             = excluded.reply_to,
            raw_blob             = excluded.raw_blob,
            media_kind           = excluded.media_kind,
            media_id             = excluded.media_id,
            media_access_hash    = excluded.media_access_hash,
            media_file_reference = excluded.media_file_reference,
            media_dc             = excluded.media_dc,
            media_filename       = excluded.media_filename,
            media_size           = excluded.media_size,
            media_mime_type      = excluded.media_mime_type,
            media_thumb_size     = excluded.media_thumb_size
    `

// messageInsertArgs builds the positional argument slice for
// messageUpsertSQL. Centralised here so SaveMessage and SaveMessages stay
// in lockstep when columns are added (e.g. a future media_caption).
func messageInsertArgs(m domain.Message) []any {
	var (
		mediaKind sql.NullString
		mediaID   sql.NullInt64
		mediaAH   sql.NullInt64
		mediaRef  []byte
		mediaDC   sql.NullInt64
		mediaName sql.NullString
		mediaSize sql.NullInt64
		mediaMime sql.NullString
		mediaThSz sql.NullString
	)
	if m.Media != nil {
		mediaKind = sql.NullString{String: string(m.Media.Kind), Valid: m.Media.Kind != ""}
		mediaID = nullableInt64(m.Media.FileID)
		mediaAH = sql.NullInt64{Int64: m.Media.AccessHash, Valid: true}
		mediaRef = m.Media.FileReference
		if m.Media.DC != 0 {
			mediaDC = sql.NullInt64{Int64: int64(m.Media.DC), Valid: true}
		}
		mediaName = nullableString(m.Media.Filename)
		mediaSize = nullableInt64(m.Media.Size)
		mediaMime = nullableString(m.Media.MimeType)
		mediaThSz = nullableString(m.Media.ThumbSize)
	}
	return []any{
		m.ID,
		m.ChatID,
		nullableInt64(m.FromID),
		m.Date.UTC().Unix(),
		m.Text,
		nullableInt64(m.ReplyTo),
		m.RawBlob,
		mediaKind,
		mediaID,
		mediaAH,
		mediaRef,
		mediaDC,
		mediaName,
		mediaSize,
		mediaMime,
		mediaThSz,
	}
}

// SaveMessages inserts or replaces a batch of messages inside a single
// transaction. The atomic write avoids the per-statement fsync amplification
// that history backfills would otherwise hit (200 messages × WAL fsync turns
// into a multi-second stall on slow disks). Empty input is a no-op.
//
// Validation matches SaveMessage: a message with chat_id == 0, id == 0 or a
// zero Date is rejected before any row is written so a single bad item cannot
// leave the rest half-applied.
func (r *Repo) SaveMessages(ctx context.Context, msgs []domain.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	if r.readOnly.Load() {
		return ErrReadOnly
	}
	for i, m := range msgs {
		if m.ChatID == 0 {
			return fmt.Errorf("message[%d]: chat_id is required", i)
		}
		if m.ID == 0 {
			return fmt.Errorf("message[%d]: id is required", i)
		}
		if m.Date.IsZero() {
			return fmt.Errorf("message[%d]: date is required", i)
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, messageUpsertSQL)
	if err != nil {
		return fmt.Errorf("prepare stmt: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, m := range msgs {
		if _, err := stmt.ExecContext(ctx, messageInsertArgs(m)...); err != nil {
			return fmt.Errorf("save message %d/%d: %w", m.ChatID, m.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// GetMessages returns up to limit messages from chatID ordered by date desc.
// Offset is applied after ordering. Pass limit <= 0 to get an empty slice.
func (r *Repo) GetMessages(ctx context.Context, chatID int64, limit, offset int) ([]domain.Message, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, messageSelectColumns+`
        FROM messages
        WHERE chat_id = ?
        ORDER BY date DESC, id DESC
        LIMIT ? OFFSET ?
    `, chatID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("query messages chat=%d: %w", chatID, err)
	}
	defer func() { _ = rows.Close() }()

	return scanMessages(rows)
}

// messageSelectColumns is the canonical SELECT fragment used by every
// row-returning helper that returns full domain.Message values. Centralised
// so a future media column lands in scanMessages without sweeping through
// half a dozen call sites.
const messageSelectColumns = `
        SELECT id, chat_id, from_id, date, text, reply_to, raw_blob,
               media_kind, media_id, media_access_hash, media_file_reference,
               media_dc, media_filename, media_size, media_mime_type, media_thumb_size
    `

// scanMessages drains rows into a slice of domain.Message, parsing the
// media columns into a *MediaInfo when media_kind is non-NULL.
func scanMessages(rows *sql.Rows) ([]domain.Message, error) {
	var out []domain.Message
	for rows.Next() {
		var (
			m         domain.Message
			fromID    sql.NullInt64
			text      sql.NullString
			replyTo   sql.NullInt64
			date      int64
			raw       []byte
			mediaKind sql.NullString
			mediaID   sql.NullInt64
			mediaAH   sql.NullInt64
			mediaRef  []byte
			mediaDC   sql.NullInt64
			mediaName sql.NullString
			mediaSize sql.NullInt64
			mediaMime sql.NullString
			mediaThSz sql.NullString
		)
		if err := rows.Scan(
			&m.ID, &m.ChatID, &fromID, &date, &text, &replyTo, &raw,
			&mediaKind, &mediaID, &mediaAH, &mediaRef,
			&mediaDC, &mediaName, &mediaSize, &mediaMime, &mediaThSz,
		); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		m.FromID = fromID.Int64
		m.Text = text.String
		m.ReplyTo = replyTo.Int64
		m.Date = time.Unix(date, 0).UTC()
		m.RawBlob = raw
		if mediaKind.Valid && mediaKind.String != "" {
			m.Media = &domain.MediaInfo{
				Kind:          domain.MediaKind(mediaKind.String),
				FileID:        mediaID.Int64,
				AccessHash:    mediaAH.Int64,
				FileReference: mediaRef,
				DC:            int(mediaDC.Int64),
				Filename:      mediaName.String,
				Size:          mediaSize.Int64,
				MimeType:      mediaMime.String,
				ThumbSize:     mediaThSz.String,
			}
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}
	return out, nil
}

// GetMessagesBefore returns up to limit messages from chatID with id strictly
// less than beforeID, ordered by id desc. Used by the thread pane for
// cursor-based pagination — passing offset would race with applyIncoming
// (live messages appended between initial load and scroll-up shift the
// "skip N rows" semantics under our feet, leaving holes in the displayed
// history). Pass beforeID = oldestID currently rendered.
//
// Pass limit <= 0 to get an empty slice. beforeID == 0 also yields nil
// because cursor-based pagination has no defined "before nothing".
func (r *Repo) GetMessagesBefore(ctx context.Context, chatID, beforeID int64, limit int) ([]domain.Message, error) {
	if limit <= 0 || beforeID <= 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, messageSelectColumns+`
        FROM messages
        WHERE chat_id = ? AND id < ?
        ORDER BY id DESC
        LIMIT ?
    `, chatID, beforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("query messages before chat=%d id=%d: %w", chatID, beforeID, err)
	}
	defer func() { _ = rows.Close() }()

	return scanMessages(rows)
}

// GetMessagesAfter returns up to limit messages from chatID with id strictly
// greater than afterID, ordered by id asc. Used by the thread pane for
// forward pagination after a search-jump landed the user in the middle
// of history — scroll-down at the bottom of the loaded ±N window asks
// for the next batch of newer rows. Cursor-based for the same reason
// GetMessagesBefore is: live appends between the jump and a scroll-down
// would otherwise shift any offset semantics out from under us.
//
// Pass limit <= 0 to get an empty slice. afterID == 0 yields nil — the
// "after nothing" case has no defined semantics; callers reaching that
// branch are working with a thread pane in a non-jump state where
// forward pagination is meaningless.
func (r *Repo) GetMessagesAfter(ctx context.Context, chatID, afterID int64, limit int) ([]domain.Message, error) {
	if limit <= 0 || afterID <= 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, messageSelectColumns+`
        FROM messages
        WHERE chat_id = ? AND id > ?
        ORDER BY id ASC
        LIMIT ?
    `, chatID, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("query messages after chat=%d id=%d: %w", chatID, afterID, err)
	}
	defer func() { _ = rows.Close() }()

	return scanMessages(rows)
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

// nullableString returns NULL for an empty string so optional text columns can
// hold real NULLs instead of empty strings (eases SQL filters like IS NULL).
func nullableString(v string) sql.NullString {
	if v == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: v, Valid: true}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
