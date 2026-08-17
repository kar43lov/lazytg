package sqlite_test

import (
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// TestChatHistoryFreshness covers the query the history backfill guard reads.
// The four states are the ones that decide whether a chat costs a
// messages.getHistory call on every re-focus or none at all, so each is pinned
// rather than inferred.
func TestChatHistoryFreshness(t *testing.T) {
	repo, ctx := openTestRepo(t)

	newest := time.Unix(1_700_000_000, 0).UTC()

	t.Run("unknown chat reports nothing without erroring", func(t *testing.T) {
		dialog, local, err := repo.ChatHistoryFreshness(ctx, 12345)
		if err != nil {
			t.Fatalf("unknown chat: %v", err)
		}
		if !dialog.IsZero() || !local.IsZero() {
			t.Errorf("want two zero times, got dialog=%v local=%v", dialog, local)
		}
	})

	t.Run("chat with no cached messages", func(t *testing.T) {
		chat := domain.Chat{ID: 1, Type: domain.ChatTypePrivate, Title: "empty", LastMessageDate: newest}
		if err := repo.SaveChat(ctx, chat); err != nil {
			t.Fatalf("SaveChat: %v", err)
		}
		dialog, local, err := repo.ChatHistoryFreshness(ctx, 1)
		if err != nil {
			t.Fatalf("freshness: %v", err)
		}
		if !dialog.Equal(newest) {
			t.Errorf("dialog date = %v, want %v", dialog, newest)
		}
		if !local.IsZero() {
			t.Errorf("local date = %v, want zero (nothing cached)", local)
		}
	})

	t.Run("cache in step with the dialog row", func(t *testing.T) {
		chat := domain.Chat{ID: 2, Type: domain.ChatTypePrivate, Title: "current", LastMessageDate: newest}
		if err := repo.SaveChat(ctx, chat); err != nil {
			t.Fatalf("SaveChat: %v", err)
		}
		msgs := []domain.Message{
			{ID: 1, ChatID: 2, Date: newest.Add(-time.Hour), Text: "older"},
			{ID: 2, ChatID: 2, Date: newest, Text: "newest"},
		}
		if err := repo.SaveMessages(ctx, msgs); err != nil {
			t.Fatalf("SaveMessages: %v", err)
		}
		dialog, local, err := repo.ChatHistoryFreshness(ctx, 2)
		if err != nil {
			t.Fatalf("freshness: %v", err)
		}
		if !local.Equal(newest) {
			t.Errorf("local = %v, want the max message date %v", local, newest)
		}
		if local.Before(dialog) {
			t.Errorf("local %v reads as behind dialog %v — the guard would keep re-fetching", local, dialog)
		}
	})

	t.Run("cache behind the dialog row", func(t *testing.T) {
		ahead := newest.Add(10 * time.Minute)
		chat := domain.Chat{ID: 3, Type: domain.ChatTypePrivate, Title: "behind", LastMessageDate: ahead}
		if err := repo.SaveChat(ctx, chat); err != nil {
			t.Fatalf("SaveChat: %v", err)
		}
		if err := repo.SaveMessages(ctx, []domain.Message{
			{ID: 1, ChatID: 3, Date: newest, Text: "stale"},
		}); err != nil {
			t.Fatalf("SaveMessages: %v", err)
		}
		dialog, local, err := repo.ChatHistoryFreshness(ctx, 3)
		if err != nil {
			t.Fatalf("freshness: %v", err)
		}
		if !local.Before(dialog) {
			t.Errorf("local %v must read as behind dialog %v", local, dialog)
		}
	})

	t.Run("another chat's messages do not count", func(t *testing.T) {
		// A subquery scoped by the outer chat id is easy to get wrong; a chat
		// with no messages of its own must not inherit a sibling's max(date).
		chat := domain.Chat{ID: 4, Type: domain.ChatTypePrivate, Title: "isolated", LastMessageDate: newest}
		if err := repo.SaveChat(ctx, chat); err != nil {
			t.Fatalf("SaveChat: %v", err)
		}
		_, local, err := repo.ChatHistoryFreshness(ctx, 4)
		if err != nil {
			t.Fatalf("freshness: %v", err)
		}
		if !local.IsZero() {
			t.Errorf("local = %v, want zero — messages of chats 2 and 3 leaked in", local)
		}
	})
}

// TestGetChats_CarriesLastMessagePreview covers the list's description line.
// Without it the chats pane rendered a name over an empty row for every chat —
// the pane looked half-drawn, which is how the first live session found it.
func TestGetChats_CarriesLastMessagePreview(t *testing.T) {
	repo, ctx := openTestRepo(t)

	base := time.Unix(1_700_000_000, 0).UTC()
	if err := repo.SaveChat(ctx, domain.Chat{ID: 9, Type: domain.ChatTypePrivate, Title: "with history", LastMessageDate: base}); err != nil {
		t.Fatalf("SaveChat: %v", err)
	}
	if err := repo.SaveChat(ctx, domain.Chat{ID: 10, Type: domain.ChatTypePrivate, Title: "no history"}); err != nil {
		t.Fatalf("SaveChat: %v", err)
	}
	if err := repo.SaveMessages(ctx, []domain.Message{
		{ID: 1, ChatID: 9, Date: base.Add(-time.Hour), Text: "older"},
		{ID: 2, ChatID: 9, Date: base, Text: "newest"},
	}); err != nil {
		t.Fatalf("SaveMessages: %v", err)
	}

	chats, err := repo.GetChats(ctx)
	if err != nil {
		t.Fatalf("GetChats: %v", err)
	}
	previews := map[int64]string{}
	for _, c := range chats {
		previews[c.ID] = c.LastMessagePreview
	}
	if got := previews[9]; got != "newest" {
		t.Errorf("preview for the chat with history = %q, want %q", got, "newest")
	}
	if got := previews[10]; got != "" {
		t.Errorf("preview for the chat without history = %q, want empty (not a sibling's text)", got)
	}
}
