package input

import (
	"testing"

	"github.com/kar43lov/lazytg/internal/core/events"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

func composerInChat(t *testing.T, chatID int64) Model {
	t.Helper()
	m := New()
	m, _ = m.Update(SetChatMsg{ChatID: chatID})
	return m
}

// The defect this exists for: type half a sentence to one person, switch
// chats to check something, press Enter — and it goes to somebody else.
func TestDraft_DoesNotFollowTheUserIntoAnotherChat(t *testing.T) {
	t.Parallel()

	m := composerInChat(t, 1)
	m.textarea.SetValue("see you at six")
	m, _ = m.Update(SetChatMsg{ChatID: 2})

	if got := m.textarea.Value(); got != "" {
		t.Fatalf("the composer still holds %q in the other chat", got)
	}
}

func TestDraft_ComesBackWithItsChat(t *testing.T) {
	t.Parallel()

	m := composerInChat(t, 1)
	m.textarea.SetValue("see you at six")
	m, _ = m.Update(SetChatMsg{ChatID: 2})
	m.textarea.SetValue("different conversation")
	m, _ = m.Update(SetChatMsg{ChatID: 1})

	if got := m.textarea.Value(); got != "see you at six" {
		t.Fatalf("the composer holds %q, want the draft back", got)
	}
	// And the other chat kept its own.
	m, _ = m.Update(SetChatMsg{ChatID: 2})
	if got := m.textarea.Value(); got != "different conversation" {
		t.Fatalf("the second chat holds %q", got)
	}
}

// "Replying to X" is part of what was being written; restoring the words
// without it would arm the wrong reply.
func TestDraft_KeepsTheReplyPointer(t *testing.T) {
	t.Parallel()

	m := composerInChat(t, 1)
	m.textarea.SetValue("yes, that one")
	parent := &domain.Message{ID: 77, ChatID: 1, Text: "which one?"}
	m, _ = m.Update(SetReplyMsg{Msg: parent})

	m, _ = m.Update(SetChatMsg{ChatID: 2})
	if m.replyTo != nil {
		t.Fatalf("the reply followed the user: %+v", m.replyTo)
	}
	m, _ = m.Update(SetChatMsg{ChatID: 1})
	if m.replyTo == nil || m.replyTo.ID != 77 {
		t.Fatalf("the reply did not come back: %+v", m.replyTo)
	}
}

// Deleting what you wrote is you saying you no longer want it.
func TestDraft_EmptyingTheBoxForgetsTheDraft(t *testing.T) {
	t.Parallel()

	m := composerInChat(t, 1)
	m.textarea.SetValue("never mind")
	m, _ = m.Update(SetChatMsg{ChatID: 2})
	m, _ = m.Update(SetChatMsg{ChatID: 1})
	m.textarea.SetValue("")
	m, _ = m.Update(SetChatMsg{ChatID: 2})
	m, _ = m.Update(SetChatMsg{ChatID: 1})

	if got := m.textarea.Value(); got != "" {
		t.Fatalf("a deleted draft came back as %q", got)
	}
	if got := m.DraftFor(1); got != "" {
		t.Fatalf("the deleted draft is still stashed: %q", got)
	}
}

// Re-binding to the same chat happens on reconnects and same-chat search
// jumps; it must not disturb what is being written.
func TestDraft_SameChatRebindLeavesTheBoxAlone(t *testing.T) {
	t.Parallel()

	m := composerInChat(t, 1)
	m.textarea.SetValue("still writing")
	m, _ = m.Update(SetChatMsg{ChatID: 1})

	if got := m.textarea.Value(); got != "still writing" {
		t.Fatalf("a same-chat rebind changed the box to %q", got)
	}

	// Nor an armed edit. A reconnect or a same-chat jump re-binds the
	// composer, and cancelling the rewrite the user is in the middle of
	// would throw their words away for no reason.
	m, _ = m.Update(StartEditMsg{ChatID: 1, MessageID: 5, Text: "the old text"})
	m, _ = m.Update(SetChatMsg{ChatID: 1})
	if !m.Editing() {
		t.Fatal("a same-chat rebind cancelled an armed edit")
	}
	if got := m.textarea.Value(); got != "the old text" {
		t.Fatalf("the edit lost its text: %q", got)
	}
}

// An edit is not a draft: leaving the mode armed across a switch would point
// Enter at a message in a conversation no longer on screen.
func TestDraft_SwitchingCancelsAnArmedEdit(t *testing.T) {
	t.Parallel()

	m := composerInChat(t, 1)
	m.textarea.SetValue("half a sentence")
	m, _ = m.Update(StartEditMsg{ChatID: 1, MessageID: 5, Text: "the old text"})
	if !m.Editing() {
		t.Fatal("setup: the edit was not armed")
	}

	m, _ = m.Update(SetChatMsg{ChatID: 2})
	if m.Editing() {
		t.Fatal("the edit mode followed the user into another chat")
	}
	if got := m.textarea.Value(); got != "" {
		t.Fatalf("the other chat's composer holds %q", got)
	}
	// And the draft the edit displaced is what comes back, not the
	// message that was being rewritten.
	m, _ = m.Update(SetChatMsg{ChatID: 1})
	if got := m.textarea.Value(); got != "half a sentence" {
		t.Fatalf("the composer holds %q, want the displaced draft", got)
	}
}

// A draft the server holds — typed on the phone, or left there before this
// session — lands in an empty composer, waits under its chat when that chat
// is not open, and never replaces or clears what was typed here.
func TestDraft_FromTheServer(t *testing.T) {
	t.Parallel()

	m := composerInChat(t, 1)
	m, _ = m.Update(events.DraftChanged{ChatID: 1, Text: "finish this **here**"})
	if got := m.textarea.Value(); got != "finish this **here**" {
		t.Fatalf("empty composer holds %q", got)
	}
	m, _ = m.Update(events.DraftChanged{ChatID: 1, Text: "something else"})
	if got := m.textarea.Value(); got != "finish this **here**" {
		t.Fatalf("a later server draft replaced the words: %q", got)
	}
	m, _ = m.Update(events.DraftChanged{ChatID: 1, Text: ""})
	if got := m.textarea.Value(); got != "finish this **here**" {
		t.Fatalf("a cleared server draft emptied the composer: %q", got)
	}

	m, _ = m.Update(events.DraftChanged{ChatID: 2, Text: "for the other chat"})
	if got := m.textarea.Value(); got != "finish this **here**" {
		t.Fatalf("another chat's draft landed in this composer: %q", got)
	}
	m, _ = m.Update(SetChatMsg{ChatID: 2})
	if got := m.textarea.Value(); got != "for the other chat" {
		t.Fatalf("the stashed server draft did not come back: %q", got)
	}
	m.textarea.SetValue("typed here")
	m, _ = m.Update(SetChatMsg{ChatID: 1})
	m, _ = m.Update(events.DraftChanged{ChatID: 2, Text: "phone again"})
	m, _ = m.Update(SetChatMsg{ChatID: 2})
	if got := m.textarea.Value(); got != "typed here" {
		t.Fatalf("a server draft replaced a local one: %q", got)
	}
}
