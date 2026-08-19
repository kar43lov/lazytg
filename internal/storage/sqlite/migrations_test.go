package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/gotd/td/telegram/updates"

	"github.com/kar43lov/lazytg/internal/storage/sqlite"
)

// freshDB opens a brand-new SQLite file but does not run migrations. Used by
// migration-flow tests that need to assert the pre/post-migration state.
func freshDB(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)

	path := filepath.Join(t.TempDir(), "fresh.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, ctx
}

func TestRunMigrations_FreshInstall(t *testing.T) {
	db, ctx := freshDB(t)

	// schema_migrations should not exist yet.
	if exists, err := tableExists(ctx, db, "schema_migrations"); err != nil {
		t.Fatalf("check before: %v", err)
	} else if exists {
		t.Fatal("schema_migrations exists on a fresh database")
	}

	if err := sqlite.RunMigrations(ctx, db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	for _, table := range []string{"accounts", "chats", "messages", "peers", "state", "channel_state", "schema_migrations"} {
		exists, err := tableExists(ctx, db, table)
		if err != nil {
			t.Fatalf("check %s: %v", table, err)
		}
		if !exists {
			t.Errorf("table %q missing after migrations", table)
		}
	}

	var version int
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("read max version: %v", err)
	}
	if version < 1 {
		t.Errorf("expected version >= 1, got %d", version)
	}
}

func TestRunMigrations_Idempotent(t *testing.T) {
	db, ctx := freshDB(t)

	if err := sqlite.RunMigrations(ctx, db); err != nil {
		t.Fatalf("first run: %v", err)
	}
	var firstCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&firstCount); err != nil {
		t.Fatalf("count after first: %v", err)
	}

	if err := sqlite.RunMigrations(ctx, db); err != nil {
		t.Fatalf("second run: %v", err)
	}
	var secondCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&secondCount); err != nil {
		t.Fatalf("count after second: %v", err)
	}

	if firstCount != secondCount {
		t.Errorf("idempotency violated: %d -> %d migration rows", firstCount, secondCount)
	}
}

// tableExists returns true if a SQLite table with the given name is present.
func tableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var found string
	err := db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`,
		name,
	).Scan(&found)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return found == name, nil
}

// TestMigrations0009And0010_UpgradeAnExistingDatabase covers the path the unit
// suite never exercises: an installed database that already carries rows,
// rather than a fresh file built by running every migration at once. 0009
// rebuilds `state` and `channel_state` to drop a foreign key that made them
// unwritable — a rebuild is exactly the kind of migration that loses data when
// it is wrong — and 0010 adds a column to a `messages` table that already has
// rows in it.
//
// The fixture is the pre-0009 schema, seeded and stamped as applied through
// version 8, so Open() has to perform the upgrade rather than create the
// tables from scratch. It carries a messages table for the same reason: a
// migration that only ever runs against the tables a test happens to create
// is not tested against anything a user has.
func TestMigrations0009And0010_UpgradeAnExistingDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	path := filepath.Join(t.TempDir(), "v8.db")
	seed, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	if _, err := seed.ExecContext(ctx, `
        CREATE TABLE accounts (id INTEGER PRIMARY KEY, phone TEXT UNIQUE NOT NULL, alias TEXT, created_at INTEGER NOT NULL);
        CREATE TABLE state (
            account_id INTEGER PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
            pts INTEGER, qts INTEGER, date INTEGER, seq INTEGER
        );
        CREATE TABLE channel_state (
            account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
            channel_id INTEGER NOT NULL,
            pts INTEGER NOT NULL,
            PRIMARY KEY (account_id, channel_id)
        );
        CREATE TABLE chats (
            id INTEGER PRIMARY KEY, type TEXT NOT NULL, title TEXT, username TEXT,
            last_message_date INTEGER, unread_count INTEGER NOT NULL DEFAULT 0,
            pinned INTEGER NOT NULL DEFAULT 0
        );
        CREATE TABLE messages (
            id INTEGER NOT NULL,
            chat_id INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
            from_id INTEGER, date INTEGER NOT NULL, text TEXT, reply_to INTEGER, raw_blob BLOB,
            media_kind TEXT, media_id INTEGER, media_access_hash INTEGER,
            media_file_reference BLOB, media_dc INTEGER, media_filename TEXT,
            media_size INTEGER, media_mime_type TEXT, media_thumb_size TEXT,
            PRIMARY KEY (chat_id, id)
        );
        CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);
        INSERT INTO accounts (id, phone, created_at) VALUES (1, '+10000000001', 0);
        INSERT INTO state (account_id, pts, qts, date, seq) VALUES (1, 111, 22, 333, 4);
        INSERT INTO channel_state (account_id, channel_id, pts) VALUES (1, 555, 66);
        INSERT INTO chats (id, type, title, last_message_date) VALUES (275641346, 'private', 'Павел Карлов', 1787141544);
        INSERT INTO messages (id, chat_id, from_id, date, text) VALUES (28, 275641346, NULL, 1787141544, 'йцу');
        INSERT INTO schema_migrations (version, applied_at)
            VALUES (1,0),(2,0),(3,0),(4,0),(5,0),(6,0),(7,0),(8,0);
    `); err != nil {
		t.Fatalf("seed pre-0009 schema: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}

	repo, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("open an existing database: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	var pts, qts, date, seq int
	if err := repo.DB().QueryRowContext(ctx,
		`SELECT pts, qts, date, seq FROM state WHERE user_id = 1`).Scan(&pts, &qts, &date, &seq); err != nil {
		t.Fatalf("read migrated state row: %v", err)
	}
	if pts != 111 || qts != 22 || date != 333 || seq != 4 {
		t.Fatalf("migrated state = (%d,%d,%d,%d), want (111,22,333,4)", pts, qts, date, seq)
	}

	var channelPts int
	if err := repo.DB().QueryRowContext(ctx,
		`SELECT pts FROM channel_state WHERE user_id = 1 AND channel_id = 555`).Scan(&channelPts); err != nil {
		t.Fatalf("read migrated channel_state row: %v", err)
	}
	if channelPts != 66 {
		t.Fatalf("migrated channel pts = %d, want 66", channelPts)
	}

	// The point of the migration: a Telegram user id no accounts row matches.
	state := sqlite.NewStateRepo(repo.DB())
	if err := state.SetState(ctx, 8385473863, updates.State{Pts: 1, Qts: 2, Date: 3, Seq: 4}); err != nil {
		t.Fatalf("SetState after upgrade: %v", err)
	}

	// 0010: the stored message survives the column addition and defaults to
	// incoming. Defaulting the other way would announce every archived
	// message as the reader's own.
	var text string
	var outgoing bool
	if err := repo.DB().QueryRowContext(ctx,
		`SELECT text, outgoing FROM messages WHERE chat_id = 275641346 AND id = 28`).Scan(&text, &outgoing); err != nil {
		t.Fatalf("read migrated message row: %v", err)
	}
	if text != "йцу" {
		t.Fatalf("migrated message text = %q, want %q", text, "йцу")
	}
	if outgoing {
		t.Fatalf("pre-0010 message came back marked outgoing")
	}

	var violations int
	if err := repo.DB().QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	if violations != 0 {
		t.Fatalf("foreign_key_check reported %d violations after the upgrade", violations)
	}
}
