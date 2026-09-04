package thread

import (
	"strings"
	"testing"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

func TestReactionRow_ShowsCountsAndMarksOurOwn(t *testing.T) {
	t.Parallel()

	row := reactionRow([]domain.Reaction{
		{Emoticon: "👍", Count: 3, Chosen: true},
		{Emoticon: "❤", Count: 1},
	})
	if !strings.Contains(row, "👍") || !strings.Contains(row, "3") {
		t.Fatalf("row does not show the count: %q", row)
	}
	// The boxed one is what tells the user that pressing the key again
	// takes their reaction back. Without it the gesture is a coin flip.
	if !strings.Contains(row, "[👍 3]") {
		t.Fatalf("our own reaction is not marked: %q", row)
	}
	// A count of one adds nothing — the emoji already says one person did
	// it — and a column of "👍 1  ❤ 1" is noise.
	if strings.Contains(row, "❤ 1") {
		t.Fatalf("a count of one was rendered: %q", row)
	}
}

func TestReactionRow_EmptyForAMessageWithNone(t *testing.T) {
	t.Parallel()

	if got := reactionRow(nil); got != "" {
		t.Fatalf("reactionRow(nil) = %q", got)
	}
	if got := reactionRow([]domain.Reaction{{Emoticon: ""}}); got != "" {
		t.Fatalf("a reaction with no emoji rendered %q", got)
	}
}

func TestApplyReactions_RedrawsTheOneMessage(t *testing.T) {
	t.Parallel()

	m := markThread(t, msgAt(1, 0, "one"), msgAt(2, 1, "two"))
	m = m.ApplyReactions(2, []domain.Reaction{{Emoticon: "🔥", Count: 2, Chosen: true}})

	body, _ := m.renderContent()
	if !strings.Contains(body, "🔥") {
		t.Fatalf("the reaction never reached the screen:\n%s", body)
	}
	if got := m.ReactionsOf(1); got != nil {
		t.Fatalf("the other message gained reactions: %v", got)
	}
}

// Reaction updates repeat: the server sends the whole list every time anyone
// reacts, and re-rendering a hundred-message viewport on a no-op is a cost
// the reader sees.
func TestApplyReactions_NoOpOnAnIdenticalSet(t *testing.T) {
	t.Parallel()

	rs := []domain.Reaction{{Emoticon: "👍", Count: 1}}
	before := markThread(t, msgAt(1, 0, "one")).ApplyReactions(1, rs)
	after := before.ApplyReactions(1, []domain.Reaction{{Emoticon: "👍", Count: 1}})

	if !sameReactions(before.ReactionsOf(1), after.ReactionsOf(1)) {
		t.Fatal("an identical update changed the stored reactions")
	}
}

// Reactions arrive for the whole account, including messages this pane is not
// showing.
func TestApplyReactions_IgnoresAMessageThatIsNotHere(t *testing.T) {
	t.Parallel()

	m := markThread(t, msgAt(1, 0, "one"))
	got := m.ApplyReactions(999, []domain.Reaction{{Emoticon: "👍", Count: 1}})
	if got.ReactionsOf(1) != nil {
		t.Fatal("the update landed on the wrong message")
	}
}

func TestApplyReactions_DoesNotMutateThePriorModel(t *testing.T) {
	t.Parallel()

	before := markThread(t, msgAt(1, 0, "one"))
	after := before.ApplyReactions(1, []domain.Reaction{{Emoticon: "👍", Count: 1}})

	if before.ReactionsOf(1) != nil {
		t.Fatal("the earlier model gained the reaction")
	}
	if len(after.ReactionsOf(1)) != 1 {
		t.Fatalf("the later model holds %v", after.ReactionsOf(1))
	}
}
