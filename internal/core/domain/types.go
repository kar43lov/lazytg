// Package domain holds the core value types shared across the storage, sync
// and UI layers. Types here MUST be free of external infrastructure imports
// (no gotd, no bubbletea, no database/sql) so that any layer can depend on
// them without dragging in transitive heavy dependencies.
package domain

import "time"

// ChatType enumerates the kinds of Telegram peers lazytg cares about.
// Stored in the database as a short string so that values remain readable
// when inspecting the DB with sqlite3 CLI.
type ChatType string

const (
	// ChatTypePrivate is a one-to-one user dialog.
	ChatTypePrivate ChatType = "private"
	// ChatTypeGroup is a small (basic) group chat.
	ChatTypeGroup ChatType = "group"
	// ChatTypeSupergroup is a supergroup or megagroup.
	ChatTypeSupergroup ChatType = "supergroup"
	// ChatTypeChannel is a broadcast channel.
	ChatTypeChannel ChatType = "channel"
)

// Account represents a single Telegram account that lazytg has logged in to.
type Account struct {
	ID        int64
	Phone     string
	Alias     string
	CreatedAt time.Time
}

// Chat is the local view of a Telegram dialog (user, group, channel) used by
// the UI and search index.
type Chat struct {
	ID              int64
	Type            ChatType
	Title           string
	Username        string
	LastMessageDate time.Time
	UnreadCount     int
	Pinned          bool
}

// Message is a stored message belonging to a Chat. RawBlob holds the
// serialised gotd payload so the UI can re-render rich content without a
// round-trip to Telegram.
type Message struct {
	ID      int64
	ChatID  int64
	FromID  int64
	Date    time.Time
	Text    string
	ReplyTo int64
	RawBlob []byte
}
