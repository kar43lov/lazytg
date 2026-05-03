package chats

import (
	"fmt"
	"strings"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
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
}

// NewChatItem builds a ChatItem from the storage-layer domain.Chat plus an
// optional preview string (the last message text, possibly truncated by
// the caller). preview can be empty for a freshly-discovered chat that has
// no cached messages yet.
func NewChatItem(c domain.Chat, preview string) ChatItem {
	return ChatItem{
		id:              c.ID,
		name:            c.Title,
		preview:         preview,
		lastMessageDate: c.LastMessageDate,
		unreadCount:     c.UnreadCount,
		pinned:          c.Pinned,
		chatType:        c.Type,
	}
}

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
		b.WriteString("[📌] ")
	}
	b.WriteString(i.name)
	if i.unreadCount > 0 {
		fmt.Fprintf(&b, " (%d)", i.unreadCount)
	}
	return b.String()
}

// Description implements list.DefaultItem and returns the last-message
// preview truncated to previewMaxRunes runes. Truncation is rune-aware
// (Cyrillic/CJK safe) and appends an ellipsis when it actually shortens.
func (i ChatItem) Description() string {
	return truncateRunes(i.preview, previewMaxRunes)
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
