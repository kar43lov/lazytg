package app

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/ui/palette"
)

// Passing messages to another chat.
//
// This is the action the marks were built for. Copy, edit and delete all act
// where the user already is; forwarding is the only one that needs a second
// question — which chat — and answering it is the whole design problem.
//
// The answer is the command palette. It already lists every chat, ranks by
// frecency and matches Unicode-fuzzily, and it is the switcher the user
// reaches for anyway; a second chat-picking widget would be the same list
// with different keys. So the palette gains a purpose rather than a twin: it
// opens saying what it is about, and what happens on Enter depends on why it
// was opened.

// pendingForward is what a chat-picking palette is about while it is open.
type pendingForward struct {
	fromChatID int64
	ids        []int64
	// count is kept separately from len(ids) only for the status line, so
	// the message reads the same after ids has been handed to the command.
	count int
}

// forwardResultMsg carries the outcome back to the UI goroutine.
type forwardResultMsg struct {
	toChatID int64
	count    int
	err      error
}

// cmdForwardTargets asks which chat to forward to, and remembers what.
//
// Nothing is sent here. The user has named the messages; naming the
// destination is a separate act, and doing both on one keypress is how a
// message ends up in the wrong conversation.
func (a App) cmdForwardTargets() (App, tea.Cmd, bool) {
	targets := a.thread.Targets()
	if len(targets) == 0 {
		return a, nil, false
	}
	ids := make([]int64, 0, len(targets))
	for _, msg := range targets {
		ids = append(ids, msg.ID)
	}
	if a.actions == nil {
		a.status = a.status.SetNotice("not connected")
		return a, nil, true
	}
	a.pendingForward = &pendingForward{
		fromChatID: a.thread.ChatID(),
		ids:        ids,
		count:      len(ids),
	}
	a.status = a.status.SetNotice(fmt.Sprintf("forwarding %s — pick a chat, esc to cancel", plural(len(ids), "message", "messages")))
	return a, cmdOpenPalette(), true
}

// handleForwardPick consumes a palette selection made for a forward, sends
// the messages and reports what happened. Returns (nil, false) when the
// palette was not opened for a forward, so the ordinary chat-switch path
// keeps its behaviour.
func (a App) handleForwardPick(msg palette.SelectedMsg) (tea.Model, tea.Cmd, bool) {
	pending := a.pendingForward
	if pending == nil {
		return a, nil, false
	}
	a.pendingForward = nil
	a = a.closePalette()

	if msg.ChatID == pending.fromChatID {
		// Telegram allows it and it is almost never meant: the user
		// picked the chat they were already in because it was top of the
		// list.
		a.status = a.status.SetNotice("that is the chat you are forwarding from")
		return a, nil, true
	}

	actions := a.actions
	from, to, ids := pending.fromChatID, msg.ChatID, pending.ids
	count := pending.count
	a.thread = a.thread.ClearMarks()
	a.status = a.status.SetNotice("forwarding…")

	return a, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		err := actions.Forward(ctx, from, to, ids, false)
		return forwardResultMsg{toChatID: to, count: count, err: err}
	}, true
}

// cancelPendingForward drops a forward whose palette was dismissed.
func (a App) cancelPendingForward() App {
	if a.pendingForward == nil {
		return a
	}
	a.pendingForward = nil
	a.status = a.status.SetNotice("")
	return a
}

// applyForwardResult reports the outcome.
func (a App) applyForwardResult(msg forwardResultMsg) App {
	if msg.err != nil {
		a.log.Warn("forward failed", "to_chat_id", msg.toChatID, "err", msg.err)
		a.status = a.status.SetNotice(fmt.Sprintf("could not forward: %v", msg.err))
		return a
	}
	title, ok := a.chatTitle(msg.toChatID)
	if !ok || title == "" {
		title = fmt.Sprintf("chat %d", msg.toChatID)
	}
	a.status = a.status.SetNotice(fmt.Sprintf("forwarded %s to %s",
		plural(msg.count, "message", "messages"), title))
	return a
}
