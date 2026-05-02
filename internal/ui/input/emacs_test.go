package input

import (
	"testing"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/pgmac/lazytg/internal/ui/keymap"
)

// keyChord fakes a non-printable / modifier-bearing key event matching
// the bubbletea v2 KeyPressMsg.String() contract.
func keyChord(code rune, mod tea.KeyMod) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Mod: mod}
}

// keyText fakes a printable-character key event (e.g. "a").
func keyText(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Text: s, Code: []rune(s)[0]}
}

// drive runs msg through textarea.Update once and returns the new
// model. Used to exercise the actual textarea behaviour after we have
// patched its KeyMap with ApplyEmacsBindings.
func drive(t *testing.T, ta textarea.Model, msg tea.Msg) textarea.Model {
	t.Helper()
	out, _ := ta.Update(msg)
	return out
}

func newFocusedTextarea(t *testing.T) textarea.Model {
	t.Helper()
	ta := textarea.New()
	ta.SetWidth(40)
	ta.SetHeight(2)
	ApplyEmacsBindings(&ta.KeyMap)
	_ = ta.Focus()
	return ta
}

func TestApplyEmacsBindingsStripsConflicts(t *testing.T) {
	t.Parallel()

	ta := textarea.New()
	ApplyEmacsBindings(&ta.KeyMap)

	cases := []struct {
		name    string
		binding []string
		want    []string
	}{
		{"InsertNewline drops enter", ta.KeyMap.InsertNewline.Keys(), []string{"ctrl+m"}},
		{"LineEnd drops ctrl+e", ta.KeyMap.LineEnd.Keys(), []string{"end"}},
		{"LinePrevious drops ctrl+p", ta.KeyMap.LinePrevious.Keys(), []string{"up"}},
		{"LineNext drops ctrl+n", ta.KeyMap.LineNext.Keys(), []string{"down"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !equalKeys(tc.binding, tc.want) {
				t.Fatalf("got %v, want %v", tc.binding, tc.want)
			}
		})
	}
}

func TestApplyEmacsBindingsKeepsCharacterMotion(t *testing.T) {
	t.Parallel()

	ta := textarea.New()
	ApplyEmacsBindings(&ta.KeyMap)

	cases := []struct {
		name string
		got  []string
	}{
		{"CharacterForward keeps ctrl+f and right", ta.KeyMap.CharacterForward.Keys()},
		{"CharacterBackward keeps ctrl+b and left", ta.KeyMap.CharacterBackward.Keys()},
		{"WordForward keeps alt+f", ta.KeyMap.WordForward.Keys()},
		{"WordBackward keeps alt+b", ta.KeyMap.WordBackward.Keys()},
		{"DeleteWordBackward keeps ctrl+w", ta.KeyMap.DeleteWordBackward.Keys()},
		{"DeleteAfterCursor keeps ctrl+k", ta.KeyMap.DeleteAfterCursor.Keys()},
		{"DeleteBeforeCursor keeps ctrl+u", ta.KeyMap.DeleteBeforeCursor.Keys()},
		{"LineStart keeps ctrl+a", ta.KeyMap.LineStart.Keys()},
	}
	expectations := map[string]string{
		"CharacterForward keeps ctrl+f and right": "ctrl+f",
		"CharacterBackward keeps ctrl+b and left": "ctrl+b",
		"WordForward keeps alt+f":                 "alt+f",
		"WordBackward keeps alt+b":                "alt+b",
		"DeleteWordBackward keeps ctrl+w":         "ctrl+w",
		"DeleteAfterCursor keeps ctrl+k":          "ctrl+k",
		"DeleteBeforeCursor keeps ctrl+u":         "ctrl+u",
		"LineStart keeps ctrl+a":                  "ctrl+a",
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := expectations[tc.name]
			if !contains(tc.got, want) {
				t.Fatalf("expected %q in %v", want, tc.got)
			}
		})
	}
}

func TestEmacsCtrlALineStart(t *testing.T) {
	t.Parallel()

	ta := newFocusedTextarea(t)
	ta.SetValue("hello world")
	ta.CursorEnd()
	if ta.Column() != 11 {
		t.Fatalf("seed: cursor should be at end (11), got %d", ta.Column())
	}
	ta = drive(t, ta, keyChord('a', tea.ModCtrl))
	if ta.Column() != 0 {
		t.Fatalf("Ctrl+A should park cursor at column 0, got %d", ta.Column())
	}
}

func TestEmacsEndKeyLineEnd(t *testing.T) {
	t.Parallel()

	ta := newFocusedTextarea(t)
	ta.SetValue("hello world")
	ta.CursorStart()
	if ta.Column() != 0 {
		t.Fatalf("seed: cursor should be at 0, got %d", ta.Column())
	}
	ta = drive(t, ta, keyChord(tea.KeyEnd, 0))
	if ta.Column() != 11 {
		t.Fatalf("End should park cursor at 11, got %d", ta.Column())
	}
}

func TestEmacsCtrlEDoesNotMoveCursor(t *testing.T) {
	t.Parallel()

	ta := newFocusedTextarea(t)
	ta.SetValue("hello world")
	ta.CursorStart()
	ta = drive(t, ta, keyChord('e', tea.ModCtrl))
	if ta.Column() != 0 {
		t.Fatalf("Ctrl+E must be ignored by textarea (reserved for OpenEditor); cursor moved to %d", ta.Column())
	}
}

func TestEmacsCtrlWDeletesLastWord(t *testing.T) {
	t.Parallel()

	ta := newFocusedTextarea(t)
	ta.SetValue("hello world")
	ta.CursorEnd()
	ta = drive(t, ta, keyChord('w', tea.ModCtrl))
	if got := ta.Value(); got != "hello " {
		t.Fatalf("Ctrl+W: want %q, got %q", "hello ", got)
	}
}

func TestEmacsCtrlKDeletesAfterCursor(t *testing.T) {
	t.Parallel()

	ta := newFocusedTextarea(t)
	ta.SetValue("hello world")
	ta.CursorStart()
	for i := 0; i < 5; i++ {
		ta = drive(t, ta, keyChord(tea.KeyRight, 0))
	}
	ta = drive(t, ta, keyChord('k', tea.ModCtrl))
	if got := ta.Value(); got != "hello" {
		t.Fatalf("Ctrl+K: want %q, got %q", "hello", got)
	}
}

func TestEmacsCtrlUDeletesBeforeCursor(t *testing.T) {
	t.Parallel()

	ta := newFocusedTextarea(t)
	ta.SetValue("hello world")
	ta.CursorStart()
	for i := 0; i < 6; i++ {
		ta = drive(t, ta, keyChord(tea.KeyRight, 0))
	}
	ta = drive(t, ta, keyChord('u', tea.ModCtrl))
	if got := ta.Value(); got != "world" {
		t.Fatalf("Ctrl+U: want %q, got %q", "world", got)
	}
}

func TestEmacsAltFWordForward(t *testing.T) {
	t.Parallel()

	ta := newFocusedTextarea(t)
	ta.SetValue("hello world")
	ta.CursorStart()
	ta = drive(t, ta, keyChord('f', tea.ModAlt))
	// textarea v2 wordRight walks past the current word and parks at
	// the trailing space (column 5, the gap between "hello" and
	// "world"). Consult bubbles/textarea doWordRight() if this changes
	// upstream.
	if got := ta.Column(); got != 5 {
		t.Fatalf("Alt+F: want column 5, got %d", got)
	}
}

func TestEmacsAltBWordBackward(t *testing.T) {
	t.Parallel()

	ta := newFocusedTextarea(t)
	ta.SetValue("hello world")
	ta.CursorEnd()
	ta = drive(t, ta, keyChord('b', tea.ModAlt))
	// Word-backward parks at the start of the current word — the 'w'
	// of "world", column 6.
	if got := ta.Column(); got != 6 {
		t.Fatalf("Alt+B: want column 6, got %d", got)
	}
}

func TestApplyEmacsBindingsHandlesNilKeyMap(t *testing.T) {
	t.Parallel()
	ApplyEmacsBindings(nil) // must not panic
}

// equalKeys is order-sensitive — the textarea key list is already in a
// stable order, and ApplyEmacsBindings preserves that order.
func equalKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// _ unused-import guard for keymap so this file compiles even if the
// emacs cases later move to keymap-driven assertions.
var _ = keymap.Keymap{}
