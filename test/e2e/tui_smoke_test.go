// Package e2e exercises the lazytg TUI top-to-bottom with mocked
// storage and send services. The plan called for teatest, but
// charmbracelet/x/exp/teatest is locked to bubbletea v1 and we ship on
// v2 (charm.land/bubbletea). We drive Update/View directly here — the
// program loop is just a select-and-route around those two methods, so
// reproducing the chats-load → select → send flow at the model layer
// catches the same regressions a true headless terminal would, without
// the cross-version compatibility tax.
package e2e

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
	uiapp "github.com/kar43lov/lazytg/internal/ui/app"
	"github.com/kar43lov/lazytg/internal/ui/input"
	"github.com/kar43lov/lazytg/internal/ui/keymap"
	"github.com/kar43lov/lazytg/internal/ui/panes/chats"
	"github.com/kar43lov/lazytg/internal/ui/panes/thread"
)

// fakeChatsRepo satisfies chats.Repository with a fixed dialog list.
type fakeChatsRepo struct {
	list []domain.Chat
}

func (f *fakeChatsRepo) GetChats(_ context.Context) ([]domain.Chat, error) {
	out := make([]domain.Chat, len(f.list))
	copy(out, f.list)
	return out, nil
}

// fakeThreadRepo satisfies thread.Repository with an in-memory map keyed
// by chat id. Each map entry holds messages newest-first (matching the
// SQL contract of *sqlite.Repo.GetMessages).
type fakeThreadRepo struct {
	byChat map[int64][]domain.Message
}

func (f *fakeThreadRepo) GetMessages(_ context.Context, chatID int64, limit, offset int) ([]domain.Message, error) {
	all := f.byChat[chatID]
	if offset >= len(all) {
		return nil, nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	out := make([]domain.Message, end-offset)
	copy(out, all[offset:end])
	return out, nil
}

// GetMessagesBefore returns up to limit messages with id strictly less
// than beforeID, ordered by id desc. f.byChat[chatID] is sorted DESC
// (newest first) by SQL contract.
func (f *fakeThreadRepo) GetMessagesBefore(_ context.Context, chatID, beforeID int64, limit int) ([]domain.Message, error) {
	all := f.byChat[chatID]
	start := -1
	for i, m := range all {
		if m.ID < beforeID {
			start = i
			break
		}
	}
	if start == -1 {
		return nil, nil
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	out := make([]domain.Message, end-start)
	copy(out, all[start:end])
	return out, nil
}

// GetMessagesAfter returns up to limit messages with id strictly
// greater than afterID, ordered by id asc. f.byChat[chatID] is sorted
// DESC; we walk from the end toward the start collecting matches and
// then reverse for ASC order.
func (f *fakeThreadRepo) GetMessagesAfter(_ context.Context, chatID, afterID int64, limit int) ([]domain.Message, error) {
	all := f.byChat[chatID]
	if limit <= 0 || afterID <= 0 {
		return nil, nil
	}
	out := make([]domain.Message, 0, limit)
	for i := len(all) - 1; i >= 0 && len(out) < limit; i-- {
		if all[i].ID > afterID {
			out = append(out, all[i])
		}
	}
	return out, nil
}

// fakeSender records SendText invocations and returns a fixed local id.
type fakeSender struct {
	mu     sync.Mutex
	calls  []sendCall
	nextID string
}

type sendCall struct {
	chatID  int64
	text    string
	replyTo int
}

func (f *fakeSender) SendText(_ context.Context, chatID int64, text string, replyTo int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, sendCall{chatID: chatID, text: text, replyTo: replyTo})
	id := f.nextID
	if id == "" {
		id = "local-1"
	}
	return id, nil
}

func (f *fakeSender) lastCall() (sendCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return sendCall{}, false
	}
	return f.calls[len(f.calls)-1], true
}

// runCmd resolves the tea.Cmd chain produced by Update calls, feeding
// each tea.Msg back into the model. Returns the final model and the
// cumulative slice of every emitted message — useful for assertions on
// downstream effects (ChatSelectedMsg → SetChatMsg, etc).
//
// The loop has a hard step cap because some bubbletea components (the
// textarea cursor blink, the bubbles list spinner) emit recurring
// time-tick cmds that would otherwise drain forever. We treat the cap
// as "stop draining and return what we have" — every assertion in the
// e2e file checks an effect that already fired by the time the cap
// kicks in (chats loaded, ChatSelectedMsg seen, send service called).
func runCmd(_ *testing.T, model tea.Model, cmd tea.Cmd) (tea.Model, []tea.Msg) {
	const stepCap = 32
	var emitted []tea.Msg
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0 && steps < stepCap; steps++ {
		next := queue[0]
		queue = queue[1:]
		if next == nil {
			continue
		}
		msg := next()
		if msg == nil {
			continue
		}
		// Drop blink/tick messages that bubbletea emits in steady state —
		// they are display-only effects irrelevant to the assertions
		// here and re-injecting them would re-arm a fresh tick cmd that
		// keeps the loop alive forever.
		if isTickMsg(msg) {
			continue
		}
		emitted = append(emitted, msg)
		var newCmd tea.Cmd
		model, newCmd = model.Update(msg)
		if newCmd != nil {
			queue = append(queue, newCmd)
		}
	}
	return model, emitted
}

// isTickMsg reports whether msg is one of the bubbletea / charmbracelet
// recurring tick payloads (cursor blink, list spinner, etc). Those
// recur indefinitely while the relevant component is focused, so we
// drop them in the smoke driver instead of queuing the follow-up cmd.
// We compare on the formatted %T rather than any concrete type because
// the relevant types (textarea.BlinkMsg, list.spinnerTickMsg) are
// internal to bubbletea/bubbles and not importable.
func isTickMsg(msg tea.Msg) bool {
	name := fmt.Sprintf("%T", msg)
	switch {
	case strings.Contains(name, "BlinkMsg"),
		strings.Contains(name, "blinkMsg"),
		strings.Contains(name, "TickMsg"),
		strings.Contains(name, "tickMsg"),
		strings.Contains(name, "spinnerTickMsg"):
		return true
	}
	return false
}

// TestTUISmoke exercises the read-and-send flow:
//
//  1. App boots → chats pane loads three chats from the fake repo.
//  2. Initial render contains all three chat titles.
//  3. KeyMsg Down + Enter → ChatSelectedMsg → thread.OpenChat fires → input binds chat id.
//  4. Tab focuses input; "hello" + Enter triggers fakeSender.SendText.
//  5. The send call records (chatID=22, text="hello", replyTo=0).
//
// The test deliberately skips the bubbletea Program loop and drives
// model.Update / View directly. That is sufficient because the program
// loop is a thin select-and-route around those entry points; everything
// observable from the user's perspective passes through them.
func TestTUISmoke(t *testing.T) {
	chatsRepo := &fakeChatsRepo{
		list: []domain.Chat{
			{ID: 11, Type: domain.ChatTypePrivate, Title: "Alice", LastMessageDate: time.Unix(1700000300, 0)},
			{ID: 22, Type: domain.ChatTypePrivate, Title: "Bob", LastMessageDate: time.Unix(1700000200, 0)},
			{ID: 33, Type: domain.ChatTypeGroup, Title: "Coffee", LastMessageDate: time.Unix(1700000100, 0)},
		},
	}
	threadRepo := &fakeThreadRepo{
		byChat: map[int64][]domain.Message{
			22: {
				{ID: 99, ChatID: 22, FromID: 22, Date: time.Unix(1700000200, 0), Text: "hi from bob"},
			},
		},
	}
	sender := &fakeSender{nextID: "local-uuid"}

	chatsModel := chats.NewWithRepo(chatsRepo, nil)
	threadModel := thread.NewWithRepo(threadRepo, nil, nil)
	inputModel := input.NewWithDeps(sender, keymap.Default(), nil)

	model := tea.Model(uiapp.New(uiapp.Deps{
		Keymap: keymap.Default(),
		Chats:  &chatsModel,
		Thread: &threadModel,
		Input:  &inputModel,
	}))

	// Resize so the pane layout is computed.
	model, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	// Init kicks off the chats load. tea.Cmd fires synchronously here
	// (chats.Init returns a fire-and-forget closure that reads the
	// fake repo), so runCmd processes the chatsLoadedMsg + the
	// underlying list.SetItems cmd immediately.
	model, _ = runCmd(t, model, model.Init())

	// Verify chat titles are rendered. lipgloss may inject ANSI escape
	// sequences, so look for substrings rather than exact lines.
	view := model.(uiapp.App).View().Content
	for _, title := range []string{"Alice", "Bob", "Coffee"} {
		if !strings.Contains(view, title) {
			t.Fatalf("expected chats list to contain %q; got:\n%s", title, view)
		}
	}

	// Press Down to highlight Bob (second item).
	down := tea.KeyPressMsg{Code: tea.KeyDown}
	model, cmd := model.Update(down)
	if cmd != nil {
		model, _ = runCmd(t, model, cmd)
	}

	// Press Enter to publish ChatSelectedMsg.
	enter := tea.KeyPressMsg{Code: tea.KeyEnter}
	model, cmd = model.Update(enter)
	if cmd == nil {
		t.Fatalf("Enter on chats pane should publish ChatSelectedMsg via cmd")
	}
	model, emitted := runCmd(t, model, cmd)

	// Among emitted messages we expect a chats.ChatSelectedMsg and a
	// thread.messagesLoadedMsg (private, but observable as a state
	// flip on the thread pane).
	var sawChatSelected bool
	for _, m := range emitted {
		if _, ok := m.(chats.ChatSelectedMsg); ok {
			sawChatSelected = true
		}
	}
	if !sawChatSelected {
		t.Fatalf("expected ChatSelectedMsg in emitted: %#v", emitted)
	}

	// Wait until thread pane reflects the loaded message. The repo
	// fetch runs in a goroutine via tea.Cmd; runCmd already drained
	// the message but the model update is sync so we can assert
	// immediately.
	ap := model.(uiapp.App)
	gotMsgs := threadMessagesFromView(ap.View().Content)
	if len(gotMsgs) == 0 || !strings.Contains(strings.Join(gotMsgs, "\n"), "hi from bob") {
		t.Fatalf("expected thread to render Bob's message; got:\n%s", ap.View().Content)
	}

	// Tab focuses the input pane.
	tab := tea.KeyPressMsg{Code: tea.KeyTab}
	model, cmd = model.Update(tab)
	if cmd != nil {
		model, _ = runCmd(t, model, cmd)
	}
	if model.(uiapp.App).Focus() != uiapp.FocusInput {
		t.Fatalf("Tab should land focus on FocusInput; got %s", model.(uiapp.App).Focus())
	}

	// Type "hello" then Enter — input pane forwards to fakeSender.
	for _, r := range "hello" {
		keyMsg := tea.KeyPressMsg{Text: string(r), Code: r}
		model, cmd = model.Update(keyMsg)
		if cmd != nil {
			model, _ = runCmd(t, model, cmd)
		}
	}
	_, cmd = model.Update(enter)
	if cmd == nil {
		t.Fatalf("Enter on input pane with non-empty text should produce send cmd")
	}
	runCmd(t, model, cmd)

	// Verify fakeSender saw the call.
	last, ok := sender.lastCall()
	if !ok {
		t.Fatalf("fakeSender.SendText was never invoked")
	}
	if last.chatID != 22 {
		t.Fatalf("expected chatID=22 (Bob), got %d", last.chatID)
	}
	if last.text != "hello" {
		t.Fatalf("expected text=%q, got %q", "hello", last.text)
	}
	if last.replyTo != 0 {
		t.Fatalf("expected replyTo=0, got %d", last.replyTo)
	}
}

// TestTUIBusBroadcast verifies that bus events delivered as tea.Msg
// (which is what cmd/lazytg/cmd/tui.go::forwardBusEvents does) reach
// every interested pane regardless of focus. This exercises the
// broadcastBusEvent routing in internal/ui/app/update.go.
func TestTUIBusBroadcast(t *testing.T) {
	chatsRepo := &fakeChatsRepo{
		list: []domain.Chat{
			{ID: 7, Type: domain.ChatTypePrivate, Title: "Channel A"},
		},
	}
	threadRepo := &fakeThreadRepo{
		byChat: map[int64][]domain.Message{
			7: {{ID: 1, ChatID: 7, FromID: 7, Date: time.Unix(1700000000, 0), Text: "first"}},
		},
	}

	chatsModel := chats.NewWithRepo(chatsRepo, nil)
	threadModel := thread.NewWithRepo(threadRepo, nil, nil)
	inputModel := input.NewWithDeps(nil, keymap.Default(), nil)

	model := tea.Model(uiapp.New(uiapp.Deps{
		Keymap: keymap.Default(),
		Chats:  &chatsModel,
		Thread: &threadModel,
		Input:  &inputModel,
	}))
	model, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model, _ = runCmd(t, model, model.Init())

	// Open Chat 7 so the thread pane has a chatID to filter against.
	model, cmd := model.Update(chats.ChatSelectedMsg{ChatID: 7})
	model, _ = runCmd(t, model, cmd)

	// Connection state changes flip the status bar.
	model, _ = model.Update(events.ConnectionStateChanged{State: "online"})
	if !strings.Contains(model.(uiapp.App).View().Content, "online") {
		t.Fatalf("expected 'online' in status bar after ConnectionStateChanged")
	}

	// Storage state changes flip the storage cell.
	model, _ = model.Update(events.StorageStateChanged{Mode: events.StorageModeReadOnly})
	if !strings.Contains(model.(uiapp.App).View().Content, "read-only") {
		t.Fatalf("expected 'read-only' marker after StorageStateChanged")
	}

	// MessageReceived for the open chat lands in the thread; for a
	// different chat it does not.
	beforeView := model.(uiapp.App).View().Content
	model, _ = model.Update(events.MessageReceived{
		ChatID:    99, // not the open chat
		MessageID: 100,
		Text:      "ignore me",
		Date:      time.Unix(1700000400, 0),
	})
	if strings.Contains(model.(uiapp.App).View().Content, "ignore me") {
		t.Fatalf("MessageReceived for other chat must not render in thread")
	}
	_ = beforeView

	model, _ = model.Update(events.MessageReceived{
		ChatID:    7,
		MessageID: 101,
		Text:      "live update body",
		Date:      time.Unix(1700000500, 0),
	})
	if !strings.Contains(model.(uiapp.App).View().Content, "live update body") {
		t.Fatalf("MessageReceived for open chat should append to thread; got:\n%s",
			model.(uiapp.App).View().Content)
	}
}

// threadMessagesFromView splits the rendered view at blank lines and
// returns the body chunks. Useful for assertions that don't care about
// chat list / status bar artefacts.
func threadMessagesFromView(view string) []string {
	out := strings.Split(view, "\n\n")
	for i := range out {
		out[i] = strings.TrimSpace(out[i])
	}
	return out
}
