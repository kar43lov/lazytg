package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
	"github.com/kar43lov/lazytg/internal/ui/panes/chats"
)

// The chat-list chords act on the highlighted chat without opening it, the
// way a right-click does in a graphical client: mute it, pin it, flag it to
// come back to or clear the flag. Each is one request on one keypress over
// a chat that already exists. They are live only while the list has the
// focus and its filter is closed — the same rule the thread's letters obey,
// and for the same reason: nothing there is typing.

// chatActionMsg reports the outcome of one chat-level action.
type chatActionMsg struct {
	chatID int64
	done   string
	err    error
}

// applyChatActionKey routes m, p and u while the chat list has the focus.
func (a App) applyChatActionKey(k tea.KeyPressMsg) (App, tea.Cmd, bool) {
	if a.focus != FocusChats || a.chats.IsFilterActive() {
		return a, nil, false
	}
	var (
		matched bool
		verb    string
		act     func(ctx context.Context, item chats.ChatItem) (string, error)
	)
	switch {
	case key.Matches(k, a.keymap.MuteChat):
		matched, verb = true, "mute"
		act = a.toggleMute
	case key.Matches(k, a.keymap.PinChat):
		matched, verb = true, "pin"
		act = a.togglePin
	case key.Matches(k, a.keymap.ToggleUnread):
		matched, verb = true, "mark"
		act = a.toggleUnread
	}
	if !matched {
		return a, nil, false
	}
	item, ok := a.chats.SelectedItem()
	if !ok {
		return a, nil, true
	}
	if a.actions == nil {
		a.status = a.status.SetNotice("not connected — cannot " + verb)
		return a, nil, true
	}
	return a, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		done, err := act(ctx, item)
		return chatActionMsg{chatID: item.ID(), done: done, err: err}
	}, true
}

// toggleMute mutes for good, or unmutes. "For good" rather than for an
// hour or a day, because a chat somebody silences from a keyboard is one
// they mean to silence; the timed variants are a phone's affordance.
func (a App) toggleMute(ctx context.Context, item chats.ChatItem) (string, error) {
	if item.Muted(time.Now()) {
		return "unmuted", a.actions.Mute(ctx, item.ID(), time.Time{})
	}
	return "muted", a.actions.Mute(ctx, item.ID(), time.Date(2038, 1, 19, 3, 14, 7, 0, time.UTC))
}

func (a App) togglePin(ctx context.Context, item chats.ChatItem) (string, error) {
	if item.Pinned() {
		return "unpinned", a.actions.Pin(ctx, item.ID(), false)
	}
	return "pinned", a.actions.Pin(ctx, item.ID(), true)
}

// toggleUnread clears whatever says "unread" — the count, the dot — or,
// when nothing does, puts the dot on.
func (a App) toggleUnread(ctx context.Context, item chats.ChatItem) (string, error) {
	if item.UnreadCount() > 0 || item.UnreadMark() {
		return "marked read", a.actions.MarkRead(ctx, item.ID(), item.UnreadMark())
	}
	return "marked unread", a.actions.MarkUnread(ctx, item.ID())
}

// applyChatAction reports the outcome. The list itself reloads from the
// DialogUpdated the service publishes, so nothing here touches it.
func (a App) applyChatAction(msg chatActionMsg) App {
	if msg.err != nil {
		a.status = a.status.SetNotice("could not change the chat: " + msg.err.Error())
		return a
	}
	a.status = a.status.SetNotice(msg.done)
	return a
}

// presenceLabel is what the status bar says about the person on the other
// side of the open private chat. Read off the chat list at draw time rather
// than kept: the list is what the live path updates, and a copy here would
// lag it by exactly the reload the reader is looking at.
func (a App) presenceLabel() string {
	item, ok := a.chats.ItemByID(a.thread.ChatID())
	if !ok || item.Type() != domain.ChatTypePrivate {
		return ""
	}
	return presenceText(item.Online(), item.LastSeen(), time.Now())
}

// presenceText words a presence the way Telegram's clients do.
func presenceText(online bool, lastSeen, now time.Time) string {
	switch {
	case online:
		return "online"
	case lastSeen.IsZero():
		return ""
	}
	lastSeen, now = lastSeen.Local(), now.Local()
	sameDay := lastSeen.Year() == now.Year() && lastSeen.YearDay() == now.YearDay()
	switch {
	case sameDay:
		return "last seen at " + lastSeen.Format("15:04")
	case now.Sub(lastSeen) < 48*time.Hour:
		return "last seen yesterday at " + lastSeen.Format("15:04")
	case now.Sub(lastSeen) < 7*24*time.Hour:
		return "last seen " + lastSeen.Format("Mon 15:04")
	default:
		return "last seen " + lastSeen.Format("02.01.06")
	}
}

// notifyEnv is the switch for the terminal bell: "off" silences it. The
// bell is the one notification every terminal, multiplexer and desktop
// already knows how to route — Ghostty bounces the dock, tmux flags the
// window, iTerm posts a banner — without this program naming any of them.
const notifyEnv = "LAZYTG_NOTIFY"

// ringCmd rings the terminal bell for a message worth being told about:
// somebody else's, new rather than edited, in a chat the reader is not
// looking at and has not muted. The check is one map lookup; the bell is
// one byte.
func (a App) ringCmd(ev events.MessageReceived) tea.Cmd {
	if !shouldRing(ev, a.thread.ChatID(), a.chats, os.Getenv(notifyEnv)) {
		return nil
	}
	return tea.Raw("\a")
}

func shouldRing(ev events.MessageReceived, openChat int64, list chats.Model, setting string) bool {
	if strings.EqualFold(setting, "off") {
		return false
	}
	if ev.Outgoing || ev.Edited || ev.ChatID == 0 || ev.ChatID == openChat {
		return false
	}
	if item, ok := list.ItemByID(ev.ChatID); ok && item.Muted(time.Now()) {
		return false
	}
	return true
}

// windowTitle is the OSC 2 escape that names the terminal tab: the badge
// in parentheses while there is one, the bare name otherwise.
func windowTitle(unread int) string {
	title := "lazytg"
	if unread > 0 {
		title = fmt.Sprintf("lazytg (%d)", unread)
	}
	return "\x1b]2;" + title + "\x07"
}
