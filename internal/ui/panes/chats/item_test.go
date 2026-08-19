package chats

import (
	"testing"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// TestNewChatItem_NamesAChatWithNoTitle covers a chat the live path created
// before dialog sync could supply a name. An update carries the peer id and
// kind only, so the row would otherwise render as an empty line — which reads
// as a rendering bug rather than as "this chat is new".
func TestNewChatItem_NamesAChatWithNoTitle(t *testing.T) {
	t.Parallel()

	item := NewChatItem(domain.Chat{ID: 275641346, Type: domain.ChatTypePrivate}, "")
	if item.Name() != "chat 275641346" {
		t.Fatalf("Name() = %q, want a placeholder carrying the id", item.Name())
	}
	if item.Title() != "chat 275641346" {
		t.Fatalf("Title() = %q, want the placeholder", item.Title())
	}
}

// TestNewChatItem_KeepsARealTitle guards the other direction: the placeholder
// must never win over a name dialog sync already stored.
func TestNewChatItem_KeepsARealTitle(t *testing.T) {
	t.Parallel()

	item := NewChatItem(domain.Chat{ID: 1, Type: domain.ChatTypePrivate, Title: "Павел Карлов"}, "")
	if item.Name() != "Павел Карлов" {
		t.Fatalf("Name() = %q, want the stored title", item.Name())
	}
}
