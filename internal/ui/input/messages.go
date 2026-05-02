package input

import "github.com/pgmac/lazytg/internal/core/domain"

// SetChatMsg is delivered by the app when the user opens a chat. It binds
// the input model to a specific chat id so subsequent Send calls know
// where to route the outgoing message. ReplyTo is reset on every chat
// change so a stale reply pointer never leaks across conversations.
type SetChatMsg struct {
	ChatID int64
}

// SetReplyMsg arms a reply pointer in the input pane. The app sends this
// after resolving the highlighted thread message into a domain.Message
// (see RequestReplyMsg below for the trigger half of the protocol). When
// Msg is nil the reply pointer is cleared (used by Esc-style cancels).
type SetReplyMsg struct {
	Msg *domain.Message
}

// RequestReplyMsg is published by the input pane when the user presses
// keymap.Reply. The app handles it by inspecting the thread pane's
// selected message and replying with a SetReplyMsg. The two-step dance
// keeps the input pane free of any direct dependency on the thread.
type RequestReplyMsg struct{}

// OpenEditorMsg is published by the input pane when the user presses
// keymap.OpenEditor and bounces back through the program loop. The
// pane catches it on the next Update cycle and spawns $EDITOR via
// OpenEditor() — emitting it as a Cmd first lets the loop flush the
// pending frame before tea.ExecProcess pauses the program. The
// editor result returns as EditorClosedMsg.
type OpenEditorMsg struct {
	CurrentText string
}

// EditorClosedMsg is delivered after the external $EDITOR exits. Text
// holds the file contents (newline-trimmed by the caller); Err is set if
// the editor exited non-zero or the temp file could not be read.
type EditorClosedMsg struct {
	Text string
	Err  error
}

// SendDispatchedMsg is the post-send notification: SendService.SendText
// has accepted the message (the optimistic record is in the repo) and
// returned a localID. The app routes this to the thread pane so the
// pending bubble can render even before the bus event arrives.
type SendDispatchedMsg struct {
	LocalID string
	ChatID  int64
	Text    string
	ReplyTo int
}

// SendFailedMsg fires when SendService.SendText itself returned an error
// (e.g. validation failed before the RPC was even attempted, or the
// optimistic write to storage broke). The textarea contents are
// preserved and re-armed so the user can retry without retyping.
type SendFailedMsg struct {
	Text    string
	ReplyTo int
	Err     error
}
