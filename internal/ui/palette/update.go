package palette

import (
	tea "charm.land/bubbletea/v2"
)

// Update routes incoming messages. Order matters:
//  1. OpenedMsg / ClosedMsg are own-package signals from the app's
//     keymap dispatcher; they flip Visible without doing anything to
//     the input value beyond Open()'s reset semantics.
//  2. LoadedMsg lands the result of a previously-scheduled candidate
//     refresh (see Open). Applied unconditionally because the call
//     is in flight by the time anything new could race with it.
//  3. Keys: Esc closes; Enter emits SelectedMsg; Up/Down move the
//     cursor through the filtered slice; everything else goes to
//     the textinput. After every textinput change the filter is
//     re-run so View renders the up-to-date filtered list.
//
// Updates that arrive while the palette is hidden are dropped except
// for OpenedMsg — every other message would mutate state the app
// cannot observe, which would make re-opening surprisingly stateful.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch typed := msg.(type) {
	case OpenedMsg:
		return m.Open()
	case ClosedMsg:
		return m.Close(), nil
	case LoadedMsg:
		return m.applyLoaded(typed), nil
	}
	if !m.Visible {
		return m, nil
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		return m.handleKey(k)
	}
	return m, nil
}

// applyLoaded captures the candidate list returned by the loader.
// The filter is recomputed against the current input value so a
// load that lands after the user already typed a few characters
// still produces the right initial filtered slice.
func (m Model) applyLoaded(msg LoadedMsg) Model {
	m.items = msg.Items
	m.loadErr = msg.Err
	m.filtered = Match(m.input.Value(), m.items)
	if m.cursor >= len(m.filtered) {
		m.cursor = 0
	}
	return m
}

// handleKey is the in-overlay keypress dispatcher. The palette is
// modal: while Visible == true every key is consumed here so the
// underlying focus target does not also act on the same press.
func (m Model) handleKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	switch k.Code {
	case tea.KeyEscape:
		return m.Close(), func() tea.Msg { return ClosedMsg{} }
	case tea.KeyEnter:
		if len(m.filtered) == 0 {
			return m, nil
		}
		idx := m.filtered[m.cursor]
		if idx < 0 || idx >= len(m.items) {
			return m, nil
		}
		chatID := m.items[idx].ChatID
		closed := m.Close()
		return closed, func() tea.Msg { return SelectedMsg{ChatID: chatID} }
	case tea.KeyUp:
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case tea.KeyDown:
		if m.cursor+1 < len(m.filtered) {
			m.cursor++
		}
		return m, nil
	}

	prev := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	if m.input.Value() != prev {
		m.filtered = Match(m.input.Value(), m.items)
		m.cursor = 0
	}
	return m, cmd
}
