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
	"sync"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
	"github.com/kar43lov/lazytg/internal/core/search"
	coresync "github.com/kar43lov/lazytg/internal/core/sync"
	"github.com/kar43lov/lazytg/internal/ui/graphics"
	"github.com/kar43lov/lazytg/internal/ui/input"
	"github.com/kar43lov/lazytg/internal/ui/keymap"
	"github.com/kar43lov/lazytg/internal/ui/overlay"
	"github.com/kar43lov/lazytg/internal/ui/palette"
	"github.com/kar43lov/lazytg/internal/ui/panes/attach"
	"github.com/kar43lov/lazytg/internal/ui/panes/chats"
	uisearch "github.com/kar43lov/lazytg/internal/ui/panes/search"
	"github.com/kar43lov/lazytg/internal/ui/panes/thread"
	"github.com/kar43lov/lazytg/internal/ui/statusbar"
)

// FileDownloader is the gotd-free contract App.handleDownloadRequest
// uses to start a download. The concrete production implementation is
// internal/core/files.DownloadService; tests substitute a fake to
// observe the call sequence without spinning up the SQLite-backed
// dedup cache.
type FileDownloader interface {
	Download(ctx context.Context, chatID int64, chatTitle string, info domain.MediaInfo) (string, error)
}

// MediaOpener is the contract App.handleOpenRequest uses to show a
// downloaded file to the user. The production implementation is
// internal/core/files.Opener, which launches the system viewer; tests
// substitute a fake so no window opens on a CI runner.
type MediaOpener interface {
	Open(ctx context.Context, path string) error
	// OpenURL hands a web address to the browser.
	OpenURL(ctx context.Context, url string) error
}

// MessageActions is the gotd-free contract for the operations a user
// performs on messages that already exist. The production implementation is
// internal/core/sync.ActionService; tests substitute a fake so no RPC
// happens and the destructive path can be asserted without a live account.
//
// Delete takes a list because the gesture does: marking a run of messages
// and removing them is one act, and sending them one at a time would be
// several requests and several chances to half-succeed.
type MessageActions interface {
	Edit(ctx context.Context, chatID, messageID int64, text string) error
	Delete(ctx context.Context, chatID int64, ids []int64, revoke bool) error
	Forward(ctx context.Context, fromChatID, toChatID int64, ids []int64, dropAuthor bool) error
	React(ctx context.Context, chatID, messageID int64, emoticon string) error
	// The chat-level actions, taken from the list without opening the chat.
	Mute(ctx context.Context, chatID int64, until time.Time) error
	Pin(ctx context.Context, chatID int64, pinned bool) error
	MarkUnread(ctx context.Context, chatID int64) error
	MarkRead(ctx context.Context, chatID int64, marked bool) error
	// OpenByUsername resolves a public handle, stores the chat and
	// returns its id, so the thread can open a conversation that was
	// not in the list.
	OpenByUsername(ctx context.Context, name string) (domain.Chat, error)
	// PressButton calls a bot back with the data of one of its buttons
	// and returns what the bot said.
	PressButton(ctx context.Context, chatID, messageID int64, data []byte) (coresync.CallbackAnswer, error)
}

// FileUploader is the gotd-free contract App.handleAttachSubmit uses
// to start an upload + sendMedia round-trip. The concrete production
// implementation is internal/core/files.UploadService; tests substitute
// a fake.
type FileUploader interface {
	SendFile(ctx context.Context, chatID int64, path, caption string, replyTo int) (uploadID int64, err error)
}

// JumpContextProvider is the contract App.handleSearchJump uses to
// load the surrounding-message window for a search hit. The concrete
// implementation is *search.Service; tests substitute a fake to assert
// the routing without exercising the SQL window query.
//
// Returns the messages ordered oldest-first plus the index of the
// target inside that slice. Returning an error makes the app fall back
// to the legacy "OpenChat + deferred ScrollTo" path so a transient
// JumpContext failure does not break the jump entirely — the user
// still lands in the chat, only without the ±around context window.
type JumpContextProvider interface {
	JumpContext(ctx context.Context, hit search.Hit, around int) ([]domain.Message, int, error)
}

// maxConcurrentHistoryRequests bounds how many history requests may be waiting
// on the backfill service at once. Well above what hand-driven navigation
// produces, low enough that a stalled consumer cannot accumulate goroutines
// without limit.
const maxConcurrentHistoryRequests = 8

// HistoryBackfiller asks for a chat's history to be pulled from Telegram into
// the local mirror. The production implementation is
// internal/core/sync.BackfillService, which rate-limits and de-duplicates
// requests; tests substitute a recorder or leave it nil.
//
// Enqueue is fire-and-forget on purpose: the pane must not block the UI thread
// on a network fetch. The rows appear when the backfill publishes
// DialogUpdated and the thread reloads from storage.
type HistoryBackfiller interface {
	Enqueue(chatID int64)
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
	Attach  *attach.Model

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

	// Opener, if set, shows a downloaded file to the user when they
	// press the OpenMedia chord ("o" by default) or click an
	// attachment badge. nil makes the gesture download-only, which is
	// what every test wants and what a build with no system viewer
	// gets.
	Opener MediaOpener
	// SelfID, if set, answers with the account's own user id, zero until
	// the session knows it. The thread hides the ticks in the chat with
	// yourself: "did they read it" has no answer there.
	SelfID func() int64

	// Uploader, if set, is invoked when the user submits a file
	// from the Attach overlay (Ctrl-U flow). nil makes the overlay
	// open / browse / submit but the actual SendFile call becomes a
	// no-op so tests stay compilable without the upload pipeline.
	Uploader FileUploader

	// Jumper, if set, is invoked by handleSearchJump to load the
	// ±N context window for a search hit so the thread pane lands
	// on the matched message even when it predates the most recent
	// initial-page slice. nil falls back to the legacy "OpenChat
	// + deferred ScrollTo" path which only works when the target
	// is still within the freshly-loaded 200-message head.
	Jumper JumpContextProvider

	// Actions, if set, performs edits and deletions on existing messages.
	// nil means the chords report that the client is offline rather than
	// pretending to have done something — a deletion that only happened
	// locally is the one outcome worse than none.
	Actions MessageActions

	// Backfiller, if set, is asked to pull history from Telegram when the
	// user opens a chat. Without it the thread pane shows only what the
	// local mirror already holds — which for a freshly synced chat list is
	// nothing at all, so the chat opens empty.
	Backfiller HistoryBackfiller
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
	attach  attach.Model

	// paletteFrecency mirrors Deps.PaletteFrecency so
	// handlePaletteSelected can call RecordVisit without a
	// separate plumb. nil means "no frecency wired" — the chat
	// switch still happens but visit counts are not updated.
	paletteFrecency palette.FrecencyStore

	// downloader is the file-download collaborator wired through
	// Deps.Downloader. nil means "no downloader wired" and the
	// Download chord becomes a friendly no-op.
	downloader FileDownloader

	// opener shows a downloaded file to the user, wired through
	// Deps.Opener. nil means the open gesture downloads and stops
	// there.
	opener MediaOpener
	selfID func() int64

	// uploader is the file-upload collaborator wired through
	// Deps.Uploader. nil means "no uploader wired" — the Attach
	// overlay still opens but Submit silently no-ops.
	uploader FileUploader

	// actions performs edits and deletions, wired through Deps.Actions.
	// nil means offline.
	actions MessageActions

	// imageProtocol is what the terminal can draw, detected once at
	// construction. ProtocolNone — the ordinary case on a plain xterm or
	// inside tmux — makes the inline gesture say so rather than painting
	// base64 into the user's screen.
	imageProtocol graphics.Protocol

	// confirm is the modal that stands in front of a deletion.
	confirm overlay.Confirm

	// emojiPicker is the browse-and-pick half of emoji entry; the
	// composer's `:shortcode` completion is the other half.
	emojiPicker overlay.EmojiPicker

	// pendingDelete remembers what a confirmation is about while the
	// modal is up. Held here rather than in the modal because the modal
	// is a widget that knows about keys and text, not about messages.
	pendingDelete pendingDelete

	// backfiller is the history-pull collaborator wired through
	// Deps.Backfiller. nil means "offline" — the thread shows only
	// what storage already holds.
	backfiller HistoryBackfiller

	// historyGate caps concurrent history requests. Enqueue blocks on a
	// full queue and takes no context, so an unbounded goroutine per chat
	// open would pile up permanently once its consumer stops. Shared by
	// value-copies of App because a channel is a reference.
	historyGate chan struct{}

	// jumper is the search-jump collaborator wired through
	// Deps.Jumper. nil means "no jump-context wired" — the search
	// jump path falls back to OpenChat + deferred ScrollTo, which
	// only locates the target when it still fits in the initial
	// 200-message page.
	jumper JumpContextProvider

	// inFlightDownloads tracks fileIDs whose Ctrl-D goroutine is
	// currently running so a repeated chord on the same media does
	// not spawn an overlapping Download call. Two writers to the
	// same .partial path would clobber each other mid-stream
	// (documented in core/files.DownloadService). The guard is a
	// shared pointer so all value-copies of App see the same
	// registry — Bubble Tea's immutable model would otherwise
	// reset state on every Update.
	inFlightDownloads *downloadRegistry

	// preSearchFocus remembers the focus target the user was on
	// before opening the search overlay so Esc / SearchJump can
	// restore it. -1 means "no overlay open" so the field is
	// effectively ignored.
	preSearchFocus FocusTarget

	// pendingForward remembers what a chat-picking palette is about. Set
	// when the user presses the forward key, consumed by the palette
	// selection, cleared when the palette closes with nothing chosen.
	pendingForward *pendingForward

	// jumpStack is the trail of places a reply-following jump started
	// from, newest last. See jump.go.
	jumpStack []jumpOrigin

	// typing is who is composing something in the chat on screen, keyed by
	// the person, with the moment their indicator goes stale. See typing.go.
	typing map[int64]typingState

	// pendingReaction remembers which message an emoji picker opened to
	// react is about, and what this account already has on it.
	pendingReaction *pendingReaction

	// prePaletteFocus is the same idea for the command palette so
	// closing it (Esc / SelectedMsg) can restore the prior focus
	// target. -1 means "no palette open".
	prePaletteFocus FocusTarget

	// titledUnread is the badge last written into the terminal's title,
	// and titled whether one was written at all.
	titledUnread int
	titled       bool

	// preAttachFocus is the same idea for the attach overlay so
	// Esc / Submit can restore the prior focus target. -1 means
	// "no attach overlay open".
	preAttachFocus FocusTarget

	// pendingScroll holds a deferred ScrollTo target produced by
	// handleSearchJump. The thread pane's applyLoaded calls
	// GotoBottom unconditionally, so a synchronous ScrollTo would
	// be overwritten. We instead remember the target and re-issue
	// the scroll once the broadcast machinery has processed the
	// messagesLoadedMsg. nil means no pending scroll.
	pendingScroll *pendingThreadScroll

	// jumpGen is the search-jump generation counter. Incremented on
	// every handleSearchJump kickoff, every handleChatSelected, and
	// every handlePaletteSelected — anything that should invalidate
	// an in-flight async jump. jumpContextCmd captures the value at
	// kickoff and applySearchJumpLoaded drops the result when the
	// generation no longer matches. Without this guard a stale
	// JumpContext completion can yank the thread back to the earlier
	// hit (or, on the error path, re-open the old chat) after the
	// user already moved on.
	jumpGen uint64

	width    int
	height   int
	focus    FocusTarget
	tooSmall bool

	// chatsWidth is the user's split position, set by dragging the separator.
	// 0 means "auto" — the 30%-of-width default. Kept as an absolute column
	// count rather than a ratio: a drag says "this wide", and re-deriving a
	// percentage would make the pane creep on every resize.
	chatsWidth int

	// draggingText is true while a left-button drag that started inside the
	// thread is in progress, so motion events extend the selection instead of
	// being ignored — and a drag that began elsewhere never does.
	draggingText bool

	// lastClickAt / lastClickCell power double-click detection: bubbletea
	// reports individual presses, so "the same spot twice, quickly" has to be
	// recognised here. Zero time means "no click yet".
	lastClickAt   time.Time
	lastClickCell [2]int

	// draggingSep is true between pressing the separator and releasing the
	// button. It is what lets the pointer wander off the one-column-wide
	// separator mid-drag without the drag being dropped — nobody can keep a
	// pointer inside a single column while moving it.
	draggingSep bool

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
		chats:             chooseModel(deps.Chats, chats.New),
		thread:            chooseModel(deps.Thread, thread.New),
		input:             chooseModel(deps.Input, input.New),
		status:            chooseStatus(deps.Status),
		help:              overlay.New(deps.Keymap),
		search:            chooseSearch(deps.Search),
		palette:           choosePalette(deps.Palette),
		attach:            chooseAttach(deps.Attach, deps.Log),
		paletteFrecency:   deps.PaletteFrecency,
		downloader:        deps.Downloader,
		opener:            deps.Opener,
		selfID:            deps.SelfID,
		uploader:          deps.Uploader,
		jumper:            deps.Jumper,
		backfiller:        deps.Backfiller,
		actions:           deps.Actions,
		confirm:           overlay.NewConfirm(),
		imageProtocol:     graphics.Detect(nil),
		emojiPicker:       overlay.NewEmojiPicker(deps.Keymap),
		historyGate:       make(chan struct{}, maxConcurrentHistoryRequests),
		inFlightDownloads: newDownloadRegistry(),
		preSearchFocus:    -1,
		prePaletteFocus:   -1,
		preAttachFocus:    -1,
		focus:             FocusChats,
		keymap:            deps.Keymap,
		bus:               deps.Bus,
		log:               orDiscard(deps.Log),
	}
	app.chats = app.chats.SetFocus(true)
	return app
}

// downloadRegistry is a tiny mutex-guarded set of in-flight FileIDs.
// The pointer receivership keeps every value-copy of App pointing at
// the same map so reserve/release calls from different Update tick
// goroutines see each other's state. Use reserve to gate a fresh
// download attempt and release on goroutine exit.
type downloadRegistry struct {
	mu      sync.Mutex
	pending map[int64]struct{}
}

func newDownloadRegistry() *downloadRegistry {
	return &downloadRegistry{pending: make(map[int64]struct{})}
}

// reserve atomically inserts fileID and returns true if the slot was
// free, false if a download for that fileID was already in flight.
// fileID == 0 always returns true so the caller never blocks on a
// missing FileID — the download service itself rejects that case
// with a clearer error.
func (r *downloadRegistry) reserve(fileID int64) bool {
	if fileID == 0 {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, busy := r.pending[fileID]; busy {
		return false
	}
	r.pending[fileID] = struct{}{}
	return true
}

func (r *downloadRegistry) release(fileID int64) {
	if fileID == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.pending, fileID)
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

// chooseAttach returns *src if non-nil, else a fresh attach overlay
// rooted at the user's home directory. The placeholder is harmless in
// app/View tests because the overlay only renders when Visible.
func chooseAttach(src *attach.Model, log *slog.Logger) attach.Model {
	if src != nil {
		return *src
	}
	return attach.New(log)
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

// AttachVisible reports whether the attach overlay is currently
// shown. Exposed for tests.
func (a App) AttachVisible() bool { return a.attach.Visible }

// AttachModel returns the embedded attach overlay (test helper).
// Value-copied; mutating it does not affect the App.
func (a App) AttachModel() attach.Model { return a.attach }

// TooSmall reports whether the last WindowSize was below MinWidth/MinHeight.
// Exposed for tests so they can assert without parsing the rendered View.
func (a App) TooSmall() bool { return a.tooSmall }

// orDiscard guarantees a usable logger.
//
// Every a.log call site would otherwise need a nil check, and the ones that
// forgot panicked only on the path they logged from — which is by definition
// the failure path, so the crash arrived exactly when something had already
// gone wrong. A test constructing Deps without a logger is the ordinary case,
// not a misuse.
func orDiscard(l *slog.Logger) *slog.Logger {
	if l != nil {
		return l
	}
	return slog.New(slog.DiscardHandler)
}
