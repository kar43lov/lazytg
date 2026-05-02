package keymap

import (
	"strings"
	"testing"
)

func TestParseKey_Valid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input string
		want  string // canonical form
	}{
		{"enter", "enter"},
		{"return", "enter"},
		{"tab", "tab"},
		{"esc", "esc"},
		{"escape", "esc"},
		{"pgup", "pgup"},
		{"pgdown", "pgdown"},
		{"pgdn", "pgdown"},
		{"home", "home"},
		{"end", "end"},
		{"space", "space"},
		{"backspace", "backspace"},
		{"delete", "delete"},
		{"del", "delete"},
		{"a", "a"},
		{"A", "a"}, // case-insensitive input
		{"?", "?"},
		{"/", "/"},
		{"f1", "f1"},
		{"f12", "f12"},
		{"ctrl+a", "ctrl+a"},
		{"ctrl+e", "ctrl+e"},
		{"alt+enter", "alt+enter"},
		{"shift+tab", "shift+tab"},
		{"shift+f1", "shift+f1"},
		{"ctrl+alt+x", "ctrl+alt+x"},
		// Modifier order is normalised to ctrl→alt→shift regardless of
		// what the user wrote in the TOML file.
		{"alt+ctrl+x", "ctrl+alt+x"},
		{"shift+ctrl+f1", "ctrl+shift+f1"},
		{"  ctrl+r  ", "ctrl+r"}, // whitespace trimmed
	}

	for _, c := range cases {
		c := c
		t.Run(c.input, func(t *testing.T) {
			t.Parallel()
			b, err := parseKey(c.input)
			if err != nil {
				t.Fatalf("parseKey(%q) returned error: %v", c.input, err)
			}
			keys := b.Keys()
			if len(keys) != 1 {
				t.Fatalf("parseKey(%q) returned %d keys, want 1", c.input, len(keys))
			}
			if keys[0] != c.want {
				t.Errorf("parseKey(%q) = %q, want %q", c.input, keys[0], c.want)
			}
		})
	}
}

func TestParseKey_Invalid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input        string
		wantContains string
	}{
		{"", "empty"},
		{"   ", "empty"},
		{"frobnicate", "unknown key"},
		{"ctrl+", "missing base key"},
		{"ctrl+ctrl+a", "duplicate modifier"},
		{"a+ctrl", "unexpected token"}, // base key not last
		{"ctrl+frobnicate", "unknown key"},
		{"f13", "unknown key"}, // out of f1..f12 range
		{"f0", "unknown key"},
		{"ctrl+f99", "unknown key"},
	}

	for _, c := range cases {
		c := c
		t.Run(c.input, func(t *testing.T) {
			t.Parallel()
			_, err := parseKey(c.input)
			if err == nil {
				t.Fatalf("parseKey(%q) succeeded, want error", c.input)
			}
			if !strings.Contains(err.Error(), c.wantContains) {
				t.Errorf("parseKey(%q) error = %q, want substring %q", c.input, err.Error(), c.wantContains)
			}
		})
	}
}
