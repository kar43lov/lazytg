package thread

import (
	"strings"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

func markThread(t *testing.T, msgs ...domain.Message) Model {
	t.Helper()
	m, _ := sized(New()).OpenChat(1)
	m, _ = m.Update(messagesLoadedMsg{chatID: 1, gen: m.loadGen, messages: msgs})
	return m
}

// replaceMessages swaps the loaded window, which is what a chat reload or a
// remote deletion does under the marks.
func replaceMessages(t *testing.T, m Model, msgs []domain.Message) Model {
	t.Helper()
	next, _ := m.Update(messagesLoadedMsg{chatID: 1, gen: m.loadGen, messages: msgs})
	return next
}

func msgAt(id int64, min int, text string) domain.Message {
	return domain.Message{
		ID:     id,
		ChatID: 1,
		FromID: 7,
		Date:   time.Date(2026, 9, 4, 12, min, 0, 0, time.UTC),
		Text:   text,
	}
}

// Targets is the rule every action rests on: act on the marks when there are
// some, otherwise on the message under the cursor. Without the fallback the
// common case — deleting exactly one message — would cost a mark first.
func TestTargets_FallsBackToTheCursorWhenNothingIsMarked(t *testing.T) {
	t.Parallel()

	m := markThread(t, msgAt(1, 0, "one"), msgAt(2, 1, "two"), msgAt(3, 2, "three"))

	targets := m.Targets()
	if len(targets) != 1 || targets[0].ID != 3 {
		t.Fatalf("unplaced cursor targeted %+v, want the newest message", targets)
	}

	m = m.SetCursor(2)
	targets = m.Targets()
	if len(targets) != 1 || targets[0].ID != 2 {
		t.Fatalf("cursor on 2 targeted %+v, want message 2", targets)
	}
}

func TestToggleMark_MarksAndUnmarksUnderTheCursor(t *testing.T) {
	t.Parallel()

	m := markThread(t, msgAt(1, 0, "one"), msgAt(2, 1, "two"))
	m = m.SetCursor(1).ToggleMark()
	if !m.IsMarked(1) || m.MarkCount() != 1 {
		t.Fatalf("after one toggle: marked=%v count=%d", m.IsMarked(1), m.MarkCount())
	}
	m = m.ToggleMark()
	if m.IsMarked(1) || m.MarkCount() != 0 {
		t.Fatalf("a second toggle should unmark: marked=%v count=%d", m.IsMarked(1), m.MarkCount())
	}
}

// Marks are drawn, or the user cannot tell what they have chosen.
func TestToggleMark_ShowsInTheRenderedThread(t *testing.T) {
	t.Parallel()

	m := markThread(t, msgAt(1, 0, "one"), msgAt(2, 1, "two"))
	before := stripANSI(m.View())
	if strings.Contains(before, markMark) {
		t.Fatalf("an unmarked thread already shows the mark glyph:\n%s", before)
	}
	m = m.SetCursor(1).ToggleMark()
	after := stripANSI(m.View())
	if !strings.Contains(after, markMark) {
		t.Fatalf("marked message does not show the glyph:\n%s", after)
	}
}

func TestMarked_ReturnsMessagesInThreadOrder(t *testing.T) {
	t.Parallel()

	m := markThread(t, msgAt(1, 0, "one"), msgAt(2, 1, "two"), msgAt(3, 2, "three"))
	// Mark them out of order; the answer must still read like the thread.
	m = m.SetCursor(3).ToggleMark().SetCursor(1).ToggleMark()

	marked := m.Marked()
	if len(marked) != 2 || marked[0].ID != 1 || marked[1].ID != 3 {
		t.Fatalf("Marked() = %+v, want ids 1 then 3", marked)
	}
}

// A mark whose message is gone — deleted from another device, or scrolled out
// of the window held in memory — must not reach an action. Nobody can see it,
// and deleting or copying it means nothing.
func TestMarked_DropsIdsThatNoLongerResolve(t *testing.T) {
	t.Parallel()

	m := markThread(t, msgAt(1, 0, "one"), msgAt(2, 1, "two"))
	m = m.SetCursor(2).ToggleMark()
	if m.MarkCount() != 1 {
		t.Fatalf("setup: MarkCount = %d", m.MarkCount())
	}

	m = replaceMessages(t, m, []domain.Message{msgAt(1, 0, "one")})
	if got := m.Marked(); len(got) != 0 {
		t.Fatalf("Marked() = %+v after the message went away, want none", got)
	}
	// The count still reports what the user pressed space on, which is the
	// honest number for a status line.
	if m.MarkCount() != 1 {
		t.Fatalf("MarkCount = %d, want the raw count of marks", m.MarkCount())
	}
}

func TestClearMarks_DropsEverything(t *testing.T) {
	t.Parallel()

	m := markThread(t, msgAt(1, 0, "one"), msgAt(2, 1, "two"))
	m = m.SetCursor(1).ToggleMark().SetCursor(2).ToggleMark()
	if m.MarkCount() != 2 {
		t.Fatalf("setup: MarkCount = %d, want 2", m.MarkCount())
	}
	m = m.ClearMarks()
	if m.MarkCount() != 0 {
		t.Fatalf("ClearMarks left %d", m.MarkCount())
	}
}

// The mark map is shared with the value receiver's copy, so a toggle that
// mutated it in place would change a model the caller still holds — the
// classic Bubble Tea aliasing bug.
func TestToggleMark_DoesNotMutateThePriorModel(t *testing.T) {
	t.Parallel()

	before := markThread(t, msgAt(1, 0, "one"), msgAt(2, 1, "two")).SetCursor(1).ToggleMark()
	after := before.SetCursor(2).ToggleMark()

	if before.MarkCount() != 1 {
		t.Fatalf("the earlier model gained a mark: count=%d", before.MarkCount())
	}
	if after.MarkCount() != 2 {
		t.Fatalf("the later model = %d marks, want 2", after.MarkCount())
	}
}

func TestMarkedText_PrefixesAuthors(t *testing.T) {
	t.Parallel()

	msgs := []domain.Message{msgAt(1, 0, "first"), msgAt(2, 1, "second")}
	got := MarkedText(msgs, func(m domain.Message) string {
		if m.ID == 1 {
			return "Ada"
		}
		return "Grace"
	})
	want := "Ada: first\n\nGrace: second"
	if got != want {
		t.Fatalf("MarkedText = %q, want %q", got, want)
	}
}

func TestApplyEdit_RewritesOneRowInPlace(t *testing.T) {
	t.Parallel()

	m := markThread(t, msgAt(1, 0, "before"), msgAt(2, 1, "untouched"))
	m = m.ApplyEdit(1, "after", nil, time.Time{})

	out := stripANSI(m.View())
	if !strings.Contains(out, "after") {
		t.Fatalf("edited text missing:\n%s", out)
	}
	if strings.Contains(out, "before") {
		t.Fatalf("old text still rendered:\n%s", out)
	}
	if !strings.Contains(out, "untouched") {
		t.Fatalf("the other message was disturbed:\n%s", out)
	}
}

func TestCanRevokeDeletes_IsFalseOnlyInAChannel(t *testing.T) {
	t.Parallel()

	cases := map[domain.ChatType]bool{
		domain.ChatTypePrivate:    true,
		domain.ChatTypeGroup:      true,
		domain.ChatTypeSupergroup: true,
		domain.ChatTypeChannel:    false,
	}
	for kind, want := range cases {
		m := markThread(t, msgAt(1, 0, "x")).SetDirectory(nil, kind)
		if got := m.CanRevokeDeletes(); got != want {
			t.Fatalf("%s: CanRevokeDeletes = %v, want %v", kind, got, want)
		}
	}
}
