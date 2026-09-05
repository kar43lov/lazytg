package thread

import (
	"reflect"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// Marking several messages at once.
//
// The cursor answers "which message", which is enough for reply, download
// and open — each acts on one. Copying and deleting are different: the unit
// a person means there is usually a run of messages, because that is how
// conversations arrive. Selecting them by dragging works only for what is on
// screen and only for text; marking works on the message, survives scrolling,
// and is what the delete path needs anyway since it sends a list of ids.
//
// Marks are ids for the same reason the cursor is: everything under them
// moves. A marked message deleted from another device leaves an id that no
// longer resolves, which Marked() drops rather than reporting as a phantom —
// see below.

// ToggleMark flips the mark on the message under the cursor and returns the
// model. It is a no-op when the thread is empty; there is nothing to mark.
func (m Model) ToggleMark() Model {
	msg, ok := m.CursorMessage()
	if !ok {
		return m
	}
	if m.marked == nil {
		m.marked = make(map[int64]bool)
	} else {
		// The map is shared with the value receiver's copy, so mutating it
		// in place would change the model the caller still holds. Copying
		// keeps the update where Bubble Tea expects it: in the returned
		// value.
		m.marked = cloneMarks(m.marked)
	}
	if m.marked[msg.ID] {
		delete(m.marked, msg.ID)
	} else {
		m.marked[msg.ID] = true
	}
	m.viewport.SetContent(m.renderAll())
	return m
}

// ClearMarks drops every mark. Bound to the same key that clears a text
// selection, because to the user they are one gesture: "never mind".
func (m Model) ClearMarks() Model {
	if len(m.marked) == 0 {
		return m
	}
	m.marked = nil
	m.viewport.SetContent(m.renderAll())
	return m
}

// MarkCount reports how many messages carry a mark, including any whose
// message has since left the loaded window. Used by the status line, where
// the honest number is the one the user has pressed space on.
func (m Model) MarkCount() int { return len(m.marked) }

// Marked returns the marked messages in the order they appear in the thread,
// dropping ids that no longer resolve to a loaded message.
//
// The dropping is deliberate and is why this returns messages rather than
// ids: a mark whose message was deleted from another device, or scrolled out
// of the window held in memory, cannot be copied or deleted meaningfully. A
// list of ids would let a caller act on messages nobody can see.
func (m Model) Marked() []domain.Message {
	if len(m.marked) == 0 {
		return nil
	}
	out := make([]domain.Message, 0, len(m.marked))
	for _, msg := range m.messages {
		if m.marked[msg.ID] {
			out = append(out, msg)
		}
	}
	return out
}

// Targets returns the messages an action should act on: the marked set when
// there is one, otherwise the message under the cursor.
//
// This is the rule the whole feature rests on, and it is what a file manager
// does — an operation with nothing selected acts on the item under the
// cursor. Without it every deletion and every copy would need the user to
// mark first, which is one keystroke too many for the common case of acting
// on exactly one message.
func (m Model) Targets() []domain.Message {
	if marked := m.Marked(); len(marked) > 0 {
		return marked
	}
	if msg, ok := m.CursorMessage(); ok {
		return []domain.Message{msg}
	}
	return nil
}

// IsMarked reports whether a message id carries a mark.
func (m Model) IsMarked(id int64) bool { return m.marked[id] }

func cloneMarks(in map[int64]bool) map[int64]bool {
	out := make(map[int64]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// MarkedText renders the marked messages the way the clipboard should
// receive them: author, then text, one blank line between messages. It is
// not the rendered block — the timestamp and the attachment badge are
// screen furniture, and pasting them into a bug report or a chat is noise.
func MarkedText(msgs []domain.Message, author func(domain.Message) string) string {
	var b []byte
	for i, msg := range msgs {
		if i > 0 {
			b = append(b, '\n', '\n')
		}
		if author != nil {
			if name := author(msg); name != "" {
				b = append(b, name...)
				b = append(b, ':', ' ')
			}
		}
		b = append(b, msg.Text...)
	}
	return string(b)
}

// AuthorLabel names who wrote a message, the way the thread draws it. Exposed
// so the app can build a multi-message clipboard payload that reads like the
// conversation it came from rather than a list of anonymous lines.
func (m Model) AuthorLabel(msg domain.Message) string {
	return resolveAuthor(msg, m.chatID, m.private, m.authorNames)
}

// CanRevokeDeletes reports whether "delete for everyone" is a choice in this
// chat.
//
// In a channel it is not: a deletion there is always for everyone, and
// offering the alternative would teach the user something false about their
// own account. Everywhere else Telegram accepts the flag; whether it honours
// it for somebody else's message is the server's call, not ours to predict.
func (m Model) CanRevokeDeletes() bool {
	return m.chatKind != domain.ChatTypeChannel
}

// ApplyEdit rewrites one message in place and redraws.
//
// In place, rather than reloading the chat: an edit changes a single message,
// and a reload would move the reader's position — the classic way a client
// makes you lose your place because somebody fixed a typo three screens up.
// SetButtons replaces the keyboard under one message; nil takes it away.
func (m Model) SetButtons(id int64, buttons [][]domain.Button) Model {
	for i := range m.messages {
		if m.messages[i].ID != id {
			continue
		}
		if reflect.DeepEqual(m.messages[i].Buttons, buttons) {
			return m
		}
		msgs := make([]domain.Message, len(m.messages))
		copy(msgs, m.messages)
		msgs[i].Buttons = buttons
		m.messages = msgs
		if m.buttonCursor >= len(flatButtons(buttons)) {
			m.buttonCursor = 0
		}
		m.viewport.SetContent(m.renderAll())
		return m
	}
	return m
}

func (m Model) ApplyEdit(id int64, text string, entities []domain.Entity, editDate time.Time) Model {
	for i := range m.messages {
		if m.messages[i].ID != id {
			continue
		}
		cur := m.messages[i]
		if cur.Text == text && reflect.DeepEqual(cur.Entities, entities) && cur.EditDate.Equal(editDate) {
			return m
		}
		msgs := make([]domain.Message, len(m.messages))
		copy(msgs, m.messages)
		msgs[i].Text = text
		msgs[i].Entities = entities
		msgs[i].EditDate = editDate
		m.messages = msgs
		m.viewport.SetContent(m.renderAll())
		return m
	}
	return m
}
