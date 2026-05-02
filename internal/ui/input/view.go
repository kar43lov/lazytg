package input

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// View composes the input pane: an optional reply hint at the top, the
// textarea body, and an optional empty-state hint at the bottom. Total
// height matches the 3-row inputH the app reserves in app/update.go;
// when the reply hint is hidden the slot is preserved as a blank line
// so the layout never shifts under the cursor.
func (m Model) View() string {
	width := m.Width
	if width <= 0 {
		width = 80
	}

	rows := make([]string, 0, 3)
	rows = append(rows, m.replyRow(width))
	rows = append(rows, m.textarea.View())
	rows = append(rows, m.hintRow(width))
	return strings.Join(rows, "\n")
}

// replyRow renders the reply hint when m.replyTo is set. Empty padding
// keeps the visible row count stable so a Send → reply-arm round trip
// does not jiggle the textarea up and down. Format mirrors the
// "↳ replying to: …" style used in the thread pane optimistic render.
func (m Model) replyRow(width int) string {
	if m.replyTo == nil {
		return strings.Repeat(" ", clampWidth(width))
	}
	preview := m.replyTo.Text
	const previewLimit = 50
	if len(preview) > previewLimit {
		preview = preview[:previewLimit] + "…"
	}
	prefix := "↳ Reply to: "
	body := prefix + preview
	style := lipgloss.NewStyle().
		Foreground(lipgloss.Color("8")). // ANSI bright black (grey)
		Italic(true)
	return style.Render(truncateRow(body, width))
}

// hintRow renders the empty-state hint when the textarea is empty.
// When the user has typed something the row is left blank so the body
// gets the visual real estate.
func (m Model) hintRow(width int) string {
	if m.textarea.Value() != "" {
		return strings.Repeat(" ", clampWidth(width))
	}
	style := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	return style.Render(truncateRow(emptyHint, width))
}

// clampWidth returns w bounded to >=0 so strings.Repeat does not panic
// on an unsized model. New() and the early app construction can both
// produce a Model with Width=0 before the first WindowSizeMsg lands.
func clampWidth(w int) int {
	if w < 0 {
		return 0
	}
	return w
}

// truncateRow shortens s to width with a single-character ellipsis if
// it would overflow. Plain byte length is fine here because the hints
// and previews we emit are ASCII; reply previews go through the same
// truncation in a predictable way.
func truncateRow(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if len(s) <= width {
		return s
	}
	if width <= 1 {
		return "…"
	}
	return s[:width-1] + "…"
}
