package input

import (
	"context"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
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
		// The draft goes with the chat it was written for: before this
		// the box simply kept its contents, so a half-written message
		// followed the user into somebody else's conversation and left
		// on the next Enter.
		m.switchChat(typed.ChatID)
		// The pending-draft tracker is per-chat: switching chats means
		// the user has moved on and any in-flight Failed event for the
		// previous chat must not re-populate the new composer with
		// stale text. Drop the slate so a late bus event finds no
		// matching localID and silently no-ops.
		m.inFlight = make(map[string]inFlightDraft)
		return m, nil
	case InsertTextMsg:
		if typed.Text != "" {
			m.textarea.InsertString(typed.Text)
			m.clearEmojiCompletion()
		}
		return m, nil
	case SetReplyMsg:
		m.replyTo = typed.Msg
		return m, nil
	case StartEditMsg:
		m.startEdit(typed)
		return m, nil
	case CancelEditMsg:
		m.cancelEdit()
		return m, nil
	case SendDispatchedMsg:
		// Remember the dispatched body + reply pointer keyed by
		// LocalID. The thread pane has its own copy for rendering; we
		// keep ours so an async Failed event from the bus can restore
		// the textarea without an extra trip through the thread.
		m.inFlight[typed.LocalID] = inFlightDraft{text: typed.Text, replyTo: typed.ReplyToMsg}
		return m, nil
	case events.OutgoingMessageStateChanged:
		return m.applyOutgoingState(typed)
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
		// Synchronous-error path: SendService.SendText returned an
		// error before the optimistic record landed (e.g. SaveOutgoing
		// hit ErrReadOnly). The body never reached a localID-tracked
		// state, so the bus-event path below cannot help — we restore
		// directly from the message payload.
		m.restoreDraft(typed.Text, typed.ReplyToMsg)
		return m, nil
	case tea.KeyPressMsg:
		if cmd, handled := m.handleChord(typed); handled {
			return m, cmd
		}
		// Any key that is not another completion press ends the cycle:
		// whatever emoji is in the box is now part of the message, and a
		// later Tab should start from what is typed rather than resume a
		// walk the user has moved on from.
		m.clearEmojiCompletion()
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
	case m.editing != nil && k.String() == "esc":
		// Esc leaves edit mode before anything else looks at the key:
		// the mode displaced a draft, and the way out has to be the
		// key every user already tries first.
		m.cancelEdit()
		return nil, true
	case key.Matches(k, m.keymap.Send):
		if m.editing != nil {
			return m.submitEdit(), true
		}
		return m.handleSend(), true
	case key.Matches(k, m.keymap.Newline):
		m.textarea.InsertString("\n")
		return nil, true
	case key.Matches(k, m.keymap.Reply):
		return m.requestReply(), true
	case key.Matches(k, m.keymap.CompleteEmoji):
		// Tab reaches the composer only when the app decided the
		// composer wants it — see App.emojiCompletionPending. If the
		// completion finds nothing the key is not consumed, so Tab still
		// cycles focus when there is no shortcode under the cursor.
		return nil, m.completeEmoji()
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
	replyToMsg := m.replyTo
	if replyToMsg != nil {
		replyTo = int(replyToMsg.ID)
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
			return SendFailedMsg{
				Text:       text,
				ReplyTo:    replyTo,
				ReplyToMsg: replyToMsg,
				Err:        err,
			}
		}
		return SendDispatchedMsg{
			LocalID:    localID,
			ChatID:     chatID,
			Text:       text,
			ReplyTo:    replyTo,
			ReplyToMsg: replyToMsg,
		}
	}
}

// applyOutgoingState reacts to the SendService bus events. Sent and
// Failed are terminal — we drop the localID from inFlight either way.
// Failed additionally restores the draft so the user can retry: this
// is the path that catches MTProto-level failures (FloodWait, network
// retries exhausted, validation), which the synchronous SendFailedMsg
// path never sees.
//
// Pending events are ignored: the thread pane already has a synchronous
// Dispatched insert and the input pane has nothing to do with the
// optimistic row's lifecycle. State strings outside the known set are
// also ignored — keeping the switch closed avoids surprise behaviour
// when a future state is added without updating this consumer.
func (m Model) applyOutgoingState(ev events.OutgoingMessageStateChanged) (Model, tea.Cmd) {
	switch ev.State {
	case events.OutgoingStateSent:
		delete(m.inFlight, ev.LocalID)
	case events.OutgoingStateFailed:
		draft, ok := m.inFlight[ev.LocalID]
		delete(m.inFlight, ev.LocalID)
		if !ok {
			return m, nil
		}
		m.restoreDraft(draft.text, draft.replyTo)
	}
	return m, nil
}

// restoreDraft writes failed text back into the composer if the user
// has not already typed something fresher; same conditional rule for
// the reply pointer. Silently clobbering an in-progress draft with a
// stale failure body is the kind of small data-loss UX bug that erodes
// trust quickly, so we err on the side of preserving the user's
// current input and only logging the dropped recovery.
func (m *Model) restoreDraft(text string, replyTo *domain.Message) {
	if m.textarea.Value() == "" {
		m.setValue(text)
	} else if m.log != nil {
		m.log.Warn("input: send failed but composer holds fresher draft, dropping failed text",
			"failed_len", len(text))
	}
	if m.replyTo == nil {
		m.replyTo = replyTo
	} else if m.log != nil && replyTo != nil && replyTo.ID != m.replyTo.ID {
		m.log.Warn("input: send failed but reply pointer has been retargeted",
			"failed_target_id", replyTo.ID, "current_target_id", m.replyTo.ID)
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
