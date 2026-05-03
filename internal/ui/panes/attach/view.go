package attach

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// modalBorderStyle is the rounded box wrapper around the overlay
// content. Identical to the search/palette overlays so the user sees a
// consistent "modal" affordance across overlays.
var modalBorderStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	Padding(0, 1)

// dirStyle highlights directory entries in the listing.
var dirStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))

// fileStyle is the default colour for regular file entries (terminal
// foreground, no override).
var fileStyle = lipgloss.NewStyle()

// cursorStyle paints the highlighted listing row.
var cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)

// errStyle highlights load errors (red).
var errStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))

// hintStyle is used for the bottom-of-modal "Tab to caption · Enter to
// submit · Esc to cancel" hint row.
var hintStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)

// maxVisibleRows caps the directory listing height. Keeps the modal a
// reasonable size on tall terminals; users with deep directories rely
// on typing the path to navigate quickly anyway.
const maxVisibleRows = 12

// View renders the overlay. width / height are the terminal
// dimensions; the modal centres itself within them.
func (m Model) View(width, height int) string {
	if !m.Visible {
		return ""
	}

	body := strings.Join([]string{
		m.pathInput.View(),
		m.captionInput.View(),
		"",
		m.renderEntries(),
		"",
		hintStyle.Render("tab: caption  ·  enter: submit  ·  esc: cancel"),
	}, "\n")

	modal := modalBorderStyle.Render(body)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal)
}

// renderEntries produces the directory listing block. The window
// scrolls so the cursor is always visible, similar to the palette.
func (m Model) renderEntries() string {
	if m.loadErr != nil {
		return errStyle.Render(fmt.Sprintf("error: %s", m.loadErr.Error()))
	}
	if len(m.entries) == 0 {
		return hintStyle.Render("(empty directory)")
	}

	start, end := windowBounds(m.cursor, len(m.entries), maxVisibleRows)
	var b strings.Builder
	for i := start; i < end; i++ {
		entry := m.entries[i]
		row := entry.Name
		if entry.IsDir {
			row += "/"
		}
		switch {
		case i == m.cursor:
			b.WriteString(cursorStyle.Render("➜ " + row))
		case entry.IsDir:
			b.WriteString("  " + dirStyle.Render(row))
		default:
			b.WriteString("  " + fileStyle.Render(row))
		}
		if i < end-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// windowBounds centres the visible window on cursor while clamping to
// [0, total). Pulled out so a future refactor (hashtable, virtualised
// list) can swap the strategy in one place.
func windowBounds(cursor, total, maxVisible int) (int, int) {
	if total <= maxVisible {
		return 0, total
	}
	half := maxVisible / 2
	start := cursor - half
	if start < 0 {
		start = 0
	}
	end := start + maxVisible
	if end > total {
		end = total
		start = end - maxVisible
	}
	return start, end
}
