package thread

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// The line that says where you stopped reading.
//
// Opening a chat with fourteen unread messages and being dropped at the
// bottom answers the wrong question. What the reader wants is not the newest
// message, it is the first one they have not seen — and the only client that
// does not tell them is one that discards the count it already has.
//
// The boundary is worked out once, when the conversation loads, and then left
// alone. It has to be: acknowledging the chat clears the count within a
// second of opening it, and a divider recomputed from that would vanish while
// the reader was still looking at it. Messages arriving afterwards go below
// the line rather than moving it, which is what every client does and what
// somebody reading a backlog expects.

// unreadStyle paints the rule. The same grey as the day separator, because it
// is the same kind of thing — a mark between messages rather than a message —
// but bold, because unlike the date it is where the reader is going.
var unreadStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Bold(true)

// MarkUnread tells the pane how many messages were unread when the chat was
// opened. Zero clears the divider.
//
// Called before the messages arrive, because that is when the count is still
// true. The boundary itself is worked out when they land.
func (m Model) MarkUnread(n int) Model {
	if n < 0 {
		n = 0
	}
	m.unreadCount = n
	m.unreadFrom = 0
	if n > 0 {
		m = m.locateUnread()
	}
	return m
}

// UnreadFrom returns the message the divider sits above, or 0. Test helper.
func (m Model) UnreadFrom() int64 { return m.unreadFrom }

// locateUnread walks back over the loaded messages until it has counted the
// unread ones, and remembers where it stopped.
//
// Only incoming messages count, because that is what Telegram's counter
// counts: replying from another device does not make your own message unread.
// A count larger than the page means the boundary is older than anything
// loaded — the divider then goes above the oldest message rather than
// nowhere, which is honest about there being more above and keeps it out of
// the middle of the page where it would be wrong.
func (m Model) locateUnread() Model {
	if m.unreadCount <= 0 || len(m.messages) == 0 {
		m.unreadFrom = 0
		return m
	}
	seen := 0
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].Outgoing {
			continue
		}
		seen++
		if seen == m.unreadCount {
			m.unreadFrom = m.messages[i].ID
			return m
		}
	}
	m.unreadFrom = m.messages[0].ID
	return m
}

// renderUnreadRule draws the divider.
func renderUnreadRule(count, width int) string {
	label := fmt.Sprintf(" %s ", unreadLabel(count))
	if width < lipgloss.Width(label)+4 {
		return unreadStyle.Render(strings.TrimSpace(label))
	}
	dashes := width - lipgloss.Width(label)
	left := dashes / 2
	right := dashes - left
	return unreadStyle.Render(strings.Repeat("─", left) + label + strings.Repeat("─", right))
}

func unreadLabel(count int) string {
	if count == 1 {
		return "1 new message"
	}
	return fmt.Sprintf("%d new messages", count)
}

// unreadAbove reports whether the divider belongs above this message.
func (m Model) unreadAbove(msg domain.Message) bool {
	return m.unreadFrom != 0 && msg.ID == m.unreadFrom
}
