package app

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/ui/input"
	"github.com/kar43lov/lazytg/internal/ui/keymap"
	"github.com/kar43lov/lazytg/internal/ui/panes/thread"
)

// newAppForJumps seeds a conversation where the newest message answers the
// oldest — the shape the gesture exists for.
func newAppForJumps(t *testing.T) App {
	t.Helper()
	threadModel := thread.New()
	now := time.Now()
	threadModel = injectMessages(threadModel, []domain.Message{
		{ID: 1, ChatID: 42, Date: now, Text: "the question"},
		{ID: 2, ChatID: 42, Date: now.Add(time.Second), Text: "unrelated"},
		{ID: 3, ChatID: 42, Date: now.Add(2 * time.Second), Text: "the answer", ReplyTo: 1},
	})
	inputModel := input.NewWithDeps(nil, keymap.Default(), nil)
	a := New(Deps{Keymap: keymap.Default(), Thread: &threadModel, Input: &inputModel})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a = model.(App)
	return tabTo(t, a, FocusThread)
}

func TestJumpToParent_MovesTheCursorToWhatWasAnswered(t *testing.T) {
	t.Parallel()

	a := newAppForJumps(t)
	a, _ = press(t, a, "p")

	if got := a.thread.Cursor(); got != 1 {
		t.Fatalf("cursor is on %d, want the message that was replied to", got)
	}
}

// A reply usually sits a few messages below what it answers, so the parent is
// already rendered: no request, no window load, just a cursor move.
func TestJumpToParent_NeedsNoLoadWhenTheParentIsOnScreen(t *testing.T) {
	t.Parallel()

	a := newAppForJumps(t)
	a, cmd := press(t, a, "p")
	if cmd != nil {
		t.Fatal("a local jump scheduled work")
	}
	if strings.Contains(a.statusText(), "looking it up") {
		t.Fatalf("status reads %q", a.statusText())
	}
}

func TestJumpToParent_SaysWhenTheMessageIsNotAReply(t *testing.T) {
	t.Parallel()

	a := newAppForJumps(t)
	a = cursorUp(t, a, 1) // onto id 2, which answers nothing
	a, _ = press(t, a, "p")

	if !strings.Contains(a.statusText(), "not a reply") {
		t.Fatalf("status reads %q", a.statusText())
	}
	if got := a.thread.Cursor(); got != 2 {
		t.Fatalf("the cursor moved to %d anyway", got)
	}
}

// The pair behaves like a jumplist, which is the model this audience already
// has from their editor.
func TestJumpBack_ReturnsWhereTheJumpStarted(t *testing.T) {
	t.Parallel()

	a := newAppForJumps(t)
	a, _ = press(t, a, "p")
	if a.thread.Cursor() != 1 {
		t.Fatalf("setup: cursor on %d", a.thread.Cursor())
	}

	model, _ := a.Update(keyChord('o', tea.ModCtrl))
	a = model.(App)
	if got := a.thread.Cursor(); got != 3 {
		t.Fatalf("cursor is on %d, want the message the jump started from", got)
	}
}

func TestJumpBack_SaysWhenThereIsNoTrail(t *testing.T) {
	t.Parallel()

	a := newAppForJumps(t)
	model, _ := a.Update(keyChord('o', tea.ModCtrl))
	a = model.(App)

	if !strings.Contains(a.statusText(), "nowhere to go back") {
		t.Fatalf("status reads %q", a.statusText())
	}
}

// Every entry in the trail names a message in the conversation being left.
func TestJumpTrail_ClearedByAChatSwitch(t *testing.T) {
	t.Parallel()

	a := newAppForJumps(t)
	a, _ = press(t, a, "p")
	if len(a.jumpStack) == 0 {
		t.Fatal("setup: no trail")
	}

	a = a.clearJumpTrail()
	if len(a.jumpStack) != 0 {
		t.Fatalf("the trail survived: %v", a.jumpStack)
	}
}

// The trail cannot grow without bound in a long session; losing the far end
// is invisible, refusing to move is not.
func TestJumpTrail_DropsTheOldestWhenFull(t *testing.T) {
	t.Parallel()

	a := newAppForJumps(t)
	for i := 0; i < jumpStackLimit+10; i++ {
		a = a.pushJumpOrigin(jumpOrigin{chatID: 42, messageID: int64(i)})
	}
	if len(a.jumpStack) != jumpStackLimit {
		t.Fatalf("trail holds %d entries, want the cap of %d", len(a.jumpStack), jumpStackLimit)
	}
	if a.jumpStack[len(a.jumpStack)-1].messageID != int64(jumpStackLimit+9) {
		t.Fatalf("the newest entry is %d", a.jumpStack[len(a.jumpStack)-1].messageID)
	}
}

// Without a window loader wired the gesture has to say why nothing happened
// rather than moving the cursor somewhere arbitrary.
func TestJumpToParent_SaysWhenTheParentIsNotLoaded(t *testing.T) {
	t.Parallel()

	threadModel := thread.New()
	threadModel = injectMessages(threadModel, []domain.Message{
		{ID: 9, ChatID: 42, Date: time.Now(), Text: "an answer to something older", ReplyTo: 1},
	})
	inputModel := input.NewWithDeps(nil, keymap.Default(), nil)
	a := New(Deps{Keymap: keymap.Default(), Thread: &threadModel, Input: &inputModel})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a = tabTo(t, model.(App), FocusThread)

	a, cmd := press(t, a, "p")
	if cmd != nil {
		t.Fatal("a jump was scheduled with no loader wired")
	}
	if !strings.Contains(a.statusText(), "not loaded") {
		t.Fatalf("status reads %q", a.statusText())
	}
}

// A divider drawn from a guess is worse than none, so a chat the list does
// not know reports nothing rather than a default.
func TestUnreadOf_ReportsNothingForAnUnknownChat(t *testing.T) {
	t.Parallel()

	a := newAppForJumps(t)
	if got := a.unreadOf(999); got != 0 {
		t.Fatalf("an unknown chat reports %d unread", got)
	}
}

// A bare digit is something people type; a client that swallows one is worse
// than a client without the shortcut. So the modifier is required.
func TestQuickChatKey_NeedsTheModifier(t *testing.T) {
	t.Parallel()

	a := newAppForJumps(t)
	_, _, handled := a.applyQuickChatKey(keyChord('3', 0))
	if handled {
		t.Fatal("a bare digit was taken as a chat shortcut")
	}
	_, _, handled = a.applyQuickChatKey(keyChord('n', tea.ModAlt))
	if handled {
		t.Fatal("alt+n was taken as a chat shortcut")
	}
}

// Saying nothing when the list is shorter than the number pressed would look
// like a missed keypress.
func TestQuickChatKey_SaysWhenThereIsNoSuchChat(t *testing.T) {
	t.Parallel()

	a := newAppForJumps(t)
	a, _, handled := a.applyQuickChatKey(keyChord('9', tea.ModAlt))
	if !handled {
		t.Fatal("alt+9 was not handled")
	}
	if !strings.Contains(a.statusText(), "no chat there") {
		t.Fatalf("status reads %q", a.statusText())
	}
}

// The composer is where digits are typed.
// Opening a chat moves the focus into the composer, so a shortcut that
// refused to fire from there worked exactly once per session: alt+2 opened a
// chat, alt+4 afterwards did nothing. Seen live on 05.09.2026.
func TestQuickChatKey_WorksFromTheComposer(t *testing.T) {
	t.Parallel()

	a := newAppForJumps(t)
	a = a.setFocus(FocusInput)
	if _, _, handled := a.applyQuickChatKey(keyChord('2', tea.ModAlt)); !handled {
		t.Fatal("alt+2 was ignored because the composer had the focus")
	}
}
