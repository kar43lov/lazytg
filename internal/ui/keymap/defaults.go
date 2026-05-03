// Package keymap defines configurable key bindings for the lazytg TUI.
//
// Defaults follow the emacs/readline tradition: navigation through Ctrl-key
// chords, Tab cycles focus, "?" toggles help, and Enter sends the message.
// Vim-style bindings are deliberately deferred to v0.2 to avoid the half-baked
// modal experience that has hurt similar projects (see CLAUDE.md "What we are
// not doing").
package keymap

import "charm.land/bubbles/v2/key"

// Keymap holds every action-level key.Binding the TUI consumes.
//
// Field names mirror the user-visible action; TOML keys are derived through
// the snake_case mapping table in loader.go. New bindings must be registered
// in three places: this struct, defaults(), and bindingFields() in loader.go.
type Keymap struct {
	Send       key.Binding
	Newline    key.Binding
	Reply      key.Binding
	OpenEditor key.Binding
	ToggleHelp key.Binding
	FocusNext  key.Binding
	FocusPrev  key.Binding
	ScrollUp   key.Binding
	ScrollDown key.Binding
	Search     key.Binding
	Quit       key.Binding
}

// Default returns the built-in emacs-flavoured keymap.
//
// Help strings are intentionally short — the help overlay (Task 10) renders
// them in a two-column table, so anything wider than ~16 chars wraps poorly.
func Default() Keymap {
	return Keymap{
		Send: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "send"),
		),
		Newline: key.NewBinding(
			key.WithKeys("alt+enter"),
			key.WithHelp("alt+enter", "newline"),
		),
		Reply: key.NewBinding(
			key.WithKeys("ctrl+r"),
			key.WithHelp("ctrl+r", "reply"),
		),
		OpenEditor: key.NewBinding(
			key.WithKeys("ctrl+e"),
			key.WithHelp("ctrl+e", "open editor"),
		),
		ToggleHelp: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "toggle help"),
		),
		FocusNext: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "focus next"),
		),
		FocusPrev: key.NewBinding(
			key.WithKeys("shift+tab"),
			key.WithHelp("shift+tab", "focus prev"),
		),
		ScrollUp: key.NewBinding(
			key.WithKeys("ctrl+b", "pgup"),
			key.WithHelp("ctrl+b/pgup", "scroll up"),
		),
		ScrollDown: key.NewBinding(
			key.WithKeys("ctrl+f", "pgdown"),
			key.WithHelp("ctrl+f/pgdn", "scroll down"),
		),
		Search: key.NewBinding(
			key.WithKeys("/"),
			key.WithHelp("/", "search"),
		),
		Quit: key.NewBinding(
			key.WithKeys("ctrl+c", "ctrl+q"),
			key.WithHelp("ctrl+c", "quit"),
		),
	}
}
