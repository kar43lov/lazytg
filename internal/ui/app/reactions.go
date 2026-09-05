package app

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/events"
	"github.com/kar43lov/lazytg/internal/ui/overlay"
)

// Reacting to a message.
//
// A reaction is the cheapest thing two people can say to each other on
// Telegram — an acknowledgement that costs neither of them a notification —
// and a client without it presents a conversation with half the replies
// removed.
//
// The gesture is one key and the emoji picker that already exists. Telegram
// Desktop shows a hover bar of six suggestions; a TUI has no hover, and a
// fixed row of six would be wrong for everybody whose circle reacts with
// something else. The picker remembers what was used this session, so the
// second reaction of a sitting is two keys away.

// pendingReaction is the message a picker opened to react is about.
type pendingReaction struct {
	chatID    int64
	messageID int64
	// chosen is what this account currently has on the message. It is what
	// makes the gesture a toggle: picking the same emoji again takes it
	// back, which is what every client does and what the boxed reaction in
	// the thread is announcing.
	chosen string
}

// reactionResultMsg carries the outcome back to the UI goroutine.
type reactionResultMsg struct {
	err error
}

// cmdReactToCursor opens the picker for the message under the cursor.
//
// Deliberately the cursor and not the marked set. Copying, deleting and
// forwarding several messages at once are ordinary things to want; reacting
// to five messages with one emoji is not, and a batch reaction is five
// requests on an account already watched for being an unofficial client.
func (a App) cmdReactToCursor() (App, tea.Cmd, bool) {
	msg, ok := a.thread.CursorMessage()
	if !ok {
		return a, nil, false
	}
	if a.actions == nil {
		a.status = a.status.SetNotice("not connected")
		return a, nil, true
	}
	a.pendingReaction = &pendingReaction{
		chatID:    a.thread.ChatID(),
		messageID: msg.ID,
		chosen:    msg.ChosenReaction(),
	}
	a.emojiPicker = a.emojiPicker.Open()
	if a.pendingReaction.chosen != "" {
		a.status = a.status.SetNotice("pick a reaction — the same one again takes yours back")
	} else {
		a.status = a.status.SetNotice("pick a reaction, esc to cancel")
	}
	return a, nil, true
}

// handleReactionPick consumes a picked emoji when the picker was opened to
// react. Returns (nil, false) when it was not, so the ordinary insert-into-
// the-composer path keeps its behaviour.
func (a App) handleReactionPick(char string) (tea.Model, tea.Cmd, bool) {
	pending := a.pendingReaction
	if pending == nil {
		return a, nil, false
	}
	a.pendingReaction = nil

	emoticon := char
	if char == pending.chosen {
		// The same one again takes it back. Removal is the same request
		// with nothing in it, which is how the protocol expresses it.
		emoticon = ""
		a.status = a.status.SetNotice("taking your reaction back…")
	} else {
		a.status = a.status.SetNotice("reacting…")
	}

	actions := a.actions
	chatID, messageID := pending.chatID, pending.messageID
	return a, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		return reactionResultMsg{err: actions.React(ctx, chatID, messageID, emoticon)}
	}, true
}

// cancelPendingReaction drops a reaction whose picker was dismissed.
func (a App) cancelPendingReaction() App {
	if a.pendingReaction == nil {
		return a
	}
	a.pendingReaction = nil
	a.status = a.status.SetNotice("")
	return a
}

// applyReactionResult reports a failure and says nothing on success — the
// reaction itself appears under the message, which is the only confirmation
// worth having.
func (a App) applyReactionResult(msg reactionResultMsg) App {
	if msg.err != nil {
		a.log.Warn("react failed", "err", msg.err)
		a.status = a.status.SetNotice(fmt.Sprintf("could not react: %v", msg.err))
		return a
	}
	a.status = a.status.SetNotice("")
	return a
}

// applyReactionsChanged redraws the one row a reaction change affects.
func (a App) applyReactionsChanged(ev events.MessageReactionsChanged) App {
	if a.thread.ChatID() != ev.ChatID {
		return a
	}
	a.thread = a.thread.ApplyReactions(ev.MessageID, ev.Reactions)
	return a
}

// ensure the overlay package stays imported for the picker type used above.
var _ = overlay.EmojiPickedMsg{}
