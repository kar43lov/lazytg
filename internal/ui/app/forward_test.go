package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/kar43lov/lazytg/internal/ui/palette"
)

// Naming the messages and naming the destination are two acts. Doing both on
// one keypress is how a message ends up in the wrong conversation.
func TestForwardKey_AsksWhichChatAndSendsNothingYet(t *testing.T) {
	t.Parallel()

	actions := &fakeActions{}
	a := newAppForActions(t, actions)

	a, cmd := press(t, a, "f")
	if cmd == nil {
		t.Fatal("f produced no command")
	}
	model, _ := a.Update(cmd())
	a = model.(App)

	if !a.palette.Visible {
		t.Fatal("the chat picker did not open")
	}
	if len(actions.forwardCalls()) != 0 {
		t.Fatal("messages were forwarded before a chat was picked")
	}
	if !strings.Contains(a.statusText(), "pick a chat") {
		t.Fatalf("nothing told the user what the palette is for: %q", a.statusText())
	}
}

func TestForward_SendsTheCursorMessageToThePickedChat(t *testing.T) {
	t.Parallel()

	actions := &fakeActions{}
	a := newAppForActions(t, actions)
	a, _ = press(t, a, "f")

	model, cmd := a.Update(palette.SelectedMsg{ChatID: 99})
	a = model.(App)
	if cmd == nil {
		t.Fatal("picking a chat produced no command")
	}
	model, _ = a.Update(cmd())
	a = model.(App)

	calls := actions.forwardCalls()
	if len(calls) != 1 {
		t.Fatalf("forwarded %d times, want 1", len(calls))
	}
	if calls[0].from != 42 || calls[0].to != 99 {
		t.Fatalf("forwarded %d→%d, want 42→99", calls[0].from, calls[0].to)
	}
	if len(calls[0].ids) != 1 || calls[0].ids[0] != 3 {
		t.Fatalf("forwarded ids %v, want the message at the cursor (3)", calls[0].ids)
	}
	if a.palette.Visible {
		t.Fatal("the picker stayed open")
	}
}

// The marks are what forwarding was built for: several messages, one gesture.
func TestForward_SendsEveryMarkedMessage(t *testing.T) {
	t.Parallel()

	actions := &fakeActions{}
	a := newAppForActions(t, actions)
	a = cursorUp(t, a, 0)
	a, _ = press(t, a, " ")
	a = cursorUp(t, a, 1)
	a, _ = press(t, a, " ")

	a, _ = press(t, a, "f")
	model, cmd := a.Update(palette.SelectedMsg{ChatID: 99})
	a = model.(App)
	if cmd == nil {
		t.Fatal("picking a chat produced no command")
	}
	model, _ = a.Update(cmd())
	a = model.(App)

	calls := actions.forwardCalls()
	if len(calls) != 1 {
		t.Fatalf("forwarded %d times, want 1", len(calls))
	}
	if len(calls[0].ids) != 2 {
		t.Fatalf("forwarded %v, want both marked messages", calls[0].ids)
	}
	// The marks are spent once the forward is away, or the next gesture
	// acts on a set the user thinks they already used.
	if a.thread.MarkCount() != 0 {
		t.Fatalf("%d marks survived the forward", a.thread.MarkCount())
	}
}

// Dismissing the picker cancels the forward. The marks stay, because the user
// changing their mind about the destination has not changed their mind about
// the messages.
func TestForward_EscapingThePickerSendsNothing(t *testing.T) {
	t.Parallel()

	actions := &fakeActions{}
	a := newAppForActions(t, actions)
	a = cursorUp(t, a, 0)
	a, _ = press(t, a, " ")
	a, _ = press(t, a, "f")

	model, _ := a.Update(palette.ClosedMsg{})
	a = model.(App)

	if len(actions.forwardCalls()) != 0 {
		t.Fatal("a dismissed picker forwarded anyway")
	}
	if a.pendingForward != nil {
		t.Fatal("the pending forward outlived its picker")
	}
	if a.thread.MarkCount() == 0 {
		t.Fatal("the marks were dropped by a cancellation")
	}
}

// Picking the chat you are forwarding from is almost never meant — it is
// top of the frecency list, so it is the easiest mistake to make.
func TestForward_RefusesTheChatItCameFrom(t *testing.T) {
	t.Parallel()

	actions := &fakeActions{}
	a := newAppForActions(t, actions)
	a, _ = press(t, a, "f")

	model, cmd := a.Update(palette.SelectedMsg{ChatID: 42})
	a = model.(App)
	if cmd != nil {
		if msg := cmd(); msg != nil {
			model, _ = a.Update(msg)
			a = model.(App)
		}
	}
	if len(actions.forwardCalls()) != 0 {
		t.Fatal("forwarded a message into its own chat")
	}
	if !strings.Contains(a.statusText(), "forwarding from") {
		t.Fatalf("no explanation: %q", a.statusText())
	}
}

func TestForward_ReportsAFailure(t *testing.T) {
	t.Parallel()

	actions := &fakeActions{err: errors.New("CHAT_WRITE_FORBIDDEN")}
	a := newAppForActions(t, actions)
	a, _ = press(t, a, "f")

	model, cmd := a.Update(palette.SelectedMsg{ChatID: 99})
	a = model.(App)
	model, _ = a.Update(cmd())
	a = model.(App)

	if !strings.Contains(a.statusText(), "CHAT_WRITE_FORBIDDEN") {
		t.Fatalf("the failure never reached the user: %q", a.statusText())
	}
}

// Without a connection the key must say so rather than opening a picker that
// leads nowhere.
func TestForward_SaysWhenOffline(t *testing.T) {
	t.Parallel()

	a := newAppForActions(t, nil)
	a, cmd := press(t, a, "f")
	if cmd != nil {
		model, _ := a.Update(cmd())
		a = model.(App)
	}
	if a.palette.Visible {
		t.Fatal("the picker opened with nothing to forward through")
	}
	if !strings.Contains(a.statusText(), "not connected") {
		t.Fatalf("status reads %q", a.statusText())
	}
}

// An ordinary palette pick still switches chats — the purpose is what makes
// the difference, and it must not leak into the next open.
func TestPalette_StillSwitchesChatsAfterAForward(t *testing.T) {
	t.Parallel()

	actions := &fakeActions{}
	a := newAppForActions(t, actions)
	a, _ = press(t, a, "f")
	model, cmd := a.Update(palette.SelectedMsg{ChatID: 99})
	a = model.(App)
	if cmd != nil {
		model, _ = a.Update(cmd())
		a = model.(App)
	}

	model, _ = a.Update(palette.SelectedMsg{ChatID: 55})
	a = model.(App)
	if got := a.thread.ChatID(); got != 55 {
		t.Fatalf("thread is on chat %d, want the newly picked 55", got)
	}
	if len(actions.forwardCalls()) != 1 {
		t.Fatalf("the second pick forwarded too: %d calls", len(actions.forwardCalls()))
	}
}
