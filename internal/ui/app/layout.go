package app

// Rows reserved at the bottom of the frame, below the two panes.
//
// The composer is no longer a fixed three rows: it grows with what is
// being written, so its height comes from the pane itself and travels
// through computeLayout into the layout. inputRows survives only as the
// resting size, used where no pane is available to ask.
const (
	statusRows = 1
	inputRows  = 3
)

// layout is the geometry of the 2-pane frame for a given terminal size.
//
// It exists so that resizing and mouse hit-testing cannot disagree: before
// this, handleResize computed the pane sizes inline, and any click routing
// would have had to re-derive the same arithmetic from scratch — the classic
// way a click lands one column off after a layout tweak.
type layout struct {
	chatsW  int // width of the chats pane
	threadW int // width of the thread pane
	paneH   int // height shared by both panes
	sepX    int // column holding the vertical separator
	threadX int // first column of the thread pane
	inputY  int // first row of the composer
	inputH  int // rows the composer occupies right now
	statusY int // the status row
}

// Minimum width of either pane box, in columns.
//
// It is the panes' own floor plus the box padding, and the padding term is the
// point: the chats list refuses to go under 20 columns (bubbles renders nothing
// narrower), so a 20-column *box* leaves it 18 and the list clamps back up to
// 20 — two columns wider than the box, which lipgloss then wraps, growing the
// frame past the terminal height. TestFrameNeverOverflowsItsWidth drags the
// split to exactly this value for that reason.
const minPaneWidth = 20 + paneHPadding

// paneHPadding is the horizontal padding the pane box spends per side, doubled.
// Mirrored as an unexported constant in each pane, which subtracts it from the
// width it is handed.
const paneHPadding = 2

// clampChatsWidth keeps a dragged split inside what both panes can render. A
// terminal too narrow to honour both minimums falls back to the automatic
// split rather than to a pane of one column.
func clampChatsWidth(requested, total int) int {
	if requested <= 0 {
		return 0 // "auto" stays auto — the caller has no split of its own.
	}
	maxW := total - 1 - minPaneWidth
	if maxW < minPaneWidth {
		return 0
	}
	if requested < minPaneWidth {
		return minPaneWidth
	}
	if requested > maxW {
		return maxW
	}
	return requested
}

// computeLayout mirrors what renderBody draws: chats pane, one separator
// column, thread pane, then the composer and the status row.
//
// chatsOverride is the user's dragged split; 0 means the automatic 30%.
// composerRows is how many rows the composer wants right now; 0 or less
// falls back to the resting size.
func computeLayout(width, height, chatsOverride, composerRows int) layout {
	if composerRows <= 0 {
		composerRows = inputRows
	}
	chatsW := width * 30 / 100
	if chatsW < minPaneWidth {
		chatsW = minPaneWidth
	}
	if chatsOverride > 0 {
		if clamped := clampChatsWidth(chatsOverride, width); clamped > 0 {
			chatsW = clamped
		}
	}
	threadW := width - chatsW - 1 // -1 for the vertical separator column.
	if threadW < 1 {
		threadW = 1
	}
	paneH := height - statusRows - composerRows
	if paneH < 1 {
		paneH = 1
	}
	return layout{
		chatsW:  chatsW,
		threadW: threadW,
		paneH:   paneH,
		sepX:    chatsW,
		threadX: chatsW + 1,
		inputY:  paneH,
		inputH:  composerRows,
		statusY: paneH + composerRows,
	}
}

// inChats reports whether a screen cell belongs to the chats pane.
func (l layout) inChats(x, y int) bool {
	return y >= 0 && y < l.paneH && x >= 0 && x < l.chatsW
}

// inThread reports whether a screen cell belongs to the thread pane. The
// separator column belongs to neither pane, so a click on it is ignored rather
// than snapped to a side.
func (l layout) inThread(x, y int) bool {
	return y >= 0 && y < l.paneH && x >= l.threadX && x < l.threadX+l.threadW
}

// inInput reports whether a screen cell belongs to the composer.
func (l layout) inInput(_, y int) bool {
	return y >= l.inputY && y < l.inputY+l.inputH
}

// onSeparator reports whether a screen cell is the draggable split between the
// two panes.
func (l layout) onSeparator(x, y int) bool {
	return y >= 0 && y < l.paneH && x == l.sepX
}

// threadLocal converts screen coordinates into coordinates local to the thread
// pane's content: the pane box starts at threadX and spends one column on
// padding, so the body's first column is threadX+1.
func (l layout) threadLocal(x, y int) (int, int) {
	return x - l.threadX - 1, y
}
