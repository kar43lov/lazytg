package app

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/pgmac/lazytg/internal/core/events"
	"github.com/pgmac/lazytg/internal/ui/input"
	"github.com/pgmac/lazytg/internal/ui/panes/chats"
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
	case chats.ChatSelectedMsg:
		return a.handleChatSelected(m)
	case input.RequestReplyMsg:
		return a.handleReplyRequest()
	case events.MessageReceived,
		events.DialogUpdated,
		events.OutgoingMessageStateChanged,
		events.ConnectionStateChanged,
		events.StorageStateChanged:
		return a.broadcastBusEvent(msg)
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
		return a.delegateToFocused(msg)
	}

	// Non-key messages are typically pane-internal results from a
	// previously-spawned tea.Cmd (e.g. chatsLoadedMsg from chats.Init or
	// messagesLoadedMsg from thread.OpenChat). They must reach the pane
	// that produced them regardless of which pane currently has focus —
	// so we broadcast and let each pane's Update filter on its own
	// payload type. Keypresses keep the focus-only routing because that
	// is what a single-active-input UX requires.
	return a.broadcastToPanes(msg)
}

// broadcastToPanes forwards msg to every sub-pane and merges the
// returned cmds via tea.Batch. Each pane's Update is responsible for
// ignoring messages that aren't its own payload type — the cost of one
// type-switch per pane per non-key message is dwarfed by the cost of an
// in-flight async load that would otherwise be routed by focus into the
// wrong pane.
func (a App) broadcastToPanes(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	updatedChats, cmd := a.chats.Update(msg)
	a.chats = updatedChats
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	updatedThread, cmd := a.thread.Update(msg)
	a.thread = updatedThread
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	updatedInput, cmd := a.input.Update(msg)
	a.input = updatedInput
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	if len(cmds) == 0 {
		return a, nil
	}
	return a, tea.Batch(cmds...)
}

// handleChatSelected fans the user's chat-pick out to every pane that
// cares: the thread loads the chat, the input binds itself to the new
// chat id, and the status bar updates its title. Routing here (rather
// than in the chats pane) keeps the panes decoupled — chats only knows
// "user pressed Enter on row X", everyone else listens.
func (a App) handleChatSelected(msg chats.ChatSelectedMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	updatedThread, cmd := a.thread.OpenChat(msg.ChatID)
	a.thread = updatedThread
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	updatedInput, cmd := a.input.Update(input.SetChatMsg{ChatID: msg.ChatID})
	a.input = updatedInput
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	if title, ok := a.chatTitle(msg.ChatID); ok {
		a.status.ChatTitle = title
	}

	return a, tea.Batch(cmds...)
}

// handleReplyRequest is the app-level resolver for the input pane's
// RequestReplyMsg → SetReplyMsg dance. We inspect the thread for its
// most recent message and arm it as the reply target. When the thread
// is empty (e.g. user pressed Reply before any history loaded) the
// request is silently dropped.
func (a App) handleReplyRequest() (tea.Model, tea.Cmd) {
	msgs := a.thread.Messages()
	if len(msgs) == 0 {
		return a, nil
	}
	target := msgs[len(msgs)-1]
	updatedInput, cmd := a.input.Update(input.SetReplyMsg{Msg: &target})
	a.input = updatedInput
	return a, cmd
}

// broadcastBusEvent fans a bus event out to every interested pane. The
// status bar only reacts to connection / storage transitions; the
// thread pane filters by chat id internally; the chats pane
// debounce-reloads on DialogUpdated. Routing here means the
// program.Send → tea.Msg fan-in (cmd/lazytg/cmd/tui.go) does not need
// to know which pane owns which event.
func (a App) broadcastBusEvent(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch ev := msg.(type) {
	case events.ConnectionStateChanged:
		a.status.ConnState = ev.State
		return a, nil
	case events.StorageStateChanged:
		a.status.StorageMode = ev.Mode
		return a, nil
	case events.DialogUpdated:
		updated, cmd := a.chats.Update(ev)
		a.chats = updated
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		return a, tea.Batch(cmds...)
	case events.MessageReceived, events.OutgoingMessageStateChanged:
		updated, cmd := a.thread.Update(ev)
		a.thread = updated
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		return a, tea.Batch(cmds...)
	}
	return a, nil
}

// chatTitle looks up the current title for the given chat id by
// scanning the chats pane's loaded items. Returns ok=false when the
// list is empty or the id isn't present (the latter happens when the
// user opens a chat ahead of the chats pane finishing its initial
// load — the title catches up on the next reload).
func (a App) chatTitle(id int64) (string, bool) {
	for _, it := range a.chats.Items() {
		if it.ID() == id {
			return it.Name(), true
		}
	}
	return "", false
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
