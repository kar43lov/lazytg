package palette

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// rowStyle / cursorRowStyle are the per-result style passes. The
// cursor row is rendered in bright blue so the active selection
// stands out against the dimmed metadata of the other rows.
var (
	paletteRowStyle      = lipgloss.NewStyle().Padding(0, 1)
	paletteCursorRow     = lipgloss.NewStyle().Padding(0, 1).Foreground(lipgloss.Color("12"))
	paletteMetaStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	paletteOverlayBox    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 2)
	paletteOverlayHint   = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
	paletteOverlayErrSty = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

// maxVisibleRows caps the rendered row count so a 5000-chat palette
// does not produce a modal that overflows the terminal. The cursor
// auto-scrolls inside this window — see renderRows.
const maxVisibleRows = 12

// View renders the centred palette overlay. width and height are
// the surrounding terminal dimensions used by lipgloss.Place; the
// modal sizes itself against its content.
//
// When Visible is false the view returns the empty string so callers
// can append it unconditionally (mirrors the help / search overlay
// contract).
func (m Model) View(width, height int) string {
	if !m.Visible {
		return ""
	}

	var body strings.Builder
	body.WriteString(m.input.View())
	body.WriteByte('\n')

	switch {
	case m.loadErr != nil:
		body.WriteString(paletteOverlayErrSty.Render("error: " + m.loadErr.Error()))
	case len(m.items) == 0:
		body.WriteString(paletteOverlayHint.Render("no chats yet — try /login or wait for sync"))
	case len(m.filtered) == 0:
		if name := UsernameFrom(m.input.Value()); name != "" {
			body.WriteString(paletteOverlayHint.Render("no matches — Enter opens @" + name))
		} else {
			body.WriteString(paletteOverlayHint.Render("no matches (type @name or t.me/name to open a chat that is not here)"))
		}
	default:
		body.WriteString(m.renderRows())
	}

	box := paletteOverlayBox.Render(body.String())
	if width <= 0 || height <= 0 {
		return box
	}
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// renderRows renders the filtered candidate list with the cursor
// row styled. The window scrolls so the cursor stays visible inside
// the maxVisibleRows cap; rows above/below the window are silently
// elided. A future iteration may add a "n more" footer if we hear
// users miss it.
func (m Model) renderRows() string {
	start := 0
	end := len(m.filtered)
	if end > maxVisibleRows {
		// Keep the cursor centred when possible; clamp at the
		// boundaries so we don't show negative or out-of-range
		// indices.
		start = m.cursor - maxVisibleRows/2
		if start < 0 {
			start = 0
		}
		end = start + maxVisibleRows
		if end > len(m.filtered) {
			end = len(m.filtered)
			start = end - maxVisibleRows
		}
	}

	var b strings.Builder
	for i := start; i < end; i++ {
		idx := m.filtered[i]
		if idx < 0 || idx >= len(m.items) {
			continue
		}
		row := paletteMetaStyle.Render(formatChatID(m.items[idx].ChatID)) + "  " + m.items[idx].Title
		style := paletteRowStyle
		if i == m.cursor {
			style = paletteCursorRow
		}
		b.WriteString(style.Render(row))
		if i < end-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// formatChatID is the meta-column formatter for a row. Mirrors the
// search overlay's "chat=N" column so muscle memory carries between
// the two modals.
func formatChatID(id int64) string { return "chat=" + itoa(id) }
