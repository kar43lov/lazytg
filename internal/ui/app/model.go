// Package app composes the lazytg TUI: chats list (left), thread view
// (right), input composer (bottom), 1-line status bar (very bottom), and a
// modal help overlay.
//
// The model owns global concerns — dimensions, focus cycling, help
// visibility, terminal-too-small fallback — and delegates everything else to
// the per-pane sub-models. Wiring of side effects (event bus → tea.Msg
// fan-in, keymap loading, send/history services) is finalised in Stage 2
// Task 11; for Task 6 the app stays self-contained and side-effect free so
// it can be exercised by Update/View tests directly.
package app

import (
	"log/slog"

	"github.com/pgmac/lazytg/internal/core/events"
	"github.com/pgmac/lazytg/internal/ui/input"
	"github.com/pgmac/lazytg/internal/ui/keymap"
	"github.com/pgmac/lazytg/internal/ui/overlay"
	"github.com/pgmac/lazytg/internal/ui/panes/chats"
	uisearch "github.com/pgmac/lazytg/internal/ui/panes/search"
	"github.com/pgmac/lazytg/internal/ui/panes/thread"
	"github.com/pgmac/lazytg/internal/ui/statusbar"
)

// Minimum terminal size below which we refuse to render the layout. 80x24 is
// the historical VT100 floor and matches the threshold every other TUI in
// our reference set (lazygit, k9s, weechat) draws the line at.
const (
	MinWidth  = 80
	MinHeight = 24
)

// FocusTarget enumerates which sub-model receives input. The cycle order
// (Chats → Input → Thread → Chats …) matches the spatial reading order for
// a 2-pane layout with a bottom composer; muscle memory from Slack/IRC
// clients carries over.
type FocusTarget int

// FocusTarget values are listed in spatial cycle order: Tab steps from
// FocusChats → FocusInput → FocusThread → FocusChats. Shift+Tab walks the
// cycle in reverse. The numeric values are an implementation detail of the
// modular arithmetic in applyFocusChange and must stay contiguous.
const (
	// FocusChats puts the chats list (left pane) in focus.
	FocusChats FocusTarget = iota
	// FocusInput puts the bottom composer in focus.
	FocusInput
	// FocusThread puts the message thread (right pane) in focus.
	FocusThread
)

// String makes FocusTarget log-friendly. Used by App.Update when emitting
// debug-level focus traces, and by tests for readable failure messages.
func (f FocusTarget) String() string {
	switch f {
	case FocusChats:
		return "chats"
	case FocusInput:
		return "input"
	case FocusThread:
		return "thread"
	default:
		return "unknown"
	}
}

// Deps bundles every collaborator the App needs at construction time. Bus
// and Log can be nil in tests — the App treats both defensively. Sub-models
// can be omitted to fall back on their package-level zero values; this is
// what keeps Task 6 testable before Tasks 7-9 are written.
type Deps struct {
	Bus    *events.Bus
	Log    *slog.Logger
	Keymap keymap.Keymap

	Chats  *chats.Model
	Thread *thread.Model
	Input  *input.Model
	Status *statusbar.Model
	Search *uisearch.Model
}

// App is the root Bubble Tea model. Sub-pane fields are concrete types
// rather than interfaces so the depguard rule (ui ⊥ gotd) stays trivially
// satisfied without interface plumbing.
type App struct {
	chats  chats.Model
	thread thread.Model
	input  input.Model
	status statusbar.Model
	help   overlay.Help
	search uisearch.Model

	// preSearchFocus remembers the focus target the user was on
	// before opening the search overlay so Esc / SearchJump can
	// restore it. -1 means "no overlay open" so the field is
	// effectively ignored.
	preSearchFocus FocusTarget

	// pendingScroll holds a deferred ScrollTo target produced by
	// handleSearchJump. The thread pane's applyLoaded calls
	// GotoBottom unconditionally, so a synchronous ScrollTo would
	// be overwritten. We instead remember the target and re-issue
	// the scroll once the broadcast machinery has processed the
	// messagesLoadedMsg. nil means no pending scroll.
	pendingScroll *pendingThreadScroll

	width    int
	height   int
	focus    FocusTarget
	tooSmall bool

	keymap keymap.Keymap
	bus    *events.Bus
	log    *slog.Logger
}

// New constructs the root model. Defaults are chosen so the first View call
// produces something coherent before any tea.WindowSizeMsg arrives — the
// status bar carries a "connecting" placeholder, panes render their
// "(unfocused)" body, focus starts on Chats.
func New(deps Deps) App {
	app := App{
		chats:          chooseModel(deps.Chats, chats.New),
		thread:         chooseModel(deps.Thread, thread.New),
		input:          chooseModel(deps.Input, input.New),
		status:         chooseStatus(deps.Status),
		help:           overlay.New(deps.Keymap),
		search:         chooseSearch(deps.Search),
		preSearchFocus: -1,
		focus:          FocusChats,
		keymap:         deps.Keymap,
		bus:            deps.Bus,
		log:            deps.Log,
	}
	app.chats = app.chats.SetFocus(true)
	return app
}

// chooseSearch returns *src if non-nil, else a no-service overlay
// constructed with default debounce. The placeholder behaves as
// "always empty" because its Service is nil — fine for
// app/View tests that never open the overlay.
func chooseSearch(src *uisearch.Model) uisearch.Model {
	if src != nil {
		return *src
	}
	return uisearch.New(nil, 0, nil)
}

// pendingThreadScroll is the deferred scroll target produced by
// handleSearchJump. The app holds it until applyLoaded (the broadcast
// path that lands messagesLoadedMsg in the thread pane) has run, at
// which point ScrollTo can find the target message in m.messages and
// position the viewport.
type pendingThreadScroll struct {
	ChatID    int64
	MessageID int64
	Around    int
}

// chooseModel returns *src if non-nil, else the package's zero-value
// constructor. The generic helper lets New tolerate partial Deps without
// repeating nil-checks per field.
func chooseModel[T any](src *T, def func() T) T {
	if src != nil {
		return *src
	}
	return def()
}

func chooseStatus(s *statusbar.Model) statusbar.Model {
	if s != nil {
		return *s
	}
	return statusbar.New()
}

// Width returns the last observed terminal width. Exposed for tests.
func (a App) Width() int { return a.width }

// Height returns the last observed terminal height. Exposed for tests.
func (a App) Height() int { return a.height }

// Focus returns the currently focused sub-model. Exposed for tests.
func (a App) Focus() FocusTarget { return a.focus }

// HelpVisible reports whether the help overlay is currently shown. Exposed
// for tests so they don't need to grep the rendered output.
func (a App) HelpVisible() bool { return a.help.Visible }

// SearchVisible reports whether the search overlay is currently
// shown. Exposed for tests so they don't need to grep the rendered
// output.
func (a App) SearchVisible() bool { return a.search.Visible }

// SearchModel returns the embedded search overlay (test helper). The
// model is value-copied; mutating the returned value does not affect
// the App.
func (a App) SearchModel() uisearch.Model { return a.search }

// TooSmall reports whether the last WindowSize was below MinWidth/MinHeight.
// Exposed for tests so they can assert without parsing the rendered View.
func (a App) TooSmall() bool { return a.tooSmall }
