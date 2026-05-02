package input

import (
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
)

// ApplyEmacsBindings configures the embedded textarea so the readline
// chords lazytg cares about behave the way an emacs-trained user
// expects, *and* so the chords reserved for the input pane (Send,
// Newline, Reply, OpenEditor, history Prev/Next) reach our own Update
// before the textarea consumes them.
//
// The textarea's stock KeyMap already covers most of what we want:
//
//   - Ctrl+A / Ctrl+E → LineStart / LineEnd
//   - Ctrl+W → DeleteWordBackward
//   - Ctrl+K / Ctrl+U → DeleteAfterCursor / DeleteBeforeCursor
//   - Alt+F / Alt+B → WordForward / WordBackward
//   - Ctrl+F / Ctrl+B → CharacterForward / CharacterBackward
//
// What we strip:
//
//   - "enter" from InsertNewline — Send (Enter) lives at the input
//     pane level; if we leave the default the textarea inserts a
//     literal newline and the chat never sends.
//   - "ctrl+e" from LineEnd — collides with OpenEditor. The "end" key
//     remains as the canonical line-end gesture.
//   - "ctrl+p" / "ctrl+n" from LinePrevious / LineNext — collides with
//     history Prev / Next. Arrow keys remain for in-line cursor moves.
//
// We *deliberately* keep Ctrl+B / Ctrl+F bound to character motion
// inside the textarea even though the global keymap binds them to
// ScrollUp / ScrollDown for the thread pane. The app routes keys to
// the focused pane, so the conflict is harmless: when the input pane
// is focused, the user expects character navigation; when the thread
// pane is focused, the textarea never receives the key.
func ApplyEmacsBindings(km *textarea.KeyMap) {
	if km == nil {
		return
	}
	km.InsertNewline.SetKeys(filterKeys(km.InsertNewline.Keys(), "enter")...)
	km.LineEnd.SetKeys(filterKeys(km.LineEnd.Keys(), "ctrl+e")...)
	km.LinePrevious.SetKeys(filterKeys(km.LinePrevious.Keys(), "ctrl+p")...)
	km.LineNext.SetKeys(filterKeys(km.LineNext.Keys(), "ctrl+n")...)
}

// filterKeys returns a copy of keys with every match for drop removed.
// Callers pass the existing key set; SetKeys requires a variadic so the
// helper returns a slice rather than a single string.
func filterKeys(keys []string, drop ...string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if containsString(drop, k) {
			continue
		}
		out = append(out, k)
	}
	return out
}

func containsString(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// Re-export key.Binding so callers don't have to import bubbles/key
// just to read m.km. Kept tucked next to ApplyEmacsBindings because
// the only other place key.Binding shows up in this package is the
// textarea customisation.
var _ = key.Binding{}
