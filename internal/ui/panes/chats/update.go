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
	case events.DraftChanged:
		return m.applyDraft(typed)
	case tea.KeyPressMsg:
		if isEnter(typed) && !m.IsFilterActive() {
			return m.handleEnter()
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// applyLoaded replaces the master slice and pushes the new items into the
// bubbles list, keeping the cursor on the chat it was on.
//
// Preserving it by index — which is what bubbles/list does on its own, and
// what this did until the second live run — silently moves the cursor
// whenever the list reorders. The list is sorted by last-message date, so an
// incoming message promotes its chat to the top and every row below shifts
// down: the highlight stays on row 0 and now points at a different
// conversation than the one the user put it on. Seen live on 19.08.2026, and
// it reads as a bug in the thread pane rather than the list — the highlighted
// chat is not the one whose messages are shown.
//
// A cursor whose chat is gone (deleted elsewhere) falls back to the index,
// which lands on the row that took its place.
//
// While the '/' filter is active the cursor is left alone: list.Select indexes
// the *filtered* list, not the one passed here, and SetItems re-runs the
// filter asynchronously through the command it returns — so any index computed
// from `items` would point into the wrong set, and would do so before the set
// it should point into exists.
func (m Model) applyLoaded(items []ChatItem) (Model, tea.Cmd) {
	var wantID int64
	if sel, ok := m.SelectedItem(); ok && m.list.FilterState() == list.Unfiltered {
		wantID = sel.ID()
	}

	m.chats = m.withDrafts(items)
	visible := m.visibleChats(m.chats)
	cmd := m.list.SetItems(listItems(visible, m.rowWidth()))

	if wantID != 0 {
		for i, it := range visible {
			if it.ID() == wantID {
				m.list.Select(i)
				break
			}
		}
	}
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

// withDrafts puts the drafts the pane knows back on freshly loaded rows.
func (m Model) withDrafts(items []ChatItem) []ChatItem {
	if len(m.drafts) == 0 {
		return items
	}
	for i := range items {
		if d, ok := m.drafts[items[i].ID()]; ok {
			items[i] = items[i].withDraft(d)
		}
	}
	return items
}

// applyDraft records a server draft and redraws the row that shows it.
func (m Model) applyDraft(ev events.DraftChanged) (Model, tea.Cmd) {
	if ev.ChatID == 0 {
		return m, nil
	}
	if m.drafts == nil {
		m.drafts = make(map[int64]string)
	}
	if ev.Text == "" {
		delete(m.drafts, ev.ChatID)
	} else {
		m.drafts[ev.ChatID] = ev.Text
	}
	// A copy, not an in-place write: the slice is shared with the model
	// value this one was derived from, and a row changed under it would
	// leak into a state that was never returned.
	rows := make([]ChatItem, len(m.chats))
	copy(rows, m.chats)
	for i := range rows {
		if rows[i].ID() == ev.ChatID {
			rows[i] = rows[i].withDraft(ev.Text)
		}
	}
	m.chats = rows
	return m, m.list.SetItems(listItems(m.visibleChats(m.chats), m.rowWidth()))
}
