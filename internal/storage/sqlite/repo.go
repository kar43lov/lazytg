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

	"github.com/kar43lov/lazytg/internal/core/domain"
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
//
// busy_timeout is not optional. SQLite allows exactly one writer at a time,
// and without a busy timeout a connection that finds the write lock taken
// fails the statement instantly with SQLITE_BUSY instead of waiting. lazytg
// has several write paths that run concurrently by design — live drain,
// history backfill, dialog sync, FTS reindex — so this was not a rare race:
// measured on a burst of 4 goroutines × 300 writes, 973 of the 1200 were lost
// this way. TestConcurrentWriters_NoBusyFailures reproduces the same shape at
// 4 × 150 (kept smaller to stay quick) and asserts zero failures.
//
// 5s covers the longest transaction this app issues by a wide margin: a full
// 5000-row Indexer.Backfill, the biggest single write transaction here, was
// measured at 151ms.
const pragmaQuery = "_pragma=journal_mode(wal)&_pragma=foreign_keys(1)&_pragma=synchronous(normal)&_pragma=busy_timeout(5000)"

// SQLite primary result codes that mean "the lock is taken" rather than
// "storage refuses writes".
//
// Masking is required, not defensive: the driver enables extended result
// codes on every connection (modernc.org/sqlite conn.go, extendedResultCodes),
// so BUSY arrives as e.g. SQLITE_BUSY_SNAPSHOT (517) and LOCKED as
// SQLITE_LOCKED_VTAB (518). Extended codes keep the primary code in the low
// byte (sqlite3.h), so compare after masking.
const (
	sqliteBusy   = 5 // SQLITE_BUSY: another connection holds the write lock.
	sqliteLocked = 6 // SQLITE_LOCKED: the table is locked, typically within
	// the same connection or a shared cache. Unlike BUSY, SQLite's busy
	// handler does not retry it, and BEGIN IMMEDIATE on a dedicated
	// connection should not produce it — it is accepted here because it
	// still describes a lock rather than a storage failure.
)

// isContention reports whether err is SQLite saying the lock is taken.
//
// The check goes through an interface rather than the driver's concrete error
// type because the driver is selected by build tag (driver_modernc.go /
// driver_sqlcipher.go) and this file is compiled under both.
//
// No genuine storage failure masks into these two: PERM(3), READONLY(8),
// IOERR(10), CORRUPT(11), FULL(13), CANTOPEN(14) and NOTADB(26) all keep a
// low byte of their own, so classification cannot swallow them.
func isContention(err error) bool {
	var coder interface{ Code() int }
	if !errors.As(err, &coder) {
		return false
	}
	switch coder.Code() & 0xFF {
	case sqliteBusy, sqliteLocked:
		return true
	default:
		return false
	}
}

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

// ProbeWrite reports whether the storage still accepts writes, without
// polluting any user-visible table: a dedicated connection issues
// BEGIN IMMEDIATE, which acquires the write lock, then rolls back.
// DegradationDetector translates a non-nil error into
// StorageStateChanged{read-only}.
//
// Note what the probe does and does not prove. BEGIN IMMEDIATE acquires the
// write lock, so failures at connection and lock level surface here; the
// DELETE below then dirties a page, which is what surfaces a read-only
// database (see the last paragraph). Which of SQLITE_FULL and the write-time
// SQLITE_IOERR variants reach this probe has not been established; do not
// assume from this code that they do.
//
// The probe deliberately bypasses the soft read-only flag so the detector
// can re-arm rw mode once the underlying issue is resolved.
//
// A dedicated *sql.Conn is used because BEGIN/ROLLBACK on a pooled
// connection would leak transaction state to whichever goroutine the
// pool hands the connection to next.
//
// Contention is reported as success. SQLITE_BUSY means another connection
// currently holds the write lock, which is positive evidence that the storage
// accepts writes — the opposite of what the detector is looking for. Treating
// it as failure put the whole repo into soft read-only mode whenever a probe
// happened to land inside a write burst: every subsequent write returned
// ErrReadOnly and the status bar blamed the filesystem, until the next probe
// 30s later found the database idle. That is how a healthy machine ended up
// reporting "read-only mode" mid-backfill.
//
// What this probe therefore does NOT detect, so nobody mistakes it for a
// general write-health check:
//   - a lock held indefinitely (a wedged transaction, an app-level deadlock).
//     Contention is indistinguishable from a stuck writer from here, and in
//     that state ordinary writes fail with BUSY of their own after the
//     busy_timeout while the detector keeps reporting healthy storage.
//
// What the contention tolerance does not cost: any other failure code that
// does reach the probe is still reported, because none of them mask to BUSY or
// LOCKED (TestIsContention_ClassifiesResultCodes). Tolerating contention
// narrows nothing that was previously detected.
//
// A read-only database file used to slip through here for the same reason:
// BEGIN IMMEDIATE takes the write lock but dirties no page, so SQLite had no
// reason to raise SQLITE_READONLY and the probe called a file healthy while
// every ordinary write failed with code 8. The DELETE below closes that —
// see TestProbeWrite_DetectsAReadOnlyDatabase.
func (r *Repo) ProbeWrite(ctx context.Context) error {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("probe conn: %w", err)
	}
	defer func() { _ = conn.Close() }()
	// No per-connection PRAGMA busy_timeout here, tempting as a shorter probe
	// timeout is: Close returns the connection to the pool, and busy_timeout
	// is connection-scoped, so the next ordinary writer to be handed this
	// connection would inherit the probe's shorter patience. Waiting out the
	// busy_timeout from the DSN costs nothing that matters — the only thing
	// delayed is a background goroutine whose sole job is to probe again later.
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		if isContention(err) {
			return nil
		}
		return fmt.Errorf("probe begin immediate: %w", err)
	}
	// From here the transaction is open and must be closed on every path out.
	// modernc's ResetSession returns nil without touching an in-flight
	// transaction (sqlite@v1.56.0/conn.go), so a connection handed back to the
	// pool mid-transaction keeps the write lock — and the next writer to be
	// given it waits out the busy_timeout and fails, for as long as the
	// process lives. The rollback therefore runs on a context that cannot be
	// cancelled: the probe's own ctx is the app's, and cancelling it during a
	// probe is exactly when this would otherwise leak.
	txOpen := true
	defer func() {
		if txOpen {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()
	// The transaction has to touch a page for this to be a write probe at all.
	// `WHERE 0` is constant-false, so the statement can never delete a row,
	// but it still opens a write cursor — which is the moment SQLite raises
	// SQLITE_READONLY on a file it cannot write.
	//
	// The contention check here is not symmetrical with the one above: we
	// already hold the write lock, so SQLITE_BUSY is unreachable at this
	// point. It stays for SQLITE_LOCKED, which table-level locking can still
	// produce, and because treating either as an outage is the mistake this
	// probe was fixed for once already.
	if _, err := conn.ExecContext(ctx, "DELETE FROM chats WHERE 0"); err != nil {
		if isContention(err) {
			return nil
		}
		return fmt.Errorf("probe write: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		return fmt.Errorf("probe rollback: %w", err)
	}
	txOpen = false
	return nil
}

// DeleteMessages removes messages a deletion update reported. Returns how
// many rows went away, which is what the caller logs — a delete that matched
// nothing is the interesting case, not the successful one.
//
// chatID zero means the update did not name a chat, which is how Telegram
// reports deletions in private chats and basic groups: the ids alone identify
// the messages across that whole id space. Channels number their messages
// independently, so the same id exists there and means something else — hence
// the type filter rather than a plain "delete by id".
//
// The FTS index needs no attention here: migration 0005 keeps it in step
// through a DELETE trigger on messages.
func (r *Repo) DeleteMessages(ctx context.Context, chatID int64, ids []int64) (int64, error) {
	if r.readOnly.Load() {
		return 0, ErrReadOnly
	}
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)

	var query string
	if chatID != 0 {
		query = "DELETE FROM messages WHERE chat_id = ? AND id IN (" + placeholders + ")"
		args = append(args, chatID)
	} else {
		query = "DELETE FROM messages WHERE id IN (" + placeholders + ") AND chat_id IN (" +
			"SELECT id FROM chats WHERE type IN ('private', 'group'))"
	}
	for _, id := range ids {
		args = append(args, id)
	}

	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("delete messages (chat=%d, n=%d): %w", chatID, len(ids), err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete messages: rows affected: %w", err)
	}
	return affected, nil
}

// DeleteChatsExcept removes every chat whose id is not in keep, and returns
// how many went. Messages follow through the ON DELETE CASCADE on
// messages.chat_id, and the FTS index follows them through its trigger.
//
// This is how a chat deleted from another device finally disappears: dialog
// sync only upserts, so before this the row survived every sync forever.
//
// 🔴 The caller must only invoke this after seeing the server's dialog list in
// full. The sync stops after five pages by design, and treating "not in the
// pages I fetched" as "deleted" would wipe most of the mirror for anyone with
// more than 500 chats. An empty keep set is refused for the same reason: one
// empty page from a hiccuping server would otherwise erase everything.
func (r *Repo) DeleteChatsExcept(ctx context.Context, keep []int64) (int64, error) {
	if r.readOnly.Load() {
		return 0, ErrReadOnly
	}
	if len(keep) == 0 {
		return 0, errors.New("delete chats: refusing to prune against an empty chat list")
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(keep)), ",")
	args := make([]any, 0, len(keep))
	for _, id := range keep {
		args = append(args, id)
	}
	// The concatenated part is a run of `?` built from len(keep) — every id
	// travels as a bound argument. Same shape as DeleteMessages above.
	//nolint:gosec // placeholders are generated; the ids themselves are bound
	query := "DELETE FROM chats WHERE id NOT IN (" + placeholders + ")"
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("delete chats not in a %d-chat list: %w", len(keep), err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete chats: rows affected: %w", err)
	}
	return affected, nil
}

// ClearUnread zeroes a chat's unread counter after the user has read it. The
// counter is otherwise only ever written by dialog sync, so without this the
// badge survives reading the chat until the next sync — and on the server the
// messages stay unread regardless, which is what ReadService reports.
func (r *Repo) ClearUnread(ctx context.Context, chatID int64) error {
	if r.readOnly.Load() {
		return ErrReadOnly
	}
	if _, err := r.db.ExecContext(ctx,
		`UPDATE chats SET unread_count = 0 WHERE id = ?`, chatID); err != nil {
		return fmt.Errorf("clear unread for chat %d: %w", chatID, err)
	}
	return nil
}

// IncrementUnread raises a chat's unread counter by one for a message that
// arrived while the user was not reading that chat.
//
// Dialog sync used to be the only writer, and it runs once at startup: a
// message arriving into a closed chat therefore showed no badge at all until
// the next launch. The counter is the only thing in the list that says "there
// is something here you have not seen", so leaving it to a sync that may not
// run for hours makes the list quietly wrong.
//
// A missing chat row is not an error. The message that triggered this is
// stored through the same code path that creates the row, so a miss means the
// two raced; the next sync carries the server's own count anyway.
func (r *Repo) IncrementUnread(ctx context.Context, chatID int64) error {
	if r.readOnly.Load() {
		return ErrReadOnly
	}
	if _, err := r.db.ExecContext(ctx,
		`UPDATE chats SET unread_count = unread_count + 1 WHERE id = ?`, chatID); err != nil {
		return fmt.Errorf("increment unread for chat %d: %w", chatID, err)
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

// EnsureChat creates a bare chats row for a peer the mirror has not seen
// before, and does nothing when the row already exists — in particular it
// never overwrites a title, so a chat discovered live keeps whatever dialog
// sync later fills in.
//
// It exists because messages.chat_id references chats(id): a message from a
// chat outside the synced dialog window had no parent row, the insert failed
// with FOREIGN KEY constraint failed (787), and the message was dropped on
// the floor. Live-observed, 19.08.2026: a first message from a brand-new
// contact was lost, and the chat itself stayed invisible until a restart ran
// dialog sync.
//
// The title is left NULL deliberately. An update carries the peer kind but
// not always a name, and inventing one here would win the ON CONFLICT race
// against the real title. Consumers render a placeholder until sync fills it.
//
// lastMessageDate is not optional in practice: GetChats orders by
// COALESCE(last_message_date, 0) DESC, so a row created without one sorts
// below every chat that has ever received a message — the chat that just
// pinged you would appear at the very bottom of the list. The caller passes
// the date of the message that prompted the row.
// The bool reports whether this call created the row. The caller uses it to
// ask for a dialog re-sync: a chat born on the live path has no title and no
// unread count, and only the server can supply them.
func (r *Repo) EnsureChat(ctx context.Context, id int64, t domain.ChatType, lastMessageDate time.Time) (bool, error) {
	if r.readOnly.Load() {
		return false, ErrReadOnly
	}
	if id == 0 || t == "" {
		return false, fmt.Errorf("ensure chat: id and type are required (id=%d type=%q)", id, t)
	}
	// Read before write. This runs on the live path, once per incoming
	// message, and almost every one of them belongs to a chat that already
	// exists — but an INSERT takes the write lock whether or not it ends up
	// inserting anything. lazytg already writes from four places at once
	// (live drain, backfill, dialog sync, FTS reindex) and pays for that in
	// contention; doubling the write-lock acquisitions on the busiest path to
	// discover "nothing to do" is the wrong trade. The SELECT is a WAL read
	// and blocks nobody.
	//
	// The check is not a lock: two callers can both find the row missing and
	// both insert. ON CONFLICT DO NOTHING makes the loser a no-op, which is
	// the same outcome as before.
	var exists int
	switch err := r.db.QueryRowContext(ctx, `SELECT 1 FROM chats WHERE id = ?`, id).Scan(&exists); {
	case err == nil:
		return false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return false, fmt.Errorf("ensure chat %d: lookup: %w", id, err)
	}
	res, err := r.db.ExecContext(ctx, `
        INSERT INTO chats (id, type, last_message_date, unread_count, pinned)
        VALUES (?, ?, ?, 0, 0)
        ON CONFLICT(id) DO NOTHING
    `, id, string(t), nullableUnix(lastMessageDate))
	if err != nil {
		return false, fmt.Errorf("ensure chat %d: %w", id, err)
	}
	created, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("ensure chat %d: rows affected: %w", id, err)
	}
	return created > 0, nil
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
	// The preview subquery rides idx_messages_chat_date (chat_id, date DESC),
	// so it is an index seek per row rather than a scan. It is part of the same
	// statement on purpose: a second round-trip per chat would make the list
	// load O(chats) queries, and the pane renders an empty description line
	// without it — which is what the first live session looked like.
	//
	// substr caps what crosses the driver boundary: the list shows one clipped
	// line, so pulling a 4 KB message body for each of 500 chats is memory spent
	// on characters nothing will ever render. SQLite's substr counts characters,
	// not bytes, so this cannot split a rune.
	rows, err := r.db.QueryContext(ctx, `
        SELECT c.id, c.type, c.title, c.username, c.last_message_date, c.unread_count, c.pinned,
               (SELECT substr(m.text, 1, 200) FROM messages m
                 WHERE m.chat_id = c.id
                 ORDER BY m.date DESC, m.id DESC
                 LIMIT 1)
        FROM chats c
        ORDER BY c.pinned DESC, COALESCE(c.last_message_date, 0) DESC, c.id ASC
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
			preview  sql.NullString
		)
		if err := rows.Scan(&c.ID, &typ, &title, &username, &lastDate, &c.UnreadCount, &pinned, &preview); err != nil {
			return nil, fmt.Errorf("scan chat: %w", err)
		}
		c.LastMessagePreview = preview.String
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

// ChatHistoryFreshness reports two timestamps for one chat: what the dialog
// list says the last message is (chats.last_message_date) and what the local
// mirror actually holds (max(messages.date)). Either may be zero — an unknown
// chat, a chat the dialog sync has not dated, or one with no messages cached.
//
// It exists so a caller can tell "the mirror is current" from "the mirror is
// behind" without a network round-trip. Both values come from one statement so
// the answer cannot straddle a concurrent write, and max(date) rides the
// existing idx_messages_chat_date index rather than scanning the chat.
func (r *Repo) ChatHistoryFreshness(ctx context.Context, chatID int64) (dialogNewest, localNewest time.Time, err error) {
	var dialogDate, localDate sql.NullInt64
	err = r.db.QueryRowContext(ctx, `
        SELECT c.last_message_date,
               (SELECT MAX(m.date) FROM messages m WHERE m.chat_id = c.id)
        FROM chats c
        WHERE c.id = ?
    `, chatID).Scan(&dialogDate, &localDate)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// No chat row yet: the caller knows nothing, which is not an error —
		// it is the "fetch it" answer.
		return time.Time{}, time.Time{}, nil
	case err != nil:
		return time.Time{}, time.Time{}, fmt.Errorf("query chat freshness %d: %w", chatID, err)
	}
	if dialogDate.Valid {
		dialogNewest = time.Unix(dialogDate.Int64, 0).UTC()
	}
	if localDate.Valid {
		localNewest = time.Unix(localDate.Int64, 0).UTC()
	}
	return dialogNewest, localNewest, nil
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
            media_dc, media_filename, media_size, media_mime_type, media_thumb_size,
            media_duration, outgoing, reactions
        )
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
            media_thumb_size     = excluded.media_thumb_size,
            media_duration       = excluded.media_duration,
            outgoing             = excluded.outgoing,
            reactions            = excluded.reactions
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
		mediaDur  int
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
		mediaDur = m.Media.Duration
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
		mediaDur,
		m.Outgoing,
		encodeReactions(m.Reactions),
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
               media_dc, media_filename, media_size, media_mime_type, media_thumb_size,
               media_duration, outgoing, reactions
    `

// ErrMessageNotFound is returned by Message when the mirror holds no such
// row. It is a distinct error rather than a zero value because the callers
// that ask for one message by id — editing it, quoting it — must not act on
// an empty message as though it were real.
var ErrMessageNotFound = errors.New("message not found")

// Message returns a single message by chat and id.
//
// The chat is part of the key, not a filter for tidiness: a channel numbers
// its own messages from one, so id 42 exists in several chats at once and
// means something different in each. Every other read in this file is scoped
// the same way, for the same reason.
func (r *Repo) Message(ctx context.Context, chatID, messageID int64) (domain.Message, error) {
	rows, err := r.db.QueryContext(ctx,
		messageSelectColumns+` FROM messages WHERE chat_id = ? AND id = ? LIMIT 1`,
		chatID, messageID)
	if err != nil {
		return domain.Message{}, fmt.Errorf("query message %d/%d: %w", chatID, messageID, err)
	}
	defer func() { _ = rows.Close() }()

	msgs, err := scanMessages(rows)
	if err != nil {
		return domain.Message{}, err
	}
	if len(msgs) == 0 {
		return domain.Message{}, fmt.Errorf("%w: chat=%d id=%d", ErrMessageNotFound, chatID, messageID)
	}
	return msgs[0], nil
}

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
			mediaDur  sql.NullInt64
			reactions sql.NullString
		)
		if err := rows.Scan(
			&m.ID, &m.ChatID, &fromID, &date, &text, &replyTo, &raw,
			&mediaKind, &mediaID, &mediaAH, &mediaRef,
			&mediaDC, &mediaName, &mediaSize, &mediaMime, &mediaThSz,
			&mediaDur, &m.Outgoing, &reactions,
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
				Duration:      int(mediaDur.Int64),
			}
		}
		m.Reactions = decodeReactions(reactions.String)
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

// SetReactions replaces the reactions stored against one message.
//
// Deliberately not part of the message upsert. A reaction update carries no
// message body, so writing it through SaveMessage would blank the text of
// every message somebody reacted to. It also does not create a row: a
// reaction on a message outside the fetched history is the ordinary case, and
// inventing an empty message to hang it on would put a blank line in the
// thread.
func (r *Repo) SetReactions(ctx context.Context, chatID, messageID int64, rs []domain.Reaction) error {
	if r.readOnly.Load() {
		return ErrReadOnly
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE messages SET reactions = ? WHERE chat_id = ? AND id = ?`,
		encodeReactions(rs), chatID, messageID)
	if err != nil {
		return fmt.Errorf("set reactions %d/%d: %w", chatID, messageID, err)
	}
	return nil
}
