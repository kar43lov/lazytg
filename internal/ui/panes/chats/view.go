package chats

import (
	"strings"
)

// View renders the pane: a focus-aware "Chats" / "Chats (focused)"
// header followed by the bubbles list body. The header is rendered
// here (not as list.Title) for two reasons:
//  1. The (focused) suffix is something the existing app/view tests
//     grep for as a focus signal — keeping it here means a single
//     place to change if the focus indicator gains styling.
//  2. The bubbles list reserves vertical space for its title even
//     when empty, while we want the row count to depend only on
//     real content; rendering the header outside the list keeps the
//     visible-row math predictable.
func (m Model) View() string {
	header := "Chats"
	if m.Focused {
		header = "Chats (focused)"
	}
	body := m.list.View()
	rows := []string{header}
	// The tab strip appears only when the account has folders: one that has
	// none should look exactly as it did before folders existed, rather than
	// gaining a row that says "All" and nothing else.
	if strip := m.folderStrip(m.Width); strip != "" {
		rows = append(rows, strip)
	}
	if body == "" {
		return strings.Join(rows, "\n")
	}
	rows = append(rows, body)
	return strings.Join(rows, "\n")
}
