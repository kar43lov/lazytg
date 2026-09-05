package app

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/events"
)

// "typing…" — the cheapest thing a chat client can do to stop feeling like a
// mailbox.
//
// Two decisions carry the feature. The indicator expires on a timer rather
// than on a "stopped" notification: Telegram sends one only sometimes, and a
// client that waited for it would leave somebody typing forever. And it is
// shown only for the chat on screen — a status line announcing activity in a
// conversation the user is not reading is a distraction, and the chat list
// already moves when something actually arrives.

// typingTTL is how long an indicator survives without a refresh.
//
// Telegram's clients repeat the notification every few seconds while somebody
// keeps writing, so anything past that is somebody who stopped. Six seconds
// is what the official clients use; shorter makes the indicator flicker
// between repeats, longer leaves it lying.
const typingTTL = 6 * time.Second

// typingSweep is how often the app looks for an expired indicator. It runs
// only while one is showing.
const typingSweep = time.Second

// typingState is who is doing what in the open chat.
type typingState struct {
	action string
	until  time.Time
}

// typingSweepMsg is the timer that clears a stale indicator.
type typingSweepMsg struct{}

// applyTyping records an activity notification and arms the sweep.
func (a App) applyTyping(ev events.PeerTyping) (App, tea.Cmd) {
	if ev.ChatID != a.thread.ChatID() {
		return a, nil
	}
	at := ev.At
	if at.IsZero() {
		at = time.Now()
	}
	already := len(a.typing) > 0
	next := make(map[int64]typingState, len(a.typing)+1)
	for k, v := range a.typing {
		next[k] = v
	}
	next[ev.FromID] = typingState{action: ev.Action, until: at.Add(typingTTL)}
	a.typing = next
	a.status = a.status.SetTyping(a.typingLine())

	if already {
		// A sweep is already running; a second one would double the
		// timers for as long as the conversation stays busy.
		return a, nil
	}
	return a, tea.Tick(typingSweep, func(time.Time) tea.Msg { return typingSweepMsg{} })
}

// sweepTyping drops expired indicators and re-arms itself while any remain.
func (a App) sweepTyping() (App, tea.Cmd) {
	if len(a.typing) == 0 {
		return a, nil
	}
	now := time.Now()
	next := make(map[int64]typingState, len(a.typing))
	for k, v := range a.typing {
		if v.until.After(now) {
			next[k] = v
		}
	}
	if len(next) == 0 {
		a.typing = nil
		a.status = a.status.SetTyping("")
		return a, nil
	}
	a.typing = next
	a.status = a.status.SetTyping(a.typingLine())
	return a, tea.Tick(typingSweep, func(time.Time) tea.Msg { return typingSweepMsg{} })
}

// clearTyping drops every indicator. Called on a chat switch: the notification
// belongs to the conversation that was on screen.
func (a App) clearTyping() App {
	a.typing = nil
	a.status = a.status.SetTyping("")
	return a
}

// typingLine is what the status bar shows, or "" when nobody is doing
// anything.
//
// One person is named by what they are doing, because that is the useful part
// — "recording a voice message" tells the reader to wait rather than to keep
// writing. Several are counted rather than listed: in a busy group the names
// would take the whole line and change every second.
func (a App) typingLine() string {
	live := 0
	action := ""
	now := time.Now()
	for _, v := range a.typing {
		if !v.until.After(now) {
			continue
		}
		live++
		action = v.action
	}
	switch live {
	case 0:
		return ""
	case 1:
		return action + "…"
	default:
		return fmt.Sprintf("%d people are typing…", live)
	}
}
