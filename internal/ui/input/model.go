// Package input hosts the bottom composer of the lazytg TUI: textarea,
// reply-state, $EDITOR delegation, and emacs-style readline bindings.
//
// Stage 2 Task 6 only needs a placeholder for the app skeleton to compose.
// Full implementation (textarea, send wiring, history) lands in Task 9 and
// Task 10.
package input

import tea "charm.land/bubbletea/v2"

// Model is the input-pane Bubble Tea model. Width gets sized by the app on
// tea.WindowSizeMsg; the rest is filled in by Tasks 9 and 10.
type Model struct {
	Width   int
	Focused bool
}

// New returns an empty placeholder model.
func New() Model { return Model{} }

// Init is a no-op until Task 9 wires the textarea.
func (m Model) Init() tea.Cmd { return nil }

// Update is a no-op until Task 9 wires the textarea + send service.
func (m Model) Update(_ tea.Msg) (Model, tea.Cmd) { return m, nil }

// View renders a placeholder body so the layout is visible during Stage 2
// development. Real composer rendering lands in Task 9.
func (m Model) View() string {
	if m.Focused {
		return "> _"
	}
	return "> "
}

// SetWidth updates the pane width; the app calls this on every resize.
func (m Model) SetWidth(w int) Model {
	m.Width = w
	return m
}

// SetFocus marks the pane as focused or not. Used by the app to switch the
// active border highlight when Tab cycles focus.
func (m Model) SetFocus(f bool) Model {
	m.Focused = f
	return m
}
