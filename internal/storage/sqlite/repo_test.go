package sqlite_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/storage/sqlite"
)

func openTestRepo(t *testing.T) (*sqlite.Repo, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)

	path := filepath.Join(t.TempDir(), "test.db")
	repo, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if err := repo.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return repo, ctx
}

func TestRepo_RoundTrip_ChatsAndMessages(t *testing.T) {
	repo, ctx := openTestRepo(t)

	now := time.Now().UTC().Truncate(time.Second)

	chats := []domain.Chat{
		{
			ID:              1001,
			Type:            domain.ChatTypePrivate,
			Title:           "Alice",
			Username:        "alice",
			LastMessageDate: now.Add(-1 * time.Minute),
			UnreadCount:     2,
			Pinned:          true,
		},
		{
			ID:              2002,
			Type:            domain.ChatTypeChannel,
			Title:           "lazytg-news",
			Username:        "lazytg_news",
			LastMessageDate: now.Add(-2 * time.Hour),
			UnreadCount:     0,
			Pinned:          false,
		},
	}
	for _, c := range chats {
		if err := repo.SaveChat(ctx, c); err != nil {
			t.Fatalf("save chat %d: %v", c.ID, err)
		}
	}

	got, err := repo.GetChats(ctx)
	if err != nil {
		t.Fatalf("get chats: %v", err)
	}
	if len(got) != len(chats) {
		t.Fatalf("got %d chats, want %d", len(got), len(chats))
	}

	// Pinned chat must be first regardless of last_message_date.
	if got[0].ID != 1001 {
		t.Errorf("pinned chat must be first, got id=%d", got[0].ID)
	}
	for _, c := range got {
		want := findChat(chats, c.ID)
		if want.Title != c.Title || want.Username != c.Username || want.Type != c.Type {
			t.Errorf("chat %d: title/username/type mismatch: got %+v want %+v", c.ID, c, want)
		}
		if want.Pinned != c.Pinned || want.UnreadCount != c.UnreadCount {
			t.Errorf("chat %d: pinned/unread mismatch: got %+v want %+v", c.ID, c, want)
		}
		if !want.LastMessageDate.Equal(c.LastMessageDate) {
			t.Errorf("chat %d: last_message_date mismatch: got %v want %v",
				c.ID, c.LastMessageDate, want.LastMessageDate)
		}
	}

	// Insert 5 messages into chat 1001.
	msgs := []domain.Message{
		{ID: 1, ChatID: 1001, FromID: 7001, Date: now.Add(-5 * time.Minute), Text: "hello"},
		{ID: 2, ChatID: 1001, FromID: 7001, Date: now.Add(-4 * time.Minute), Text: "сообщение"},
		{ID: 3, ChatID: 1001, FromID: 7002, Date: now.Add(-3 * time.Minute), Text: "with reply", ReplyTo: 1},
		{ID: 4, ChatID: 1001, FromID: 7002, Date: now.Add(-2 * time.Minute), Text: "blob payload", RawBlob: []byte{0x01, 0x02, 0x03}},
		{ID: 5, ChatID: 1001, FromID: 7001, Date: now.Add(-1 * time.Minute), Text: "latest"},
	}
	for _, m := range msgs {
		if err := repo.SaveMessage(ctx, m); err != nil {
			t.Fatalf("save msg %d: %v", m.ID, err)
		}
	}

	gotMsgs, err := repo.GetMessages(ctx, 1001, 10, 0)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(gotMsgs) != len(msgs) {
		t.Fatalf("got %d messages, want %d", len(gotMsgs), len(msgs))
	}
	// Newest first.
	if gotMsgs[0].ID != 5 || gotMsgs[len(gotMsgs)-1].ID != 1 {
		t.Errorf("messages not ordered desc by date: ids=%v", msgIDs(gotMsgs))
	}

	for _, want := range msgs {
		got := findMsg(gotMsgs, want.ID)
		if got.ChatID != want.ChatID || got.Text != want.Text || got.FromID != want.FromID {
			t.Errorf("msg %d: mismatch: got %+v want %+v", want.ID, got, want)
		}
		if got.ReplyTo != want.ReplyTo {
			t.Errorf("msg %d: reply_to mismatch: got %d want %d", want.ID, got.ReplyTo, want.ReplyTo)
		}
		if string(got.RawBlob) != string(want.RawBlob) {
			t.Errorf("msg %d: raw_blob mismatch", want.ID)
		}
		if !got.Date.Equal(want.Date) {
			t.Errorf("msg %d: date mismatch: got %v want %v", want.ID, got.Date, want.Date)
		}
	}
}

func TestRepo_SaveChat_Upsert(t *testing.T) {
	repo, ctx := openTestRepo(t)

	c := domain.Chat{ID: 42, Type: domain.ChatTypeGroup, Title: "old", UnreadCount: 1}
	if err := repo.SaveChat(ctx, c); err != nil {
		t.Fatalf("first save: %v", err)
	}
	c.Title = "new"
	c.UnreadCount = 0
	if err := repo.SaveChat(ctx, c); err != nil {
		t.Fatalf("second save: %v", err)
	}
	got, err := repo.GetChats(ctx)
	if err != nil {
		t.Fatalf("get chats: %v", err)
	}
	if len(got) != 1 || got[0].Title != "new" || got[0].UnreadCount != 0 {
		t.Fatalf("upsert failed: got %+v", got)
	}
}

func TestRepo_SaveMessage_RequiresChatID(t *testing.T) {
	repo, ctx := openTestRepo(t)
	err := repo.SaveMessage(ctx, domain.Message{ID: 1, Date: time.Now()})
	if err == nil {
		t.Fatal("expected error for zero chat_id, got nil")
	}
}

func TestRepo_SaveMessage_RequiresID(t *testing.T) {
	repo, ctx := openTestRepo(t)
	err := repo.SaveMessage(ctx, domain.Message{ChatID: 1, Date: time.Now()})
	if err == nil {
		t.Fatal("expected error for zero id, got nil")
	}
}

// TestRepo_SaveMessage_RequiresDate guards against zero-Time messages landing
// in the DB as Unix epoch -62135596800 (year 1 BCE) and poisoning ordering /
// future FTS5 before:/after: filters.
func TestRepo_SaveMessage_RequiresDate(t *testing.T) {
	repo, ctx := openTestRepo(t)
	err := repo.SaveMessage(ctx, domain.Message{ID: 1, ChatID: 1})
	if err == nil {
		t.Fatal("expected error for zero date, got nil")
	}
}

// TestRepo_Open_DBFileMode0600 verifies that Open enforces the 0600 mode the
// SECURITY.md threat model promises, regardless of the process umask.
func TestRepo_Open_DBFileMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission model not applicable on Windows")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)

	path := filepath.Join(t.TempDir(), "perm.db")
	repo, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	mode := info.Mode().Perm()
	if mode != 0o600 {
		t.Errorf("db file mode: got %o, want 0600", mode)
	}
}

func TestRepo_AccountsCRUD(t *testing.T) {
	repo, ctx := openTestRepo(t)

	t1 := time.Now().UTC().Truncate(time.Second).Add(-2 * time.Hour)
	t2 := t1.Add(time.Hour)

	want := []domain.Account{
		{Phone: "+79990001111", Alias: "primary", CreatedAt: t1},
		{Phone: "+79990002222", Alias: "secondary", CreatedAt: t2},
	}
	for _, a := range want {
		if err := repo.SaveAccount(ctx, a); err != nil {
			t.Fatalf("save account %q: %v", a.Phone, err)
		}
	}

	got, err := repo.GetAccounts(ctx)
	if err != nil {
		t.Fatalf("get accounts: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d accounts, want 2", len(got))
	}
	if got[0].Phone != "+79990001111" || got[1].Phone != "+79990002222" {
		t.Fatalf("accounts not ordered by created_at asc: got %+v", got)
	}

	// Upsert: second SaveAccount with same phone updates the alias.
	updated := domain.Account{Phone: "+79990001111", Alias: "renamed", CreatedAt: t1}
	if err := repo.SaveAccount(ctx, updated); err != nil {
		t.Fatalf("update account: %v", err)
	}
	got, err = repo.GetAccounts(ctx)
	if err != nil {
		t.Fatalf("get accounts: %v", err)
	}
	if got[0].Alias != "renamed" {
		t.Errorf("alias not updated, got %q", got[0].Alias)
	}

	if err := repo.DeleteAccount(ctx, "+79990001111"); err != nil {
		t.Fatalf("delete account: %v", err)
	}
	if err := repo.DeleteAccount(ctx, "+79990001111"); err != nil {
		t.Fatalf("delete account (idempotent): %v", err)
	}
	got, err = repo.GetAccounts(ctx)
	if err != nil {
		t.Fatalf("get accounts: %v", err)
	}
	if len(got) != 1 || got[0].Phone != "+79990002222" {
		t.Fatalf("expected one remaining account, got %+v", got)
	}
}

func TestRepo_SaveAccount_RequiresPhone(t *testing.T) {
	repo, ctx := openTestRepo(t)
	if err := repo.SaveAccount(ctx, domain.Account{Alias: "x"}); err == nil {
		t.Fatal("expected error for missing phone, got nil")
	}
}

// TestRepo_SaveAccount_PreservesAliasOnReUpsert documents the contract that a
// SaveAccount call with an empty Alias must NOT overwrite an existing alias
// — that way `lazytg login` re-running on a known account never silently
// nukes whatever alias the user (or a future rename UI) previously set.
func TestRepo_SaveAccount_PreservesAliasOnReUpsert(t *testing.T) {
	repo, ctx := openTestRepo(t)
	created := time.Now().UTC().Truncate(time.Second)

	if err := repo.SaveAccount(ctx, domain.Account{
		Phone: "+79990001111", Alias: "work", CreatedAt: created,
	}); err != nil {
		t.Fatalf("initial save: %v", err)
	}

	if err := repo.SaveAccount(ctx, domain.Account{
		Phone: "+79990001111", CreatedAt: created,
	}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}

	got, err := repo.GetAccounts(ctx)
	if err != nil {
		t.Fatalf("get accounts: %v", err)
	}
	if len(got) != 1 || got[0].Alias != "work" {
		t.Fatalf("alias must survive re-upsert with empty Alias, got %+v", got)
	}
}

func TestRepo_GetMessages_LimitZeroReturnsNil(t *testing.T) {
	repo, ctx := openTestRepo(t)
	got, err := repo.GetMessages(ctx, 1, 0, 0)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil slice, got %v", got)
	}
}

// TestRepo_ForeignKeysEnforcedAcrossPool inserts a message that references a
// non-existent chat_id from many goroutines in parallel. Without the DSN-level
// _pragma=foreign_keys(1), only one connection in the pool would enforce the
// constraint and the rest would silently accept the orphan row — see the
// comment in repo.Open. Every concurrent attempt must fail.
func TestRepo_ForeignKeysEnforcedAcrossPool(t *testing.T) {
	repo, ctx := openTestRepo(t)
	const goroutines = 16
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			errs <- repo.SaveMessage(ctx, domain.Message{
				ID: int64(i + 1), ChatID: 99999, Date: time.Now().UTC(), Text: "orphan",
			})
		}(i)
	}
	for i := 0; i < goroutines; i++ {
		if err := <-errs; err == nil {
			t.Errorf("iteration %d: expected FK violation, got nil", i)
		}
	}
}

// TestRepo_SaveMessages_BatchAndUpsert exercises the batch insert path: a
// single transaction must be atomic (all-or-nothing) and idempotent
// (re-inserting the same (chat_id, id) updates the row instead of erroring).
func TestRepo_SaveMessages_BatchAndUpsert(t *testing.T) {
	repo, ctx := openTestRepo(t)
	now := time.Now().UTC().Truncate(time.Second)

	if err := repo.SaveChat(ctx, domain.Chat{ID: 9001, Type: domain.ChatTypePrivate, Title: "p"}); err != nil {
		t.Fatalf("save chat: %v", err)
	}

	msgs := []domain.Message{
		{ID: 1, ChatID: 9001, FromID: 1, Date: now.Add(-3 * time.Minute), Text: "a"},
		{ID: 2, ChatID: 9001, FromID: 1, Date: now.Add(-2 * time.Minute), Text: "b"},
		{ID: 3, ChatID: 9001, FromID: 1, Date: now.Add(-1 * time.Minute), Text: "c"},
	}
	if err := repo.SaveMessages(ctx, msgs); err != nil {
		t.Fatalf("first batch: %v", err)
	}

	// Update the middle message and add a new one in the next batch — UPSERT
	// must rewrite text on conflict and append the new row.
	msgs[1].Text = "b-updated"
	more := []domain.Message{
		msgs[1],
		{ID: 4, ChatID: 9001, FromID: 1, Date: now, Text: "d"},
	}
	if err := repo.SaveMessages(ctx, more); err != nil {
		t.Fatalf("second batch: %v", err)
	}

	got, err := repo.GetMessages(ctx, 9001, 100, 0)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d messages, want 4", len(got))
	}
	if findMsg(got, 2).Text != "b-updated" {
		t.Fatalf("upsert did not rewrite text: %+v", findMsg(got, 2))
	}
}

func TestRepo_SaveMessages_EmptyIsNoop(t *testing.T) {
	repo, ctx := openTestRepo(t)
	if err := repo.SaveMessages(ctx, nil); err != nil {
		t.Fatalf("empty SaveMessages should be a no-op, got %v", err)
	}
}

// TestRepo_SaveMessages_ValidatesAllBeforeWriting documents the atomic
// behaviour: if any item is invalid, none of the items are persisted.
func TestRepo_SaveMessages_ValidatesAllBeforeWriting(t *testing.T) {
	repo, ctx := openTestRepo(t)
	now := time.Now().UTC().Truncate(time.Second)
	if err := repo.SaveChat(ctx, domain.Chat{ID: 11, Type: domain.ChatTypeGroup, Title: "g"}); err != nil {
		t.Fatalf("save chat: %v", err)
	}
	mixed := []domain.Message{
		{ID: 1, ChatID: 11, Date: now, Text: "ok"},
		{ID: 2, ChatID: 0, Date: now, Text: "bad"}, // missing chat_id
	}
	if err := repo.SaveMessages(ctx, mixed); err == nil {
		t.Fatalf("expected validation error for missing chat_id")
	}
	got, err := repo.GetMessages(ctx, 11, 10, 0)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("partial write detected: %d rows persisted, want 0", len(got))
	}
}

func findChat(cs []domain.Chat, id int64) domain.Chat {
	for _, c := range cs {
		if c.ID == id {
			return c
		}
	}
	return domain.Chat{}
}

func findMsg(ms []domain.Message, id int64) domain.Message {
	for _, m := range ms {
		if m.ID == id {
			return m
		}
	}
	return domain.Message{}
}

func msgIDs(ms []domain.Message) []int64 {
	out := make([]int64, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

// TestEnsureChat_LetsAMessageFromAnUnknownChatLand covers the storage half of
// the 19.08.2026 data loss: messages.chat_id references chats(id), so a
// message from a peer dialog sync has not reached yet was rejected outright.
// The first half of this test is the bug reproduction — it asserts the raw
// SaveMessage still fails, so the day the schema changes, this test says so
// instead of quietly testing nothing.
func TestEnsureChat_LetsAMessageFromAnUnknownChatLand(t *testing.T) {
	repo, ctx := openTestRepo(t)

	msg := domain.Message{ID: 18, ChatID: 275641346, Date: time.Now().UTC(), Text: "2134"}
	if err := repo.SaveMessage(ctx, msg); err == nil {
		t.Fatalf("SaveMessage into an unknown chat succeeded — the foreign key this test is about is gone")
	}

	created, err := repo.EnsureChat(ctx, msg.ChatID, domain.ChatTypePrivate, msg.Date)
	if err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	if !created {
		t.Fatalf("EnsureChat reported no row created for a chat that did not exist")
	}
	if err := repo.SaveMessage(ctx, msg); err != nil {
		t.Fatalf("SaveMessage after EnsureChat: %v", err)
	}
}

// TestEnsureChat_DoesNotClobberAKnownChat pins the "does nothing" half. The
// title arrives from dialog sync and is the one thing the live path cannot
// supply, so an EnsureChat landing after sync must leave it alone.
func TestEnsureChat_DoesNotClobberAKnownChat(t *testing.T) {
	repo, ctx := openTestRepo(t)

	want := domain.Chat{ID: 275641346, Type: domain.ChatTypePrivate, Title: "Павел Карлов", UnreadCount: 3}
	if err := repo.SaveChat(ctx, want); err != nil {
		t.Fatalf("SaveChat: %v", err)
	}
	created, err := repo.EnsureChat(ctx, want.ID, domain.ChatTypeSupergroup, time.Now().UTC())
	if err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	if created {
		t.Fatalf("EnsureChat claimed to create a row for a chat that already existed")
	}

	chats, err := repo.GetChats(ctx)
	if err != nil {
		t.Fatalf("GetChats: %v", err)
	}
	if len(chats) != 1 {
		t.Fatalf("chats = %d, want 1", len(chats))
	}
	if chats[0].Title != want.Title || chats[0].Type != want.Type || chats[0].UnreadCount != want.UnreadCount {
		t.Fatalf("EnsureChat overwrote a known chat: %+v, want %+v", chats[0], want)
	}
}

// TestEnsureChat_RefusesAnEmptyKind guards the NOT NULL column from being
// filled with an empty string, which would render as an unidentifiable row.
func TestEnsureChat_RefusesAnEmptyKind(t *testing.T) {
	repo, ctx := openTestRepo(t)
	if _, err := repo.EnsureChat(ctx, 1, "", time.Now().UTC()); err == nil {
		t.Fatalf("EnsureChat with an empty kind succeeded")
	}
}

// TestEnsureChat_SortsANewChatToTheTop covers a defect the first version of
// EnsureChat introduced: it left last_message_date NULL, and GetChats orders
// by COALESCE(last_message_date, 0) DESC — so the chat that had just received
// a message appeared below every chat that ever had one, which on a real
// account means off the bottom of the pane.
func TestEnsureChat_SortsANewChatToTheTop(t *testing.T) {
	repo, ctx := openTestRepo(t)

	older := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if err := repo.SaveChat(ctx, domain.Chat{
		ID: 1, Type: domain.ChatTypePrivate, Title: "older", LastMessageDate: older,
	}); err != nil {
		t.Fatalf("SaveChat: %v", err)
	}

	fresh := time.Now().UTC().Truncate(time.Second)
	if _, err := repo.EnsureChat(ctx, 275641346, domain.ChatTypePrivate, fresh); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}

	chats, err := repo.GetChats(ctx)
	if err != nil {
		t.Fatalf("GetChats: %v", err)
	}
	if len(chats) != 2 {
		t.Fatalf("chats = %d, want 2", len(chats))
	}
	if chats[0].ID != 275641346 {
		t.Fatalf("chat list starts with %d, want the freshly discovered 275641346 — the new row sorted to the bottom", chats[0].ID)
	}
	if !chats[0].LastMessageDate.Equal(fresh) {
		t.Fatalf("LastMessageDate = %v, want %v", chats[0].LastMessageDate, fresh)
	}
}

// TestDeleteMessages_ScopedToOneChat covers the channel case, where the
// deletion update names its channel and only that chat may lose rows.
func TestDeleteMessages_ScopedToOneChat(t *testing.T) {
	repo, ctx := openTestRepo(t)

	seedChat(t, ctx, repo, 100, domain.ChatTypeSupergroup)
	seedChat(t, ctx, repo, 200, domain.ChatTypeSupergroup)
	seedMessage(t, ctx, repo, 100, 7)
	seedMessage(t, ctx, repo, 200, 7)

	removed, err := repo.DeleteMessages(ctx, 100, []int64{7})
	if err != nil {
		t.Fatalf("DeleteMessages: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if got := countMessages(t, ctx, repo, 100); got != 0 {
		t.Fatalf("chat 100 still holds %d messages", got)
	}
	if got := countMessages(t, ctx, repo, 200); got != 1 {
		t.Fatalf("chat 200 lost a message with the same id: %d left, want 1", got)
	}
}

// TestDeleteMessages_WithoutAChatSparesChannels is the case that makes the
// type filter load-bearing. Telegram reports deletions in private chats and
// basic groups with ids alone, and those ids are unique only across that
// space — a channel numbers its own messages from one, so the same id exists
// there and belongs to somebody else's message.
func TestDeleteMessages_WithoutAChatSparesChannels(t *testing.T) {
	repo, ctx := openTestRepo(t)

	seedChat(t, ctx, repo, 100, domain.ChatTypePrivate)
	seedChat(t, ctx, repo, 200, domain.ChatTypeGroup)
	seedChat(t, ctx, repo, 300, domain.ChatTypeChannel)
	seedChat(t, ctx, repo, 400, domain.ChatTypeSupergroup)
	for _, chat := range []int64{100, 200, 300, 400} {
		seedMessage(t, ctx, repo, chat, 42)
	}

	removed, err := repo.DeleteMessages(ctx, 0, []int64{42})
	if err != nil {
		t.Fatalf("DeleteMessages: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2 (the private chat and the basic group)", removed)
	}
	for _, chat := range []int64{100, 200} {
		if got := countMessages(t, ctx, repo, chat); got != 0 {
			t.Fatalf("chat %d kept %d messages, want 0", chat, got)
		}
	}
	for _, chat := range []int64{300, 400} {
		if got := countMessages(t, ctx, repo, chat); got != 1 {
			t.Fatalf("channel %d lost a message to a private-space deletion", chat)
		}
	}
}

// TestDeleteChatsExcept_RemovesChatsAndTheirMessages covers the chat deleted
// from another device: dialog sync only upserts, so this is the only path
// that can ever remove the row. Messages must follow it out through the
// cascade, or the mirror keeps orphaned history forever.
func TestDeleteChatsExcept_RemovesChatsAndTheirMessages(t *testing.T) {
	repo, ctx := openTestRepo(t)

	seedChat(t, ctx, repo, 100, domain.ChatTypePrivate)
	seedChat(t, ctx, repo, 200, domain.ChatTypePrivate)
	seedMessage(t, ctx, repo, 100, 1)
	seedMessage(t, ctx, repo, 200, 1)

	removed, err := repo.DeleteChatsExcept(ctx, []int64{100})
	if err != nil {
		t.Fatalf("DeleteChatsExcept: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}

	chats, err := repo.GetChats(ctx)
	if err != nil {
		t.Fatalf("GetChats: %v", err)
	}
	if len(chats) != 1 || chats[0].ID != 100 {
		t.Fatalf("chats after prune = %+v, want only 100", chats)
	}
	if got := countMessages(t, ctx, repo, 200); got != 0 {
		t.Fatalf("deleted chat left %d messages behind — the cascade did not fire", got)
	}
	if got := countMessages(t, ctx, repo, 100); got != 1 {
		t.Fatalf("surviving chat lost its messages: %d left", got)
	}
}

// TestDeleteChatsExcept_RefusesAnEmptyKeepSet guards the whole mirror against
// one bad answer from the server: an empty dialog page must never be read as
// "the user has no chats any more".
func TestDeleteChatsExcept_RefusesAnEmptyKeepSet(t *testing.T) {
	repo, ctx := openTestRepo(t)
	seedChat(t, ctx, repo, 100, domain.ChatTypePrivate)

	if _, err := repo.DeleteChatsExcept(ctx, nil); err == nil {
		t.Fatalf("pruning against an empty list succeeded")
	}
	chats, err := repo.GetChats(ctx)
	if err != nil {
		t.Fatalf("GetChats: %v", err)
	}
	if len(chats) != 1 {
		t.Fatalf("chats = %d, want the mirror untouched", len(chats))
	}
}

// TestClearUnread_ZeroesTheBadge covers the counter the read path resets.
func TestClearUnread_ZeroesTheBadge(t *testing.T) {
	repo, ctx := openTestRepo(t)
	if err := repo.SaveChat(ctx, domain.Chat{
		ID: 100, Type: domain.ChatTypePrivate, Title: "unread", UnreadCount: 7,
	}); err != nil {
		t.Fatalf("SaveChat: %v", err)
	}
	if err := repo.ClearUnread(ctx, 100); err != nil {
		t.Fatalf("ClearUnread: %v", err)
	}
	chats, err := repo.GetChats(ctx)
	if err != nil {
		t.Fatalf("GetChats: %v", err)
	}
	if chats[0].UnreadCount != 0 {
		t.Fatalf("unread = %d, want 0", chats[0].UnreadCount)
	}
}

func seedChat(t *testing.T, ctx context.Context, repo *sqlite.Repo, id int64, kind domain.ChatType) {
	t.Helper()
	if err := repo.SaveChat(ctx, domain.Chat{ID: id, Type: kind, Title: "seed"}); err != nil {
		t.Fatalf("seed chat %d: %v", id, err)
	}
}

func seedMessage(t *testing.T, ctx context.Context, repo *sqlite.Repo, chatID, id int64) {
	t.Helper()
	if err := repo.SaveMessage(ctx, domain.Message{
		ID: id, ChatID: chatID, Date: time.Now().UTC(), Text: "seed",
	}); err != nil {
		t.Fatalf("seed message %d/%d: %v", chatID, id, err)
	}
}

func countMessages(t *testing.T, ctx context.Context, repo *sqlite.Repo, chatID int64) int {
	t.Helper()
	var n int
	if err := repo.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM messages WHERE chat_id = ?`, chatID).Scan(&n); err != nil {
		t.Fatalf("count messages for %d: %v", chatID, err)
	}
	return n
}

// TestRepo_RoundTrip_MessageDirection covers migration 0010's column end to
// end. It is worth its own test rather than a field on an existing fixture:
// the direction is the only thing distinguishing the reader's own messages in
// a 1:1 chat, where Telegram sends no sender at all, so a value that failed to
// bind would relabel every private conversation as service messages — exactly
// the defect 0010 exists to fix, silently reintroduced one layer down.
func TestRepo_RoundTrip_MessageDirection(t *testing.T) {
	repo, ctx := openTestRepo(t)

	now := time.Now().UTC().Truncate(time.Second)
	if err := repo.SaveChat(ctx, domain.Chat{
		ID: 275641346, Type: domain.ChatTypePrivate, Title: "Павел Карлов", LastMessageDate: now,
	}); err != nil {
		t.Fatalf("save chat: %v", err)
	}
	if err := repo.SaveMessages(ctx, []domain.Message{
		{ID: 1, ChatID: 275641346, Date: now, Text: "theirs", Outgoing: false},
		{ID: 2, ChatID: 275641346, Date: now, Text: "mine", Outgoing: true},
	}); err != nil {
		t.Fatalf("save messages: %v", err)
	}

	got, err := repo.GetMessages(ctx, 275641346, 10, 0)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d messages, want 2", len(got))
	}
	byID := map[int64]domain.Message{}
	for _, m := range got {
		byID[m.ID] = m
	}
	if byID[1].Outgoing {
		t.Errorf("incoming message came back outgoing")
	}
	if !byID[2].Outgoing {
		t.Errorf("outgoing message came back incoming")
	}

	// The upsert path has its own column list; a message re-fetched from the
	// server must not lose the direction the live path recorded.
	if err := repo.SaveMessage(ctx, domain.Message{
		ID: 2, ChatID: 275641346, Date: now, Text: "mine, edited", Outgoing: true,
	}); err != nil {
		t.Fatalf("re-save message: %v", err)
	}
	got, err = repo.GetMessages(ctx, 275641346, 10, 0)
	if err != nil {
		t.Fatalf("get messages after upsert: %v", err)
	}
	for _, m := range got {
		if m.ID == 2 && !m.Outgoing {
			t.Errorf("upsert dropped the direction")
		}
	}
}

// The kind and the duration are what the badge reads, so a round trip
// that loses either turns "video note, 0:07" back into an opaque blob.
// Both column lists are exercised — insert and upsert — because they are
// written separately and a column added to one and forgotten in the
// other fails only on a re-fetch, which is the ordinary case: opening a
// chat pulls history over messages the live path already stored.
func TestRepo_RoundTrip_MediaKindAndDuration(t *testing.T) {
	repo, ctx := openTestRepo(t)

	now := time.Now().UTC().Truncate(time.Second)
	if err := repo.SaveChat(ctx, domain.Chat{
		ID: 42, Type: domain.ChatTypePrivate, Title: "Peer", LastMessageDate: now,
	}); err != nil {
		t.Fatalf("save chat: %v", err)
	}
	note := &domain.MediaInfo{
		Kind: domain.MediaKindVideoNote, FileID: 5123, AccessHash: 7,
		Filename: "video_note_5123.mp4", Size: 1258291, MimeType: "video/mp4", Duration: 7,
	}
	if err := repo.SaveMessages(ctx, []domain.Message{
		{ID: 1, ChatID: 42, Date: now, Media: note},
	}); err != nil {
		t.Fatalf("save message: %v", err)
	}

	got, err := repo.GetMessages(ctx, 42, 10, 0)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(got) != 1 || got[0].Media == nil {
		t.Fatalf("read %d messages, media present: %v", len(got), len(got) == 1 && got[0].Media != nil)
	}
	if got[0].Media.Kind != domain.MediaKindVideoNote {
		t.Errorf("kind = %q, want %q", got[0].Media.Kind, domain.MediaKindVideoNote)
	}
	if got[0].Media.Duration != 7 {
		t.Errorf("duration = %d, want 7", got[0].Media.Duration)
	}

	// The upsert path: re-fetching the same message must not flatten it.
	if err := repo.SaveMessage(ctx, domain.Message{ID: 1, ChatID: 42, Date: now, Media: note}); err != nil {
		t.Fatalf("re-save message: %v", err)
	}
	got, err = repo.GetMessages(ctx, 42, 10, 0)
	if err != nil {
		t.Fatalf("get messages after upsert: %v", err)
	}
	if got[0].Media == nil || got[0].Media.Duration != 7 || got[0].Media.Kind != domain.MediaKindVideoNote {
		t.Fatalf("upsert lost the media detail: %+v", got[0].Media)
	}
}
