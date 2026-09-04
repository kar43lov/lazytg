package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/ui/keymap"
	"github.com/kar43lov/lazytg/internal/ui/overlay"
	"github.com/kar43lov/lazytg/internal/ui/panes/thread"
)

func newAppForEmoji(t *testing.T) App {
	t.Helper()
	threadModel := thread.New()
	a := New(Deps{Keymap: keymap.Default(), Thread: &threadModel})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return model.(App)
}

func TestAltE_OpensThePicker(t *testing.T) {
	t.Parallel()

	a := newAppForEmoji(t)
	model, cmd := a.Update(keyChord('e', tea.ModAlt))
	a = model.(App)
	if cmd == nil {
		t.Fatal("alt+e produced no command")
	}
	model, _ = a.Update(cmd())
	a = model.(App)
	if !a.emojiPicker.Visible() {
		t.Fatal("the picker did not open")
	}
	// It is drawn over the layout rather than replacing it: choosing a
	// smiley must not hide the message being written.
	view := a.View().Content
	if !strings.Contains(view, "type to search") {
		t.Fatal("the picker is not on screen")
	}
}

func TestPickedEmoji_LandsInTheComposer(t *testing.T) {
	t.Parallel()

	a := newAppForEmoji(t)
	model, _ := a.Update(overlay.EmojiPickedMsg{Char: "🚀"})
	a = model.(App)

	if got := a.input.Value(); !strings.Contains(got, "🚀") {
		t.Fatalf("composer holds %q", got)
	}
	// And the focus follows, because the next thing the user does is keep
	// typing.
	if a.focus != FocusInput {
		t.Fatalf("focus is %v after picking, want the composer", a.focus)
	}
}

// Tab has two meanings and the composer only gets it while there is a
// shortcode to finish.
func TestTab_StillCyclesFocusWithNothingToComplete(t *testing.T) {
	t.Parallel()

	a := newAppForEmoji(t)
	before := a.focus
	model, cmd := a.Update(keyChord(tea.KeyTab, 0))
	a = model.(App)
	if cmd != nil {
		model, _ = a.Update(cmd())
		a = model.(App)
	}
	if a.focus == before {
		t.Fatalf("focus stayed on %v — tab was swallowed", before)
	}
}

func TestTab_CompletesInsteadOfCyclingWhenAShortcodeIsTyped(t *testing.T) {
	t.Parallel()

	a := newAppForEmoji(t)
	a = a.setFocus(FocusInput)
	for _, r := range ":rocket" {
		model, _ := a.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		a = model.(App)
	}

	model, cmd := a.Update(keyChord(tea.KeyTab, 0))
	a = model.(App)
	if cmd != nil {
		model, _ = a.Update(cmd())
		a = model.(App)
	}
	if a.focus != FocusInput {
		t.Fatalf("focus moved to %v — tab was taken by the cycler", a.focus)
	}
	if got := a.input.Value(); !strings.Contains(got, "🚀") {
		t.Fatalf("composer holds %q, want the emoji", got)
	}
}
