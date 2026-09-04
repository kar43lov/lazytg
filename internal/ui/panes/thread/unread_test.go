package thread

import (
	"strings"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
)

// loadedThread puts messages in through the load path, which is where the
// divider is worked out.
func loadedThread(t *testing.T, unread int, msgs ...domain.Message) Model {
	t.Helper()
	m, _ := sized(New()).OpenChat(42)
	m = m.MarkUnread(unread)
	m, _ = m.applyLoaded(messagesLoadedMsg{chatID: 42, gen: m.loadGen, messages: msgs})
	return m
}

func incoming(id int64, offset int, text string) domain.Message {
	return domain.Message{
		ID: id, ChatID: 42, FromID: 7, Text: text,
		Date: time.Date(2026, 9, 5, 12, 0, offset, 0, time.UTC),
	}
}

func outgoing(id int64, offset int, text string) domain.Message {
	m := incoming(id, offset, text)
	m.FromID = 0
	m.Outgoing = true
	return m
}

func TestUnread_DividerSitsAboveTheFirstUnreadMessage(t *testing.T) {
	t.Parallel()

	m := loadedThread(t, 2,
		incoming(1, 0, "read one"),
		incoming(2, 1, "read two"),
		incoming(3, 2, "unread one"),
		incoming(4, 3, "unread two"),
	)
	if got := m.UnreadFrom(); got != 3 {
		t.Fatalf("divider is above message %d, want 3", got)
	}
	body, _ := m.renderContent()
	if !strings.Contains(body, "2 new messages") {
		t.Fatalf("no rule in the body:\n%s", body)
	}
	if strings.Index(body, "2 new messages") > strings.Index(body, "unread one") {
		t.Fatal("the rule is below the message it should be above")
	}
}

// Telegram's counter counts what arrived, not what you wrote: replying from
// another device does not make your own message unread.
func TestUnread_CountsOnlyIncomingMessages(t *testing.T) {
	t.Parallel()

	m := loadedThread(t, 1,
		incoming(1, 0, "read"),
		incoming(2, 1, "the unread one"),
		outgoing(3, 2, "my reply from the phone"),
	)
	if got := m.UnreadFrom(); got != 2 {
		t.Fatalf("divider is above %d, want the incoming message 2", got)
	}
}

func TestUnread_NoDividerWithNothingUnread(t *testing.T) {
	t.Parallel()

	m := loadedThread(t, 0, incoming(1, 0, "one"), incoming(2, 1, "two"))
	if got := m.UnreadFrom(); got != 0 {
		t.Fatalf("a divider appeared at %d with nothing unread", got)
	}
	body, _ := m.renderContent()
	if strings.Contains(body, "new message") {
		t.Fatalf("a rule was drawn:\n%s", body)
	}
}

// More unread than the page holds means the boundary is older than anything
// loaded. Above the oldest message is honest about there being more above;
// anywhere in the middle would be a lie.
func TestUnread_MoreUnreadThanLoadedPutsItAtTheTop(t *testing.T) {
	t.Parallel()

	m := loadedThread(t, 50, incoming(1, 0, "one"), incoming(2, 1, "two"))
	if got := m.UnreadFrom(); got != 1 {
		t.Fatalf("divider is above %d, want the oldest loaded message", got)
	}
}

// A message arriving afterwards goes below the line rather than moving it —
// which is what every client does and what somebody reading a backlog expects.
func TestUnread_ArrivingMessagesDoNotMoveTheLine(t *testing.T) {
	t.Parallel()

	m := loadedThread(t, 1, incoming(1, 0, "read"), incoming(2, 1, "unread"))
	before := m.UnreadFrom()

	m, _ = m.applyIncoming(events.MessageReceived{
		ChatID: 42, MessageID: 3, FromID: 7, Text: "and another", Date: time.Now(),
	})
	if got := m.UnreadFrom(); got != before {
		t.Fatalf("the line moved from %d to %d", before, got)
	}
}

// Opening another conversation starts again: the count belonged to the one
// being left.
func TestUnread_ClearedByAChatSwitch(t *testing.T) {
	t.Parallel()

	m := loadedThread(t, 2, incoming(1, 0, "one"), incoming(2, 1, "two"))
	if m.UnreadFrom() == 0 {
		t.Fatal("setup: no divider")
	}
	m, _ = m.OpenChat(99)
	if got := m.UnreadFrom(); got != 0 {
		t.Fatalf("the divider survived a chat switch: %d", got)
	}
}

func TestUnreadRule_ReadsAsASentence(t *testing.T) {
	t.Parallel()

	if got := renderUnreadRule(1, 40); !strings.Contains(got, "1 new message") || strings.Contains(got, "messages") {
		t.Fatalf("one message reads %q", got)
	}
	if got := renderUnreadRule(7, 40); !strings.Contains(got, "7 new messages") {
		t.Fatalf("seven messages read %q", got)
	}
	// A narrow pane keeps the words and drops the rule around them.
	if got := renderUnreadRule(7, 5); !strings.Contains(got, "7 new messages") {
		t.Fatalf("a narrow pane rendered %q", got)
	}
}
