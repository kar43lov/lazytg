package chats

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/ui/safetext"
)

// previewMaxRunes is the upper bound on Description() output. 60 runes
// (counted in code points, not bytes) is the same width WeeChat uses for
// the bottom-line preview and keeps the column readable on a 30%-of-80
// chats pane (≈24 cols visible after padding).
const previewMaxRunes = 60

// ChatItem is the per-row payload the bubbles/list delegate consumes.
// Fields are unexported because the struct must satisfy the DefaultItem
// interface (Title() / Description() methods), and Go forbids a method
// with the same name as an exported field. Public access goes through
// accessor methods so callers (the app, tests) cannot accidentally mutate
// state the list is currently rendering from.
type ChatItem struct {
	id              int64
	name            string
	preview         string
	lastMessageDate time.Time
	unreadCount     int
	pinned          bool
	chatType        domain.ChatType
	mutedUntil      time.Time
	unreadMark      bool
	online          bool
	lastSeen        time.Time
	readOutboxMaxID int64
	username        string
	draft           string
	// width is the room the row has, set when the list is laid out. Zero
	// means "unknown", and the row then carries no right-hand column.
	width int
	// now is the clock the row is drawn against; zero means time.Now.
	now time.Time
}

// NewChatItem builds a ChatItem from the storage-layer domain.Chat plus an
// optional preview string (the last message text, possibly truncated by
// the caller). preview can be empty for a freshly-discovered chat that has
// no cached messages yet.
func NewChatItem(c domain.Chat, preview string) ChatItem {
	// A preview is one row in a list: newlines from a multi-line message would
	// push every chat below it down and break the row arithmetic the mouse
	// hit-test relies on.
	// CleanLine also collapses the whitespace, and strips the escape
	// sequences a sender could otherwise aim at the terminal from a
	// message the user never opened — see internal/ui/safetext.
	preview = safetext.CleanLine(preview)
	// A chat discovered by the live path has no title until dialog sync runs:
	// an update carries the peer id and kind, not a name. Rendering the raw
	// empty string produced a blank row that looked like a drawing bug, so the
	// id stands in until the real title arrives.
	name := safetext.CleanLine(c.Title)
	if name == "" {
		name = fmt.Sprintf("chat %d", c.ID)
	}
	return ChatItem{
		id:              c.ID,
		name:            name,
		preview:         preview,
		lastMessageDate: c.LastMessageDate,
		unreadCount:     c.UnreadCount,
		pinned:          c.Pinned,
		chatType:        c.Type,
		mutedUntil:      c.MutedUntil,
		unreadMark:      c.UnreadMark,
		online:          c.Online,
		lastSeen:        c.LastSeen,
		readOutboxMaxID: c.ReadOutboxMaxID,
		username:        c.Username,
	}
}

// Username is the public handle of the chat, empty when it has none.
func (i ChatItem) Username() string { return i.username }

// ReadOutboxMaxID is the newest of your messages the other side has read.
func (i ChatItem) ReadOutboxMaxID() int64 { return i.readOutboxMaxID }

// Muted reports whether the chat's notifications are off at now.
func (i ChatItem) Muted(now time.Time) bool {
	return !i.mutedUntil.IsZero() && i.mutedUntil.After(now)
}

// UnreadMark reports the by-hand unread dot.
func (i ChatItem) UnreadMark() bool { return i.unreadMark }

// Online reports whether the other party of a private chat is online.
func (i ChatItem) Online() bool { return i.online }

// LastSeen is when the other party was last online, zero when unknown.
func (i ChatItem) LastSeen() time.Time { return i.lastSeen }

// ID returns the underlying chat id (Telegram peer id).
func (i ChatItem) ID() int64 { return i.id }

// Name returns the raw chat title without pin/unread decorations.
func (i ChatItem) Name() string { return i.name }

// LastMessageDate returns the timestamp used by Sort.
func (i ChatItem) LastMessageDate() time.Time { return i.lastMessageDate }

// UnreadCount returns the unread counter persisted by the storage layer.
func (i ChatItem) UnreadCount() int { return i.unreadCount }

// Pinned reports whether the user has pinned this chat. Pinned items are
// always sorted before unpinned ones.
func (i ChatItem) Pinned() bool { return i.pinned }

// Type returns the underlying chat type (private/group/supergroup/channel).
func (i ChatItem) Type() domain.ChatType { return i.chatType }

// Title implements list.DefaultItem. Format follows the plan:
// "[📌] <Name> (<unread>)". The pin prefix and unread suffix only appear
// when relevant — empty placeholders would shift the title column.
func (i ChatItem) Title() string {
	var b strings.Builder
	if i.pinned {
		b.WriteString("📌 ")
	}
	b.WriteString(i.name)
	return padBetween(b.String(), chatTimeLabel(i.lastMessageDate, i.clock()), i.width)
}

// Description implements list.DefaultItem and returns the last-message
// preview truncated to previewMaxRunes runes. Truncation is rune-aware
// (Cyrillic/CJK safe) and appends an ellipsis when it actually shortens.
func (i ChatItem) Description() string {
	var badge strings.Builder
	switch {
	case i.unreadCount > 0:
		fmt.Fprintf(&badge, "(%d)", i.unreadCount)
	case i.unreadMark:
		badge.WriteString("●")
	}
	if i.Muted(i.clock()) {
		if badge.Len() > 0 {
			badge.WriteString(" ")
		}
		badge.WriteString("🔕")
	}
	left := truncateRunes(i.preview, previewMaxRunes)
	if i.draft != "" {
		// A draft outranks the last message, the way it does in every
		// official client: the row is about what the user was doing here.
		left = draftStyle.Render("Draft:") + " " + truncateRunes(i.draft, previewMaxRunes)
	}
	return padBetween(left, badge.String(), i.width)
}

// draftStyle paints the word that says the preview is your own unsent
// text, in the red every official client uses for it.
var draftStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))

// withDraft is the row told what is half-written in it.
func (i ChatItem) withDraft(text string) ChatItem {
	i.draft = safetext.CleanLine(text)
	return i
}

// Draft is the half-written text the row shows, empty when there is none.
func (i ChatItem) Draft() string { return i.draft }

// withWidth is the row told how much room it has.
func (i ChatItem) withWidth(w int) ChatItem {
	i.width = w
	return i
}

func (i ChatItem) clock() time.Time {
	if i.now.IsZero() {
		return time.Now()
	}
	return i.now
}

// padBetween lays left and right at the two ends of width columns. With no
// width known the two are simply joined, and when they do not both fit the
// left side gives way: the name can be cut, the time cannot.
func padBetween(left, right string, width int) string {
	if right == "" {
		return left
	}
	if width <= 0 {
		return left + "  " + right
	}
	rw := lipgloss.Width(right)
	room := width - rw - 1
	if room < 1 {
		return truncateCells(right, width)
	}
	if lipgloss.Width(left) > room {
		left = truncateCells(left, room)
	}
	gap := width - lipgloss.Width(left) - rw
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// truncateCells cuts s to width terminal cells with an ellipsis, counting
// wide characters as the two cells they take.
func truncateCells(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	if width <= 1 {
		return "…"
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := lipgloss.Width(string(r))
		if used+w > width-1 {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + "…"
}

// chatTimeLabel is how the list dates a chat's last message: the clock
// today, "Yesterday", the weekday inside the week, the date beyond it.
// Local time, like the thread's headers.
func chatTimeLabel(at, now time.Time) string {
	if at.IsZero() {
		return ""
	}
	at, now = at.Local(), now.Local()
	day := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()) }
	switch days := int(day(now).Sub(day(at)).Hours() / 24); {
	case days <= 0:
		return at.Format("15:04")
	case days == 1:
		return "Yesterday"
	case days < 7:
		return at.Format("Mon")
	case at.Year() == now.Year():
		return at.Format("02.01")
	default:
		return at.Format("02.01.06")
	}
}

// FilterValue implements list.Item. The built-in fuzzy filter matches
// against the raw name only — including emoji prefixes or unread counters
// here would make typing "Alice" miss "[📌] Alice (3)".
func (i ChatItem) FilterValue() string { return i.name }

// truncateRunes cuts s to at most n runes, appending '…' when it had to
// drop tail bytes. Uses rune iteration so multi-byte chars never get sliced
// mid-codepoint (the app status bar would render '\xc2' otherwise).
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}

// Chat rebuilds the domain view of this row.
//
// The item keeps the fields it renders rather than the whole record, so a
// folder — which asks questions about type and unread state, not about how
// the row looks — needs them handed back in the shape its rules are written
// against.
func (i ChatItem) Chat() domain.Chat {
	return domain.Chat{
		ID:              i.id,
		Title:           i.name,
		Type:            i.chatType,
		UnreadCount:     i.unreadCount,
		Pinned:          i.pinned,
		LastMessageDate: i.lastMessageDate,
		MutedUntil:      i.mutedUntil,
		UnreadMark:      i.unreadMark,
		Online:          i.online,
		LastSeen:        i.lastSeen,
		ReadOutboxMaxID: i.readOutboxMaxID,
		Username:        i.username,
	}
}
