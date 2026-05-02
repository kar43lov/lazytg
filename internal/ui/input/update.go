package input

import (
	"context"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// sendTimeout caps how long SendService.SendText is allowed to take
// before the input pane gives up and surfaces an error. SendService
// itself is responsible for retries; this guard only catches a wedged
// optimistic write (storage hang, etc).
const sendTimeoutSeconds = 10

// Update routes incoming messages. The order matters:
//
//  1. App-protocol messages (SetChatMsg, SetReplyMsg, EditorClosedMsg)
//     run first so a chat switch or editor roundtrip has a chance to
//     reset state before any subsequent key processing in the same
//     batch.
//  2. KeyPressMsg handling routes our own chords (Send, Newline,
//     Reply, OpenEditor, history Prev/Next) before the textarea sees
//     them. ApplyEmacsBindings already removed the conflicting keys
//     from the textarea KeyMap, but we still want to publish the
//     RequestReplyMsg / OpenEditorMsg / Send Cmd on our terms.
//  3. Anything left over goes to the textarea (printable characters,
//     emacs cursor moves, deletions, etc).
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch typed := msg.(type) {
	case SetChatMsg:
		m.chatID = typed.ChatID
		m.replyTo = nil
		return m, nil
	case SetReplyMsg:
		m.replyTo = typed.Msg
		return m, nil
	case OpenEditorMsg:
		// The chord handler emits OpenEditorMsg as a Cmd so the
		// program loop has a chance to flush the previous frame
		// before tea.ExecProcess pauses the program. Spawning the
		// editor here keeps the editor lifecycle (temp file →
		// process → EditorClosedMsg) inside the input pane.
		return m, OpenEditor(typed.CurrentText)
	case EditorClosedMsg:
		return m.applyEditorResult(typed)
	case SendFailedMsg:
		// Restore the draft so the user can retry. ReplyTo is rearmed
		// so the retry context is preserved.
		m.setValue(typed.Text)
		return m, nil
	case tea.KeyPressMsg:
		if cmd, handled := m.handleChord(typed); handled {
			return m, cmd
		}
	}

	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	return m, cmd
}

// handleChord intercepts the input-pane chords before the textarea
// sees them. Returns (cmd, true) when the key was consumed; otherwise
// the key falls through to the textarea.
func (m *Model) handleChord(k tea.KeyPressMsg) (tea.Cmd, bool) {
	switch {
	case key.Matches(k, m.keymap.Send):
		return m.handleSend(), true
	case key.Matches(k, m.keymap.Newline):
		m.textarea.InsertString("\n")
		return nil, true
	case key.Matches(k, m.keymap.Reply):
		return m.requestReply(), true
	case key.Matches(k, m.keymap.OpenEditor):
		return m.requestEditor(), true
	case isHistoryPrev(k):
		return m.historyPrev(), true
	case isHistoryNext(k):
		return m.historyNext(), true
	}
	return nil, false
}

// handleSend dispatches the textarea contents to SendService. Returns
// no Cmd (and does not clear the textarea) when the send service is
// missing or the body is empty so a stray Enter on a blank composer is
// a no-op rather than a spurious history entry.
func (m *Model) handleSend() tea.Cmd {
	text := m.textarea.Value()
	if text == "" {
		return nil
	}
	if m.send == nil || m.chatID == 0 {
		return nil
	}
	replyTo := 0
	if m.replyTo != nil {
		replyTo = int(m.replyTo.ID)
	}
	chatID := m.chatID
	send := m.send
	m.history.Add(text)
	m.resetComposer()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeoutSeconds*sendCtxUnit)
		defer cancel()
		localID, err := send.SendText(ctx, chatID, text, replyTo)
		if err != nil {
			return SendFailedMsg{Text: text, ReplyTo: replyTo, Err: err}
		}
		return SendDispatchedMsg{
			LocalID: localID,
			ChatID:  chatID,
			Text:    text,
			ReplyTo: replyTo,
		}
	}
}

// requestReply asks the app to resolve the highlighted thread message
// into a domain.Message and bounce it back via SetReplyMsg. The two-
// step protocol keeps the input pane decoupled from the thread.
func (m *Model) requestReply() tea.Cmd {
	return func() tea.Msg { return RequestReplyMsg{} }
}

// requestEditor asks the app to spawn $EDITOR with the current
// textarea contents. The editor result returns as EditorClosedMsg.
func (m *Model) requestEditor() tea.Cmd {
	current := m.textarea.Value()
	return func() tea.Msg {
		return OpenEditorMsg{CurrentText: current}
	}
}

// applyEditorResult writes the editor's output back into the textarea
// and re-focuses it. Errors are logged at warn level — the user keeps
// whatever they had typed before pressing Ctrl+E.
func (m Model) applyEditorResult(msg EditorClosedMsg) (Model, tea.Cmd) {
	if msg.Err != nil {
		if m.log != nil {
			m.log.Warn("input: editor returned error", "err", msg.Err)
		}
		return m, nil
	}
	m.setValue(msg.Text)
	if m.Focused {
		_ = m.textarea.Focus()
	}
	return m, nil
}

// historyPrev replaces the textarea contents with the previous entry
// (older). When already at the oldest entry the textarea is left
// untouched so the user does not lose context to a stuck cursor.
func (m *Model) historyPrev() tea.Cmd {
	text, ok := m.history.Prev()
	if !ok && text == "" {
		return nil
	}
	m.textarea.SetValue(text)
	m.textarea.CursorEnd()
	return nil
}

// historyNext replaces the textarea contents with the next entry
// (newer). When walking past the newest entry the textarea is cleared
// (the "draft slot") so the user can resume typing a fresh message.
func (m *Model) historyNext() tea.Cmd {
	text, ok := m.history.Next()
	if !ok {
		return nil
	}
	m.textarea.SetValue(text)
	m.textarea.CursorEnd()
	return nil
}

// isHistoryPrev reports whether k matches the readline "previous in
// history" chord (Ctrl+P). Kept as a helper rather than a Keymap field
// because Ctrl+P is a fixed readline convention, not a user-tunable
// surface — exposing it as Keymap.HistoryPrev would invite conflicts
// with the textarea's own LinePrevious binding which we already
// scrubbed in ApplyEmacsBindings.
func isHistoryPrev(k tea.KeyPressMsg) bool {
	return k.Mod == tea.ModCtrl && k.Code == 'p'
}

// isHistoryNext is the symmetric helper for Ctrl+N.
func isHistoryNext(k tea.KeyPressMsg) bool {
	return k.Mod == tea.ModCtrl && k.Code == 'n'
}
