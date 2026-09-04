package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/ui/keymap"
)

// appForComposer builds an app sized to a real terminal with the
// composer focused, which is where typing happens.
func appForComposer(t *testing.T) App {
	t.Helper()
	a := New(Deps{Keymap: keymap.Default()})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	a = model.(App)
	return tabTo(t, a, FocusInput)
}

// typeInto sends each rune of s to the app as a key press.
func typeInto(t *testing.T, a App, s string) App {
	t.Helper()
	for _, r := range s {
		model, _ := a.Update(keyText(string(r)))
		a = model.(App)
	}
	return a
}

// The composer was one row, so a message longer than the width scrolled
// its own beginning out of sight as it was typed — there was no way to
// see what you had just written. It grows now, and the panes above give
// up the rows it takes.
func TestComposer_GrowsWithTheMessageAndGivesTheRowsBack(t *testing.T) {
	t.Parallel()

	a := appForComposer(t)
	restRows := a.input.Rows()
	restPaneH := a.layout().paneH

	// Three screen-widths of text: comfortably more than one row, and
	// still inside the four-row ceiling.
	a = typeInto(t, a, strings.Repeat("длинное сообщение ", 12))

	grown := a.input.Rows()
	if grown <= restRows {
		t.Fatalf("composer stayed at %d rows while a long message was typed", grown)
	}
	if got := a.layout().paneH; got >= restPaneH {
		t.Fatalf("panes kept %d rows while the composer grew to %d — the frame would overflow", got, grown)
	}

	// Deleting the draft must give the rows back.
	for i := 0; i < 12*len([]rune("длинное сообщение ")); i++ {
		model, _ := a.Update(keyChord(tea.KeyBackspace, 0))
		a = model.(App)
	}
	if got := a.input.Value(); got != "" {
		t.Fatalf("draft still holds %q after deleting every rune", got)
	}
	if got := a.input.Rows(); got != restRows {
		t.Fatalf("composer stayed at %d rows after the draft was cleared, want %d", got, restRows)
	}
	if got := a.layout().paneH; got != restPaneH {
		t.Fatalf("panes kept %d rows after the composer shrank, want %d back", got, restPaneH)
	}
}

// The ceiling is what keeps a long message from eating the conversation.
func TestComposer_StopsGrowingAtTheCeiling(t *testing.T) {
	t.Parallel()

	a := appForComposer(t)
	a = typeInto(t, a, strings.Repeat("это очень длинное сообщение которое точно не влезет ", 20))

	if got, want := a.input.Rows(), 4+2; got != want {
		t.Fatalf("composer grew to %d rows, want it capped at %d", got, want)
	}
}

// The frame must still fit the terminal with the composer at full
// stretch — the whole point of moving rows between them.
func TestComposer_FrameStillFitsWhenTheComposerIsFull(t *testing.T) {
	t.Parallel()

	a := appForComposer(t)
	a = typeInto(t, a, strings.Repeat("текст ", 60))

	if got := strings.Count(a.View().Content, "\n") + 1; got > 24 {
		t.Fatalf("frame is %d rows tall on a 24-row terminal", got)
	}
}
