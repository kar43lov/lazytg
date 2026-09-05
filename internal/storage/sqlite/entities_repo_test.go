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

// The other side's read pointer only moves forward: neither a stale update
// nor a dialog page fetched before the update may take a tick away.
func TestChatState_ReadOutboxOnlyMovesForward(t *testing.T) {
	t.Parallel()
	repo, ctx := openTestRepo(t)

	if err := repo.SaveChat(ctx, domain.Chat{ID: 42, Type: domain.ChatTypePrivate, Title: "friend", ReadOutboxMaxID: 5}); err != nil {
		t.Fatalf("SaveChat: %v", err)
	}
	if err := repo.SetReadOutbox(ctx, 42, 9); err != nil {
		t.Fatalf("SetReadOutbox: %v", err)
	}
	if err := repo.SetReadOutbox(ctx, 42, 3); err != nil {
		t.Fatalf("SetReadOutbox (stale): %v", err)
	}
	if err := repo.SaveChat(ctx, domain.Chat{ID: 42, Type: domain.ChatTypePrivate, Title: "friend", ReadOutboxMaxID: 4}); err != nil {
		t.Fatalf("SaveChat (older page): %v", err)
	}
	chats, err := repo.GetChats(ctx)
	if err != nil || len(chats) != 1 {
		t.Fatalf("GetChats: %v %v", chats, err)
	}
	if chats[0].ReadOutboxMaxID != 9 {
		t.Fatalf("read pointer = %d, want 9", chats[0].ReadOutboxMaxID)
	}
}

// A chat reached by its handle is inserted when new and left alone when it
// is already listed: the resolved object carries no dialog facts, and
// writing it over the row would zero the count, the pin and the mute.
func TestSaveChatIfMissing_KeepsAListedRow(t *testing.T) {
	t.Parallel()
	repo, ctx := openTestRepo(t)

	if err := repo.SaveChatIfMissing(ctx, domain.Chat{ID: 1, Type: domain.ChatTypePrivate, Title: "New"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := repo.SaveChat(ctx, domain.Chat{ID: 2, Type: domain.ChatTypePrivate, Title: "Listed", UnreadCount: 4, Pinned: true}); err != nil {
		t.Fatalf("SaveChat: %v", err)
	}
	if err := repo.SaveChatIfMissing(ctx, domain.Chat{ID: 2, Type: domain.ChatTypePrivate, Title: "Resolved"}); err != nil {
		t.Fatalf("insert over listed: %v", err)
	}
	chats, err := repo.GetChats(ctx)
	if err != nil || len(chats) != 2 {
		t.Fatalf("GetChats: %v %v", chats, err)
	}
	for _, c := range chats {
		switch c.ID {
		case 1:
			if c.Title != "New" {
				t.Fatalf("new row = %+v", c)
			}
		case 2:
			if c.Title != "Listed" || c.UnreadCount != 4 || !c.Pinned {
				t.Fatalf("listed row was overwritten: %+v", c)
			}
		}
	}
}

func TestMessageButtons_RoundTrip(t *testing.T) {
	t.Parallel()
	repo, ctx := openTestRepo(t)
	if err := repo.SaveChat(ctx, domain.Chat{ID: 5, Type: domain.ChatTypePrivate, Title: "bot"}); err != nil {
		t.Fatalf("SaveChat: %v", err)
	}
	want := [][]domain.Button{{{Text: "Yes", Kind: domain.ButtonCallback, Data: []byte("y")}}, {{Text: "Docs", Kind: domain.ButtonURL, URL: "https://x"}}}
	if err := repo.SaveMessage(ctx, domain.Message{ID: 1, ChatID: 5, Date: time.Now(), Text: "pick", Buttons: want}); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	if err := repo.SaveMessage(ctx, domain.Message{ID: 2, ChatID: 5, Date: time.Now(), Text: "plain"}); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	msgs, err := repo.GetMessages(ctx, 5, 10, 0)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("GetMessages: %v %v", msgs, err)
	}
	for _, m := range msgs {
		switch m.ID {
		case 1:
			if !reflect.DeepEqual(m.Buttons, want) {
				t.Fatalf("keyboard = %+v, want %+v", m.Buttons, want)
			}
		case 2:
			if m.Buttons != nil {
				t.Fatalf("a plain message grew a keyboard: %+v", m.Buttons)
			}
		}
	}
	// An edit that takes the keyboard away is stored as none.
	if err := repo.SaveMessage(ctx, domain.Message{ID: 1, ChatID: 5, Date: time.Now(), Text: "picked"}); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	msgs, _ = repo.GetMessages(ctx, 5, 10, 0)
	for _, m := range msgs {
		if m.ID == 1 && m.Buttons != nil {
			t.Fatalf("the keyboard survived the edit that removed it: %+v", m.Buttons)
		}
	}
}

func TestMessageOriginAndPinned_RoundTrip(t *testing.T) {
	t.Parallel()
	repo, ctx := openTestRepo(t)
	if err := repo.SaveChat(ctx, domain.Chat{ID: 5, Type: domain.ChatTypeGroup, Title: "g"}); err != nil {
		t.Fatalf("SaveChat: %v", err)
	}
	at := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	if err := repo.SaveMessage(ctx, domain.Message{ID: 1, ChatID: 5, Date: time.Now(), Text: "fwd", Forwarded: &domain.Forward{From: "News", FromID: 500, Date: at}}); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	if err := repo.SaveMessage(ctx, domain.Message{ID: 2, ChatID: 5, Date: time.Now(), Text: "plain"}); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	if err := repo.SetPinnedMessages(ctx, 5, []int64{2, 77}, true); err != nil {
		t.Fatalf("SetPinnedMessages: %v", err)
	}
	msgs, err := repo.GetMessages(ctx, 5, 10, 0)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("GetMessages: %v %v", msgs, err)
	}
	for _, m := range msgs {
		switch m.ID {
		case 1:
			if m.Forwarded == nil || m.Forwarded.From != "News" || m.Forwarded.FromID != 500 || !m.Forwarded.Date.Equal(at) || m.Pinned {
				t.Fatalf("forwarded row = %+v", m)
			}
		case 2:
			if m.Forwarded != nil || !m.Pinned {
				t.Fatalf("pinned row = %+v", m)
			}
		}
	}
	if err := repo.SetPinnedMessages(ctx, 5, []int64{2}, false); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	msgs, _ = repo.GetMessages(ctx, 5, 10, 0)
	for _, m := range msgs {
		if m.Pinned {
			t.Fatalf("still pinned: %+v", m)
		}
	}
}

func TestChatArchived_RoundTripAndSetter(t *testing.T) {
	t.Parallel()
	repo, ctx := openTestRepo(t)
	if err := repo.SaveChat(ctx, domain.Chat{ID: 1, Type: domain.ChatTypePrivate, Title: "hidden", Archived: true}); err != nil {
		t.Fatalf("SaveChat: %v", err)
	}
	if err := repo.SaveChatIfMissing(ctx, domain.Chat{ID: 2, Type: domain.ChatTypePrivate, Title: "also", Archived: true}); err != nil {
		t.Fatalf("SaveChatIfMissing: %v", err)
	}
	chats, _ := repo.GetChats(ctx)
	if len(chats) != 2 || !chats[0].Archived || !chats[1].Archived {
		t.Fatalf("archived flags lost: %+v", chats)
	}
	if err := repo.SetArchived(ctx, 1, false); err != nil {
		t.Fatalf("SetArchived: %v", err)
	}
	chats, _ = repo.GetChats(ctx)
	for _, c := range chats {
		if (c.ID == 1) == c.Archived {
			t.Fatalf("after SetArchived: %+v", c)
		}
	}
}
