// Package attach hosts the file-attachment overlay of the lazytg TUI: a
// centred modal driven by a textinput-backed path field plus a small
// directory listing. Enter on a regular file emits a SubmitMsg which
// the app routes into UploadService.SendFile.
//
// We deliberately do not use bubbles/filepicker: that component
// consumes Esc/Enter without exposing a callback, which would force
// the app to pre-empt them at a higher layer than the rest of our
// overlays do. The custom listing in this package gives us the same
// modal lifecycle as the search and palette overlays.
package attach

// OpenedMsg is emitted by the app when the user activates the Attach
// keymap binding (Ctrl-U by default). The overlay reacts by becoming
// visible, focusing the path input, and listing the current
// directory.
type OpenedMsg struct {
	// ChatID is the chat the file should land in. Captured at open
	// time because the overlay is modal — the chat focus could change
	// underneath it before the user picks a file otherwise.
	ChatID int64
}

// ClosedMsg is emitted by the overlay when the user presses Esc, and
// broadcast by the app to drop the overlay. The app uses it to
// restore the previous focus target.
type ClosedMsg struct{}

// SubmitMsg is emitted when the user picks a file (Enter on a regular
// file). ChatID echoes back the value captured at open time; Path is
// the absolute path of the chosen file; Caption is whatever the user
// typed in the caption input (empty when blank).
type SubmitMsg struct {
	ChatID  int64
	Path    string
	Caption string
}
