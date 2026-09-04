package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/ui/input"
	"github.com/kar43lov/lazytg/internal/ui/keymap"
	"github.com/kar43lov/lazytg/internal/ui/panes/thread"
)

// fakeActions records what the UI asked for without touching a network.
type fakeActions struct {
	mu      sync.Mutex
	edits   []editRecord
	deletes []deleteRecord
	err     error
}

type editRecord struct {
	chatID    int64
	messageID int64
	text      string
}

type deleteRecord struct {
	chatID int64
	ids    []int64
	revoke bool
}

func (f *fakeActions) Edit(_ context.Context, chatID, messageID int64, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, editRecord{chatID, messageID, text})
	return f.err
}

func (f *fakeActions) Delete(_ context.Context, chatID int64, ids []int64, revoke bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, deleteRecord{chatID, append([]int64(nil), ids...), revoke})
	return f.err
}

func (f *fakeActions) snapshotDeletes() []deleteRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]deleteRecord(nil), f.deletes...)
}

func (f *fakeActions) snapshotEdits() []editRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]editRecord(nil), f.edits...)
}

// newAppForActions builds an app on the thread pane holding three messages:
// two the account sent and one it received.
func newAppForActions(t *testing.T, actions MessageActions) App {
	t.Helper()
	threadModel := thread.New()
	now := time.Now()
	threadModel = injectMessages(threadModel, []domain.Message{
		{ID: 1, ChatID: 42, Date: now, Text: "mine one", Outgoing: true},
		{ID: 2, ChatID: 42, Date: now.Add(time.Second), Text: "theirs", FromID: 7},
		{ID: 3, ChatID: 42, Date: now.Add(2 * time.Second), Text: "mine two", Outgoing: true},
	})
	inputModel := input.NewWithDeps(nil, keymap.Default(), nil)
	a := New(Deps{
		Keymap:  keymap.Default(),
		Thread:  &threadModel,
		Input:   &inputModel,
		Actions: actions,
	})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a = model.(App)
	return tabTo(t, a, FocusThread)
}

func press(t *testing.T, a App, s string) (App, tea.Cmd) {
	t.Helper()
	model, cmd := a.Update(keyText(s))
	return model.(App), cmd
}

// cursorUp moves the message cursor up n messages.
//
// The first press does not move anything: an unplaced cursor resolves to the
// newest message, so pressing up once puts the marker there rather than
// skipping past it. Tests that want the message above have to account for
// that, and doing it here keeps the arithmetic in one place.
func cursorUp(t *testing.T, a App, n int) App {
	t.Helper()
	for i := 0; i <= n; i++ {
		model, _ := a.Update(keyChord(tea.KeyUp, 0))
		a = model.(App)
	}
	return a
}

// Copying with nothing marked takes the message under the cursor — the
// file-manager rule, and the one that makes the common case one keystroke.
func TestCopy_TakesTheCursorMessageWhenNothingIsMarked(t *testing.T) {
	t.Parallel()

	a := newAppForActions(t, &fakeActions{})
	a, cmd := press(t, a, "y")
	if cmd == nil {
		t.Fatal("y produced no clipboard command")
	}
	if !strings.Contains(a.status.Notice, "copied 1 message") {
		t.Fatalf("notice = %q, want it to name one message", a.status.Notice)
	}
}

func TestCopy_TakesEveryMarkedMessage(t *testing.T) {
	t.Parallel()

	a := newAppForActions(t, &fakeActions{})
	a, _ = press(t, a, " ")
	a = cursorUp(t, a, 1)
	a, _ = press(t, a, " ")

	a, cmd := press(t, a, "y")
	if cmd == nil {
		t.Fatal("y produced no clipboard command")
	}
	if !strings.Contains(a.status.Notice, "copied 2 messages") {
		t.Fatalf("notice = %q, want it to name two messages", a.status.Notice)
	}
	// Copying consumes the selection: leaving the marks behind means the
	// next delete acts on messages the user thought they had finished with.
	if a.thread.MarkCount() != 0 {
		t.Fatalf("marks survived the copy: %d", a.thread.MarkCount())
	}
}

// The refusal is local because the mirror already knows the answer, and a
// round trip to be told "not yours" is a round trip the user waits for.
func TestEdit_RefusesSomebodyElsesMessageWithoutAskingTheServer(t *testing.T) {
	t.Parallel()

	actions := &fakeActions{}
	a := newAppForActions(t, actions)
	// Move the cursor onto the incoming message (id 2).
	a = cursorUp(t, a, 1)

	a, cmd := press(t, a, "e")
	if cmd != nil {
		t.Fatal("editing somebody else's message should not arm the composer")
	}
	if !strings.Contains(a.status.Notice, "only your own") {
		t.Fatalf("notice = %q, want it to explain the refusal", a.status.Notice)
	}
	if len(actions.snapshotEdits()) != 0 {
		t.Fatal("a refused edit reached the service")
	}
}

func TestEdit_ArmsTheComposerWithTheCurrentText(t *testing.T) {
	t.Parallel()

	a := newAppForActions(t, &fakeActions{})
	a, cmd := press(t, a, "e")
	if cmd == nil {
		t.Fatal("e on an own message should arm the composer")
	}
	msg, ok := cmd().(input.StartEditMsg)
	if !ok {
		t.Fatalf("command produced %T, want input.StartEditMsg", cmd())
	}
	if msg.MessageID != 3 || msg.Text != "mine two" {
		t.Fatalf("edit target = %+v, want message 3 with its current text", msg)
	}
	// Focus has to follow, or the user types into the thread.
	if a.focus != FocusInput {
		t.Fatalf("focus = %v, want the composer", a.focus)
	}
}

// Nothing is destroyed without the modal. This is the only gesture in the
// client that removes messages from other people's devices.
func TestDelete_AsksBeforeDoingAnything(t *testing.T) {
	t.Parallel()

	actions := &fakeActions{}
	a := newAppForActions(t, actions)
	a, _ = press(t, a, "d")

	if !a.confirm.Visible() {
		t.Fatal("d did not open the confirmation")
	}
	if len(actions.snapshotDeletes()) != 0 {
		t.Fatal("d deleted before the user confirmed")
	}
	view := ansi.Strip(a.View().Content)
	if !strings.Contains(view, "Delete 1 message?") {
		t.Fatalf("the modal does not say what it will delete:\n%s", view)
	}
}

func TestDelete_AnyOtherKeyCancels(t *testing.T) {
	t.Parallel()

	actions := &fakeActions{}
	a := newAppForActions(t, actions)
	a, _ = press(t, a, "d")
	a, cmd := press(t, a, "x")

	if a.confirm.Visible() {
		t.Fatal("an unrelated key left the modal up")
	}
	if cmd != nil {
		t.Fatal("an unrelated key started a deletion")
	}
	if len(actions.snapshotDeletes()) != 0 {
		t.Fatal("a cancelled deletion reached the service")
	}
}

func TestDelete_ConfirmingCarriesTheRevokeChoice(t *testing.T) {
	t.Parallel()

	cases := []struct {
		key        string
		wantRevoke bool
	}{
		{"e", true},
		{"m", false},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()
			actions := &fakeActions{}
			a := newAppForActions(t, actions)
			a, _ = press(t, a, "d")
			_, cmd := press(t, a, tc.key)
			if cmd == nil {
				t.Fatalf("%q did not start the deletion", tc.key)
			}
			cmd()

			got := actions.snapshotDeletes()
			if len(got) != 1 {
				t.Fatalf("service calls = %+v, want one", got)
			}
			if got[0].revoke != tc.wantRevoke {
				t.Fatalf("revoke = %v, want %v", got[0].revoke, tc.wantRevoke)
			}
			if len(got[0].ids) != 1 || got[0].ids[0] != 3 {
				t.Fatalf("ids = %v, want the message under the cursor", got[0].ids)
			}
		})
	}
}

func TestDelete_ActsOnEveryMarkedMessage(t *testing.T) {
	t.Parallel()

	actions := &fakeActions{}
	a := newAppForActions(t, actions)
	a, _ = press(t, a, " ")
	a = cursorUp(t, a, 1)
	a, _ = press(t, a, " ")

	a, _ = press(t, a, "d")
	view := ansi.Strip(a.View().Content)
	if !strings.Contains(view, "Delete 2 messages?") {
		t.Fatalf("modal does not name both messages:\n%s", view)
	}
	_, cmd := press(t, a, "m")
	if cmd == nil {
		t.Fatal("confirming did not start the deletion")
	}
	cmd()

	got := actions.snapshotDeletes()
	if len(got) != 1 || len(got[0].ids) != 2 {
		t.Fatalf("service call = %+v, want one call with two ids", got)
	}
}

func TestActionResult_ReportsAFailure(t *testing.T) {
	t.Parallel()

	a := newAppForActions(t, &fakeActions{err: errors.New("MESSAGE_DELETE_FORBIDDEN")})
	a = a.applyActionResult(messageActionsResultMsg{
		err: errors.New("MESSAGE_DELETE_FORBIDDEN"), what: "delete",
	})
	if !strings.Contains(a.status.Notice, "delete failed") {
		t.Fatalf("notice = %q, want it to report the failure", a.status.Notice)
	}
}

// Without a service wired the chord must say so rather than appear to work.
func TestDelete_OfflineSaysSo(t *testing.T) {
	t.Parallel()

	a := newAppForActions(t, nil)
	a, _ = press(t, a, "d")
	_, cmd := press(t, a, "m")
	if cmd == nil {
		t.Fatal("confirming produced no command")
	}
	res, ok := cmd().(messageActionsResultMsg)
	if !ok || res.err == nil {
		t.Fatalf("offline delete produced %#v, want an error result", cmd())
	}
}

// The bare letters must not reach the thread while the composer has focus:
// there they are text somebody is typing.
func TestMessageActionKeys_AreThreadOnly(t *testing.T) {
	t.Parallel()

	actions := &fakeActions{}
	a := newAppForActions(t, actions)
	a = tabTo(t, a, FocusInput)

	for _, k := range []string{"y", "e", "d"} {
		next, _ := press(t, a, k)
		if next.confirm.Visible() {
			t.Fatalf("%q opened the delete modal from the composer", k)
		}
		if next.input.Value() == "" {
			t.Fatalf("%q did not reach the composer as text", k)
		}
		a = newAppForActions(t, actions)
		a = tabTo(t, a, FocusInput)
	}
}
