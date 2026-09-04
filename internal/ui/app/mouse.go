package app

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// wheelStep is how many chat rows one wheel notch moves. The thread pane uses
// the viewport's own MouseWheelDelta (3 lines); a chat list moves by item, and
// one item per notch is what a list of a few hundred chats wants — three would
// skip past the chat you were aiming for.
const wheelStep = 1

// handleMouseClick routes a left click to the pane under the pointer: the chats
// pane selects the row, the thread and the composer take focus.
//
// Until this existed the TUI was in the worst of both worlds — View sets
// MouseMode = MouseModeCellMotion, so the terminal handed mouse events to the
// application and stopped doing its own text selection, while nothing here
// consumed them. Clicks did nothing *and* selecting text with the mouse was
// broken.
//
// Overlays (help, palette, search, attach) swallow the click: they are modal by
// contract and have no coordinate map of their own, so acting on a click behind
// them would move focus the user cannot see.
func (a App) handleMouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if msg.Button != tea.MouseLeft || a.tooSmall || a.overlayVisible() {
		return a, nil
	}
	// Stamp every press, not just the ones inside the thread: the second click
	// of a "double" has to be the immediate successor of the first, and a press
	// on a chat row in between must break the pair. Stamping only in the thread
	// branch made click-thread → click-chats → click-thread read as a double
	// click and copy a message the user never asked for.
	double := a.isDoubleClick(msg.X, msg.Y)
	a = a.stampClick(msg.X, msg.Y)

	l := a.layout()
	switch {
	case l.onSeparator(msg.X, msg.Y):
		// Grabbing the split. Focus stays where it was: resizing panes is not
		// choosing one.
		a.draggingSep = true
		return a, nil
	case l.inChats(msg.X, msg.Y):
		a = a.setFocus(FocusChats)
		a.thread = a.thread.ClearSelection()
		updated, cmd, hit := a.chats.SelectAt(msg.Y)
		a.chats = updated
		if !hit {
			// Empty space below the last chat: focus moved, nothing selected.
			return a, nil
		}
		return a, cmd
	case l.inThread(msg.X, msg.Y):
		a = a.setFocus(FocusThread)
		x, y := l.threadLocal(msg.X, msg.Y)
		if double {
			// Second click on the same spot: take the whole message, the way a
			// double click selects a word elsewhere. Copying happens straight
			// away — a double click is a complete gesture, there is no release
			// to wait for.
			updated, text, ok := a.thread.SelectMessageAt(x, y)
			a.thread = updated
			if !ok || text == "" {
				return a, nil
			}
			return a, tea.SetClipboard(text)
		}
		// A click on the attachment badge opens the attachment — the
		// gesture the badge advertises. It is restricted to that one
		// line so the rest of a message with a photo in it can still be
		// pointed at, selected and dragged like any other.
		if target, ok := a.thread.MediaClickAt(x, y); ok {
			a.thread = a.thread.SetCursor(target.ID)
			if cmd, ok := a.cmdOpenCursorMedia(); ok {
				return a, cmd
			}
			return a, nil
		}
		// A plain click puts the cursor on the message under the
		// pointer, so the mouse and the arrow keys address the same
		// thing: press "o" after clicking and you open what you clicked.
		a.thread, _ = a.thread.CursorAt(x, y)
		updated, started := a.thread.StartSelection(x, y)
		a.thread = updated
		// The pane's own title row starts nothing: focus moved there, and that
		// is all a press on chrome should do.
		a.draggingText = started
		return a, nil
	case l.inInput(msg.X, msg.Y):
		a.thread = a.thread.ClearSelection()
		return a.setFocus(FocusInput), nil
	}
	// The status row belongs to no pane.
	return a, nil
}

// isDoubleClick reports whether this press continues the previous one: same
// cell, inside the double-click window. Bubble Tea reports presses individually,
// so the gesture has to be assembled here.
func (a App) isDoubleClick(x, y int) bool {
	if a.lastClickAt.IsZero() || a.lastClickCell != [2]int{x, y} {
		return false
	}
	return time.Since(a.lastClickAt) <= doubleClickWindow
}

// stampClick records a press for the next double-click test.
func (a App) stampClick(x, y int) App {
	a.lastClickAt = time.Now()
	a.lastClickCell = [2]int{x, y}
	return a
}

// handleMouseMotion carries a drag in progress. MouseModeCellMotion reports
// motion only while a button is held, so every motion that arrives here is part
// of a drag rather than a hover.
func (a App) handleMouseMotion(msg tea.MouseMotionMsg) (tea.Model, tea.Cmd) {
	switch {
	case a.draggingSep:
		return a.dragSeparatorTo(msg.X), nil
	case a.draggingText:
		// Coordinates are not clamped to the pane: a drag that runs past the
		// bottom edge should keep extending, which is what the pane's own
		// clamping to the content does for us.
		l := a.layout()
		x, y := l.threadLocal(msg.X, msg.Y)
		a.thread = a.thread.ExtendSelection(x, y)
		return a, nil
	}
	return a, nil
}

// handleMouseRelease ends a drag. The final position is taken from the release
// event too: a fast drag can end with the pointer past the last motion report.
func (a App) handleMouseRelease(msg tea.MouseReleaseMsg) (tea.Model, tea.Cmd) {
	switch {
	case a.draggingSep:
		a = a.dragSeparatorTo(msg.X)
		a.draggingSep = false
		return a, nil
	case a.draggingText:
		l := a.layout()
		x, y := l.threadLocal(msg.X, msg.Y)
		a.thread = a.thread.ExtendSelection(x, y)
		a.draggingText = false
		updated, text := a.thread.FinishSelection()
		a.thread = updated
		if text == "" {
			// A click with no drag: nothing to copy, and the empty highlight
			// would sit there looking like a stuck selection.
			a.thread = a.thread.ClearSelection()
			return a, nil
		}
		// A drag that actually selected something also ends the double-click
		// sequence: pressing again on the same cell within the window is the
		// user starting a second drag, not completing a double click, and
		// treating it as one would replace their selection with the whole
		// message. Desktop toolkits break the pair on movement for the same
		// reason.
		a.lastClickAt = time.Time{}
		// OSC 52 rather than a pbcopy/xclip shell-out: it is the terminal's own
		// clipboard channel, so it works over ssh and inside tmux (with
		// set-clipboard on) without lazytg knowing anything about the host.
		return a, tea.SetClipboard(text)
	}
	return a, nil
}

// doubleClickWindow is how close two presses must be to count as one gesture.
// 400ms is the usual desktop default and comfortably above a deliberate
// double tap without catching two separate clicks on the same word.
const doubleClickWindow = 400 * time.Millisecond

// dragSeparatorTo moves the split to column x and re-sizes the panes. The width
// is clamped so neither pane can be dragged into unusability — a chats pane of
// three columns is not a preference, it is a broken layout the user then has to
// undo blind.
func (a App) dragSeparatorTo(x int) App {
	if a.tooSmall {
		return a
	}
	width := clampChatsWidth(x, a.width)
	if width == 0 || width == a.chatsWidth {
		return a
	}
	a.chatsWidth = width
	return a.applySizes()
}

// layout is the geometry of the current frame, including the user's split.
func (a App) layout() layout {
	return computeLayout(a.width, a.height, a.chatsWidth)
}

// handleMouseWheel scrolls whatever the pointer is over, without moving focus.
// Scrolling to read is not the same act as choosing where to type, and a wheel
// that stole focus would silently redirect the next keystroke.
func (a App) handleMouseWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	if a.tooSmall || a.overlayVisible() {
		return a, nil
	}
	step, ok := wheelDirection(msg.Button)
	if !ok {
		return a, nil
	}
	l := a.layout()
	switch {
	case l.inChats(msg.X, msg.Y):
		a.chats = a.chats.ScrollBy(step)
		return a, nil
	case l.inThread(msg.X, msg.Y):
		// The thread owns its own wheel handling: the viewport scrolls and the
		// pane decides whether the scroll crossed into unloaded history.
		updated, cmd := a.thread.Update(msg)
		a.thread = updated
		return a, cmd
	}
	return a, nil
}

// wheelDirection converts a wheel button into a signed step, reporting false for
// horizontal wheels — neither pane scrolls sideways, and treating a sideways
// nudge as vertical movement makes trackpads feel broken.
func wheelDirection(b tea.MouseButton) (int, bool) {
	switch b {
	case tea.MouseWheelDown:
		return wheelStep, true
	case tea.MouseWheelUp:
		return -wheelStep, true
	default:
		return 0, false
	}
}

// overlayVisible reports whether any modal overlay is on screen.
func (a App) overlayVisible() bool {
	return a.help.Visible || a.attach.Visible || a.palette.Visible || a.search.Visible
}
