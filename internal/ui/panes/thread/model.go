// Package thread hosts the right pane of the lazytg TUI — the message list
// for the currently-selected chat.
//
// Stage 2 Task 6 only needs a placeholder for the app skeleton to compose.
// Full implementation (history loading, pagination, optimistic rendering)
// lands in Task 8.
package thread

import tea "charm.land/bubbletea/v2"

// Model is the thread-pane Bubble Tea model. Width/Height get sized by the
// app on tea.WindowSizeMsg; the rest is filled in by Task 8.
type Model struct {
	Width   int
	Height  int
	Focused bool
}

// New returns an empty placeholder model.
func New() Model { return Model{} }

// Init is a no-op until Task 8 wires history loading.
func (m Model) Init() tea.Cmd { return nil }

// Update is a no-op until Task 8 implements navigation and pagination.
func (m Model) Update(_ tea.Msg) (Model, tea.Cmd) { return m, nil }

// View renders a placeholder body so the layout is visible during Stage 2
// development. Real message rendering lands in Task 8.
func (m Model) View() string {
	if m.Focused {
		return "Thread (focused)"
	}
	return "Thread"
}

// SetSize updates the pane dimensions; the app calls this on every resize.
func (m Model) SetSize(width, height int) Model {
	m.Width = width
	m.Height = height
	return m
}

// SetFocus marks the pane as focused or not. Used by the app to switch the
// active border highlight when Tab cycles focus.
func (m Model) SetFocus(f bool) Model {
	m.Focused = f
	return m
}
