// Package search hosts the search overlay of the lazytg TUI: a centred
// modal driven by a textinput, a debounced query → service.Search
// pipeline, and a result list whose Enter publishes a JumpMsg the
// app converts into a chat switch + thread scroll.
//
// The overlay is created with a Service so unit tests
// can swap in a fake without spinning up SQLite. Live wiring (the
// search.Service handle, the keymap binding, focus precedence over the
// per-pane handlers) lives in the app package — see app/update.go.
package search

import "github.com/pgmac/lazytg/internal/core/search"

// OpenedMsg is emitted by the app when the user activates the Search
// keymap binding. The overlay reacts by focusing the textinput and
// clearing previous results.
type OpenedMsg struct{}

// ClosedMsg is emitted by the overlay (Esc) and broadcast by the app
// to drop the overlay. The app uses it to restore the previous focus
// target.
type ClosedMsg struct{}

// QueryChangedMsg is emitted by the debounce tick when the textinput
// value has not changed for the debounce window. The Update converts
// it into a service.Search call and emits ResultsMsg.
type QueryChangedMsg struct{ Query string }

// ResultsMsg carries the result of an in-flight service.Search. Hits
// is empty (and Err nil) on a successful empty-result query — the
// view is responsible for distinguishing "no hits yet" from "no
// matches".
type ResultsMsg struct {
	Hits []search.Hit
	Err  error
}

// JumpMsg is emitted by the overlay when the user presses Enter on
// a result. The app converts it into a core/events.SearchJumpRequested
// + chats Update + thread.OpenChat / ScrollTo dance.
type JumpMsg struct{ Hit search.Hit }
