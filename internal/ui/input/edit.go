package input

import (
	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// Editing an existing message.
//
// The composer is where a person writes, so it is where they should rewrite —
// a separate popup would mean two text editors with two sets of bindings, and
// the one you reach for by habit would be the wrong one. The pane therefore
// gains a mode rather than a sibling: while an edit is armed, Enter submits a
// change to a message that already exists instead of sending a new one.
//
// The mode is deliberately visible and deliberately easy to leave. It changes
// the hint line, and Esc cancels it and restores whatever draft was in the
// box — because arming an edit destroys a half-written message otherwise, and
// that is the kind of loss a user does not forgive twice.

// StartEditMsg arms the composer to rewrite an existing message. The app
// sends it after the user presses the edit chord on the thread cursor.
type StartEditMsg struct {
	ChatID    int64
	MessageID int64
	// Text is what the message currently says: the starting point for the
	// edit, so the user changes a word rather than retyping the sentence.
	Text string
}

// CancelEditMsg disarms the composer without submitting.
type CancelEditMsg struct{}

// EditSubmittedMsg is emitted when the user confirms an edit. The app routes
// it into core; the composer does not speak to services other than its own
// sender, so that the "who may edit what" rules live in one place.
type EditSubmittedMsg struct {
	ChatID    int64
	MessageID int64
	Text      string
}

// editTarget is the message being rewritten, plus the draft the mode
// displaced so cancelling can put it back.
type editTarget struct {
	chatID    int64
	messageID int64
	draft     string
	replyTo   *domain.Message
}

// Editing reports whether the composer is rewriting a message rather than
// composing a new one. The status bar and the app's key routing both ask.
func (m Model) Editing() bool { return m.editing != nil }

// EditingMessageID returns the message being rewritten, or 0.
func (m Model) EditingMessageID() int64 {
	if m.editing == nil {
		return 0
	}
	return m.editing.messageID
}

// startEdit arms edit mode, stashing the current draft.
func (m *Model) startEdit(msg StartEditMsg) {
	m.editing = &editTarget{
		chatID:    msg.ChatID,
		messageID: msg.MessageID,
		draft:     m.textarea.Value(),
		replyTo:   m.replyTo,
	}
	// A reply pointer and an edit are mutually exclusive: an edit rewrites a
	// message that was already sent, and whatever it replied to is settled.
	m.replyTo = nil
	m.setValue(msg.Text)
}

// cancelEdit disarms edit mode and restores the displaced draft.
func (m *Model) cancelEdit() {
	if m.editing == nil {
		return
	}
	draft, replyTo := m.editing.draft, m.editing.replyTo
	m.editing = nil
	m.replyTo = replyTo
	m.setValue(draft)
}

// submitEdit emits the edit and leaves the mode. The composer does not wait
// for the server: the thread redraws when the edit lands on the bus, and a
// composer frozen until an RPC returns is a composer that feels broken on a
// slow link.
//
// An empty body cancels rather than submitting. Telegram rejects an empty
// edit — it would be a deletion, and deleting is a separate, deliberate
// gesture with its own confirmation.
func (m *Model) submitEdit() tea.Cmd {
	if m.editing == nil {
		return nil
	}
	text := m.textarea.Value()
	target := *m.editing
	if text == "" {
		m.cancelEdit()
		return nil
	}
	m.editing = nil
	m.replyTo = target.replyTo
	m.setValue(target.draft)
	return func() tea.Msg {
		return EditSubmittedMsg{
			ChatID:    target.chatID,
			MessageID: target.messageID,
			Text:      text,
		}
	}
}
