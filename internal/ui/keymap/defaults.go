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
	Send           key.Binding
	Newline        key.Binding
	Reply          key.Binding
	OpenEditor     key.Binding
	ToggleHelp     key.Binding
	FocusNext      key.Binding
	FocusPrev      key.Binding
	NextChat       key.Binding
	PrevChat       key.Binding
	ScrollUp       key.Binding
	ScrollDown     key.Binding
	Search         key.Binding
	OpenPalette    key.Binding
	Download       key.Binding
	OpenMedia      key.Binding
	NextFolder     key.Binding
	PrevFolder     key.Binding
	ShowImage      key.Binding
	MarkMessage    key.Binding
	ForwardMessage key.Binding
	ReactMessage   key.Binding
	JumpToReply    key.Binding
	JumpBack       key.Binding
	EmojiPicker    key.Binding
	// CompleteEmoji shares its key with FocusNext on purpose. Tab means
	// "finish what I am typing" everywhere a shell or an editor is
	// involved, and it only reaches the composer when there is a
	// `:shortcode` under the cursor to finish — otherwise it cycles focus
	// as before. Rebinding either one leaves the other alone.
	CompleteEmoji key.Binding
	CopyMessage   key.Binding
	CopyLink      key.Binding
	EditMessage   key.Binding
	DeleteMsg     key.Binding
	Attach        key.Binding
	Quit          key.Binding
	// The three chat-list chords: live only while the list has the focus
	// and its filter is closed, so a bare letter is safe there for the
	// same reason it is in the thread.
	MuteChat     key.Binding
	PinChat      key.Binding
	ToggleUnread key.Binding
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
		ForwardMessage: key.NewBinding(
			key.WithKeys("f"),
			key.WithHelp("f", "forward message(s)"),
		),
		ReactMessage: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "react to message"),
		),
		JumpToReply: key.NewBinding(
			key.WithKeys("p"),
			key.WithHelp("p", "go to the replied message"),
		),
		JumpBack: key.NewBinding(
			key.WithKeys("ctrl+o"),
			key.WithHelp("ctrl+o", "back where you jumped from"),
		),
		EmojiPicker: key.NewBinding(
			key.WithKeys("alt+e"),
			key.WithHelp("alt+e", "emoji picker"),
		),
		CompleteEmoji: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "complete :emoji"),
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
		// The four thread-only actions below are bare letters, safe for
		// the same reason "o" is: the composer is a separate pane, so
		// nothing in the thread is typing. Each one is the letter its
		// action has in a file manager or a mail client, which is the
		// point — the chord should be one the user already knows.
		// Bracket keys for the folder tabs: they are what a browser and a
		// tmux session both use for "the next one of these", they are free
		// everywhere outside the composer, and they read as arrows on a
		// row of tabs.
		NextFolder: key.NewBinding(
			key.WithKeys("]"),
			key.WithHelp("]", "next folder"),
		),
		PrevFolder: key.NewBinding(
			key.WithKeys("["),
			key.WithHelp("[", "prev folder"),
		),
		// "i" for inline, next to "o" for open — the two answers to "let me
		// see it": one draws the picture here, the other hands the file to
		// whatever the system uses.
		ShowImage: key.NewBinding(
			key.WithKeys("i"),
			key.WithHelp("i", "show image inline"),
		),
		MarkMessage: key.NewBinding(
			key.WithKeys(" "),
			key.WithHelp("space", "mark message"),
		),
		CopyMessage: key.NewBinding(
			key.WithKeys("y"),
			key.WithHelp("y", "copy message(s)"),
		),
		// "l" for link: the address of the message under the cursor, the
		// way every official client offers "Copy link" on a channel or
		// supergroup post. A private chat has no address, and the key
		// says so instead of copying something that opens nothing.
		CopyLink: key.NewBinding(
			key.WithKeys("l"),
			key.WithHelp("l", "copy link to message"),
		),
		EditMessage: key.NewBinding(
			key.WithKeys("e"),
			key.WithHelp("e", "edit message"),
		),
		DeleteMsg: key.NewBinding(
			key.WithKeys("d"),
			key.WithHelp("d", "delete message(s)"),
		),
		Quit: key.NewBinding(
			key.WithKeys("ctrl+c", "ctrl+q"),
			key.WithHelp("ctrl+c", "quit"),
		),
		// Chat-list chords. "p" is also "go to the replied message" in the
		// thread; the two never share a focus, and pin is what "p" means
		// in every list that has pinning.
		MuteChat: key.NewBinding(
			key.WithKeys("m"),
			key.WithHelp("m", "mute / unmute chat"),
		),
		PinChat: key.NewBinding(
			key.WithKeys("p"),
			key.WithHelp("p", "pin / unpin chat"),
		),
		ToggleUnread: key.NewBinding(
			key.WithKeys("u"),
			key.WithHelp("u", "mark chat read / unread"),
		),
	}
}
