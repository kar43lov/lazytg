package thread

import (
	"strings"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// The per-message cursor.
//
// Until this existed the thread had no notion of "the message the user
// means". Reply armed the newest message and download took the newest
// attachment, both documented as interim UX — which works exactly until
// the user wants to answer the message above, or save the photo from
// this morning. It also made an attachment unreachable in the ordinary
// case: a conversation where somebody sends two photos in a row can only
// ever download the second one.
//
// The cursor is held as a message id, not an index. Everything about
// this pane's content moves underneath it: live messages arrive, older
// pages load above, deletions from another device remove rows. An index
// would keep pointing at the same position while the message under it
// changed — which is the same defect the chat list had, where the
// highlight stayed on row 0 and came to mean a different conversation.
//
// Zero means "not placed", which resolves to the newest message. That
// keeps the untouched pane behaving as it did: a user who never moves
// the cursor sees the same thing they saw before it existed.

// Cursor returns the message id the cursor points at, or 0 when it has
// not been placed. Exposed for tests and for the app layer's status line.
func (m Model) Cursor() int64 { return m.cursorID }

// CursorMessage returns the message the cursor points at. When the
// cursor is unplaced, or points at a message that is no longer loaded
// (deleted elsewhere, or scrolled out of the window that is in memory),
// it resolves to the newest message in the thread.
func (m Model) CursorMessage() (domain.Message, bool) {
	if len(m.messages) == 0 {
		return domain.Message{}, false
	}
	if idx := m.cursorIndex(); idx >= 0 {
		return m.messages[idx], true
	}
	return m.messages[len(m.messages)-1], true
}

// cursorIndex locates the cursor in m.messages, or -1 when it is not
// placed or no longer there.
func (m Model) cursorIndex() int {
	if m.cursorID == 0 {
		return -1
	}
	for i, msg := range m.messages {
		if msg.ID == m.cursorID {
			return i
		}
	}
	return -1
}

// MoveCursor steps the cursor by delta messages — negative towards older
// history, positive towards the newest — and scrolls just enough to keep
// it on screen. It clamps at both ends rather than wrapping: a cursor
// that jumps from the oldest loaded message to the newest looks like a
// scroll glitch, not like navigation.
//
// From an unplaced cursor the first step lands on the newest message and
// then moves from there, so pressing "up" once in a fresh thread selects
// the last message rather than jumping to the top of the loaded window.
func (m Model) MoveCursor(delta int) Model {
	if len(m.messages) == 0 || delta == 0 {
		return m
	}
	idx := m.cursorIndex()
	if idx < 0 {
		idx = len(m.messages) - 1
		if delta > 0 {
			// Already at the newest: nothing above to move to.
			m.cursorID = m.messages[idx].ID
			return m.applyCursor()
		}
		delta++ // the first step is the placement itself
	}
	idx += delta
	if idx < 0 {
		idx = 0
	}
	if idx >= len(m.messages) {
		idx = len(m.messages) - 1
	}
	m.cursorID = m.messages[idx].ID
	return m.applyCursor()
}

// SetCursor places the cursor on a specific message id. An id that is
// not loaded is ignored, so a stale click or a jump to a message that
// has since been deleted cannot strand the cursor somewhere invisible.
func (m Model) SetCursor(id int64) Model {
	if id == 0 {
		return m
	}
	for _, msg := range m.messages {
		if msg.ID == id {
			m.cursorID = id
			return m.applyCursor()
		}
	}
	return m
}

// CursorAt places the cursor on the message under a pane-local pointer
// position and reports whether it landed on one. This is the single
// left click: it selects a message the way an arrow key would, so the
// mouse and the keyboard address the same thing.
func (m Model) CursorAt(x, y int) (Model, bool) {
	span, ok := m.spanAt(x, y)
	if !ok || span.id == 0 {
		return m, false
	}
	m.cursorID = span.id
	return m.applyCursor(), true
}

// MediaClickAt reports whether a pane-local pointer position is on a
// message's media badge — the line that says what the attachment is.
//
// Restricting the gesture to that one line is deliberate. A click
// anywhere in a message moves the cursor, and a click that also opened
// an attachment would mean the user cannot put the cursor on a message
// with a photo without launching a viewer.
func (m Model) MediaClickAt(x, y int) (domain.Message, bool) {
	span, ok := m.spanAt(x, y)
	if !ok || span.id == 0 || span.mediaLine < 0 {
		return domain.Message{}, false
	}
	if m.cellAt(x, y).line != span.mediaLine {
		return domain.Message{}, false
	}
	for _, msg := range m.messages {
		if msg.ID == span.id && msg.Media != nil {
			return msg, true
		}
	}
	return domain.Message{}, false
}

// spanAt resolves a pane-local pointer position to the rendered block
// under it.
func (m Model) spanAt(x, y int) (blockSpan, bool) {
	if !m.contentRow(y) || len(m.messages) == 0 {
		return blockSpan{}, false
	}
	content, spans := m.renderContent()
	lines := strings.Split(content, "\n")
	at := clampCell(m.cellAt(x, y), lines)
	idx := spanIndexAt(spans, at.line)
	if idx < 0 {
		return blockSpan{}, false
	}
	return spans[idx], true
}

// MediaTarget returns the message whose attachment a media action should
// act on: the one under the cursor when it has an attachment, and
// otherwise the nearest one above it.
//
// Scanning upwards rather than requiring a hit keeps the chord working
// the way it did before the cursor existed — an untouched cursor sits at
// the newest message, so the search finds the newest attachment, which
// is exactly what Ctrl-D used to do. Once the user moves the cursor the
// same rule reads naturally: the attachment at or above where I am.
func (m Model) MediaTarget() (domain.Message, bool) {
	start := m.cursorIndex()
	if start < 0 {
		start = len(m.messages) - 1
	}
	for i := start; i >= 0; i-- {
		if m.messages[i].Media != nil {
			return m.messages[i], true
		}
	}
	return domain.Message{}, false
}

// applyCursor re-renders with the marker in its new place and scrolls
// the minimum needed to bring the cursor into view.
//
// The minimum matters: ScrollTo centres its target, which is right for a
// search jump landing somewhere unseen and wrong for stepping one
// message, where re-centring on every press makes the whole thread slide
// under a stationary cursor.
func (m Model) applyCursor() Model {
	content, spans := m.renderContent()
	m.viewport.SetContent(applyCursorSelection(m, content, spans))

	idx := -1
	for i, span := range spans {
		if span.id != 0 && span.id == m.cursorID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return m
	}
	span := spans[idx]
	top := m.viewport.YOffset()
	height := m.viewport.Height()
	if height <= 0 {
		return m
	}
	switch {
	case span.start < top:
		m.viewport.SetYOffset(span.start)
	case span.end > top+height:
		offset := span.end - height
		if offset < span.start {
			// A message taller than the viewport: show its head rather
			// than its tail, because the head carries the author and the
			// badge — the parts the cursor exists to point at.
			offset = span.start
		}
		m.viewport.SetYOffset(offset)
	}
	return m
}

// applyCursorSelection re-applies the text selection on top of freshly
// rendered content, mirroring renderAll. Split out so applyCursor does
// not have to re-render a second time to get the same string.
func applyCursorSelection(m Model, content string, spans []blockSpan) string {
	if m.sel == nil {
		return content
	}
	return applySelection(content, spans, *m.sel)
}

// Has reports whether a message id is currently rendered.
//
// Callers use it to tell "the cursor did not move" from "the message is not
// on this page", which are two different answers and need two different
// responses: one is a no-op, the other is a reason to go and load it.
func (m Model) Has(id int64) bool {
	for _, msg := range m.messages {
		if msg.ID == id {
			return true
		}
	}
	return false
}

// FocusMessage puts the cursor on a message and scrolls it into view,
// reporting whether the message was there to focus.
func (m Model) FocusMessage(id int64) (Model, bool) {
	if !m.Has(id) {
		return m, false
	}
	return m.SetCursor(id), true
}
