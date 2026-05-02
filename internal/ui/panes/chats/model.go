// Package chats hosts the left pane of the lazytg TUI — the dialog list.
//
// Stage 2 Task 6 only needs a minimal placeholder so the app skeleton has a
// real model to compose. Full implementation (loading from repo, sorting,
// filter, navigation) lands in Task 7.
package chats

import tea "charm.land/bubbletea/v2"

// Model is the chats-pane Bubble Tea model. Width/Height get sized by the
// app on tea.WindowSizeMsg; everything else is filled in by Task 7.
type Model struct {
	Width   int
	Height  int
	Focused bool
}

// New returns an empty placeholder model.
func New() Model { return Model{} }

// Init is a no-op until Task 7 wires repo loading.
func (m Model) Init() tea.Cmd { return nil }

// Update is a no-op until Task 7 implements navigation and selection.
func (m Model) Update(_ tea.Msg) (Model, tea.Cmd) { return m, nil }

// View renders a placeholder body. Borders/highlight are drawn by the app
// layer so the pane stays stylistically consistent across its own state
// transitions.
func (m Model) View() string {
	if m.Focused {
		return "Chats (focused)"
	}
	return "Chats"
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
