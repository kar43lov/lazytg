package keymap

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
)

// namedKeys lists multi-character key names accepted in keymap.toml.
//
// We canonicalise aliases at parse time (escape→esc, return→enter,
// pgdn→pgdown, del→delete) so downstream key.Matches comparisons work
// regardless of which spelling the user picked. The right-hand side must
// match what bubbletea v2 emits via tea.KeyPressMsg.String().
var namedKeys = map[string]string{
	"enter":     "enter",
	"return":    "enter",
	"tab":       "tab",
	"esc":       "esc",
	"escape":    "esc",
	"space":     "space",
	"backspace": "backspace",
	"delete":    "delete",
	"del":       "delete",
	"up":        "up",
	"down":      "down",
	"left":      "left",
	"right":     "right",
	"home":      "home",
	"end":       "end",
	"pgup":      "pgup",
	"pgdown":    "pgdown",
	"pgdn":      "pgdown",
}

// parseKey converts a single user-facing chord description (e.g. "ctrl+r",
// "alt+enter", "shift+f1") into a single-key key.Binding.
//
// The returned binding has the canonical form "<ctrl>+<alt>+<shift>+<base>",
// matching bubbletea v2's KeyPressMsg.String() output. parseKey rejects:
//   - empty input
//   - duplicate or unknown modifiers
//   - modifier without a base key ("ctrl+")
//   - unknown named keys ("frobnicate")
//
// Help text is set to the canonical string with empty description so callers
// can override it later via SetHelp without losing the key column.
func parseKey(s string) (key.Binding, error) {
	raw := strings.ToLower(strings.TrimSpace(s))
	if raw == "" {
		return key.Binding{}, errors.New("keymap: empty key string")
	}

	parts := strings.Split(raw, "+")
	var modCtrl, modAlt, modShift bool
	var base string

	for i, p := range parts {
		isLast := i == len(parts)-1
		switch p {
		case "ctrl":
			if isLast {
				return key.Binding{}, fmt.Errorf("keymap: modifier %q requires a base key in %q", p, s)
			}
			if modCtrl {
				return key.Binding{}, fmt.Errorf("keymap: duplicate modifier %q in %q", p, s)
			}
			modCtrl = true
		case "alt":
			if isLast {
				return key.Binding{}, fmt.Errorf("keymap: modifier %q requires a base key in %q", p, s)
			}
			if modAlt {
				return key.Binding{}, fmt.Errorf("keymap: duplicate modifier %q in %q", p, s)
			}
			modAlt = true
		case "shift":
			if isLast {
				return key.Binding{}, fmt.Errorf("keymap: modifier %q requires a base key in %q", p, s)
			}
			if modShift {
				return key.Binding{}, fmt.Errorf("keymap: duplicate modifier %q in %q", p, s)
			}
			modShift = true
		default:
			if !isLast {
				return key.Binding{}, fmt.Errorf("keymap: unexpected token %q in %q (modifiers must precede the base key)", p, s)
			}
			base = p
		}
	}

	if base == "" {
		return key.Binding{}, fmt.Errorf("keymap: missing base key in %q", s)
	}

	canonBase, ok := canonicalBaseKey(base)
	if !ok {
		return key.Binding{}, fmt.Errorf("keymap: unknown key %q in %q", base, s)
	}

	canon := assembleCanonical(modCtrl, modAlt, modShift, canonBase)
	return key.NewBinding(
		key.WithKeys(canon),
		key.WithHelp(canon, ""),
	), nil
}

// canonicalBaseKey validates a base key (the part after all modifiers) and
// returns its canonical spelling. It accepts:
//   - any single rune (letters, digits, "?", "/", ".", etc.)
//   - named keys from the namedKeys table
//   - function keys f1..f12
func canonicalBaseKey(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	if mapped, ok := namedKeys[s]; ok {
		return mapped, true
	}
	// Function keys f1..f12.
	if strings.HasPrefix(s, "f") && len(s) >= 2 && len(s) <= 3 {
		n, err := strconv.Atoi(s[1:])
		if err == nil && n >= 1 && n <= 12 {
			return s, true
		}
	}
	// Single-rune keys (letters, digits, punctuation). Bubbletea emits these
	// verbatim in KeyPressMsg.String(), so we pass them through unchanged.
	if utf8.RuneCountInString(s) == 1 {
		return s, true
	}
	return "", false
}

// assembleCanonical joins modifiers and the base key in the order bubbletea
// v2 produces: ctrl, alt, shift, then key.
func assembleCanonical(c, a, sh bool, base string) string {
	var b strings.Builder
	if c {
		b.WriteString("ctrl+")
	}
	if a {
		b.WriteString("alt+")
	}
	if sh {
		b.WriteString("shift+")
	}
	b.WriteString(base)
	return b.String()
}
