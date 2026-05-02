package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/pgmac/lazytg/internal/storage/sqlite"
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
