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
	preview := truncRunes(m.replyTo.Text, replyPreviewLimit)
	prefix := "↳ Reply to: "
	body := prefix + preview
	style := lipgloss.NewStyle().
		Foreground(lipgloss.Color("8")). // ANSI bright black (grey)
		Italic(true)
	return style.Render(truncateRow(body, width))
}

// replyPreviewLimit caps how many runes of the parent message we echo
// in the inline hint. Counted in runes (not bytes) so a truncation at
// the limit never slices a Cyrillic / CJK codepoint mid-byte and emits
// invalid UTF-8 to the terminal. Same value the thread pane uses for
// its own reply preview so the two render consistently.
const replyPreviewLimit = 50

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

// truncateRow shortens s to width runes with a single-character
// ellipsis if it would overflow. Counted in runes (not bytes) so reply
// previews containing Cyrillic / CJK never get sliced mid-codepoint —
// the empty-state hint is plain ASCII so it is unaffected, but reply
// previews can carry arbitrary UTF-8.
func truncateRow(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if width <= 1 {
		return "…"
	}
	return truncRunes(s, width)
}

// truncRunes is the rune-aware analogue of strings byte-slicing. It
// returns s unchanged when its rune count fits in n; otherwise it cuts
// at n-1 runes and appends a single-rune ellipsis. n <= 0 collapses to
// the empty string. Mirrors the thread pane's own truncRunes helper —
// duplicated rather than imported to keep input free of cross-pane
// dependencies.
func truncRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}
