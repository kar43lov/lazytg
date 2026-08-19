package chats

import (
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/events"
)

// Update routes incoming messages. The order matters:
//  1. Domain payloads (chatsLoadedMsg, *chatsLoadFailedMsg, reloadDebouncedMsg)
//     run first so a list refresh from the repo is applied before any
//     subsequent key processing.
//  2. events.DialogUpdated and events.MessageReceived both schedule a
//     debounced reload — DialogUpdated is the canonical "dialog metadata
//     changed" signal, while MessageReceived is the "new message
//     arrived" signal. Either should bubble the chat to the top of the
//     list, and the 200ms debounce coalesces bursts so we don't redo the
//     repo round-trip on every event.
//  3. Enter is intercepted before the list sees it so we can publish a
//     ChatSelectedMsg with the selected ID. Everything else goes to the
//     list (cursor movement, filter, paging).
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch typed := msg.(type) {
	case chatsLoadedMsg:
		return m.applyLoaded(typed.items)
	case chatsLoadFailedMsg:
		if m.log != nil {
			m.log.Warn("chats: repo load failed", "err", typed.err)
		}
		return m, nil
	case reloadDebouncedMsg:
		return m.applyDebouncedReload(typed.generation)
	case events.DialogUpdated, events.MessageReceived, events.MessagesDeleted:
		return m.scheduleReload()
	case tea.KeyPressMsg:
		if isEnter(typed) && !m.IsFilterActive() {
			return m.handleEnter()
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// applyLoaded replaces the master slice and pushes the new items into
// the bubbles list. Selection is preserved by index — bubbles/list does
// not expose a "select by id" helper and re-aligning by id here would
// add complexity for a UX nicety we don't need yet.
func (m Model) applyLoaded(items []ChatItem) (Model, tea.Cmd) {
	m.chats = items
	asListItems := make([]list.Item, len(items))
	for i, it := range items {
		asListItems[i] = it
	}
	cmd := m.list.SetItems(asListItems)
	return m, cmd
}

// scheduleReload bumps the generation counter and arms a tea.Tick. The
// debounce drops every tick whose generation does not match the current
// counter so a flurry of DialogUpdates only triggers one repo read.
func (m Model) scheduleReload() (Model, tea.Cmd) {
	if m.repo == nil {
		// No repo wired (placeholder mode) — still acknowledge the event
		// so callers don't see a "wasn't handled" surface, but skip the
		// tick entirely.
		return m, nil
	}
	m.reloadGeneration++
	gen := m.reloadGeneration
	return m, tea.Tick(debounceWindow, func(time.Time) tea.Msg {
		return reloadDebouncedMsg{generation: gen}
	})
}

// applyDebouncedReload runs the repo read iff the tick we received is
// the latest one we scheduled. Stale ticks (gen < current) are no-ops.
func (m Model) applyDebouncedReload(gen uint64) (Model, tea.Cmd) {
	if gen != m.reloadGeneration || m.repo == nil {
		return m, nil
	}
	return m, loadCmd(m.repo)
}

// handleEnter publishes ChatSelectedMsg with the highlighted chat id.
// Returns no-op when the list is empty (e.g. fresh start before the
// initial load completed) so a stray Enter does not crash anything.
func (m Model) handleEnter() (Model, tea.Cmd) {
	item, ok := m.SelectedItem()
	if !ok {
		return m, nil
	}
	chatID := item.ID()
	return m, func() tea.Msg {
		return ChatSelectedMsg{ChatID: chatID}
	}
}

// isEnter checks whether msg represents the unmodified Enter / Return
// key. Modifier-bearing variants (Ctrl+Enter, Alt+Enter) are reserved
// for the input pane (Task 9) and must not be misclassified as a "open
// chat" intent here.
func isEnter(k tea.KeyPressMsg) bool {
	if k.Code != tea.KeyEnter {
		return false
	}
	return k.Mod == 0
}

// IsFilterActive reports whether the bubbles/list is currently in a
// state where it should own input keys (filter editing). When filtering,
// Enter applies the filter and "?" is a printable character — the app
// must skip its own ToggleHelp/Enter handling to avoid stealing those
// keystrokes. Exported because the app reads it during global-key
// dispatch in addition to the in-pane Enter check.
func (m Model) IsFilterActive() bool {
	return m.list.SettingFilter()
}
