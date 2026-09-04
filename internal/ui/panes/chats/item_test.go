package chats

import (
	"strings"
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

// TestNewChatItem_DoesNotLetAChatNameDriveTheTerminal covers the row a
// user sees without opening anything. The title and the preview are both
// written by other people, and the chats pane draws them on every frame —
// so an escape sequence here reaches the terminal from a conversation the
// user has never looked at.
func TestNewChatItem_DoesNotLetAChatNameDriveTheTerminal(t *testing.T) {
	t.Parallel()

	item := NewChatItem(
		domain.Chat{ID: 1, Type: domain.ChatTypePrivate, Title: "Ivan\x1b]52;c;cHduZWQ=\x07"},
		"look\x1b[2J\u202eaway",
	)
	for _, field := range []string{item.Name(), item.Title(), item.Description()} {
		for _, bad := range []string{"\x1b]52", "\x1b[2J", "\u202e"} {
			if strings.Contains(field, bad) {
				t.Fatalf("rendered field %q still carries %q", field, bad)
			}
		}
	}
	if !strings.Contains(item.Name(), "Ivan") {
		t.Fatalf("cleaning ate the title: %q", item.Name())
	}
	if !strings.Contains(item.Description(), "look") {
		t.Fatalf("cleaning ate the preview: %q", item.Description())
	}
}
