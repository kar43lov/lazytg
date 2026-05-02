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
