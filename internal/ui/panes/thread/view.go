package thread

import "strings"

// View renders the pane: a focus-aware "Thread" / "Thread (focused)"
// header followed by the viewport body. The header is rendered here
// (not as a viewport-internal title) for two reasons:
//  1. The (focused) suffix is something the existing app/view tests
//     grep for as a focus signal — keeping it here means a single
//     place to change if the focus indicator gains styling.
//  2. The bubbles viewport does not expose a built-in title slot;
//     stacking the header above it keeps the body math predictable
//     (height − 1 row for the header).
//
// While loading we stack a one-line "Loading..." status under the
// header so the user sees feedback during the initial fetch — without
// it the pane stays blank for the SQLite round-trip and feels broken.
func (m Model) View() string {
	header := "Thread"
	if m.Focused {
		header = "Thread (focused)"
	}
	if m.loading && len(m.messages) == 0 {
		return strings.Join([]string{header, "Loading..."}, "\n")
	}
	body := m.viewport.View()
	if body == "" {
		return header
	}
	return strings.Join([]string{header, body}, "\n")
}
