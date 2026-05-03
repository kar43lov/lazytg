package app

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/pgmac/lazytg/internal/core/events"
	"github.com/pgmac/lazytg/internal/ui/input"
	"github.com/pgmac/lazytg/internal/ui/panes/chats"
	uisearch "github.com/pgmac/lazytg/internal/ui/panes/search"
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
	case input.SendDispatchedMsg:
		// The input pane reports a successful queue-up. Insert the
		// optimistic row in the thread immediately so the user sees
		// "[⏳] hello" before the SendService bus event lands. Pass
		// the same message back through input.Update so the pane can
		// register the localID in its inFlight tracker — needed for
		// async draft restoration on a later Failed bus event (the
		// MTProto-level failure surface that synchronous SendFailedMsg
		// does not reach).
		a.thread = a.thread.ApplyDispatched(m.LocalID, m.ChatID, m.Text)
		updated, cmd := a.input.Update(m)
		a.input = updated
		return a, cmd
	case events.MessageReceived,
		events.DialogUpdated,
		events.OutgoingMessageStateChanged,
		events.ConnectionStateChanged,
		events.StorageStateChanged:
		return a.broadcastBusEvent(msg)
	case uisearch.OpenedMsg:
		return a.openSearch()
	case uisearch.ClosedMsg:
		return a.closeSearch(), nil
	case uisearch.JumpMsg:
		return a.handleSearchJump(m)
	case uisearch.ResultsMsg, uisearch.QueryChangedMsg:
		updated, cmd := a.search.Update(msg)
		a.search = updated
		return a, cmd
	}

	if a.help.Visible {
		updatedHelp, cmd := a.help.Update(msg)
		a.help = updatedHelp
		return a, cmd
	}

	if a.search.Visible {
		updated, cmd := a.search.Update(msg)
		a.search = updated
		return a, cmd
	}

	if k, ok := msg.(tea.KeyPressMsg); ok {
		if cmd, handled := a.handleGlobalKey(k); handled {
			return a, cmd
		}
		if updated, handled := a.applyScrollKey(k); handled {
			return updated, nil
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
	return a.withPendingScroll(a.broadcastToPanes(msg))
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
// debounce-reloads on DialogUpdated *and* MessageReceived (the latter
// drives the dialog reorder when a new message lands — LiveService
// persists the row but does not republish a DialogUpdated, see
// internal/core/sync/live.go). Routing here means the program.Send →
// tea.Msg fan-in (cmd/lazytg/cmd/tui.go) does not need to know which
// pane owns which event.
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
	case events.MessageReceived:
		// Both panes care: thread filters by current chat id internally
		// and appends the message to the visible list; chats reloads
		// the dialog list so the chat hosting this message bubbles to
		// the top.
		updatedThread, cmdT := a.thread.Update(ev)
		a.thread = updatedThread
		if cmdT != nil {
			cmds = append(cmds, cmdT)
		}
		updatedChats, cmdC := a.chats.Update(ev)
		a.chats = updatedChats
		if cmdC != nil {
			cmds = append(cmds, cmdC)
		}
		return a, tea.Batch(cmds...)
	case events.OutgoingMessageStateChanged:
		// Both the thread (renders the [⏳]/[✗] pill) and the input
		// pane (restores the draft on Failed) need this event. Routing
		// here keeps each pane's concerns separate while a single
		// program.Send fan-in stays the upstream entry point.
		updatedThread, cmdT := a.thread.Update(ev)
		a.thread = updatedThread
		if cmdT != nil {
			cmds = append(cmds, cmdT)
		}
		updatedInput, cmdI := a.input.Update(ev)
		a.input = updatedInput
		if cmdI != nil {
			cmds = append(cmds, cmdI)
		}
		return a, tea.Batch(cmds...)
	}
	return a, nil
}

// openSearch flips the overlay visible, remembers the previous focus
// target so closeSearch can restore it, and forwards Open() to the
// overlay so its textinput is focused (cursor blink scheduling).
func (a App) openSearch() (tea.Model, tea.Cmd) {
	if a.preSearchFocus < 0 {
		a.preSearchFocus = a.focus
	}
	updated, cmd := a.search.Open()
	a.search = updated
	return a, cmd
}

// closeSearch hides the overlay and restores the focus target the
// user was on when they opened it. Idempotent — calling on an
// already-hidden overlay is a no-op.
func (a App) closeSearch() App {
	a.search = a.search.Close()
	if a.preSearchFocus >= 0 {
		a.preSearchFocus = -1
	}
	return a
}

// handleSearchJump consumes a JumpMsg from the overlay: switches
// focus to the target chat, opens the thread on it, scrolls to the
// matched message with surrounding context, and (if a bus is wired)
// publishes a SearchJumpRequested event so other subscribers can
// react. The overlay is closed as part of the jump because the user
// signalled they want to read the chat now.
//
// The actual ScrollTo is deferred via a.pendingScroll: the thread
// pane's applyLoaded calls GotoBottom unconditionally, so a
// synchronous scroll would be overwritten by the loadCmd's
// messagesLoadedMsg. broadcastToPanes (which routes the loaded
// message to thread) honours pendingScroll on its way out so the
// scroll always lands after the history was rendered.
func (a App) handleSearchJump(msg uisearch.JumpMsg) (tea.Model, tea.Cmd) {
	chatID := msg.Hit.ChatID
	messageID := msg.Hit.Message.ID

	a = a.closeSearch()

	a.pendingScroll = &pendingThreadScroll{
		ChatID:    chatID,
		MessageID: messageID,
		Around:    5,
	}

	updatedThread, cmd := a.thread.OpenChat(chatID)
	a.thread = updatedThread
	cmds := []tea.Cmd{}
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	if title, ok := a.chatTitle(chatID); ok {
		a.status.ChatTitle = title
	}

	if a.bus != nil {
		a.bus.Publish(events.SearchJumpRequested{
			ChatID:    chatID,
			MessageID: messageID,
		})
	}

	updatedInput, inputCmd := a.input.Update(input.SetChatMsg{ChatID: chatID})
	a.input = updatedInput
	if inputCmd != nil {
		cmds = append(cmds, inputCmd)
	}

	if len(cmds) == 0 {
		return a, nil
	}
	return a, tea.Batch(cmds...)
}

// applyPendingScroll runs after broadcastToPanes / broadcastBusEvent
// so any deferred ScrollTo issued by handleSearchJump can land now
// that the thread pane has had a chance to apply its
// messagesLoadedMsg. The scroll is dropped silently if the target
// message is not in the now-loaded slice (the message is older than
// the initialPageSize cap and the user will need to paginate or load
// the full context — Stage 3 leaves that interaction as a follow-up).
func (a App) applyPendingScroll() App {
	if a.pendingScroll == nil {
		return a
	}
	if a.thread.ChatID() != a.pendingScroll.ChatID {
		return a
	}
	if a.thread.Loading() {
		return a
	}
	a.thread = a.thread.ScrollTo(a.pendingScroll.MessageID, a.pendingScroll.Around)
	a.pendingScroll = nil
	return a
}

// withPendingScroll wraps the (App, Cmd) tuple a routing helper
// returned and invokes applyPendingScroll on the App. Used by the
// Update arms that route through broadcastToPanes / broadcastBusEvent
// so a deferred SearchJump scroll lands the moment its preconditions
// are met.
func (a App) withPendingScroll(model tea.Model, cmd tea.Cmd) (tea.Model, tea.Cmd) {
	app, ok := model.(App)
	if !ok {
		return model, cmd
	}
	return app.applyPendingScroll(), cmd
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
	a.search = a.search.SetSize(a.width, a.height)
	return a
}

// handleGlobalKey applies app-level shortcuts (toggle help, focus cycling,
// quit). Returns (cmd, true) when the key was consumed; otherwise the key
// falls through to the focused sub-pane.
//
// ToggleHelp is suppressed when the input pane has focus or the chats
// pane is in filter-input mode. The default chord is "?" — a printable
// character — so swallowing it globally would prevent the user from
// typing "?" into the message body or the chats filter. Quit/FocusNext/
// FocusPrev are non-printable chords and remain global.
func (a App) handleGlobalKey(k tea.KeyPressMsg) (tea.Cmd, bool) {
	helpAllowed := a.focus != FocusInput && !a.chats.IsFilterActive()
	// Search activation has the same suppression rules as ToggleHelp:
	// the default chord ("/") is a printable character that the input
	// pane composer and the chats filter both legitimately consume.
	// When the chats pane is focused the bubbles/list "/" filter is the
	// expected behaviour, so /-search is only available from Thread (or
	// any non-filter Chats moment via the global chord on a future
	// vim-style "ctrl+s" alias).
	searchAllowed := a.focus != FocusInput && !a.chats.IsFilterActive() && a.focus != FocusChats
	switch {
	case helpAllowed && key.Matches(k, a.keymap.ToggleHelp):
		return cmdToggleHelp(), true
	case searchAllowed && key.Matches(k, a.keymap.Search):
		return cmdOpenSearch(), true
	case key.Matches(k, a.keymap.FocusNext):
		return cmdNextFocus(), true
	case key.Matches(k, a.keymap.FocusPrev):
		return cmdPrevFocus(), true
	case key.Matches(k, a.keymap.Quit):
		return cmdQuit(), true
	}
	return nil, false
}

// applyScrollKey checks whether the key matches the configurable
// ScrollUp/ScrollDown chords and, if so, scrolls the thread viewport.
// Scroll keys are intentionally a focus-aware override that only fires
// when the thread pane (or chats pane) is focused — when the input pane
// has focus the same chord (ctrl+b / ctrl+f by default) is the emacs
// character motion the textarea expects, and stealing it would break
// every emacs muscle memory.
//
// Returns (updated app, true) when the chord was consumed, otherwise
// the original app and false so the caller can fall through to the
// regular delegate path.
func (a App) applyScrollKey(k tea.KeyPressMsg) (App, bool) {
	if a.focus == FocusInput {
		return a, false
	}
	switch {
	case key.Matches(k, a.keymap.ScrollUp):
		a.thread = a.thread.ScrollUp()
		return a, true
	case key.Matches(k, a.keymap.ScrollDown):
		a.thread = a.thread.ScrollDown()
		return a, true
	}
	return a, false
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
