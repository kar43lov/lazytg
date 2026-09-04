package app

import (
	"strings"

	"charm.land/lipgloss/v2"

	tea "charm.land/bubbletea/v2"
)

// View composes the full TUI frame. The result is wrapped in tea.NewView so
// the renderer treats the whole thing as a single styled string — sub-pane
// styles are baked in via lipgloss before composition.
//
// The terminal-too-small fallback short-circuits the layout entirely:
// rendering the 2-pane layout into <80x24 produces a confusing tangle of
// truncated borders, so we just print a centred message and ask the user to
// resize.
//
// Bubble Tea v2 prefers declarative view fields over imperative program
// options, so AltScreen + MouseMode are set on the returned tea.View
// rather than passed as tea.WithAltScreen / tea.WithMouseCellMotion at
// program construction time. The fields are applied every frame, which
// is fine — bubbletea diffs the request and only emits the relevant
// terminal escape sequence when the value actually changes.
func (a App) View() tea.View {
	if a.tooSmall {
		view := tea.NewView(a.renderTooSmall())
		view.AltScreen = true
		return view
	}

	body := a.renderBody()
	// The confirmation is drawn over the layout rather than replacing it,
	// unlike every overlay below: the user is being asked about messages
	// they can see, and a modal that hides them asks the question without
	// the context needed to answer it.
	if a.confirm.Visible() {
		body = a.overlayConfirm(body)
	}
	// The emoji picker is drawn over the layout for the same reason: the
	// user is picking something to put in a message they are in the middle
	// of writing, and hiding the draft to choose a smiley is absurd.
	if a.emojiPicker.Visible() {
		body = a.overlayBox(body, a.emojiPicker.View())
	}
	switch {
	case a.help.Visible:
		body = a.help.View(a.width, maxInt(a.height-1, 1))
	case a.attach.Visible:
		body = a.attach.View(a.width, maxInt(a.height-1, 1))
	case a.palette.Visible:
		body = a.palette.View(a.width, maxInt(a.height-1, 1))
	case a.search.Visible:
		body = a.search.View(a.width, maxInt(a.height-1, 1))
	}
	view := tea.NewView(body)
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

// renderTooSmall draws a centred "Terminal too small (min 80x24)" notice on
// whatever real estate is available.
func (a App) renderTooSmall() string {
	msg := "Terminal too small (min 80x24)"
	if a.width <= 0 || a.height <= 0 {
		return msg
	}
	return lipgloss.Place(a.width, a.height, lipgloss.Center, lipgloss.Center, msg)
}

// renderBody composes the full layout: 2 panes joined horizontally with a
// vertical separator on top of the input and status rows.
func (a App) renderBody() string {
	chatsView := paneStyle(a.focus == FocusChats).
		Width(a.chats.Width).
		Height(a.chats.Height).
		Render(a.chats.View())
	threadView := paneStyle(a.focus == FocusThread).
		Width(a.thread.Width).
		Height(a.thread.Height).
		Render(a.thread.View())
	separator := verticalSeparator(a.chats.Height, a.draggingSep)

	upper := lipgloss.JoinHorizontal(lipgloss.Top, chatsView, separator, threadView)

	inputView := inputStyle(a.focus == FocusInput).
		Width(a.width).
		Render(a.input.View())
	statusView := a.status.View(a.width)

	return lipgloss.JoinVertical(lipgloss.Left, upper, inputView, statusView)
}

// paneStyle returns the per-pane border style. Focused panes get a high-
// intensity foreground on the border so the active edge is visible without
// changing layout dimensions.
func paneStyle(focused bool) lipgloss.Style {
	style := lipgloss.NewStyle().Padding(0, 1)
	if focused {
		style = style.Foreground(lipgloss.Color("12")) // ANSI bright blue
	}
	return style
}

// inputStyle is the border around the bottom composer. It is intentionally
// simple in Stage 2 Task 6 — Task 9 will replace this with a richer, multi-
// line composer.
func inputStyle(focused bool) lipgloss.Style {
	style := lipgloss.NewStyle().Padding(0, 1)
	if focused {
		style = style.Foreground(lipgloss.Color("12"))
	}
	return style
}

// verticalSeparator returns a column of "│" matching pane height. While the
// user is dragging it, the column is highlighted: a one-column-wide grab handle
// gives no other feedback that the pointer actually caught it.
func verticalSeparator(height int, dragging bool) string {
	if height <= 0 {
		height = 1
	}
	col := strings.Repeat("│\n", height-1) + "│"
	if dragging {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Render(col) // ANSI bright blue
	}
	return col
}

// maxInt is a tiny inline helper; Go 1.21+ has builtins.max but the local
// name keeps the call site obvious and avoids the revive
// redefines-builtin-id warning.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// overlayConfirm centres the confirmation box on top of the rendered layout.
func (a App) overlayConfirm(body string) string {
	return a.overlayBox(body, a.confirm.View())
}

// overlayBox centres one rendered box on top of the layout, leaving the
// layout visible around it.
func (a App) overlayBox(body, box string) string {
	if box == "" || a.width <= 0 || a.height <= 0 {
		return body
	}
	return lipgloss.Place(a.width, maxInt(a.height-1, 1),
		lipgloss.Center, lipgloss.Center, box,
		lipgloss.WithWhitespaceChars(" "))
}
