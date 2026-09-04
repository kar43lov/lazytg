package input

import "github.com/kar43lov/lazytg/internal/core/domain"

// A half-written message belongs to the conversation it was written in.
//
// Before this the composer simply kept whatever was in it across a chat
// switch, which is not a missing feature but a defect with teeth: type half a
// sentence to one person, switch chats to check something, press Enter, and
// it goes to somebody else. The alternative — clearing the box on every
// switch — trades a wrong recipient for a lost message, which is better and
// still bad.
//
// So the draft moves with the chat, the way it does in every Telegram client.
// A reply pointer goes with it, because "replying to X" is part of what was
// being written and restoring the words without it would arm the wrong reply.
//
// Kept in memory for the session rather than saved to the server. Telegram
// has messages.saveDraft and syncs drafts between devices; using it means a
// request every time somebody stops typing, on an account already watched for
// running an unofficial client. Losing a draft when the program exits is a
// smaller cost than that, and the decision is easy to revisit — this is the
// one place that would change.

// draft is what was in the composer for one chat.
type draft struct {
	text    string
	replyTo *domain.Message
}

// switchChat stashes the current draft under the old chat and restores the
// new chat's, if it has one.
func (m *Model) switchChat(chatID int64) {
	if m.chatID == chatID {
		// Re-binding to the same chat happens on reconnects and on a
		// same-chat search jump. Stashing and restoring would be a no-op
		// with one exception — an in-flight edit — so the whole thing is
		// skipped rather than made subtle.
		return
	}
	// An edit is not a draft. Leaving the mode armed across a switch would
	// point Enter at a message in a conversation no longer on screen, so
	// the mode is cancelled here, which also puts back whatever draft it
	// displaced — and that is what gets stashed.
	if m.editing != nil {
		m.cancelEdit()
	}
	m.stashDraft()
	m.chatID = chatID
	m.restoreDraftFor(chatID)
}

// stashDraft records what is in the composer against the chat it was written
// for. An empty box stores nothing and clears any earlier draft: the user
// deleting what they wrote is them saying they no longer want it.
func (m *Model) stashDraft() {
	if m.chatID == 0 {
		return
	}
	text := m.textarea.Value()
	if text == "" && m.replyTo == nil {
		delete(m.drafts, m.chatID)
		return
	}
	if m.drafts == nil {
		m.drafts = make(map[int64]draft)
	}
	m.drafts[m.chatID] = draft{text: text, replyTo: m.replyTo}
}

// restoreDraftFor puts a chat's draft back in the composer, or empties it.
func (m *Model) restoreDraftFor(chatID int64) {
	d, ok := m.drafts[chatID]
	if !ok {
		m.textarea.Reset()
		m.replyTo = nil
		return
	}
	m.textarea.SetValue(d.text)
	m.textarea.MoveToEnd()
	m.replyTo = d.replyTo
}

// DraftFor returns the stashed draft text for a chat. Test helper, and the
// hook a "Draft: …" preview in the chat list would use.
func (m Model) DraftFor(chatID int64) string {
	return m.drafts[chatID].text
}
