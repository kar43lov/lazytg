package attach

import (
	tea "charm.land/bubbletea/v2"
)

// Update routes incoming messages. Order:
//  1. OpenedMsg / ClosedMsg flip Visible without touching the input
//     value (mirrors the search overlay protocol).
//  2. dirLoadedMsg lands the listing of the path-input directory.
//  3. KeyPressMsg dispatches:
//     Esc — close + emit ClosedMsg.
//     Enter — submit on a regular file, descend into a directory,
//     or no-op when the listing is empty.
//     Tab/Shift-Tab — toggle the focused input.
//     Up/Down — move the cursor (path mode only).
//     anything else — forward to the focused textinput. When the path
//     input value changes the listing reloads.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch typed := msg.(type) {
	case OpenedMsg:
		return m.Open(typed.ChatID)
	case ClosedMsg:
		return m.Close(), nil
	case dirLoadedMsg:
		return m.applyDirLoaded(typed), nil
	}
	if !m.Visible {
		return m, nil
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		return m.handleKey(k)
	}
	return m, nil
}

// applyDirLoaded folds a fresh directory listing into the model.
// loadErr is preserved for the View renderer; entries / cursor are
// reset to the top of the new listing.
func (m Model) applyDirLoaded(msg dirLoadedMsg) Model {
	if msg.err != nil {
		m.loadErr = msg.err
		m.entries = nil
		m.cursor = 0
		return m
	}
	m.loadErr = nil
	m.entries = msg.entries
	m.cursor = 0
	if msg.dir != "" && msg.dir != m.pathInput.Value() {
		m.pathInput.SetValue(msg.dir)
		m.pathInput.CursorEnd()
	}
	return m
}

// handleKey dispatches a single keypress. The overlay is modal, so
// every key is consumed and the underlying focus target never sees
// the press.
func (m Model) handleKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	switch k.Code {
	case tea.KeyEscape:
		return m.Close(), func() tea.Msg { return ClosedMsg{} }
	case tea.KeyEnter:
		return m.handleEnter()
	case tea.KeyTab:
		return m.toggleMode(), nil
	}

	if m.mode == ModePath {
		switch k.Code {
		case tea.KeyUp:
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		case tea.KeyDown:
			if m.cursor+1 < len(m.entries) {
				m.cursor++
			}
			return m, nil
		}
		prev := m.pathInput.Value()
		var cmd tea.Cmd
		m.pathInput, cmd = m.pathInput.Update(k)
		if m.pathInput.Value() != prev {
			loadCmd := m.loadDir()
			if cmd == nil {
				return m, loadCmd
			}
			return m, tea.Batch(cmd, loadCmd)
		}
		return m, cmd
	}

	var cmd tea.Cmd
	m.captionInput, cmd = m.captionInput.Update(k)
	return m, cmd
}

// handleEnter is the submit / descend dispatcher. Behaviour:
//   - Caption mode + non-empty path input → submit if the highlighted
//     entry is a regular file, else descend.
//   - Path mode + cursor on directory → descend into it.
//   - Path mode + cursor on file → submit.
//   - Empty listing → no-op.
//
// Submission emits a SubmitMsg through a Cmd so the app can observe
// it the same way it observes ClosedMsg.
func (m Model) handleEnter() (Model, tea.Cmd) {
	if len(m.entries) == 0 {
		return m, nil
	}
	entry := m.entries[m.cursor]
	if entry.IsDir {
		m.pathInput.SetValue(entry.Path)
		m.pathInput.CursorEnd()
		m.cursor = 0
		return m, m.loadDir()
	}
	if m.chatID == 0 {
		return m, nil
	}
	chatID := m.chatID
	path := entry.Path
	caption := m.captionInput.Value()
	closed := m.Close()
	return closed, func() tea.Msg {
		return SubmitMsg{ChatID: chatID, Path: path, Caption: caption}
	}
}

// toggleMode swaps focus between the path and caption inputs.
func (m Model) toggleMode() Model {
	if m.mode == ModePath {
		m.mode = ModeCaption
		m.pathInput.Blur()
		_ = m.captionInput.Focus()
	} else {
		m.mode = ModePath
		m.captionInput.Blur()
		_ = m.pathInput.Focus()
	}
	return m
}
