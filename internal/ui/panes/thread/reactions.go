package thread

import "github.com/kar43lov/lazytg/internal/core/domain"

// ApplyReactions replaces the reactions on one rendered message.
//
// Like ApplyEdit it redraws the one row rather than reloading the
// conversation: a reaction changes one message, and a reload would move the
// reader's position for no reason. A message not on screen is not an error —
// reactions arrive for the whole account, including chats and pages the pane
// is not showing.
func (m Model) ApplyReactions(id int64, rs []domain.Reaction) Model {
	for i := range m.messages {
		if m.messages[i].ID != id {
			continue
		}
		if sameReactions(m.messages[i].Reactions, rs) {
			return m
		}
		msgs := make([]domain.Message, len(m.messages))
		copy(msgs, m.messages)
		msgs[i].Reactions = append([]domain.Reaction(nil), rs...)
		m.messages = msgs
		m.viewport.SetContent(m.renderAll())
		return m
	}
	return m
}

// sameReactions reports whether two reaction lists would render identically.
//
// Worth the comparison because reaction updates repeat: the server sends the
// whole list every time anyone reacts anywhere in the chat, and re-rendering
// the viewport on a no-op change is a visible cost in a pane that holds a
// hundred messages.
func sameReactions(a, b []domain.Reaction) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ReactionsOf returns the reactions rendered against a message. Test helper.
func (m Model) ReactionsOf(id int64) []domain.Reaction {
	for _, msg := range m.messages {
		if msg.ID == id {
			return msg.Reactions
		}
	}
	return nil
}
