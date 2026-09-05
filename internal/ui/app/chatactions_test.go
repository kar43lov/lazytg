package app

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/ui/keymap"
	"github.com/kar43lov/lazytg/internal/ui/panes/chats"
)

// appWithChatRows builds an app over the given chat rows with the list in
// focus and a recording action service wired.
func appWithChatRows(t *testing.T, rows []domain.Chat) (App, *fakeActions) {
	t.Helper()
	pane := chats.NewWithRepo(fakeChatsRepo{chats: rows}, nil)
	pane, _ = pane.Update(pane.Init()())
	actions := &fakeActions{}
	a := New(Deps{Keymap: keymap.Default(), Chats: &pane, Actions: actions})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return model.(App), actions
}

// runChatKey presses the key and runs the command it returns, so the
// action's own message comes back through Update the way it would live.
func runChatKey(t *testing.T, a App, k tea.KeyPressMsg) App {
	t.Helper()
	updated, cmd, handled := a.applyChatActionKey(k)
	if !handled {
		t.Fatalf("%q was not handled by the chat list", k.String())
	}
	if cmd != nil {
		model, _ := updated.Update(cmd())
		return model.(App)
	}
	return updated
}

func TestChatActionKey_MuteTogglesAgainstWhatTheRowSays(t *testing.T) {
	t.Parallel()

	a, actions := appWithChatRows(t, []domain.Chat{
		{ID: 7, Title: "quiet", Type: domain.ChatTypePrivate, MutedUntil: time.Date(2038, 1, 1, 0, 0, 0, 0, time.UTC)},
	})
	a = runChatKey(t, a, keyChord('m', 0))
	calls := actions.chatCalls()
	if len(calls) != 1 || calls[0].kind != "mute" || calls[0].chatID != 7 || !calls[0].until.IsZero() {
		t.Fatalf("a muted chat was not unmuted: %+v", calls)
	}
	if !strings.Contains(a.statusText(), "unmuted") {
		t.Fatalf("status reads %q", a.statusText())
	}
}

func TestChatActionKey_PinAndUnreadReadTheRow(t *testing.T) {
	t.Parallel()

	a, actions := appWithChatRows(t, []domain.Chat{
		{ID: 7, Title: "busy", Type: domain.ChatTypePrivate, UnreadCount: 3, UnreadMark: true},
	})
	a = runChatKey(t, a, keyChord('p', 0))
	a = runChatKey(t, a, keyChord('u', 0))
	calls := actions.chatCalls()
	if len(calls) != 2 {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[0].kind != "pin" || !calls[0].flag {
		t.Fatalf("an unpinned chat was not pinned: %+v", calls[0])
	}
	if calls[1].kind != "read" || !calls[1].flag {
		t.Fatalf("a chat with unread and a dot was not marked read with the dot cleared: %+v", calls[1])
	}
	if !strings.Contains(a.statusText(), "marked read") {
		t.Fatalf("status reads %q", a.statusText())
	}
}

func TestChatActionKey_UnreadOnACleanChatSetsTheDot(t *testing.T) {
	t.Parallel()

	a, actions := appWithChatRows(t, []domain.Chat{{ID: 7, Title: "clean", Type: domain.ChatTypePrivate}})
	runChatKey(t, a, keyChord('u', 0))
	calls := actions.chatCalls()
	if len(calls) != 1 || calls[0].kind != "unread" {
		t.Fatalf("calls = %+v", calls)
	}
}

// The letters belong to the list. In the thread "p" follows a reply, and in
// the composer they are typed.
func TestChatActionKey_StaysInTheList(t *testing.T) {
	t.Parallel()

	a, actions := appWithChatRows(t, []domain.Chat{{ID: 7, Title: "clean", Type: domain.ChatTypePrivate}})
	for _, focus := range []FocusTarget{FocusInput, FocusThread} {
		b := a.setFocus(focus)
		if _, _, handled := b.applyChatActionKey(keyChord('m', 0)); handled {
			t.Fatalf("m fired with focus %v", focus)
		}
	}
	if len(actions.chatCalls()) != 0 {
		t.Fatalf("calls = %+v", actions.chatCalls())
	}
}

func TestChatActionKey_SaysWhenOffline(t *testing.T) {
	t.Parallel()

	pane := chats.NewWithRepo(fakeChatsRepo{chats: []domain.Chat{{ID: 7, Title: "x", Type: domain.ChatTypePrivate}}}, nil)
	pane, _ = pane.Update(pane.Init()())
	a := New(Deps{Keymap: keymap.Default(), Chats: &pane})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a = model.(App)
	a, _, handled := a.applyChatActionKey(keyChord('p', 0))
	if !handled || !strings.Contains(a.statusText(), "not connected") {
		t.Fatalf("handled=%v status=%q", handled, a.statusText())
	}
}

func TestPresenceText(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.Local)
	cases := []struct {
		online bool
		seen   time.Time
		want   string
	}{
		{true, time.Time{}, "online"},
		{false, time.Time{}, ""},
		{false, now.Add(-time.Hour), "last seen at 11:00"},
		{false, now.Add(-26 * time.Hour), "last seen yesterday at 10:00"},
		{false, now.Add(-3 * 24 * time.Hour), "last seen Wed 12:00"},
		{false, now.Add(-30 * 24 * time.Hour), "last seen 06.08.26"},
	}
	for _, tc := range cases {
		if got := presenceText(tc.online, tc.seen, now); got != tc.want {
			t.Errorf("presenceText(%v, %v) = %q, want %q", tc.online, tc.seen, got, tc.want)
		}
	}
}

// The status bar reads the presence off the chat list at draw time, so a
// person coming online shows without anything else being told.
func TestStatusBar_ShowsPresenceOfTheOpenChat(t *testing.T) {
	t.Parallel()

	a, _ := appWithChatRows(t, []domain.Chat{{ID: 7, Title: "Friend", Type: domain.ChatTypePrivate, Online: true}})
	model, _ := a.Update(chats.ChatSelectedMsg{ChatID: 7})
	a = model.(App)
	if !strings.Contains(a.View().Content, "Friend · online") {
		t.Fatalf("status bar does not say online:\n%s", a.View().Content)
	}
}
