package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/ui/keymap"
)

// newApp returns a fresh App with default keymap, sized for a roomy
// terminal so tests don't accidentally trip the small-terminal fallback.
func newApp(t *testing.T) App {
	t.Helper()
	a := New(Deps{Keymap: keymap.Default()})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return model.(App)
}

// keyText fakes a printable-character key event (e.g. "?", "a"). Matches the
// uv.Key.String() contract: when Text is non-empty and not " ", String
// returns Text verbatim, which is what key.Matches inspects.
func keyText(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Text: s, Code: []rune(s)[0]}
}

// keyChord fakes a non-printable / modifier-bearing key event. Code is the
// base rune (e.g. tea.KeyTab) and Mod is the OR'd modifier set (e.g.
// tea.ModShift|tea.ModCtrl). Mirrors the tea.KeyPressMsg constructor used
// by the bubbletea input parser.
func keyChord(code rune, mod tea.KeyMod) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Mod: mod}
}

func TestNewDefaultsFocusToChats(t *testing.T) {
	t.Parallel()

	a := New(Deps{Keymap: keymap.Default()})
	if a.Focus() != FocusChats {
		t.Fatalf("expected initial focus FocusChats, got %s", a.Focus())
	}
	if a.HelpVisible() {
		t.Fatalf("help should be hidden on construction")
	}
}

func TestWindowSizeAcceptsLargeTerminal(t *testing.T) {
	t.Parallel()

	a := New(Deps{Keymap: keymap.Default()})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	got := model.(App)
	if got.TooSmall() {
		t.Fatalf("100x30 should not be tooSmall")
	}
	if got.Width() != 100 || got.Height() != 30 {
		t.Fatalf("dimensions not stored: got %dx%d", got.Width(), got.Height())
	}
}

func TestWindowSizeRejectsSmallTerminal(t *testing.T) {
	t.Parallel()

	a := New(Deps{Keymap: keymap.Default()})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	got := model.(App)
	if !got.TooSmall() {
		t.Fatalf("60x20 should be tooSmall")
	}
	view := got.View().Content
	if !strings.Contains(view, "Terminal too small") {
		t.Fatalf("expected too-small notice in view, got %q", view)
	}
}

func TestWindowSizeRecoversFromSmallToLarge(t *testing.T) {
	t.Parallel()

	a := New(Deps{Keymap: keymap.Default()})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	if !model.(App).TooSmall() {
		t.Fatalf("expected tooSmall after small resize")
	}
	model, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if model.(App).TooSmall() {
		t.Fatalf("expected tooSmall to clear after large resize")
	}
}

func TestToggleHelpRoundTrip(t *testing.T) {
	t.Parallel()

	a := newApp(t)

	// "?" toggles help via cmd → focusCycledMsg / helpToggledMsg path.
	model, cmd := a.Update(keyText("?"))
	if cmd == nil {
		t.Fatalf("expected cmd from help toggle key")
	}
	model, _ = model.Update(cmd())
	if !model.(App).HelpVisible() {
		t.Fatalf("help should be visible after first ?")
	}

	// Pressing "?" again hides it (the help overlay swallows it directly).
	model, _ = model.Update(keyText("?"))
	if model.(App).HelpVisible() {
		t.Fatalf("help should hide after second ? while overlay is modal")
	}
}

func TestHelpModalSwallowsOtherKeys(t *testing.T) {
	t.Parallel()

	a := newApp(t)
	// Open help.
	model, cmd := a.Update(keyText("?"))
	model, _ = model.Update(cmd())
	if !model.(App).HelpVisible() {
		t.Fatalf("help should be visible")
	}

	// Tab while help is open must NOT cycle focus — the overlay is modal.
	startFocus := model.(App).Focus()
	model, cmd = model.Update(keyChord(tea.KeyTab, 0))
	if cmd != nil {
		// Even if a Cmd is returned we shouldn't apply the cycle —
		// but the overlay path returns no Cmd from Tab, so this is
		// expected to be nil. If it ever returns one, we must not
		// process focusCycledMsg.
		model, _ = model.Update(cmd())
	}
	if model.(App).Focus() != startFocus {
		t.Fatalf("focus should not cycle while help is open")
	}
}

// TestQuestionMarkNotHijackedFromInputPane locks the fix for the
// global "?" hijack: with the input pane focused, "?" is a printable
// character that must reach the textarea instead of toggling the help
// overlay. Toggling help on a "?" keystroke would make composing any
// message ending with a question mark literally impossible.
func TestQuestionMarkNotHijackedFromInputPane(t *testing.T) {
	t.Parallel()

	a := newApp(t)
	// Cycle focus into the input pane.
	model, cmd := a.Update(keyChord(tea.KeyTab, 0))
	model, _ = model.Update(cmd())
	if model.(App).Focus() != FocusInput {
		t.Fatalf("precondition: expected FocusInput, got %s", model.(App).Focus())
	}

	// "?" must not produce a help-toggle Cmd while input is focused.
	model, cmd = model.Update(keyText("?"))
	if cmd != nil {
		// If the chord was matched globally we'd see a helpToggledMsg;
		// applying it would flip Visible to true.
		model, _ = model.Update(cmd())
	}
	if model.(App).HelpVisible() {
		t.Fatalf("? while input is focused must not toggle help overlay")
	}
}

func TestHelpEscDismisses(t *testing.T) {
	t.Parallel()

	a := newApp(t)
	model, cmd := a.Update(keyText("?"))
	model, _ = model.Update(cmd())

	model, _ = model.Update(keyChord(tea.KeyEscape, 0))
	if model.(App).HelpVisible() {
		t.Fatalf("Esc should dismiss help overlay")
	}
}

func TestTabCyclesFocusForward(t *testing.T) {
	t.Parallel()

	a := newApp(t)
	if a.Focus() != FocusChats {
		t.Fatalf("precondition: expected FocusChats, got %s", a.Focus())
	}

	model, cmd := a.Update(keyChord(tea.KeyTab, 0))
	if cmd == nil {
		t.Fatalf("expected cmd from Tab keybinding")
	}
	model, _ = model.Update(cmd())
	if model.(App).Focus() != FocusInput {
		t.Fatalf("expected FocusInput after Tab, got %s", model.(App).Focus())
	}

	model, cmd = model.Update(keyChord(tea.KeyTab, 0))
	model, _ = model.Update(cmd())
	if model.(App).Focus() != FocusThread {
		t.Fatalf("expected FocusThread after second Tab, got %s", model.(App).Focus())
	}

	model, cmd = model.Update(keyChord(tea.KeyTab, 0))
	model, _ = model.Update(cmd())
	if model.(App).Focus() != FocusChats {
		t.Fatalf("expected wrap-around to FocusChats after third Tab, got %s", model.(App).Focus())
	}
}

func TestShiftTabCyclesFocusBackward(t *testing.T) {
	t.Parallel()

	a := newApp(t)
	model, cmd := a.Update(keyChord(tea.KeyTab, tea.ModShift))
	if cmd == nil {
		t.Fatalf("expected cmd from Shift+Tab keybinding")
	}
	model, _ = model.Update(cmd())
	if model.(App).Focus() != FocusThread {
		t.Fatalf("expected wrap-around to FocusThread after Shift+Tab from FocusChats, got %s", model.(App).Focus())
	}
}

func TestQuitKeyEmitsTeaQuit(t *testing.T) {
	t.Parallel()

	a := newApp(t)
	_, cmd := a.Update(keyChord('c', tea.ModCtrl))
	if cmd == nil {
		t.Fatalf("Ctrl+C should produce a Cmd")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg from Ctrl+C, got %T", msg)
	}
}

func TestViewIncludesStatusBar(t *testing.T) {
	t.Parallel()

	a := newApp(t)
	view := a.View().Content
	if !strings.Contains(view, "connecting") {
		t.Fatalf("status bar should be visible in default view; got %q", view)
	}
}

func TestScrollChordRoutedToThreadWhenFocused(t *testing.T) {
	t.Parallel()

	// ScrollUp / ScrollDown chords (default ctrl+b / ctrl+f) must reach the
	// thread pane's viewport when the thread is focused, otherwise a
	// user-customisable scroll binding is just decorative.
	a := newApp(t)
	model, cmd := a.Update(keyChord(tea.KeyTab, 0)) // chats → input
	model, _ = model.Update(cmd())
	model, cmd = model.Update(keyChord(tea.KeyTab, 0)) // input → thread
	model, _ = model.Update(cmd())
	if got := model.(App).Focus(); got != FocusThread {
		t.Fatalf("precondition: expected FocusThread, got %s", got)
	}

	// Chord must be consumed (no cmd fall-through to delegateToFocused
	// since we apply scroll before delegating). The viewport scroll itself
	// is exercised elsewhere — what we verify here is that the chord does
	// not fall through to the focused pane's Update (which would attempt
	// pagination).
	_, cmd = model.Update(keyChord('b', tea.ModCtrl))
	if cmd != nil {
		t.Fatalf("ScrollUp chord must not produce a fall-through Cmd, got %T", cmd())
	}
	_, cmd = model.Update(keyChord('f', tea.ModCtrl))
	if cmd != nil {
		t.Fatalf("ScrollDown chord must not produce a fall-through Cmd, got %T", cmd())
	}
}

func TestScrollChordIgnoredWhenInputFocused(t *testing.T) {
	t.Parallel()

	// Ctrl+B / Ctrl+F when the input pane is focused are emacs character-
	// motion chords that the textarea expects. They must NOT be hijacked
	// by the global scroll handler.
	a := newApp(t)
	model, cmd := a.Update(keyChord(tea.KeyTab, 0))
	model, _ = model.Update(cmd())
	if got := model.(App).Focus(); got != FocusInput {
		t.Fatalf("precondition: expected FocusInput, got %s", got)
	}

	// The textarea returns nil for unfocused-but-routed key updates here,
	// but the test's contract is just that App didn't claim the key as a
	// global scroll. Verify by checking focus is unchanged and no panic.
	updated, _ := model.Update(keyChord('b', tea.ModCtrl))
	if updated.(App).Focus() != FocusInput {
		t.Fatalf("Ctrl+B in input must not change focus, got %s", updated.(App).Focus())
	}
}

func TestFocusedPaneAppliesFocusFlag(t *testing.T) {
	t.Parallel()

	a := newApp(t)
	view := a.View().Content
	if !strings.Contains(view, "Chats (focused)") {
		t.Fatalf("Chats pane should advertise focus on first render; got %q", view)
	}

	model, cmd := a.Update(keyChord(tea.KeyTab, 0))
	model, _ = model.Update(cmd())
	view = model.(App).View().Content
	if !strings.Contains(view, "Chats") || strings.Contains(view, "Chats (focused)") {
		t.Fatalf("Chats pane should drop focus on Tab; got %q", view)
	}
	if !strings.Contains(view, "› ") {
		t.Fatalf("Input pane should advertise focus marker after Tab; got %q", view)
	}
}
