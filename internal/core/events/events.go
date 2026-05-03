// Package events defines the typed event bus used by core/sync, core/storage and the UI layer.
//
// All events implement the Event marker interface. Consumers receive events via Bus.Subscribe
// and use a type switch to react to specific event variants.
package events

import (
	"time"

	"github.com/pgmac/lazytg/internal/core/domain"
)

// Event is the marker interface implemented by every event type in this package.
// The unexported eventMarker method prevents accidental implementations from other packages.
type Event interface {
	eventMarker()
}

// MessageReceived is emitted when a new message is observed for a known chat.
// Media, when non-nil, carries the attachment metadata extracted from the
// gotd update so consumers (LiveService persistence, thread pane render,
// download pipeline) all see the same view of the message without having
// to re-fetch it. nil means plain-text or an unsupported media variant.
type MessageReceived struct {
	ChatID    int64
	MessageID int64
	Text      string
	FromID    int64
	Date      time.Time
	Media     *domain.MediaInfo
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

// Reasons emitted on StorageStateChanged.Reason. The detector uses
// degradation-shaped strings ("permission denied", "disk i/o error", …)
// while the dbsize monitor uses ReasonDBSizeWarning so the UI can
// distinguish "the file is unwriteable" from "the file is just very
// large" without parsing free-form text.
const (
	// ReasonDBSizeWarning marks an event emitted by obs.DBSizeMonitor
	// when the SQLite file crosses the configured size threshold (1 GB
	// by default). Mode stays at StorageModeReadWrite — the DB still
	// works, the warning is purely informational.
	ReasonDBSizeWarning = "db_size_warning"
)

// StorageStateChanged is emitted by the degradation detector when the
// repository's write-access mode changes, and by the obs.DBSizeMonitor
// when the SQLite file grows past its warning threshold. Mode is one of
// the StorageMode constants; Reason is a short human-readable explanation
// that the status bar shows verbatim (e.g. "permission denied", "disk
// i/o error", "db_size_warning").
//
// DBSizeMB is the current size of the SQLite file rounded to whole
// megabytes; it is 0 for events emitted by paths that do not measure the
// file (i.e. the degradation detector). Subscribers must treat 0 as "not
// applicable", not "0 MB".
type StorageStateChanged struct {
	Mode     string
	Reason   string
	DBSizeMB int
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

// FileDownloadStarted is emitted by core/files.DownloadService once a
// download has been queued. FileID identifies the Telegram media; Path
// is the on-disk destination (or .partial during transfer); Filename is
// the user-visible name shown by the status bar; Size is the
// originally-advertised byte count (0 when unknown). Subscribers
// typically reset their progress widget to 0% on Started.
type FileDownloadStarted struct {
	FileID   int64
	ChatID   int64
	Path     string
	Filename string
	Size     int64
}

func (FileDownloadStarted) eventMarker() {}

// FileDownloadProgress is emitted periodically while the file is being
// streamed. BytesDownloaded is the cumulative byte count; TotalBytes
// echoes Size from the matching Started event. Producers throttle the
// emit rate (every 5% or every 1 MiB by default) so a busy bus does not
// flood the UI render loop with tens of thousands of ticks per file.
type FileDownloadProgress struct {
	FileID          int64
	BytesDownloaded int64
	TotalBytes      int64
}

func (FileDownloadProgress) eventMarker() {}

// FileDownloadCompleted is emitted once the file has been written and
// the .partial → final rename has succeeded. Path is the user-visible
// final location.
type FileDownloadCompleted struct {
	FileID int64
	Path   string
	Size   int64
}

func (FileDownloadCompleted) eventMarker() {}

// FileDownloadFailed is emitted when a download is aborted. Err carries
// the underlying error message in human-readable form so the status
// bar / future toast notification can show it without a separate
// translation step.
type FileDownloadFailed struct {
	FileID int64
	Err    string
}

func (FileDownloadFailed) eventMarker() {}

// UploadID is the in-process identifier the upload pipeline assigns to a
// single Ctrl-U invocation. It is not the Telegram-assigned file id (gotd
// generates that internally and exposes it only after the upload
// completes) — the status bar needs *something* to dedupe its progress
// rows the moment the upload starts, before any RPC has fired. A
// monotonic int64 is sufficient and keeps the bar's keying scheme
// identical to the download path (which uses the Telegram file_id).
type UploadID = int64

// FileUploadStarted is emitted by core/files.UploadService once a file
// upload has been queued. UploadID is the in-process identifier the
// status bar uses to dedupe its progress widget; ChatID is where the
// file is being sent; Path is the source path on disk; Filename is the
// user-visible name (typically filepath.Base(Path)); Size is the byte
// count read from os.Stat at queue time.
type FileUploadStarted struct {
	UploadID UploadID
	ChatID   int64
	Path     string
	Filename string
	Size     int64
}

func (FileUploadStarted) eventMarker() {}

// FileUploadProgress is emitted periodically while the file is being
// streamed to Telegram. BytesUploaded is the cumulative byte count the
// gotd uploader has confirmed; TotalBytes echoes Size from the matching
// Started event. Producers throttle the emit rate so a busy bus does
// not flood the UI render loop.
type FileUploadProgress struct {
	UploadID      UploadID
	BytesUploaded int64
	TotalBytes    int64
}

func (FileUploadProgress) eventMarker() {}

// FileUploadCompleted is emitted once the upload has been accepted by
// Telegram (messages.sendMedia returned success). MessageID is the
// server-assigned message id of the message that carries the just-
// uploaded media — 0 when the server only acks via PTS without
// surfacing one (rare for sendMedia but defensive).
type FileUploadCompleted struct {
	UploadID  UploadID
	ChatID    int64
	MessageID int64
}

func (FileUploadCompleted) eventMarker() {}

// FileUploadFailed is emitted when an upload is aborted. Err carries
// the underlying error message in human-readable form so the status
// bar / future toast notification can show it without a separate
// translation step.
type FileUploadFailed struct {
	UploadID UploadID
	Err      string
}

func (FileUploadFailed) eventMarker() {}

// FileUploadWarning is emitted when an upload exceeds the soft size
// threshold (50 MiB by default). The upload still proceeds — the event
// is purely informational so the status bar can show "uploading large
// file…" alongside the regular progress widget. Hard rejection of
// over-2-GiB files surfaces as a normal Failed event instead.
type FileUploadWarning struct {
	UploadID  UploadID
	Size      int64
	Threshold int64
}

func (FileUploadWarning) eventMarker() {}
