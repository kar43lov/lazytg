package thread

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// selStyle paints the selected range. Reverse video rather than a colour pair:
// it is the one highlight every terminal renders the same way, and it stays
// legible whatever palette the user's theme uses.
var selStyle = lipgloss.NewStyle().Reverse(true)

// cell is a position inside the rendered thread: a line index into the joined
// content (not the visible window) and a column in display cells.
type cell struct {
	line int
	col  int
}

// before reports whether c sorts before other in reading order.
func (c cell) before(other cell) bool {
	if c.line != other.line {
		return c.line < other.line
	}
	return c.col < other.col
}

// selection is an in-progress or finished drag over the thread.
type selection struct {
	anchor cell // where the button went down
	cursor cell // where the pointer is now
}

// blockSpan is the line range one message occupies in the rendered content,
// end exclusive.
//
// id and mediaLine are what let a pointer position mean something other than
// "these characters": the message the pointer is over, and — when that message
// has an attachment — the one line where its badge is drawn, so a click on the
// badge can open the attachment while a click anywhere else in the same
// message does not. Both are produced by the renderer for the same reason the
// line range is: a second implementation of the layout would drift.
type blockSpan struct {
	start int
	end   int
	// id is the message id, zero for an optimistic (not yet sent) row.
	id int64
	// mediaLine is the absolute line carrying the media badge, or -1.
	mediaLine int
}

// contains reports whether line falls inside the span.
func (s blockSpan) contains(line int) bool { return line >= s.start && line < s.end }

// resolveSelection turns a raw drag into the range that gets highlighted and
// copied, widening it to whole messages when it crosses a message boundary.
//
// The widening is the behaviour Telegram Desktop has and the reason this is not
// a plain character range: the moment a drag crosses from one message into
// another, selecting "half of the first and a third of the second" is never
// what the user meant — they are collecting messages, not characters. Inside a
// single message the drag stays character-exact, which is what you want when
// copying one link or one code snippet out of a longer paragraph.
func resolveSelection(sel selection, spans []blockSpan, lines []string) (start, end cell) {
	start, end = sel.anchor, sel.cursor
	if end.before(start) {
		start, end = end, start
	}
	start = clampCell(start, lines)
	end = clampCell(end, lines)

	from := spanIndexAt(spans, start.line)
	to := spanIndexAt(spans, end.line)
	if from < 0 || to < 0 || from == to {
		return start, end
	}

	// Different messages: take both of them whole, plus everything between.
	start = cell{line: spans[from].start, col: 0}
	lastLine := spans[to].end - 1
	if lastLine >= len(lines) {
		lastLine = len(lines) - 1
	}
	end = cell{line: lastLine, col: lineWidth(lines, lastLine)}
	return start, end
}

// clampCell keeps a position inside the rendered content. A drag can leave the
// pane in any direction, and the pointer keeps reporting positions past the
// last line or past the end of a short line.
func clampCell(c cell, lines []string) cell {
	if len(lines) == 0 {
		return cell{}
	}
	if c.line < 0 {
		c.line = 0
	}
	if c.line >= len(lines) {
		c.line = len(lines) - 1
	}
	if c.col < 0 {
		c.col = 0
	}
	if w := lineWidth(lines, c.line); c.col > w {
		c.col = w
	}
	return c
}

func lineWidth(lines []string, i int) int {
	if i < 0 || i >= len(lines) {
		return 0
	}
	return ansi.StringWidth(lines[i])
}

// spanIndexAt returns the index of the message occupying line, or -1 for the
// blank rows between messages.
func spanIndexAt(spans []blockSpan, line int) int {
	for i, s := range spans {
		if s.contains(line) {
			return i
		}
	}
	return -1
}

// applySelection re-renders content with the resolved range in reverse video.
//
// Slicing is ANSI-aware (ansi.Cut counts display cells, not bytes), so the
// styling of the surrounding text survives; the selected slice itself is
// stripped before being re-styled, because a reset sequence inside it would
// otherwise cancel the highlight halfway through.
func applySelection(content string, spans []blockSpan, sel selection) string {
	lines := strings.Split(content, "\n")
	start, end := resolveSelection(sel, spans, lines)
	if start == end {
		return content
	}
	for i := start.line; i <= end.line && i < len(lines); i++ {
		from := 0
		if i == start.line {
			from = start.col
		}
		to := lineWidth(lines, i)
		if i == end.line && end.col < to {
			to = end.col
		}
		if from >= to {
			// An empty line inside the range, or a zero-width slice: leave the
			// line alone rather than emitting a highlight of nothing.
			continue
		}
		lines[i] = ansi.Cut(lines[i], 0, from) +
			selStyle.Render(ansi.Strip(ansi.Cut(lines[i], from, to))) +
			ansi.Cut(lines[i], to, lineWidth(lines, i))
	}
	return strings.Join(lines, "\n")
}

// selectionText returns the plain text of the resolved range, ready for the
// clipboard: no escape sequences, and the newlines the user saw.
func selectionText(content string, spans []blockSpan, sel selection) string {
	lines := strings.Split(content, "\n")
	start, end := resolveSelection(sel, spans, lines)
	if start == end {
		return ""
	}
	var out []string
	for i := start.line; i <= end.line && i < len(lines); i++ {
		from := 0
		if i == start.line {
			from = start.col
		}
		to := lineWidth(lines, i)
		if i == end.line && end.col < to {
			to = end.col
		}
		if from >= to {
			out = append(out, "")
			continue
		}
		out = append(out, strings.TrimRight(ansi.Strip(ansi.Cut(lines[i], from, to)), " "))
	}
	return strings.Join(out, "\n")
}

// threadHeaderRows is how many rows View draws above the viewport body. The
// pane renders its own "Thread" header (see view.go), so a pointer at pane row
// N is looking at viewport row N-1.
const threadHeaderRows = 1

// cellAt converts a pane-local pointer position into a position in the rendered
// content, accounting for the header row and the current scroll offset.
func (m Model) cellAt(x, y int) cell {
	return cell{line: m.viewport.YOffset() + y - threadHeaderRows, col: x}
}

// contentRow reports whether a pane row carries body content at all. The pane's
// own title row does not: mapping it through cellAt yields the line above the
// viewport, which clamps onto the first message, so pressing the word "Thread"
// used to start a selection there and double-clicking it copied a message the
// pointer was nowhere near.
func (m Model) contentRow(y int) bool { return y >= threadHeaderRows }

// HasSelection reports whether anything is currently highlighted.
func (m Model) HasSelection() bool { return m.sel != nil }

// renderedThread is a snapshot of the body and its per-message line ranges.
type renderedThread struct {
	content string
	spans   []blockSpan
}

// StartSelection begins a drag at a pane-local position, discarding whatever
// was selected before, and snapshots the rendered body for the duration of the
// drag (see Model.dragCache for why).
func (m Model) StartSelection(x, y int) (Model, bool) {
	if !m.contentRow(y) {
		return m, false
	}
	m.dragCache = nil // the previous drag's snapshot must not survive into this one
	content, spans := m.renderContent()
	m.dragCache = &renderedThread{content: content, spans: spans}
	at := m.cellAt(x, y)
	m.sel = &selection{anchor: at, cursor: at}
	m.viewport.SetContent(m.renderAll())
	return m, true
}

// FinishSelection ends a drag: it returns the selected text and drops the
// snapshot, so anything that arrived while the button was down renders on the
// next frame. The highlight stays until something clears it.
func (m Model) FinishSelection() (Model, string) {
	text := m.SelectionText()
	m.dragCache = nil
	m.viewport.SetContent(m.renderAll())
	return m, text
}

// ExtendSelection moves the loose end of an in-progress drag. A drag that
// started outside the thread never reaches here, so a nil selection is a
// no-op rather than an implicit start — otherwise a drag begun in the chats
// pane would start selecting text the moment it crossed the separator.
func (m Model) ExtendSelection(x, y int) Model {
	if m.sel == nil {
		return m
	}
	next := m.cellAt(x, y)
	if next == m.sel.cursor {
		return m
	}
	sel := *m.sel
	sel.cursor = next
	m.sel = &sel
	m.viewport.SetContent(m.renderAll())
	return m
}

// SelectMessageAt highlights the whole message under a pane-local position and
// returns its text — the double-click gesture. Reports false when the position
// is between messages or past the end of the thread.
func (m Model) SelectMessageAt(x, y int) (Model, string, bool) {
	if !m.contentRow(y) {
		return m, "", false
	}
	content, spans := m.renderContent()
	lines := strings.Split(content, "\n")
	at := clampCell(m.cellAt(x, y), lines)
	idx := spanIndexAt(spans, at.line)
	if idx < 0 {
		return m, "", false
	}
	span := spans[idx]
	last := span.end - 1
	sel := selection{
		anchor: cell{line: span.start, col: 0},
		cursor: cell{line: last, col: lineWidth(lines, last)},
	}
	m.sel = &sel
	m.viewport.SetContent(m.renderAll())
	return m, selectionText(content, spans, sel), true
}

// SelectionText returns the plain text currently highlighted, empty when there
// is no selection. Called on button release to fill the clipboard.
func (m Model) SelectionText() string {
	if m.sel == nil {
		return ""
	}
	content, spans := m.renderContent()
	return selectionText(content, spans, *m.sel)
}

// ClearSelection drops the highlight.
func (m Model) ClearSelection() Model {
	if m.sel == nil && m.dragCache == nil {
		return m
	}
	m.sel = nil
	m.dragCache = nil
	m.viewport.SetContent(m.renderAll())
	return m
}

// dropSelection forgets whatever was highlighted, and where the cursor was.
// Every caller is a place that replaces the body under both — opening another
// chat, switching to one, loading a jump window, reloading after a failed
// jump. A selection is a range of line/column cells with no tie to the
// messages it covered, so carrying it across a content swap lights up
// whichever lines happen to fall inside the range in the new conversation.
// Found by review, not by use: the mouse paths cleared it by hand and the
// keyboard paths did not.
//
// The cursor goes for a related but distinct reason: a message id is unique
// per chat, not across chats — a channel numbers its own messages from one —
// so an id carried into another conversation can match a completely different
// message rather than simply missing. Resetting to zero puts it back on the
// newest message, which is where a freshly opened chat should start.
func (m Model) dropSelection() Model {
	m.sel = nil
	m.dragCache = nil
	m.cursorID = 0
	m.marked = nil
	// Drawn images go for a third reason, and the sharpest one: a picture
	// is not part of the text grid. The terminal placed it at a position,
	// and a body that now describes a different conversation would have
	// last chat's photos sitting on top of this one's messages.
	m.inline = nil
	return m
}
