// Package input hosts the bottom composer of the lazytg TUI: textarea,
// reply-state, $EDITOR delegation, and emacs-style readline bindings.
//
// The pane wraps bubbles/textarea v2 with three additions:
//
//  1. ApplyEmacsBindings prunes the textarea's stock KeyMap so the
//     chords reserved for the input pane (Send, Newline, Reply,
//     OpenEditor, history Prev/Next) reach Update before the textarea
//     consumes them. See emacs.go for the conflict matrix.
//  2. A History ring buffer behind Ctrl+P / Ctrl+N — see history.go.
//  3. SendServiceInterface, the gotd-free contract Update calls when
//     Send fires. Tests swap in fakes; production wires
//     internal/core/sync.SendService.
package input

import (
	"context"
	"log/slog"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/ui/keymap"
)

// SendServiceInterface is the gotd-free contract the input pane needs
// from the outgoing pipeline. The signature mirrors
// internal/core/sync.SendService.SendText so the production wiring is a
// trivial assignment; tests swap in a fake that records the call.
type SendServiceInterface interface {
	SendText(ctx context.Context, chatID int64, text string, replyTo int) (localID string, err error)
}

// composerHeight is the resting height of the textarea body, and
// composerMaxHeight the tallest it grows to. Two more rows sit around it
// in the pane — the optional reply hint above and the hint line below —
// so the composer occupies composerHeight+2 rows at rest and
// composerMaxHeight+2 at full stretch. Rows() is what the app layout
// asks for; nothing outside this package should assume a fixed number.
//
// A single row was the Stage 2 decision, with $EDITOR as the escape
// hatch for anything longer. It reads as broken the moment you write two
// sentences: the text scrolls out of sight one line at a time and there
// is no way to see what you just typed. Four rows covers the ordinary
// long message without permanently spending screen on the composer, and
// Ctrl-E is still there for a genuinely long one.
const (
	composerHeight    = 1
	composerMaxHeight = 4
)

// composerCharLimit caps how many characters can sit in the textarea
// at once. Telegram itself rejects messages over 4096 bytes; we cap at
// 4096 runes for parity. Anything larger is the user's cue to press
// Ctrl+E and edit in $EDITOR.
const composerCharLimit = 4096

// emptyHint is the help line shown below the textarea when it is
// empty. Visible whether the pane is focused or not so the user can
// learn the chords just by looking at the screen.
const emptyHint = "Enter to send, Alt+Enter for newline, Ctrl+E for editor, Ctrl+R to reply"

// editHint replaces it while the composer is rewriting a message, because
// Enter then means something else entirely.
const editHint = "editing a message — Enter to save, Esc to cancel"

// placeholder is the text rendered inside the empty textarea body. It
// uses an em-dash ellipsis so the Unicode profile of the composer is
// consistent with the status bar's "connecting…" indicator.
const placeholder = "Type a message…"

// Model is the input-pane Bubble Tea model. Width is set by the app on
// every WindowSizeMsg; Focused is set by the app's focus cycler.
//
// All other fields are private because they describe state the app is
// not allowed to poke at directly: the textarea owns its own cursor
// position, the history owns its browse cursor, and the replyTo
// pointer is set exclusively via SetReplyMsg so the lifecycle is
// linear (set → consume on Send → clear).
type Model struct {
	Width   int
	Focused bool

	textarea textarea.Model
	history  *History
	keymap   keymap.Keymap
	send     SendServiceInterface
	log      *slog.Logger

	chatID  int64
	replyTo *domain.Message

	// inFlight remembers the body and reply pointer of every send that
	// has been dispatched but not yet reached a terminal Sent / Failed
	// state on the bus. Used by Update to restore the draft on
	// OutgoingMessageStateChanged{Failed} — SendService publishes this
	// async (FloodWait, network retries, validation), so the
	// synchronous SendFailedMsg path alone covers only the optimistic-
	// store-write failure surface and would silently drop everything
	// else.
	inFlight map[string]inFlightDraft

	// editing is non-nil while the composer is rewriting a message that
	// already exists rather than composing a new one. See edit.go.
	editing *editTarget

	// emoji is non-nil while a `:shortcode` completion is cycling. See
	// emoji.go.
	emoji *emojiCompletion

	// drafts holds what was half-written in each chat, so switching
	// conversations does not carry a message to the wrong person. See
	// drafts.go.
	drafts map[int64]draft
}

// inFlightDraft is the state the input pane needs to rebuild a textarea
// composition after a failed send: the body text and the parent message
// that was armed as the reply target. Keyed by LocalID returned from
// SendService.SendText.
type inFlightDraft struct {
	text    string
	replyTo *domain.Message
}

// New returns a placeholder Model with no send service or keymap
// wired. Used by app construction in tests and by the early Stage 2
// skeleton. Calling Update on a no-send model will silently ignore
// Enter (the textarea simply clears) so smoke tests stay deterministic.
func New() Model {
	return NewWithDeps(nil, keymap.Default(), nil)
}

// NewWithDeps returns a fully-wired Model. send may be nil in tests
// that exercise UI plumbing only; km is required (use keymap.Default()
// if no override file is in play); log may be nil and will fall back to
// slog.Default.
func NewWithDeps(send SendServiceInterface, km keymap.Keymap, log *slog.Logger) Model {
	if log == nil {
		log = slog.Default()
	}
	ta := textarea.New()
	ta.Placeholder = placeholder
	ta.Prompt = blurredPrompt
	// The composer grows with what is being written and shrinks back when
	// it is sent. One row is the resting size — most messages are one
	// line, and a permanently tall composer would take rows away from the
	// conversation for nothing. Four is the ceiling: past that the text
	// scrolls inside the box, and anything genuinely long belongs in
	// $EDITOR via Ctrl-E.
	ta.DynamicHeight = true
	ta.MinHeight = composerHeight
	ta.MaxHeight = composerMaxHeight
	ta.SetHeight(composerHeight)
	ta.CharLimit = composerCharLimit
	ta.ShowLineNumbers = false
	ta.SetVirtualCursor(true)
	ApplyEmacsBindings(&ta.KeyMap)
	// Defer Focus() to SetFocus(true) so the textarea state matches
	// the app-level focus invariant from the start.
	return Model{
		textarea: ta,
		history:  NewHistory(0),
		keymap:   km,
		send:     send,
		log:      log,
		inFlight: make(map[string]inFlightDraft),
	}
}

// Init focuses the textarea if the model boots already focused (the
// app does not explicitly call SetFocus on the initially-focused pane
// because the constructor already records Focused=true for FocusChats
// only — the input pane starts blurred). Returning the focus Cmd here
// keeps the cursor blink scheduling consistent.
func (m Model) Init() tea.Cmd {
	if m.Focused {
		return m.textarea.Focus()
	}
	return nil
}

// Value returns the current textarea contents. Exposed for tests so
// they can assert what would be sent without rendering the View.
func (m Model) Value() string { return m.textarea.Value() }

// Rows reports how many terminal rows the composer needs right now: the
// textarea's current height plus the reply-hint row above it and the
// hint row below.
//
// The app layout reads this on every frame rather than a constant,
// because the textarea grows as the user writes. Panes above give up a
// row when it grows and take it back when the message is sent.
func (m Model) Rows() int {
	h := m.textarea.Height()
	if h < composerHeight {
		h = composerHeight
	}
	if h > composerMaxHeight {
		h = composerMaxHeight
	}
	return h + 2
}

// ReplyTo returns the currently-armed reply pointer or nil. Test
// helper.
func (m Model) ReplyTo() *domain.Message { return m.replyTo }

// ChatID returns the chat id the pane is currently bound to (set via
// SetChatMsg). Test helper.
func (m Model) ChatID() int64 { return m.chatID }

// History returns the underlying ring for tests that want to inspect
// browse state without round-tripping through Ctrl+P / Ctrl+N.
func (m Model) History() *History { return m.history }

// SetWidth updates the pane width and propagates it to the textarea.
// The app calls this on every resize. We deduct one column for the
// leading prompt glyph so the textarea body stays aligned with the
// reply hint above.
func (m Model) SetWidth(w int) Model {
	m.Width = w
	if w < 4 {
		w = 4
	}
	m.textarea.SetWidth(w)
	return m
}

// SetFocus toggles the pane's focused state and mirrors it onto the
// textarea so the cursor blinks (or stops) at the right moments.
// Returns the model — the focus Cmd is intentionally dropped because
// the app's Update routes the next tick anyway and tests don't need
// the blink scheduling.
//
// We also swap the textarea's Prompt glyph (focused = "› ", blurred =
// "  ") so external integration tests can grep the rendered View for
// a deterministic focus marker without parsing ANSI cursor escapes.
func (m Model) SetFocus(f bool) Model {
	m.Focused = f
	if f {
		m.textarea.Prompt = focusedPrompt
		_ = m.textarea.Focus()
	} else {
		m.textarea.Prompt = blurredPrompt
		m.textarea.Blur()
	}
	return m
}

// focusedPrompt and blurredPrompt are the prompt glyphs the textarea
// uses to advertise focus. The single-character glyph keeps the body
// width math (textarea.SetWidth - 2) stable across focus changes.
const (
	focusedPrompt = "› "
	blurredPrompt = "  "
)

// setValue writes s into the textarea, parks the cursor at the end,
// and resets the history browse cursor so the next Prev starts from
// the newest entry. Used by both Update (history navigation, editor
// roundtrip) and tests.
func (m *Model) setValue(s string) {
	m.textarea.SetValue(s)
	m.textarea.CursorEnd()
	m.history.Reset()
}

// resetComposer clears the textarea and any armed reply pointer. Called
// after a successful Send so the next message starts from a clean
// slate.
func (m *Model) resetComposer() {
	m.textarea.Reset()
	m.replyTo = nil
	m.history.Reset()
}
