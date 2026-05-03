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
	"context"
	"log/slog"

	"github.com/pgmac/lazytg/internal/core/domain"
	"github.com/pgmac/lazytg/internal/core/events"
	"github.com/pgmac/lazytg/internal/ui/input"
	"github.com/pgmac/lazytg/internal/ui/keymap"
	"github.com/pgmac/lazytg/internal/ui/overlay"
	"github.com/pgmac/lazytg/internal/ui/palette"
	"github.com/pgmac/lazytg/internal/ui/panes/chats"
	uisearch "github.com/pgmac/lazytg/internal/ui/panes/search"
	"github.com/pgmac/lazytg/internal/ui/panes/thread"
	"github.com/pgmac/lazytg/internal/ui/statusbar"
)

// FileDownloader is the gotd-free contract App.handleDownloadRequest
// uses to start a download. The concrete production implementation is
// internal/core/files.DownloadService; tests substitute a fake to
// observe the call sequence without spinning up the SQLite-backed
// dedup cache.
type FileDownloader interface {
	Download(ctx context.Context, chatID int64, chatTitle string, info domain.MediaInfo) (string, error)
}

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

	Chats   *chats.Model
	Thread  *thread.Model
	Input   *input.Model
	Status  *statusbar.Model
	Search  *uisearch.Model
	Palette *palette.Model

	// PaletteFrecency, if set, is invoked by the app on every
	// SelectedMsg so the just-visited chat ranks higher in the
	// next palette open. Tests typically leave it nil; production
	// passes the same FrecencyStore the palette itself reads from.
	PaletteFrecency palette.FrecencyStore

	// Downloader, if set, is invoked when the user presses the
	// Download chord (Ctrl-D by default) on a message that carries
	// media. Tests typically leave it nil — the chord becomes a
	// no-op in that case and the app stays compilable without
	// internal/core/files in scope.
	Downloader FileDownloader
}

// App is the root Bubble Tea model. Sub-pane fields are concrete types
// rather than interfaces so the depguard rule (ui ⊥ gotd) stays trivially
// satisfied without interface plumbing.
type App struct {
	chats   chats.Model
	thread  thread.Model
	input   input.Model
	status  statusbar.Model
	help    overlay.Help
	search  uisearch.Model
	palette palette.Model

	// paletteFrecency mirrors Deps.PaletteFrecency so
	// handlePaletteSelected can call RecordVisit without a
	// separate plumb. nil means "no frecency wired" — the chat
	// switch still happens but visit counts are not updated.
	paletteFrecency palette.FrecencyStore

	// downloader is the file-download collaborator wired through
	// Deps.Downloader. nil means "no downloader wired" and the
	// Download chord becomes a friendly no-op.
	downloader FileDownloader

	// preSearchFocus remembers the focus target the user was on
	// before opening the search overlay so Esc / SearchJump can
	// restore it. -1 means "no overlay open" so the field is
	// effectively ignored.
	preSearchFocus FocusTarget

	// prePaletteFocus is the same idea for the command palette so
	// closing it (Esc / SelectedMsg) can restore the prior focus
	// target. -1 means "no palette open".
	prePaletteFocus FocusTarget

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
		chats:           chooseModel(deps.Chats, chats.New),
		thread:          chooseModel(deps.Thread, thread.New),
		input:           chooseModel(deps.Input, input.New),
		status:          chooseStatus(deps.Status),
		help:            overlay.New(deps.Keymap),
		search:          chooseSearch(deps.Search),
		palette:         choosePalette(deps.Palette),
		paletteFrecency: deps.PaletteFrecency,
		downloader:      deps.Downloader,
		preSearchFocus:  -1,
		prePaletteFocus: -1,
		focus:           FocusChats,
		keymap:          deps.Keymap,
		bus:             deps.Bus,
		log:             deps.Log,
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

// choosePalette returns *src if non-nil, else a no-deps palette.
// The placeholder behaves as "always empty" (frecency / chats both
// nil → loadCandidates returns an empty slice) — fine for app/View
// tests that never open the palette.
func choosePalette(src *palette.Model) palette.Model {
	if src != nil {
		return *src
	}
	return palette.New(nil, nil, nil)
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

// PaletteVisible reports whether the command palette overlay is
// currently shown. Exposed for tests.
func (a App) PaletteVisible() bool { return a.palette.Visible }

// PaletteModel returns the embedded palette overlay (test helper).
// Value-copied; mutating it does not affect the App.
func (a App) PaletteModel() palette.Model { return a.palette }

// TooSmall reports whether the last WindowSize was below MinWidth/MinHeight.
// Exposed for tests so they can assert without parsing the rendered View.
func (a App) TooSmall() bool { return a.tooSmall }
