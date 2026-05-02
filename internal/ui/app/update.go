package app

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// Init is the initial Cmd batch. Each sub-pane gets its own Init so they can
// kick off their own loaders (Task 7+: chats reads from repo, etc.). Stage 2
// Task 6 returns no-ops from the placeholders — that is fine, tea.Batch on
// a list of nils is itself nil.
func (a App) Init() tea.Cmd {
	return tea.Batch(
		a.chats.Init(),
		a.thread.Init(),
		a.input.Init(),
	)
}

// Update is the central event router. Order matters:
//  1. Resize is handled before anything else so the small-terminal fallback
//     can short-circuit further work.
//  2. The help overlay swallows every key while visible (modal contract).
//  3. Global key bindings (toggle help, focus cycling, quit) take priority
//     over per-pane key handling so the user can always dismiss/quit even
//     when a sub-pane has consumed its own key set.
//  4. Anything left over is delegated to the focused sub-pane.
//  5. Internal Cmd-driven messages (focusCycledMsg, helpToggledMsg) are
//     applied last — they always arrive as a plain tea.Msg so the route
//     order doesn't matter.
func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		return a.handleResize(m), nil
	case focusCycledMsg:
		return a.applyFocusChange(m.Direction), nil
	case helpToggledMsg:
		a.help.Visible = !a.help.Visible
		return a, nil
	}

	if a.help.Visible {
		updatedHelp, cmd := a.help.Update(msg)
		a.help = updatedHelp
		return a, cmd
	}

	if k, ok := msg.(tea.KeyPressMsg); ok {
		if cmd, handled := a.handleGlobalKey(k); handled {
			return a, cmd
		}
	}

	return a.delegateToFocused(msg)
}

// handleResize updates the cached dimensions, recomputes per-pane sizes, and
// flips the small-terminal flag. Called from Update on tea.WindowSizeMsg.
func (a App) handleResize(msg tea.WindowSizeMsg) App {
	a.width, a.height = msg.Width, msg.Height
	a.tooSmall = msg.Width < MinWidth || msg.Height < MinHeight
	if a.tooSmall {
		return a
	}

	chatsW := a.width * 30 / 100
	if chatsW < 20 {
		chatsW = 20
	}
	threadW := a.width - chatsW - 1 // -1 for the vertical separator column.
	if threadW < 1 {
		threadW = 1
	}

	const (
		statusH = 1
		inputH  = 3
	)
	paneH := a.height - statusH - inputH
	if paneH < 1 {
		paneH = 1
	}

	a.chats = a.chats.SetSize(chatsW, paneH)
	a.thread = a.thread.SetSize(threadW, paneH)
	a.input = a.input.SetWidth(a.width)
	return a
}

// handleGlobalKey applies app-level shortcuts (toggle help, focus cycling,
// quit). Returns (cmd, true) when the key was consumed; otherwise the key
// falls through to the focused sub-pane.
func (a App) handleGlobalKey(k tea.KeyPressMsg) (tea.Cmd, bool) {
	chord := k.String()
	switch {
	case key.Matches(k, a.keymap.ToggleHelp):
		return cmdToggleHelp(), true
	case key.Matches(k, a.keymap.FocusNext):
		return cmdNextFocus(), true
	case key.Matches(k, a.keymap.FocusPrev):
		return cmdPrevFocus(), true
	case key.Matches(k, a.keymap.Quit):
		return cmdQuit(), true
	}
	_ = chord
	return nil, false
}

// applyFocusChange shifts focus by +/-1 (mod 3) and propagates the change
// into the affected sub-models. Calling SetFocus on every pane keeps the
// "single focused element" invariant trivial — no need to remember which one
// was previously focused.
func (a App) applyFocusChange(dir int) App {
	const total = 3 // FocusChats / FocusInput / FocusThread.
	next := (int(a.focus) + dir + total) % total
	a.focus = FocusTarget(next)
	a.chats = a.chats.SetFocus(a.focus == FocusChats)
	a.input = a.input.SetFocus(a.focus == FocusInput)
	a.thread = a.thread.SetFocus(a.focus == FocusThread)
	return a
}

// delegateToFocused dispatches msg to the currently focused sub-model.
// Returns the updated app and any Cmd produced by the sub-model.
func (a App) delegateToFocused(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch a.focus {
	case FocusChats:
		a.chats, cmd = a.chats.Update(msg)
	case FocusInput:
		a.input, cmd = a.input.Update(msg)
	case FocusThread:
		a.thread, cmd = a.thread.Update(msg)
	}
	return a, cmd
}
