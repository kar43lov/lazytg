// Package events defines the typed event bus used by core/sync, core/storage and the UI layer.
//
// All events implement the Event marker interface. Consumers receive events via Bus.Subscribe
// and use a type switch to react to specific event variants.
package events

import "time"

// Event is the marker interface implemented by every event type in this package.
// The unexported eventMarker method prevents accidental implementations from other packages.
type Event interface {
	eventMarker()
}

// MessageReceived is emitted when a new message is observed for a known chat.
type MessageReceived struct {
	ChatID    int64
	MessageID int64
	Text      string
	FromID    int64
	Date      time.Time
}

func (MessageReceived) eventMarker() {}

// DialogUpdated is emitted when the chat-list ordering or metadata changes (new last message,
// rename, pin/unpin, unread counter change).
type DialogUpdated struct {
	ChatID int64
}

func (DialogUpdated) eventMarker() {}

// AuthStateChanged is emitted when the Telegram authorization state changes (logged-in/out,
// password requested, code requested).
type AuthStateChanged struct {
	State string
}

func (AuthStateChanged) eventMarker() {}

// ConnectionStateChanged is emitted when the underlying MTProto connection state changes
// (connecting, connected, reconnecting, disconnected).
type ConnectionStateChanged struct {
	State string
}

func (ConnectionStateChanged) eventMarker() {}

// OutgoingMessageState enumerates the lifecycle of a locally-composed message
// that has been handed to the send pipeline. The string form is what gets
// persisted in the outgoing table so SQLite inspection stays human-readable.
const (
	// OutgoingStatePending — record stored locally, RPC not yet completed.
	OutgoingStatePending = "pending"
	// OutgoingStateSent — server acknowledged the message and assigned an id.
	OutgoingStateSent = "sent"
	// OutgoingStateFailed — RPC failed and exhausted any allowed retries.
	OutgoingStateFailed = "failed"
)

// OutgoingMessageStateChanged is emitted when an outgoing message advances
// through its state machine (pending → sent | failed). LocalID is the UUID
// the SendService assigned when the user pressed Enter; ServerID is the
// Telegram-assigned message id and is only meaningful for state="sent".
// Error carries the human-readable failure reason for state="failed".
type OutgoingMessageStateChanged struct {
	LocalID  string
	ChatID   int64
	ServerID int64
	State    string
	Error    string
}

func (OutgoingMessageStateChanged) eventMarker() {}

// StorageMode enumerates the access modes the SQLite-backed repository can
// surface to the rest of the app. The string form is what gets emitted on
// the bus and shown in the status bar so it stays human-readable.
const (
	// StorageModeReadWrite — repo accepts both reads and writes; nominal mode.
	StorageModeReadWrite = "rw"
	// StorageModeReadOnly — writes are rejected (filesystem permission, disk
	// full, SQLITE_READONLY etc); the UI degrades to read-only and surfaces
	// the reason via the status bar.
	StorageModeReadOnly = "read-only"
)

// StorageStateChanged is emitted by the degradation detector when the
// repository's write-access mode changes. Mode is one of the StorageMode
// constants; Reason is a short human-readable explanation that the status
// bar shows verbatim (e.g. "permission denied", "disk i/o error").
type StorageStateChanged struct {
	Mode   string
	Reason string
}

func (StorageStateChanged) eventMarker() {}

// ReindexProgress is emitted by the search ReindexService after each chat in
// a multi-chat reindex pass completes. ChatID is the chat just processed;
// Indexed is the number of newly-indexed rows for that chat (0 if the chat
// was already up to date); Total is the cumulative count across the whole
// pass so far. Done is true on the very last event for the pass — UI uses
// this to flip the "indexing…" indicator off without having to count chats
// itself.
type ReindexProgress struct {
	ChatID  int64
	Indexed int
	Total   int
	Done    bool
}

func (ReindexProgress) eventMarker() {}

// SearchJumpRequested is emitted by the search overlay when the user
// presses Enter on a hit. The app routes it into a chat switch (chats
// pane Update + thread pane OpenChat) followed by a thread-pane
// ScrollTo(MessageID, ±5 around). Routing through the bus keeps the
// search overlay decoupled from chats / thread internals — anyone
// subscribed to the bus can react to a jump (status bar can show the
// jump target, future history-of-jumps panel can record it, etc.).
type SearchJumpRequested struct {
	ChatID    int64
	MessageID int64
}

func (SearchJumpRequested) eventMarker() {}
