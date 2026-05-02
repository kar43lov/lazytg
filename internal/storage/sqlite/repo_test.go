package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/pgmac/lazytg/internal/core/domain"
	"github.com/pgmac/lazytg/internal/storage/sqlite"
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
