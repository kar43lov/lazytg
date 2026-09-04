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
	Send        key.Binding
	Newline     key.Binding
	Reply       key.Binding
	OpenEditor  key.Binding
	ToggleHelp  key.Binding
	FocusNext   key.Binding
	FocusPrev   key.Binding
	NextChat    key.Binding
	PrevChat    key.Binding
	ScrollUp    key.Binding
	ScrollDown  key.Binding
	Search      key.Binding
	OpenPalette key.Binding
	Download    key.Binding
	OpenMedia   key.Binding
	Attach      key.Binding
	Quit        key.Binding
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
		// Chat cycling is global on purpose: it works from the composer too, so
		// switching conversations never costs a focus change first.
		//
		// ctrl+tab reaches the application only in terminals that implement key
		// disambiguation (the Kitty keyboard protocol — Ghostty, kitty, WezTerm,
		// recent iTerm2); elsewhere the terminal sends a plain tab and focus
		// cycling takes it. The alt+n / alt+p aliases are the portable path, and
		// keymap.toml can rebind either.
		NextChat: key.NewBinding(
			key.WithKeys("ctrl+tab", "alt+n"),
			key.WithHelp("ctrl+tab/alt+n", "next chat"),
		),
		PrevChat: key.NewBinding(
			key.WithKeys("ctrl+shift+tab", "alt+p"),
			key.WithHelp("ctrl+shift+tab/alt+p", "prev chat"),
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
		// "ctrl+space" is the canonical name bubbletea v2 emits for
		// Ctrl-Space on most terminals. Some older terminals report it
		// as the NUL byte (canonical "ctrl+@") so the binding lists
		// both spellings; key.Matches compares against every entry.
		OpenPalette: key.NewBinding(
			key.WithKeys("ctrl+space", "ctrl+@"),
			key.WithHelp("ctrl+space", "command palette"),
		),
		Download: key.NewBinding(
			key.WithKeys("ctrl+d"),
			key.WithHelp("ctrl+d", "download media"),
		),
		// A bare letter, which is safe because this one is only live
		// while the thread pane has focus — the composer is a separate
		// pane, so nothing here is typing. "o" is what a file manager,
		// a mail client and a browser all use for "open", which is the
		// point: the chord should be the one the user already knows.
		OpenMedia: key.NewBinding(
			key.WithKeys("o"),
			key.WithHelp("o", "open media"),
		),
		Attach: key.NewBinding(
			key.WithKeys("ctrl+u"),
			key.WithHelp("ctrl+u", "attach file"),
		),
		Quit: key.NewBinding(
			key.WithKeys("ctrl+c", "ctrl+q"),
			key.WithHelp("ctrl+c", "quit"),
		),
	}
}
