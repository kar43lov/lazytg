package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
	"github.com/kar43lov/lazytg/internal/ui/overlay"
)

func TestReactKey_OpensThePickerAndSendsNothingYet(t *testing.T) {
	t.Parallel()

	actions := &fakeActions{}
	a := newAppForActions(t, actions)

	a, _ = press(t, a, "r")
	if !a.emojiPicker.Visible() {
		t.Fatal("the picker did not open")
	}
	if len(actions.reactCalls()) != 0 {
		t.Fatal("a reaction went out before one was picked")
	}
	if !strings.Contains(a.statusText(), "pick a reaction") {
		t.Fatalf("nothing said what the picker is for: %q", a.statusText())
	}
}

func TestReact_SendsThePickedEmojiForTheCursorMessage(t *testing.T) {
	t.Parallel()

	actions := &fakeActions{}
	a := newAppForActions(t, actions)
	a, _ = press(t, a, "r")

	model, cmd := a.Update(overlay.EmojiPickedMsg{Char: "🔥"})
	a = model.(App)
	if cmd == nil {
		t.Fatal("picking produced no command")
	}
	model, _ = a.Update(cmd())
	a = model.(App)

	calls := actions.reactCalls()
	if len(calls) != 1 {
		t.Fatalf("reacted %d times, want 1", len(calls))
	}
	if calls[0].chatID != 42 || calls[0].messageID != 3 || calls[0].emoticon != "🔥" {
		t.Fatalf("call = %+v, want the cursor message with 🔥", calls[0])
	}
	// The picked emoji must not also land in the composer.
	if strings.Contains(a.input.Value(), "🔥") {
		t.Fatalf("the reaction was typed into the message: %q", a.input.Value())
	}
}

// Picking what you already have takes it back — that is what the boxed
// reaction under the message is announcing, and it is what every client does.
func TestReact_SameEmojiAgainTakesItBack(t *testing.T) {
	t.Parallel()

	actions := &fakeActions{}
	a := newAppForActions(t, actions)
	a.thread = a.thread.ApplyReactions(3, []domain.Reaction{{Emoticon: "🔥", Count: 1, Chosen: true}})

	a, _ = press(t, a, "r")
	model, cmd := a.Update(overlay.EmojiPickedMsg{Char: "🔥"})
	a = model.(App)
	model, _ = a.Update(cmd())
	a = model.(App)

	calls := actions.reactCalls()
	if len(calls) != 1 {
		t.Fatalf("reacted %d times, want 1", len(calls))
	}
	// Removal is the same request with nothing in it.
	if calls[0].emoticon != "" {
		t.Fatalf("sent %q, want an empty reaction to clear it", calls[0].emoticon)
	}
}

func TestReact_DifferentEmojiReplacesIt(t *testing.T) {
	t.Parallel()

	actions := &fakeActions{}
	a := newAppForActions(t, actions)
	a.thread = a.thread.ApplyReactions(3, []domain.Reaction{{Emoticon: "🔥", Count: 1, Chosen: true}})

	a, _ = press(t, a, "r")
	model, cmd := a.Update(overlay.EmojiPickedMsg{Char: "👍"})
	a = model.(App)
	model, _ = a.Update(cmd())
	a = model.(App)

	if got := actions.reactCalls()[0].emoticon; got != "👍" {
		t.Fatalf("sent %q, want the new emoji", got)
	}
}

func TestReact_DismissingThePickerSendsNothing(t *testing.T) {
	t.Parallel()

	actions := &fakeActions{}
	a := newAppForActions(t, actions)
	a, _ = press(t, a, "r")

	model, _ := a.Update(overlay.EmojiClosedMsg{})
	a = model.(App)

	if len(actions.reactCalls()) != 0 {
		t.Fatal("a dismissed picker reacted anyway")
	}
	if a.pendingReaction != nil {
		t.Fatal("the pending reaction outlived its picker")
	}
}

// After a reaction the picker goes back to typing emoji into the composer.
func TestPicker_ReturnsToInsertingAfterAReaction(t *testing.T) {
	t.Parallel()

	actions := &fakeActions{}
	a := newAppForActions(t, actions)
	a, _ = press(t, a, "r")
	model, cmd := a.Update(overlay.EmojiPickedMsg{Char: "🔥"})
	a = model.(App)
	model, _ = a.Update(cmd())
	a = model.(App)

	model, _ = a.Update(overlay.EmojiPickedMsg{Char: "🚀"})
	a = model.(App)
	if !strings.Contains(a.input.Value(), "🚀") {
		t.Fatalf("the next pick did not reach the composer: %q", a.input.Value())
	}
	if len(actions.reactCalls()) != 1 {
		t.Fatalf("it reacted again: %d calls", len(actions.reactCalls()))
	}
}

func TestReact_ReportsAFailure(t *testing.T) {
	t.Parallel()

	actions := &fakeActions{err: errors.New("REACTION_INVALID")}
	a := newAppForActions(t, actions)
	a, _ = press(t, a, "r")
	model, cmd := a.Update(overlay.EmojiPickedMsg{Char: "🔥"})
	a = model.(App)
	model, _ = a.Update(cmd())
	a = model.(App)

	if !strings.Contains(a.statusText(), "REACTION_INVALID") {
		t.Fatalf("the failure never reached the user: %q", a.statusText())
	}
}

func TestReact_SaysWhenOffline(t *testing.T) {
	t.Parallel()

	a := newAppForActions(t, nil)
	a, _ = press(t, a, "r")
	if a.emojiPicker.Visible() {
		t.Fatal("the picker opened with nothing to react through")
	}
	if !strings.Contains(a.statusText(), "not connected") {
		t.Fatalf("status reads %q", a.statusText())
	}
}

// A reaction from another device arrives on the bus and redraws the row.
func TestReactionsChanged_RedrawsTheMessage(t *testing.T) {
	t.Parallel()

	a := newAppForActions(t, &fakeActions{})
	model, _ := a.Update(events.MessageReactionsChanged{
		ChatID: 42, MessageID: 3,
		Reactions: []domain.Reaction{{Emoticon: "👀", Count: 2}},
	})
	a = model.(App)

	if got := a.thread.ReactionsOf(3); len(got) != 1 || got[0].Count != 2 {
		t.Fatalf("reactions = %v", got)
	}
}

func TestReactionsChanged_IgnoresAnotherChat(t *testing.T) {
	t.Parallel()

	a := newAppForActions(t, &fakeActions{})
	model, _ := a.Update(events.MessageReactionsChanged{
		ChatID: 999, MessageID: 3,
		Reactions: []domain.Reaction{{Emoticon: "👀", Count: 2}},
	})
	a = model.(App)

	if got := a.thread.ReactionsOf(3); got != nil {
		t.Fatalf("another chat's reactions landed here: %v", got)
	}
}
