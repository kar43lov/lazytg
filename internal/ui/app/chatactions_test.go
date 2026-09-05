package app

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/events"
	coresync "github.com/kar43lov/lazytg/internal/core/sync"
	"github.com/kar43lov/lazytg/internal/ui/palette"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/ui/input"
	"github.com/kar43lov/lazytg/internal/ui/keymap"
	"github.com/kar43lov/lazytg/internal/ui/panes/chats"
	"github.com/kar43lov/lazytg/internal/ui/panes/thread"
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

// Opening a chat through the palette has to move the list's highlight too:
// the chords act on the highlighted row, and a row that is not the chat on
// screen is how somebody mutes the wrong conversation.
func TestPaletteSelection_MovesTheListHighlight(t *testing.T) {
	t.Parallel()

	a, _ := appWithChatRows(t, []domain.Chat{
		{ID: 1, Title: "first", Type: domain.ChatTypePrivate},
		{ID: 2, Title: "second", Type: domain.ChatTypePrivate},
	})
	model, _ := a.Update(palette.SelectedMsg{ChatID: 2})
	a = model.(App)
	if sel, ok := a.chats.SelectedItem(); !ok || sel.ID() != 2 {
		t.Fatalf("highlight on %+v, want chat 2", sel)
	}
}

func TestShouldRing(t *testing.T) {
	t.Parallel()

	a, _ := appWithChatRows(t, []domain.Chat{
		{ID: 1, Title: "loud", Type: domain.ChatTypePrivate},
		{ID: 2, Title: "quiet", Type: domain.ChatTypePrivate, MutedUntil: time.Now().Add(time.Hour)},
	})
	incoming := func(chat int64) events.MessageReceived { return events.MessageReceived{ChatID: chat, MessageID: 1} }
	cases := []struct {
		name    string
		ev      events.MessageReceived
		open    int64
		setting string
		want    bool
	}{
		{"another chat rings", incoming(1), 9, "", true},
		{"the open chat is silent", incoming(1), 1, "", false},
		{"a muted chat is silent", incoming(2), 9, "", false},
		{"an unknown chat rings", incoming(77), 9, "", true},
		{"own messages are silent", events.MessageReceived{ChatID: 1, MessageID: 1, Outgoing: true}, 9, "", false},
		{"edits are silent", events.MessageReceived{ChatID: 1, MessageID: 1, Edited: true}, 9, "", false},
		{"off is off", incoming(1), 9, "off", false},
		{"OFF is off too", incoming(1), 9, "OFF", false},
	}
	for _, tc := range cases {
		if got := shouldRing(tc.ev, tc.open, a.chats, tc.setting); got != tc.want {
			t.Errorf("%s: got %v", tc.name, got)
		}
	}
}

func TestWindowTitle(t *testing.T) {
	t.Parallel()

	if got := windowTitle(0); got != "\x1b]2;lazytg\x07" {
		t.Fatalf("no badge: %q", got)
	}
	if got := windowTitle(4); got != "\x1b]2;lazytg (4)\x07" {
		t.Fatalf("badge: %q", got)
	}
}

// The badge in the status bar and the title come from the list, so a chat
// read on the phone lowers both once the list reloads.
func TestStatusBar_UnreadTotalFromTheList(t *testing.T) {
	t.Parallel()

	a, _ := appWithChatRows(t, []domain.Chat{
		{ID: 1, Title: "a", Type: domain.ChatTypePrivate, UnreadCount: 3},
		{ID: 2, Title: "b", Type: domain.ChatTypePrivate, UnreadCount: 4, MutedUntil: time.Now().Add(time.Hour)},
	})
	if !strings.Contains(a.View().Content, "unread 3") {
		t.Fatalf("status bar:\n%s", a.View().Content)
	}
}

// Opening a chat hands the thread the read pointer from the list row: the
// ticks have to be right on the first draw, not after the next update.
func TestOpenChat_CarriesTheReadPointer(t *testing.T) {
	t.Parallel()

	a, _ := appWithChatRows(t, []domain.Chat{{ID: 7, Title: "Friend", Type: domain.ChatTypePrivate, ReadOutboxMaxID: 31}})
	model, _ := a.Update(chats.ChatSelectedMsg{ChatID: 7})
	a = model.(App)
	if got := a.thread.ReadOutboxMaxID(); got != 31 {
		t.Fatalf("thread read pointer = %d, want 31", got)
	}
	model, _ = a.Update(events.ChatReadOutbox{ChatID: 7, MaxID: 40})
	a = model.(App)
	if got := a.thread.ReadOutboxMaxID(); got != 40 {
		t.Fatalf("the update did not reach the thread: %d", got)
	}
}

// The chat with yourself draws no ticks; the app knows which one it is
// from the session's own id.
func TestOpenChat_HidesTicksInSavedMessages(t *testing.T) {
	t.Parallel()

	pane := chats.NewWithRepo(fakeChatsRepo{chats: []domain.Chat{{ID: 8385, Title: "Saved Messages", Type: domain.ChatTypePrivate}, {ID: 7, Title: "Friend", Type: domain.ChatTypePrivate}}}, nil)
	pane, _ = pane.Update(pane.Init()())
	threadModel := thread.New()
	a := New(Deps{Keymap: keymap.Default(), Chats: &pane, Thread: &threadModel, SelfID: func() int64 { return 8385 }})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a = model.(App)

	model, _ = a.Update(chats.ChatSelectedMsg{ChatID: 8385})
	a = model.(App)
	if !a.thread.SelfChat() {
		t.Fatal("saved messages was not marked as the chat with yourself")
	}
	model, _ = a.Update(chats.ChatSelectedMsg{ChatID: 7})
	a = model.(App)
	if a.thread.SelfChat() {
		t.Fatal("the mark survived into another chat")
	}
}

// A server draft reaches both the composer and the list row.
func TestDraftChanged_ReachesComposerAndList(t *testing.T) {
	t.Parallel()

	pane := chats.NewWithRepo(fakeChatsRepo{chats: []domain.Chat{{ID: 7, Title: "Friend", Type: domain.ChatTypePrivate}}}, nil)
	pane, _ = pane.Update(pane.Init()())
	inputModel := input.NewWithDeps(nil, keymap.Default(), nil)
	a := New(Deps{Keymap: keymap.Default(), Chats: &pane, Input: &inputModel})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model, _ = model.(App).Update(chats.ChatSelectedMsg{ChatID: 7})
	model, _ = model.(App).Update(events.DraftChanged{ChatID: 7, Text: "from the phone"})
	a = model.(App)
	if got := a.input.Value(); got != "from the phone" {
		t.Fatalf("composer holds %q", got)
	}
	if it, _ := a.chats.ItemByID(7); it.Draft() != "from the phone" {
		t.Fatalf("row draft = %q", it.Draft())
	}
}

// "@name" from the palette goes to the server once, and the answer opens
// like a palette pick; a refusal lands in the status bar.
func TestPalette_OpensAChatByUsername(t *testing.T) {
	t.Parallel()

	actions := &fakeActions{resolveID: 4242}
	threadModel := thread.New()
	a := New(Deps{Keymap: keymap.Default(), Thread: &threadModel, Actions: actions})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model, cmd := model.(App).Update(palette.OpenUsernameMsg{Username: "durov"})
	if cmd == nil {
		t.Fatal("no lookup command")
	}
	model, _ = model.(App).Update(cmd())
	a = model.(App)
	if got := actions.resolved; len(got) != 1 || got[0] != "durov" {
		t.Fatalf("resolved = %v", got)
	}
	if a.thread.ChatID() != 4242 {
		t.Fatalf("thread opened chat %d, want 4242", a.thread.ChatID())
	}

	actions.resolveErr = fmt.Errorf("@nobody: %w", coresync.ErrNoSuchUsername)
	model, cmd = a.Update(palette.OpenUsernameMsg{Username: "nobody"})
	model, _ = model.(App).Update(cmd())
	a = model.(App)
	if s := a.statusText(); !strings.Contains(s, "@nobody: no such username") || strings.Contains(s, "@nobody: @nobody") {
		t.Fatalf("status %q", s)
	}
	if a.thread.ChatID() != 4242 {
		t.Fatal("a refused lookup moved the thread")
	}
}

func appWithBotMessage(t *testing.T, actions *fakeActions, opener *fakeOpener) App {
	t.Helper()
	threadModel := thread.New()
	threadModel = injectMessages(threadModel, []domain.Message{{
		ID: 41, ChatID: 42, Date: time.Now(), Text: "pick one",
		Buttons: [][]domain.Button{
			{{Text: "Yes", Kind: domain.ButtonCallback, Data: []byte("y")}, {Text: "Docs", Kind: domain.ButtonURL, URL: "https://example.com/d"}},
			{{Text: "/start", Kind: domain.ButtonText}, {Text: "Copy", Kind: domain.ButtonCopy, URL: "token"}, {Text: "Pay", Kind: domain.ButtonOther}},
		},
	}})
	inputModel := input.NewWithDeps(nil, keymap.Default(), nil)
	a := New(Deps{Keymap: keymap.Default(), Thread: &threadModel, Input: &inputModel, Actions: actions, Opener: opener})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a = tabTo(t, model.(App), FocusThread)
	a.thread = a.thread.MoveCursor(-1)
	return a
}

func pressChosen(t *testing.T, a App) App {
	t.Helper()
	next, cmd := press(t, a, "enter")
	if cmd == nil {
		return next
	}
	model, _ := next.Update(cmd())
	return model.(App)
}

func right(a App) App {
	updated, _ := a.thread.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	a.thread = updated
	return a
}

// Enter on a callback key calls the bot with the key's data and shows what
// it said; a bot that answers with nothing is reported as pressed.
func TestBotButton_CallbackGoesToTheBot(t *testing.T) {
	t.Parallel()

	actions := &fakeActions{pressAnswer: coresync.CallbackAnswer{Message: "Thanks!", Alert: true}}
	a := appWithBotMessage(t, actions, &fakeOpener{})
	a = pressChosen(t, a)
	if got := actions.presses; len(got) != 1 || got[0].chatID != 42 || got[0].messageID != 41 || string(got[0].data) != "y" {
		t.Fatalf("presses = %+v", got)
	}
	if !strings.Contains(a.statusText(), "⚠ Thanks!") {
		t.Fatalf("status %q", a.statusText())
	}
	actions.pressAnswer = coresync.CallbackAnswer{}
	a = pressChosen(t, a)
	if !strings.Contains(a.statusText(), "pressed “Yes”") {
		t.Fatalf("status %q", a.statusText())
	}
}

// The other kinds: a link key opens the browser, a reply-keyboard key goes
// into the composer, a copy key onto the clipboard, and the rest say why not.
func TestBotButton_OtherKinds(t *testing.T) {
	t.Parallel()

	actions := &fakeActions{}
	opener := &fakeOpener{}
	a := appWithBotMessage(t, actions, opener)

	a = right(a)
	a = pressChosen(t, a)
	if got := opener.snapshot(); len(got) != 1 || got[0] != "url:https://example.com/d" {
		t.Fatalf("link key: opener got %v", got)
	}

	a = right(a)
	a = pressChosen(t, a)
	if a.Focus() != FocusInput || a.input.Value() != "/start" {
		t.Fatalf("text key: focus %v composer %q", a.Focus(), a.input.Value())
	}
	a = tabTo(t, a, FocusThread)

	a = right(a)
	next, cmd := press(t, a, "enter")
	if cmd == nil {
		t.Fatal("copy key produced no clipboard command")
	}
	if !strings.Contains(next.statusText(), "copied") {
		t.Fatalf("copy key: status %q", next.statusText())
	}

	a = right(next)
	a = pressChosen(t, a)
	if !strings.Contains(a.statusText(), "cannot press") {
		t.Fatalf("other key: status %q", a.statusText())
	}
	if len(actions.presses) != 0 {
		t.Fatalf("a non-callback key reached the bot: %+v", actions.presses)
	}
}
