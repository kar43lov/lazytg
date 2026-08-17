package sqlite_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	// Drag in the storage package so that the SQLite driver registration
	// runs (driver_modernc.go / driver_sqlcipher.go).
	_ "github.com/kar43lov/lazytg/internal/storage/sqlite"
)

// TestFTS5TrigramSpike is a RISK-BLOCKER: it verifies that the modernc.org/sqlite
// build available on this platform actually supports the built-in `trigram`
// tokenizer that lazytg's local search relies on. If this test fails the
// project must switch to the `porter` tokenizer (English-only) before stage 3
// or wire ICU through CGo, which contradicts the pure-Go default.
func TestFTS5TrigramSpike(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.ExecContext(ctx,
		`CREATE VIRTUAL TABLE messages_fts USING fts5(text, tokenize='trigram')`); err != nil {
		t.Fatalf(
			"modernc/sqlite does not support FTS5 trigram tokenizer (err: %v).\n"+
				"Switch search/index plan to the 'porter' tokenizer before stage 3, "+
				"or accept ICU/CGo dependency.",
			err)
	}

	// 100 rows: 50 mostly Russian, 50 mostly English. Plain-text bodies so we
	// can assert tokenization by trigram across both alphabets.
	insertStmt, err := db.PrepareContext(ctx, `INSERT INTO messages_fts(text) VALUES (?)`)
	if err != nil {
		t.Fatalf("prepare insert: %v", err)
	}
	defer func() { _ = insertStmt.Close() }()

	russianBase := []string{
		"привет, как дела сегодня",
		"тестовое сообщение про погоду",
		"локальный поиск работает",
		"клиент Telegram на Go",
		"шифрование секретов через keyring",
	}
	englishBase := []string{
		"hello world from go",
		"local first search engine",
		"telegram tui client mvp",
		"trigram tokenizer test",
		"sqlite fts5 spike check",
	}
	for i := 0; i < 50; i++ {
		text := russianBase[i%len(russianBase)] + " " + suffix(i)
		if _, err := insertStmt.ExecContext(ctx, text); err != nil {
			t.Fatalf("insert ru row %d: %v", i, err)
		}
	}
	for i := 0; i < 50; i++ {
		text := englishBase[i%len(englishBase)] + " " + suffix(i)
		if _, err := insertStmt.ExecContext(ctx, text); err != nil {
			t.Fatalf("insert en row %d: %v", i, err)
		}
	}

	cases := []struct {
		name   string
		query  string
		minHit int
	}{
		{"russian word", "сообщение", 1},
		{"russian fragment", "поиск", 1},
		{"english word", "hello", 1},
		{"english fragment", "trigram", 1},
		{"unicode fragment 'клиент'", "клиент", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var n int
			if err := db.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM messages_fts WHERE text MATCH ?`, c.query,
			).Scan(&n); err != nil {
				t.Fatalf("match query %q: %v", c.query, err)
			}
			if n < c.minHit {
				t.Errorf("query %q: got %d hits, want >= %d", c.query, n, c.minHit)
			}
		})
	}
}

// suffix returns a deterministic per-row token so that the FTS index sees a
// little uniqueness in each row instead of 50 identical bodies.
func suffix(i int) string {
	parts := []string{"row", "id", strings.Repeat("x", 1+i%3)}
	return fmt.Sprintf("%s-%d", strings.Join(parts, "_"), i)
}
