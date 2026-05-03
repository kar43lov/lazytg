package search

import (
	tea "charm.land/bubbletea/v2"
)

// Update routes incoming messages. Order matters:
//  1. OpenedMsg / ClosedMsg are own-package signals from
//     the app's keymap dispatcher; they flip Visible without doing
//     anything to the input value.
//  2. ResultsMsg lands the result of a previously-scheduled
//     query — applied unconditionally because the call is already
//     in flight by the time the request is racing with anything new.
//  3. debounceTickMsg fires after the debounce window. It runs
//     service.Search iff the captured generation still matches the
//     current one (otherwise the user typed again, and this tick is
//     stale).
//  4. Keys: Esc closes; Enter emits JumpMsg; Up/Down move the
//     cursor; everything else is forwarded to the textinput. When
//     the textinput value changes, scheduleQuery arms a fresh
//     debounce tick.
//
// Updates that arrive while the overlay is hidden are dropped except
// for OpenedMsg — every other message would mutate state the
// app cannot observe, which would make re-opening surprisingly
// stateful.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch typed := msg.(type) {
	case OpenedMsg:
		return m.Open()
	case ClosedMsg:
		return m.Close(), nil
	case ResultsMsg:
		return m.applyResults(typed)
	case debounceTickMsg:
		return m.applyDebounceTick(typed)
	}
	if !m.Visible {
		return m, nil
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		return m.handleKey(k)
	}
	return m, nil
}

// applyResults lands a freshly-returned slice of hits. The cursor is
// reset to 0 so a new query always starts with the best-ranked hit
// highlighted.
func (m Model) applyResults(msg ResultsMsg) (Model, tea.Cmd) {
	m.loading = false
	if msg.Err != nil {
		m.err = msg.Err
		m.hits = nil
		m.cursor = 0
		return m, nil
	}
	m.err = nil
	m.hits = msg.Hits
	if m.cursor >= len(m.hits) {
		m.cursor = 0
	}
	return m, nil
}

// applyDebounceTick runs the actual service.Search if the captured
// generation matches. Stale ticks are silently dropped.
func (m Model) applyDebounceTick(msg debounceTickMsg) (Model, tea.Cmd) {
	if msg.generation != m.queryGeneration {
		return m, nil
	}
	q := trimSpace(m.input.Value())
	if q == "" {
		// Empty query — drop any displayed results so the user
		// sees the placeholder state instead of stale hits from a
		// previous query.
		m.hits = nil
		m.lastQuery = ""
		m.cursor = 0
		m.err = nil
		m.loading = false
		return m, nil
	}
	if q == m.lastQuery && !m.loading {
		// Same query as the one currently displayed — skip the
		// re-fire. This mostly matters for the synthetic
		// OpenedMsg path that re-opens the overlay with the
		// last value already populated.
		return m, nil
	}
	m.lastQuery = q
	m.loading = true
	return m, m.runSearch(q)
}

// handleKey is the in-overlay keypress dispatcher. The overlay is
// modal: while Visible == true every key is consumed here so the
// underlying focus target does not also act on the same press.
func (m Model) handleKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	switch k.Code {
	case tea.KeyEscape:
		return m.Close(), func() tea.Msg { return ClosedMsg{} }
	case tea.KeyEnter:
		if len(m.hits) == 0 {
			return m, nil
		}
		hit := m.hits[m.cursor]
		closed := m.Close()
		return closed, func() tea.Msg { return JumpMsg{Hit: hit} }
	case tea.KeyUp:
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case tea.KeyDown:
		if m.cursor+1 < len(m.hits) {
			m.cursor++
		}
		return m, nil
	}

	// Forward everything else to the textinput. After the update,
	// detect whether the value actually changed and arm a debounce
	// tick if so.
	prev := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	if m.input.Value() != prev {
		debounceCmd := m.scheduleQuery()
		if cmd == nil {
			return m, debounceCmd
		}
		return m, tea.Batch(cmd, debounceCmd)
	}
	return m, cmd
}
