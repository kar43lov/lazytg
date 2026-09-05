// Package palette hosts the L1 command palette of the lazytg TUI: a
// centred modal driven by a textinput that fuzzy-matches against the
// user's chats, ranked by frecency (recency + frequency of visits).
//
// The L1 scope is intentionally narrow — only chat switching. Global
// commands (the "L2" palette behind a `>` prefix) are deferred to v0.2
// to keep this iteration shippable. See docs/plans/lazytg-v0.1.0.md.
//
// The palette is created with a FrecencyStore + ChatLister so unit
// tests can swap both for fakes without spinning up SQLite. Live
// wiring (the keymap binding, focus precedence over per-pane handlers,
// the chat-switch + RecordVisit dance) lives in the app package.
package palette

// OpenedMsg is emitted by the app when the user activates the
// OpenPalette keymap binding. The palette reacts by focusing the
// textinput, refreshing the candidate list, and resetting the cursor.
type OpenedMsg struct{}

// ClosedMsg is emitted by the palette (Esc) and broadcast by the app
// to drop the overlay. The app uses it to restore the previous focus
// target.
type ClosedMsg struct{}

// QueryChangedMsg is emitted internally on every textinput change.
// The Update converts it into a fuzzy filter pass; exporting it lets
// future call sites (tests, programmatic seeding) trigger the same
// path without synthesising key events.
type QueryChangedMsg struct{ Query string }

// SelectedMsg is emitted when the user presses Enter on a row. The
// app converts it into a chat switch (chats Update + thread.OpenChat)
// followed by FrecencyStore.RecordVisit so subsequent palette opens
// rank the just-visited chat higher.
type SelectedMsg struct{ ChatID int64 }

// OpenUsernameMsg is emitted on Enter when the query names a public handle
// that matches nothing in the list — "@durov", "t.me/durov" — and the
// user wants the conversation that is not here yet.
type OpenUsernameMsg struct{ Username string }

// LoadedMsg is the result of Open()'s candidate-list refresh: the
// frecency-ranked top-N chat ids merged with whatever the chat list
// can supply for chats that have never been visited. The palette
// applies it to its internal items slice; tests assert against the
// resulting Items() snapshot.
type LoadedMsg struct {
	Items []Item
	Err   error
}
