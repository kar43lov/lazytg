package sqlite_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// The formatting column rides the same upsert as everything else about a
// message. A column the insert lists and the scan forgets comes back nil,
// which for formatting means every message rendered flat.
func TestSaveMessage_CarriesEntities(t *testing.T) {
	t.Parallel()

	repo, ctx := openTestRepo(t)
	if err := repo.SaveChat(ctx, domain.Chat{ID: 42, Type: domain.ChatTypePrivate, Title: "friend"}); err != nil {
		t.Fatalf("SaveChat: %v", err)
	}
	want := []domain.Entity{
		{Kind: domain.EntityBold, Offset: 0, Length: 2},
		{Kind: domain.EntityTextURL, Offset: 3, Length: 4, URL: "https://x.y/z"},
	}
	if err := repo.SaveMessage(ctx, domain.Message{ID: 7, ChatID: 42, Date: time.Now(), Text: "hi there", Entities: want}); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	got, err := repo.Message(ctx, 42, 7)
	if err != nil {
		t.Fatalf("Message: %v", err)
	}
	if !reflect.DeepEqual(got.Entities, want) {
		t.Fatalf("entities = %+v, want %+v", got.Entities, want)
	}
	page, err := repo.GetMessages(ctx, 42, 10, 0)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(page) != 1 || !reflect.DeepEqual(page[0].Entities, want) {
		t.Fatalf("page carries %+v, want %+v", page, want)
	}
}

// The four list facts ride the chat upsert and come back off the page
// query; each setter changes its one column and leaves the rest alone.
func TestChatState_RoundTripAndSetters(t *testing.T) {
	t.Parallel()

	repo, ctx := openTestRepo(t)
	until := time.Date(2038, 1, 19, 3, 14, 7, 0, time.UTC)
	seen := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	if err := repo.SaveChat(ctx, domain.Chat{ID: 42, Type: domain.ChatTypePrivate, Title: "friend",
		UnreadCount: 3, MutedUntil: until, UnreadMark: true, Online: true, LastSeen: seen}); err != nil {
		t.Fatalf("SaveChat: %v", err)
	}
	chats, err := repo.GetChats(ctx)
	if err != nil || len(chats) != 1 {
		t.Fatalf("GetChats: %v %v", chats, err)
	}
	c := chats[0]
	if !c.MutedUntil.Equal(until) || !c.UnreadMark || !c.Online || !c.LastSeen.Equal(seen) {
		t.Fatalf("round trip lost a fact: %+v", c)
	}

	if err := repo.SetMutedUntil(ctx, 42, time.Time{}); err != nil {
		t.Fatalf("SetMutedUntil: %v", err)
	}
	if err := repo.SetUnread(ctx, 42, 0); err != nil {
		t.Fatalf("SetUnread: %v", err)
	}
	if err := repo.SetUnreadMark(ctx, 42, false); err != nil {
		t.Fatalf("SetUnreadMark: %v", err)
	}
	if err := repo.SetPresence(ctx, 42, false, seen.Add(time.Hour)); err != nil {
		t.Fatalf("SetPresence: %v", err)
	}
	if err := repo.SetPinned(ctx, 42, true); err != nil {
		t.Fatalf("SetPinned: %v", err)
	}
	chats, _ = repo.GetChats(ctx)
	c = chats[0]
	if c.Muted(time.Now()) || c.UnreadCount != 0 || c.UnreadMark || c.Online || !c.LastSeen.Equal(seen.Add(time.Hour)) || !c.Pinned {
		t.Fatalf("setters left %+v", c)
	}
	if c.Title != "friend" {
		t.Fatalf("a setter touched the title: %+v", c)
	}
}
