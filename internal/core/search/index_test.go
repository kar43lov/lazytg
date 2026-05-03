package search_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/search"
	"github.com/kar43lov/lazytg/internal/storage/sqlite"
)

// openTestRepo opens a fresh SQLite repo with all migrations applied (incl.
// 0005_fts) and returns it together with a context bounded to the test.
func openTestRepo(t *testing.T) (*sqlite.Repo, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)

	path := filepath.Join(t.TempDir(), "search.db")
	repo, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo, ctx
}

// seedChat ensures the chat row exists so SaveMessage's FK constraint passes.
func seedChat(t *testing.T, repo *sqlite.Repo, ctx context.Context, chatID int64, title string) {
	t.Helper()
	if err := repo.SaveChat(ctx, domain.Chat{
		ID:    chatID,
		Type:  domain.ChatTypePrivate,
		Title: title,
	}); err != nil {
		t.Fatalf("save chat: %v", err)
	}
}

// matchCount runs `messages_fts MATCH ?` and returns the row count, scoped to
// chatID via a JOIN through messages so cross-chat noise from other tests
// does not bleed into a single assertion.
func matchCount(t *testing.T, db *sql.DB, ctx context.Context, chatID int64, query string) int {
	t.Helper()
	var n int
	err := db.QueryRowContext(ctx, `
        SELECT COUNT(*)
        FROM messages_fts f
        JOIN messages m ON m.rowid = f.rowid
        WHERE messages_fts MATCH ? AND m.chat_id = ?
    `, query, chatID).Scan(&n)
	if err != nil {
		t.Fatalf("match query %q: %v", query, err)
	}
	return n
}

// TestTriggers_Insert verifies the AFTER INSERT trigger folds new rows into
// the FTS index automatically — no Backfill required for the steady state.
func TestTriggers_Insert(t *testing.T) {
	repo, ctx := openTestRepo(t)
	const chatID int64 = 1001
	seedChat(t, repo, ctx, chatID, "Alice")

	if err := repo.SaveMessage(ctx, domain.Message{
		ID:     1,
		ChatID: chatID,
		Date:   time.Now().UTC(),
		Text:   "Привет мир",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	if got := matchCount(t, repo.DB(), ctx, chatID, "при"); got != 1 {
		t.Errorf("MATCH 'при': got %d hits, want 1", got)
	}
	if got := matchCount(t, repo.DB(), ctx, chatID, "мир"); got != 1 {
		t.Errorf("MATCH 'мир': got %d hits, want 1", got)
	}
}

// TestTriggers_Update verifies the AFTER UPDATE trigger evicts the old text
// from the index and folds in the new one.
func TestTriggers_Update(t *testing.T) {
	repo, ctx := openTestRepo(t)
	const chatID int64 = 2002
	seedChat(t, repo, ctx, chatID, "Bob")

	if err := repo.SaveMessage(ctx, domain.Message{
		ID:     1,
		ChatID: chatID,
		Date:   time.Now().UTC(),
		Text:   "первый текст",
	}); err != nil {
		t.Fatalf("save initial: %v", err)
	}

	// Direct UPDATE so the AFTER UPDATE trigger fires instead of the
	// upsert's AFTER INSERT path (SaveMessage uses ON CONFLICT DO UPDATE
	// which engages the UPDATE trigger anyway, but a raw UPDATE keeps the
	// test intent unambiguous).
	if _, err := repo.DB().ExecContext(ctx,
		`UPDATE messages SET text = ? WHERE chat_id = ? AND id = ?`,
		"новое содержание", chatID, 1,
	); err != nil {
		t.Fatalf("update: %v", err)
	}

	if got := matchCount(t, repo.DB(), ctx, chatID, "пер"); got != 0 {
		t.Errorf("MATCH 'пер' after update: got %d hits, want 0 (old text should be evicted)", got)
	}
	if got := matchCount(t, repo.DB(), ctx, chatID, "нов"); got != 1 {
		t.Errorf("MATCH 'нов' after update: got %d hits, want 1", got)
	}
}

// TestTriggers_Delete verifies the AFTER DELETE trigger removes index entries
// for the deleted row.
func TestTriggers_Delete(t *testing.T) {
	repo, ctx := openTestRepo(t)
	const chatID int64 = 3003
	seedChat(t, repo, ctx, chatID, "Carol")

	if err := repo.SaveMessage(ctx, domain.Message{
		ID:     1,
		ChatID: chatID,
		Date:   time.Now().UTC(),
		Text:   "удаляемое сообщение",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	if got := matchCount(t, repo.DB(), ctx, chatID, "уда"); got != 1 {
		t.Fatalf("MATCH 'уда' before delete: got %d, want 1", got)
	}

	if _, err := repo.DB().ExecContext(ctx,
		`DELETE FROM messages WHERE chat_id = ? AND id = ?`, chatID, 1,
	); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if got := matchCount(t, repo.DB(), ctx, chatID, "уда"); got != 0 {
		t.Errorf("MATCH 'уда' after delete: got %d, want 0", got)
	}
}

// TestTrigramCornerCases documents how the trigram tokenizer deals with
// short inputs, mixed scripts and emoji. The trigram tokenizer indexes
// every 3-character sliding window (Unicode code-points), so 1- and
// 2-character inputs are unindexable and return zero hits even on exact
// match. Tests serve as living documentation for callers.
func TestTrigramCornerCases(t *testing.T) {
	repo, ctx := openTestRepo(t)
	const chatID int64 = 4004
	seedChat(t, repo, ctx, chatID, "Trigram")

	rows := []struct {
		id   int64
		text string
	}{
		{1, "ok"},          // 2 chars — no trigrams, skipped silently
		{2, "Привет мир"},  // mixed case, expects case-insensitive match
		{3, "🚀🎉"},          // 2 emoji code points — no trigrams either
		{4, "abc"},         // exactly one trigram "abc"
		{5, "ab"},          // 2 chars — like row 1, no trigrams
		{6, "🚀🎉🎊"},         // 3 emoji code points — one trigram
		{7, "hello world"}, // baseline ASCII
	}
	for _, r := range rows {
		if err := repo.SaveMessage(ctx, domain.Message{
			ID:     r.id,
			ChatID: chatID,
			Date:   time.Now().UTC(),
			Text:   r.text,
		}); err != nil {
			t.Fatalf("save row %d: %v", r.id, err)
		}
	}

	cases := []struct {
		name  string
		query string
		want  int
	}{
		// "ok" is two chars: trigram tokenizer cannot index it, MATCH
		// returns zero hits. This is expected and documented.
		{"two-char input not indexed", "ok", 0},
		// Russian fragment, case-insensitive thanks to case_sensitive=0.
		{"russian fragment lowercased query", "при", 1},
		// Same fragment, mixed case query.
		{"russian fragment capitalised query", "При", 1},
		// Three-char ASCII trigram.
		{"abc trigram", "abc", 1},
		// Hello in baseline.
		{"hello", "hello", 1},
		// Three-emoji trigram is indexable.
		{"three-emoji trigram", "🚀🎉🎊", 1},
		// Two-emoji query is not.
		{"two-emoji query unindexable", "🚀🎉", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := matchCount(t, repo.DB(), ctx, chatID, c.query); got != c.want {
				t.Errorf("MATCH %q: got %d hits, want %d", c.query, got, c.want)
			}
		})
	}
}

// TestBackfill_EmptyChat checks that running Backfill against a chat with no
// messages reports zero rows indexed without producing a SQL error.
func TestBackfill_EmptyChat(t *testing.T) {
	repo, ctx := openTestRepo(t)
	const chatID int64 = 5005
	seedChat(t, repo, ctx, chatID, "Empty")

	indexer := search.NewIndexer(repo, nil)
	n, err := indexer.Backfill(ctx, chatID, 0)
	if err != nil {
		t.Fatalf("backfill empty: %v", err)
	}
	if n != 0 {
		t.Errorf("backfill empty: got %d, want 0", n)
	}
}

// TestBackfill_HistoricRows simulates an upgrade where messages were already
// in the database before the FTS triggers existed: the AFTER INSERT trigger
// is dropped, 100 rows are written, then re-created. Backfill must catch
// those orphans up; a second call must be a no-op.
func TestBackfill_HistoricRows(t *testing.T) {
	repo, ctx := openTestRepo(t)
	const chatID int64 = 6006
	seedChat(t, repo, ctx, chatID, "Historic")

	db := repo.DB()
	if _, err := db.ExecContext(ctx, `DROP TRIGGER messages_ai`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}

	now := time.Now().UTC()
	msgs := make([]domain.Message, 100)
	for i := 0; i < 100; i++ {
		msgs[i] = domain.Message{
			ID:     int64(i + 1),
			ChatID: chatID,
			Date:   now.Add(time.Duration(i) * time.Second),
			Text:   "историческое сообщение номер " + suffix(i),
		}
	}
	if err := repo.SaveMessages(ctx, msgs); err != nil {
		t.Fatalf("save batch: %v", err)
	}

	// Restore the trigger so future writes stay in sync, exactly like the
	// migration would after an upgrade.
	if _, err := db.ExecContext(ctx, `
        CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN
            INSERT INTO messages_fts(rowid, text) VALUES (new.rowid, new.text);
        END
    `); err != nil {
		t.Fatalf("recreate trigger: %v", err)
	}

	// Sanity check: index is empty for this chat before Backfill.
	if got := matchCount(t, db, ctx, chatID, "истор"); got != 0 {
		t.Fatalf("pre-backfill: got %d hits, want 0 (rows were inserted with trigger off)", got)
	}

	indexer := search.NewIndexer(repo, nil)
	n, err := indexer.Backfill(ctx, chatID, 0)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if n != 100 {
		t.Errorf("first backfill: got %d, want 100", n)
	}
	if got := matchCount(t, db, ctx, chatID, "истор"); got != 100 {
		t.Errorf("post-backfill MATCH: got %d, want 100", got)
	}

	n2, err := indexer.Backfill(ctx, chatID, 0)
	if err != nil {
		t.Fatalf("backfill repeat: %v", err)
	}
	if n2 != 0 {
		t.Errorf("repeat backfill: got %d, want 0 (idempotent)", n2)
	}
}

// TestBackfill_SkipsEmptyText verifies that NULL/empty bodies (Telegram
// service messages such as joins/leaves) are excluded — indexing them would
// only burn rowid space without ever matching a query.
func TestBackfill_SkipsEmptyText(t *testing.T) {
	repo, ctx := openTestRepo(t)
	const chatID int64 = 7007
	seedChat(t, repo, ctx, chatID, "Empties")

	db := repo.DB()
	if _, err := db.ExecContext(ctx, `DROP TRIGGER messages_ai`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}

	now := time.Now().UTC().Unix()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO messages(id, chat_id, date, text) VALUES (?, ?, ?, ?), (?, ?, ?, NULL), (?, ?, ?, ?)`,
		1, chatID, now, "первое сообщение",
		2, chatID, now+1,
		3, chatID, now+2, "",
	); err != nil {
		t.Fatalf("seed messages: %v", err)
	}

	indexer := search.NewIndexer(repo, nil)
	n, err := indexer.Backfill(ctx, chatID, 0)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if n != 1 {
		t.Errorf("backfill skipping empty: got %d, want 1", n)
	}
}

func suffix(i int) string {
	const alphabet = "abcdefghij"
	return string(alphabet[i%len(alphabet)])
}
