package chats

import (
	tea "charm.land/bubbletea/v2"
)

// Rows this pane renders above the first chat row. Splitting the two makes it
// obvious which side owns which line when the layout changes:
//
//   - headerRows — the focus-aware "Chats" / "Chats (focused)" line from View.
//   - listChromeRows — bubbles' own title/filter line. It is drawn whenever
//     filtering is enabled, even with SetShowTitle(false) and no active filter
//     (list.View: `showTitle || (showFilter && filteringEnabled)`), which is why
//     the offset is 2 and not 1.
//
// TestItemIndexAt_MatchesRenderedRows ties both to the real rendering, so a
// bubbles upgrade that adds or drops a line fails a test instead of quietly
// selecting the wrong chat.
const (
	headerRows     = 1
	listChromeRows = 1
)

// ItemIndexAt maps a pane-local row (0 = the pane's own first rendered line) to
// an index in the currently visible item set, or -1 when the row carries no
// item — the header, the chrome line, or empty space below the last chat.
//
// The index is into VisibleItems, not the master slice: with a filter applied
// those differ, and the list's own cursor is in visible-space too.
func (m Model) ItemIndexAt(row int) int {
	if m.itemRows <= 0 {
		return -1
	}
	body := row - headerRows - listChromeRows
	if body < 0 {
		return -1
	}
	perPage := m.list.Paginator.PerPage
	if perPage <= 0 {
		return -1
	}
	// A click on the blank spacing row under an item counts as that item: it is
	// the row the user sees as part of it, and integer division already lands
	// there.
	onPage := body / m.itemRows
	if onPage >= perPage {
		return -1
	}
	idx := m.list.Paginator.Page*perPage + onPage
	if idx >= len(m.list.VisibleItems()) {
		return -1
	}
	return idx
}

// SelectAt moves the highlight to the item under a pane-local row and returns
// the same ChatSelectedMsg the Enter key produces, so a click and a keypress
// travel one code path. The bool reports whether a row was hit at all.
//
// A click on an already-selected chat still re-emits: re-opening the current
// chat is harmless (the thread reloads from cache) and swallowing it would make
// the first click after a focus change feel dead.
func (m Model) SelectAt(row int) (Model, tea.Cmd, bool) {
	idx := m.ItemIndexAt(row)
	if idx < 0 {
		return m, nil, false
	}
	m.list.Select(idx)
	updated, cmd := m.handleEnter()
	return updated, cmd, true
}

// ScrollBy moves the highlight by delta items (positive = down), clamped to the
// visible set. The wheel moves the selection rather than a separate viewport
// offset because the bubbles list has no independent scroll position: its page
// follows the cursor, so moving the cursor *is* scrolling.
func (m Model) ScrollBy(delta int) Model {
	count := len(m.list.VisibleItems())
	if count == 0 || delta == 0 {
		return m
	}
	idx := m.list.Index() + delta
	if idx < 0 {
		idx = 0
	}
	if idx >= count {
		idx = count - 1
	}
	m.list.Select(idx)
	return m
}

// CycleSelection moves the highlight by delta with wraparound and opens the
// chat it lands on, returning the same ChatSelectedMsg a click or Enter emits.
//
// Wraparound rather than clamping: this is the "next conversation" gesture, and
// stopping silently at the end of the list would read as a broken key. The
// order is the list's own — pinned first, then by last message, the same order
// Telegram shows — so "next" means what the user sees.
//
// The bool reports whether anything was selected; an empty list is a no-op.
func (m Model) CycleSelection(delta int) (Model, tea.Cmd, bool) {
	count := len(m.list.VisibleItems())
	if count == 0 || delta == 0 {
		return m, nil, false
	}
	idx := (m.list.Index() + delta) % count
	if idx < 0 {
		idx += count
	}
	m.list.Select(idx)
	updated, cmd := m.handleEnter()
	return updated, cmd, true
}

// SelectNth opens the nth chat in the list as it is currently shown,
// counting from one, and reports whether there was one.
//
// "As currently shown" is the whole contract: the folder tabs filter the
// list, so Alt+2 means the second row the user can see, not the second chat
// the account has. A number past the end does nothing rather than clamping to
// the last row — pressing Alt+9 in a three-chat folder and landing on the
// third would be a different chat every time the list changed length.
func (m Model) SelectNth(n int) (Model, tea.Cmd, bool) {
	if n < 1 {
		return m, nil, false
	}
	if n > len(m.list.VisibleItems()) {
		return m, nil, false
	}
	m.list.Select(n - 1)
	updated, cmd := m.handleEnter()
	return updated, cmd, true
}

// SelectByID moves the highlight onto a chat opened by some other road —
// the palette, a search jump — so the row the chords act on is the one
// the reader is looking at. Nothing is published: the chat is already
// open. A chat not in the visible list (another folder) leaves the
// highlight where it was.
func (m Model) SelectByID(id int64) Model {
	for i, it := range m.list.VisibleItems() {
		if ci, ok := it.(ChatItem); ok && ci.ID() == id {
			m.list.Select(i)
			return m
		}
	}
	return m
}
