package app

import (
	"context"
	"fmt"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
	"github.com/kar43lov/lazytg/internal/core/markdown"
	"github.com/kar43lov/lazytg/internal/ui/input"
	"github.com/kar43lov/lazytg/internal/ui/overlay"
	"github.com/kar43lov/lazytg/internal/ui/panes/thread"
)

// Acting on messages that already exist: copying them, rewriting one,
// deleting several.
//
// All three share the same rule for deciding what they act on — the marked
// set if there is one, otherwise the message under the cursor — which lives
// in thread.Targets(). Putting it there rather than here is what keeps a
// gesture consistent: the user marks once and every action agrees about what
// they meant.

// actionTimeout caps an edit or a delete round trip. Long enough to ride out
// a slow link, short enough that a wedged request does not leave the user
// staring at a message that may or may not be gone.
const actionTimeout = 30 * time.Second

// pendingDelete is the deletion waiting on the confirmation modal.
type pendingDelete struct {
	chatID int64
	ids    []int64
	// revocable records whether "delete for everyone" is on the table at
	// all. It is not in a channel — a deletion there is always for
	// everyone — and offering a choice that does not exist teaches the
	// user something false about their own account.
	revocable bool
}

// messageActionsResultMsg reports how an edit or a delete ended. Both share
// one message type because the UI does the same thing with either: shows what
// happened in the status bar.
type messageActionsResultMsg struct {
	err error
	// what names the operation for the status line ("edit", "delete").
	what string
	// count is how many messages a delete removed; zero for an edit.
	count int
}

// cmdCopyTargets puts the marked messages — or the one under the cursor — on
// the clipboard.
//
// It copies text, not the rendered rows: the timestamp and the attachment
// badge are furniture, and pasting them into a bug report or another chat is
// noise. Several messages get their author prefixed, because a run of
// messages pasted without names is unreadable; a single message does not,
// because the user copying one line wants that line.
func (a App) cmdCopyTargets() (App, tea.Cmd, bool) {
	targets := a.thread.Targets()
	if len(targets) == 0 {
		return a, nil, false
	}
	var text string
	if len(targets) == 1 {
		text = targets[0].Text
	} else {
		text = thread.MarkedText(targets, a.thread.AuthorLabel)
	}
	if text == "" {
		return a, nil, false
	}
	a.thread = a.thread.ClearMarks()
	a.status = a.status.SetNotice(fmt.Sprintf("copied %s", plural(len(targets), "message", "messages")))
	return a, tea.SetClipboard(text), true
}

// cmdEditTarget arms the composer to rewrite the message under the cursor.
//
// Only the user's own message is offered, and the refusal is local: the check
// is a field the mirror already holds, so telling the user "not yours" costs
// nothing, while sending the request would cost a round trip to learn the
// same thing. Core repeats the check — it owns the rule — and this one exists
// so the answer is instant.
func (a App) cmdEditTarget() (App, tea.Cmd, bool) {
	msg, ok := a.thread.CursorMessage()
	if !ok {
		return a, nil, false
	}
	if !msg.Outgoing {
		a.status = a.status.SetNotice("only your own messages can be edited")
		return a, nil, true
	}
	if msg.Media != nil && msg.Text == "" {
		// An attachment with no caption has nothing this composer can
		// edit: Telegram would take the request as a caption change,
		// which is a different feature and not one this client has.
		a.status = a.status.SetNotice("nothing to edit — that message is an attachment")
		return a, nil, true
	}
	chatID := a.thread.ChatID()
	a = a.setFocus(FocusInput)
	return a, func() tea.Msg {
		// The composer gets the message the way it was written, markup
		// and all, so a bold word survives a typo fix.
		return input.StartEditMsg{ChatID: chatID, MessageID: msg.ID, Text: markdown.Render(msg.Text, msg.Entities)}
	}, true
}

// cmdDeleteTargets opens the confirmation for the marked messages, or the one
// under the cursor.
//
// Nothing is deleted here. The modal is not a formality: this is the only
// gesture in the client that destroys something on other people's devices,
// and it is two keystrokes away from the arrow keys.
func (a App) cmdDeleteTargets() (App, tea.Cmd, bool) {
	targets := a.thread.Targets()
	if len(targets) == 0 {
		return a, nil, false
	}
	chatID := a.thread.ChatID()
	if chatID == 0 {
		return a, nil, false
	}
	ids := make([]int64, 0, len(targets))
	mine := true
	for _, msg := range targets {
		if msg.ID == 0 {
			// An optimistic row has no server id yet. Deleting it would
			// mean cancelling a send, which is a different operation.
			continue
		}
		ids = append(ids, msg.ID)
		if !msg.Outgoing {
			mine = false
		}
	}
	if len(ids) == 0 {
		a.status = a.status.SetNotice("nothing to delete — that message has not been sent yet")
		return a, nil, true
	}

	revocable := a.thread.CanRevokeDeletes()
	a.pendingDelete = pendingDelete{chatID: chatID, ids: ids, revocable: revocable}
	a.confirm = a.confirm.Show(
		fmt.Sprintf("Delete %s?", plural(len(ids), "message", "messages")),
		deleteChoices(revocable, mine),
	)
	return a, nil, true
}

// deleteChoices builds the answers the modal offers.
//
// "For everyone" comes first when the messages are the user's own, because
// that is what they almost always mean — a message deleted only on your own
// screen is still on the other person's. When the set includes somebody
// else's message the order flips: Telegram often refuses to revoke those, and
// leading with an answer the server will reject is a worse first impression
// than leading with the one that always works.
func deleteChoices(revocable, mine bool) []overlay.Choice {
	forMe := overlay.Choice{Key: "m", Label: "delete for me only"}
	forAll := overlay.Choice{Key: "e", Label: "delete for everyone", Destructive: true}
	switch {
	case !revocable:
		return []overlay.Choice{{Key: "d", Label: "delete", Destructive: true}}
	case mine:
		return []overlay.Choice{forAll, forMe}
	default:
		return []overlay.Choice{forMe, forAll}
	}
}

// handleConfirmKey routes a key while the confirmation modal is up. Every key
// that is not one of the offered answers dismisses it.
func (a App) handleConfirmKey(k tea.KeyPressMsg) (App, tea.Cmd) {
	choice, ok := a.confirm.Resolve(k.String())
	a.confirm = a.confirm.Hide()
	if !ok {
		a.pendingDelete = pendingDelete{}
		return a, nil
	}
	pending := a.pendingDelete
	a.pendingDelete = pendingDelete{}
	revoke := choice.Key == "e" || (choice.Key == "d" && !pending.revocable)
	a.thread = a.thread.ClearMarks()
	return a, a.cmdDelete(pending, revoke)
}

// cmdDelete performs the deletion off the UI goroutine.
func (a App) cmdDelete(pending pendingDelete, revoke bool) tea.Cmd {
	if a.actions == nil {
		return func() tea.Msg {
			return messageActionsResultMsg{err: errOffline, what: "delete"}
		}
	}
	actions := a.actions
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		err := actions.Delete(ctx, pending.chatID, pending.ids, revoke)
		return messageActionsResultMsg{err: err, what: "delete", count: len(pending.ids)}
	}
}

// cmdSubmitEdit performs an edit off the UI goroutine.
func (a App) cmdSubmitEdit(msg input.EditSubmittedMsg) tea.Cmd {
	if a.actions == nil {
		return func() tea.Msg {
			return messageActionsResultMsg{err: errOffline, what: "edit"}
		}
	}
	actions := a.actions
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		err := actions.Edit(ctx, msg.ChatID, msg.MessageID, msg.Text)
		return messageActionsResultMsg{err: err, what: "edit"}
	}
}

// applyActionResult puts the outcome in the status bar.
func (a App) applyActionResult(msg messageActionsResultMsg) App {
	if msg.err != nil {
		a.log.Warn("message action failed", "what", msg.what, "err", msg.err)
		a.status = a.status.SetNotice(fmt.Sprintf("%s failed: %v", msg.what, msg.err))
		return a
	}
	switch msg.what {
	case "delete":
		a.status = a.status.SetNotice(fmt.Sprintf("deleted %s", plural(msg.count, "message", "messages")))
	default:
		a.status = a.status.SetNotice("message edited")
	}
	return a
}

// applyMessageEdited redraws the one row an edit changed.
func (a App) applyMessageEdited(ev events.MessageEdited) App {
	if a.thread.ChatID() != ev.ChatID {
		return a
	}
	a.thread = a.thread.ApplyEdit(ev.MessageID, ev.Text, ev.Entities, ev.EditDate)
	return a
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// errOffline is what every action reports when no service is wired. A
// sentence rather than a sentinel: it reaches the user directly.
var errOffline = fmt.Errorf("not connected")

// ensure the domain import is used even if the file is trimmed later.
var _ = domain.Message{}

// applyMessageActionKey routes the thread-only chords that act on messages.
//
// Every one of them is gated on the thread having focus, and for a reason
// stronger than tidiness: they are bare letters. In the composer "d" is a
// letter somebody is typing, and a client that eats it to open a deletion
// dialog is worse than one with no shortcut at all. The chats filter is
// excluded for the same reason.
func (a App) applyMessageActionKey(k tea.KeyPressMsg) (App, tea.Cmd, bool) {
	if a.focus != FocusThread || a.chats.IsFilterActive() {
		return a, nil, false
	}
	switch {
	case key.Matches(k, a.keymap.CopyMessage):
		return a.cmdCopyTargets()
	case key.Matches(k, a.keymap.CopyLink):
		return a.cmdCopyLink()
	case key.Matches(k, a.keymap.PressButton):
		return a.cmdPressButton()
	case key.Matches(k, a.keymap.EditMessage):
		return a.cmdEditTarget()
	case key.Matches(k, a.keymap.DeleteMsg):
		return a.cmdDeleteTargets()
	case key.Matches(k, a.keymap.JumpToReply):
		return a.cmdJumpToParent()
	case key.Matches(k, a.keymap.JumpBack):
		return a.cmdJumpBack()
	case key.Matches(k, a.keymap.ReactMessage):
		return a.cmdReactToCursor()
	case key.Matches(k, a.keymap.ForwardMessage):
		return a.cmdForwardTargets()
	case key.Matches(k, a.keymap.ShowImage):
		return a.cmdToggleInlineImage()
	}
	return a, nil, false
}

// applyFolderKey routes the folder tabs.
//
// Unlike the message actions these are live in the chats pane as well as the
// thread: switching folders is a navigation gesture, and needing to focus a
// particular pane first would make it feel like a mode. The composer is still
// excluded — there the brackets are punctuation somebody is typing.
func (a App) applyFolderKey(k tea.KeyPressMsg) (App, tea.Cmd, bool) {
	if a.focus == FocusInput || a.chats.IsFilterActive() {
		return a, nil, false
	}
	switch {
	case key.Matches(k, a.keymap.NextFolder):
		updated, cmd := a.chats.NextFolder()
		a.chats = updated
		return a, cmd, true
	case key.Matches(k, a.keymap.PrevFolder):
		updated, cmd := a.chats.PrevFolder()
		a.chats = updated
		return a, cmd, true
	}
	return a, nil, false
}

// cmdOpenEmojiPicker asks the app to show the picker. Routed as a message
// rather than flipping the field here so the gesture behaves like every other
// overlay open and is testable from outside.
func cmdOpenEmojiPicker() tea.Cmd {
	return func() tea.Msg { return openEmojiPickerMsg{} }
}

type openEmojiPickerMsg struct{}

// insertEmoji puts a picked character into the composer and leaves the focus
// there, because the next thing the user does is keep typing.
func (a App) insertEmoji(char string) (tea.Model, tea.Cmd) {
	if char == "" {
		return a, nil
	}
	updated, cmd := a.input.Update(input.InsertTextMsg{Text: char})
	a.input = updated
	return a.setFocus(FocusInput), cmd
}

// applyQuickChatKey routes Alt+1 … Alt+9 to the nth chat in the list.
//
// The chat list is where a TUI spends its keystrokes: cycling to the sixth
// conversation costs six presses, and the six chats a person actually uses
// are almost always at the top of a frecency- and recency-ordered list. Alt
// rather than a bare digit because a bare digit is something people type, and
// a client that swallows one is worse than a client without the shortcut.
//
// Not routed through the keymap: nine bindings that differ only by their
// digit would be nine near-identical entries in the file and in the help, to
// express one rule. The modifier is what a user would want to change, and
// that is a smaller thing to add later than nine bindings are to remove.
//
// Live from the composer as well, like alt+n and alt+p: opening a chat puts
// the focus there, so a shortcut that stops at the composer works exactly
// once — the first live run pressed alt+2, then alt+4, and the second one
// went nowhere. Alt+digit is not something a person types, so nothing is
// swallowed. The chat-list filter is the one place it stays out of: there
// the digit may well be part of what is being searched for.
func (a App) applyQuickChatKey(k tea.KeyPressMsg) (App, tea.Cmd, bool) {
	if a.chats.IsFilterActive() {
		return a, nil, false
	}
	if k.Mod != tea.ModAlt {
		return a, nil, false
	}
	r := k.Code
	if r < '1' || r > '9' {
		return a, nil, false
	}
	updated, cmd, ok := a.chats.SelectNth(int(r - '0'))
	if !ok {
		// The list is shorter than the number pressed. Saying nothing
		// would look like a missed keypress.
		a.status = a.status.SetNotice("no chat there")
		return a, nil, true
	}
	a.chats = updated
	return a, cmd, true
}
