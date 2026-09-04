package app

import (
	"strings"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/events"
	"github.com/kar43lov/lazytg/internal/ui/panes/chats"
)

func TestTyping_ShowsWhoIsDoingWhat(t *testing.T) {
	t.Parallel()

	a := newAppForActions(t, &fakeActions{})
	model, cmd := a.Update(events.PeerTyping{
		ChatID: 42, FromID: 7, Action: "recording a voice message", At: time.Now(),
	})
	a = model.(App)

	if !strings.Contains(a.statusText(), "recording a voice message") {
		t.Fatalf("status reads %q", a.statusText())
	}
	// A sweep has to be armed, or the indicator never goes away: Telegram's
	// "stopped" notification is not reliably sent.
	if cmd == nil {
		t.Fatal("no expiry was scheduled")
	}
}

// A status line announcing activity in a conversation the user is not reading
// is a distraction; the chat list already moves when something arrives.
func TestTyping_IgnoresOtherChats(t *testing.T) {
	t.Parallel()

	a := newAppForActions(t, &fakeActions{})
	model, _ := a.Update(events.PeerTyping{ChatID: 999, FromID: 7, Action: "typing", At: time.Now()})
	a = model.(App)

	if strings.Contains(a.statusText(), "typing") {
		t.Fatalf("another chat's typing reached the status line: %q", a.statusText())
	}
}

func TestTyping_ExpiresOnItsOwn(t *testing.T) {
	t.Parallel()

	a := newAppForActions(t, &fakeActions{})
	// Arrived long enough ago that it is already stale — the same state the
	// sweep finds when somebody stops writing without saying so.
	model, _ := a.Update(events.PeerTyping{
		ChatID: 42, FromID: 7, Action: "typing", At: time.Now().Add(-2 * typingTTL),
	})
	a = model.(App)

	model, cmd := a.Update(typingSweepMsg{})
	a = model.(App)

	if strings.Contains(a.statusText(), "typing") {
		t.Fatalf("a stale indicator survived the sweep: %q", a.statusText())
	}
	// And the sweep stops rather than ticking forever over an empty map.
	if cmd != nil {
		t.Fatal("the sweep re-armed itself with nothing left to expire")
	}
}

func TestTyping_RefreshKeepsItAlive(t *testing.T) {
	t.Parallel()

	a := newAppForActions(t, &fakeActions{})
	model, _ := a.Update(events.PeerTyping{
		ChatID: 42, FromID: 7, Action: "typing", At: time.Now().Add(-2 * typingTTL),
	})
	a = model.(App)
	model, _ = a.Update(events.PeerTyping{ChatID: 42, FromID: 7, Action: "typing", At: time.Now()})
	a = model.(App)

	model, _ = a.Update(typingSweepMsg{})
	a = model.(App)
	if !strings.Contains(a.statusText(), "typing") {
		t.Fatalf("a refreshed indicator was swept away: %q", a.statusText())
	}
}

// In a busy group the names would take the whole line and change every
// second, so several people are counted rather than listed.
func TestTyping_CountsSeveralPeople(t *testing.T) {
	t.Parallel()

	a := newAppForActions(t, &fakeActions{})
	now := time.Now()
	for _, id := range []int64{7, 8, 9} {
		model, _ := a.Update(events.PeerTyping{ChatID: 42, FromID: id, Action: "typing", At: now})
		a = model.(App)
	}
	if !strings.Contains(a.statusText(), "3 people are typing") {
		t.Fatalf("status reads %q", a.statusText())
	}
}

// Only one sweep may be in flight, or a busy conversation doubles its timers
// with every notification.
func TestTyping_ArmsOneSweepForAConversation(t *testing.T) {
	t.Parallel()

	a := newAppForActions(t, &fakeActions{})
	now := time.Now()
	model, first := a.Update(events.PeerTyping{ChatID: 42, FromID: 7, Action: "typing", At: now})
	a = model.(App)
	if first == nil {
		t.Fatal("the first notification armed no sweep")
	}
	_, second := a.Update(events.PeerTyping{ChatID: 42, FromID: 8, Action: "typing", At: now})
	if second != nil {
		t.Fatal("a second notification armed a second sweep")
	}
}

// The indicator belongs to the conversation that was on screen.
func TestTyping_ClearedByAChatSwitch(t *testing.T) {
	t.Parallel()

	a := newAppForActions(t, &fakeActions{})
	model, _ := a.Update(events.PeerTyping{ChatID: 42, FromID: 7, Action: "typing", At: time.Now()})
	a = model.(App)

	model, _ = a.Update(chats.ChatSelectedMsg{ChatID: 77})
	a = model.(App)

	if strings.Contains(a.statusText(), "typing") {
		t.Fatalf("the indicator followed the user into another chat: %q", a.statusText())
	}
}
