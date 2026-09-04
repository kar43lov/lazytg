// Package thread hosts the right pane of the lazytg TUI — the message
// list for the currently-selected chat.
//
// The pane is a thin shell over bubbles/viewport.Model that adds (1) a
// repo-backed loader with offset pagination, (2) per-message rendering
// via FormatMessage, (3) live append on events.MessageReceived, and (4)
// optimistic-UI integration for outgoing messages whose state the
// SendService advances asynchronously. Width/Height/Focused stay public
// so the app's view layer can keep rendering each pane in its own
// lipgloss box without poking at internal viewport state.
package thread

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// initialPageSize is how many messages OpenChat asks the repo for on a
// fresh chat selection. 200 is a compromise between "scroll-back goes
// far enough that the user rarely paginates" (good UX) and "the
// viewport.SetContent re-render stays under 16ms" (good frame budget).
const initialPageSize = 200

// pageSize is the per-step pagination batch. Same number as initial so
// the user perceives uniform "another screenful" when scrolling up.
const pageSize = 200

// loadTimeout caps how long a repo read can stall the UI. SQLite reads
// against the local DB normally complete in microseconds, but a stuck
// VFS lock should not be allowed to freeze the thread pane forever.
const loadTimeout = 5 * time.Second

// minViewportWidth/Height keep the viewport visible even when the app
// composer has not yet received a WindowSize. The bubbles viewport
// rejects zero/negative dimensions silently and renders nothing — which
// would hide the placeholder and break the existing app/view tests.
const (
	minViewportWidth  = 10
	minViewportHeight = 3
)

// Model is the thread-pane Bubble Tea model. Width/Height/Focused are
// public so the app's view layer can size the surrounding lipgloss
// border. The viewport itself receives sizes via SetSize; the model
// also persists them so a SetFocus call (which does not carry
// dimensions) does not lose them.
//
// The outgoing slice holds optimistic-UI entries for messages the
// user has just sent: pending until SendService publishes
// OutgoingMessageStateChanged{State: sent}, at which point the entry
// is dropped (the server-echoed MessageReceived takes its place via
// applyIncoming). On {State: failed} the entry is kept with a red [✗]
// glyph so the user can see why and decide whether to retry.
type Model struct {
	Width   int
	Height  int
	Focused bool

	viewport viewport.Model
	chatID   int64

	// sel is the in-progress or finished text selection, nil when nothing is
	// selected. Held as a pointer so the zero Model has no selection without a
	// separate flag.
	sel *selection

	// dragCache holds the rendered body for the duration of a drag. Re-rendering
	// every message on every mouse-motion event measured at 3.1ms of the 4.1ms
	// each event costs on a 200-message page (BenchmarkRenderContent vs
	// BenchmarkApplySelectionOnly) — at 60 events per second that is a quarter
	// of a core spent re-formatting text that did not change, on the goroutine
	// the whole UI runs on. The trade-off is deliberate and bounded: a message
	// arriving mid-drag becomes visible when the drag ends.
	dragCache *renderedThread

	// cursorID is the message the user has singled out — the one reply,
	// download and open act on. Held as an id rather than an index
	// because everything under it moves; see cursor.go.
	cursorID int64

	// inline holds pictures drawn under messages, keyed by message id.
	// See inline.go.
	inline map[int64]inlineImage

	// marked holds the ids the user has picked out for an action that
	// takes several — copying a run of messages, deleting them. Ids for
	// the same reason as the cursor; see marks.go.
	marked map[int64]bool

	// now supplies the current time to the date separators, which need
	// it to say "Today" and "Yesterday". Injectable so a golden test of
	// the rendered thread does not change meaning overnight.
	now func() time.Time

	// authorNames maps a sender id to a display name, supplied by the app from
	// the chat list. Private tells resolveAuthor it may treat "not the peer" as
	// "the reader"; both are refreshed by SetDirectory when a chat is opened.
	authorNames map[int64]string
	private     bool
	chatKind    domain.ChatType
	messages    []domain.Message
	outgoing    []OutgoingMessage
	// pendingServerIDs maps localID → serverID for sent optimistic
	// rows whose server-echo MessageReceived has not yet arrived. Used
	// by applyIncoming to dedupe — the next live event with that
	// serverID drops the optimistic row instead of appending a
	// duplicate.
	pendingServerIDs map[int64]string
	// finalizedLocalIDs records every localID that has reached a
	// terminal Sent or Failed state on the bus. Used by ApplyDispatched
	// to short-circuit when the synchronous SendDispatchedMsg arrives
	// AFTER the bus event (the inverted race). Without this guard the
	// Dispatched insert would re-create a Pending row that no future
	// event could ever resolve, leaving a phantom [⏳] in the thread.
	finalizedLocalIDs map[string]struct{}
	repo              Repository
	provider          HistoryProvider
	log               *slog.Logger

	loading bool
	hasMore bool
	// oldestID tracks the first (smallest-ID) message currently
	// rendered. Pagination uses len(messages) as the SQL offset for
	// simplicity, but oldestID is exposed so future callers (cursor-
	// based MTProto backfill) can ask for "older than X".
	oldestID int64

	// hasNewer is true when forward pagination can still load messages
	// strictly newer than forwardCursorID. Set by LoadJumpWindow (the
	// jump landed in the middle of history, so the present is reachable
	// only through scroll-down) and cleared by applyLoaded /
	// OpenChat (initial load lands at the tail) or when forward
	// pagination returns an empty / short page.
	hasNewer bool
	// forwardCursorID is the upper boundary of the contiguously-loaded
	// prefix that grew out of LoadJumpWindow's installed window. Forward
	// pagination uses this as the "id > X" SQL cursor. We track it
	// separately from a "newest in m.messages" computation because live
	// MessageReceived events can land far above the loaded window
	// (creating a visual gap) — using their id as the cursor would skip
	// over the unloaded gap and break the "scroll down to fill toward
	// present" UX.
	forwardCursorID int64

	// loadGen is bumped on every state-resetting transition (OpenChat,
	// SwitchTo, LoadJumpWindow). loadCmd / paginateCmd / paginateNewerCmd
	// capture the value at scheduling time; the corresponding apply*
	// handlers drop messages whose gen no longer matches the current
	// model. Without this, a slow initial load (or a still-in-flight
	// older-page pagination) for the same chat could land after a
	// search-jump's LoadJumpWindow and yank the user back to the
	// freshly-loaded chat tail.
	loadGen uint64
}

// New returns a placeholder model with no repo wired. Used by app
// construction in tests and by the early Stage 2 skeleton where wiring
// happens lazily. Calling Init / OpenChat on a no-repo model is a
// no-op.
func New() Model { return newModel(nil, nil, nil) }

// NewWithRepo returns a model that loads messages from repo on
// OpenChat. provider is optional and only used to backfill thin
// history; pass nil to disable backfill. log can be nil; the model
// uses slog.Default in that case.
func NewWithRepo(repo Repository, provider HistoryProvider, log *slog.Logger) Model {
	return newModel(repo, provider, log)
}

func newModel(repo Repository, provider HistoryProvider, log *slog.Logger) Model {
	if log == nil {
		log = slog.Default()
	}
	vp := viewport.New(
		viewport.WithWidth(minViewportWidth),
		viewport.WithHeight(minViewportHeight),
	)
	vp.SoftWrap = true
	return Model{
		viewport:          vp,
		repo:              repo,
		provider:          provider,
		log:               log,
		pendingServerIDs:  make(map[int64]string),
		finalizedLocalIDs: make(map[string]struct{}),
	}
}

// Init is a no-op — chat content is not loaded until the user picks a
// chat in the chats pane (ChatSelectedMsg → OpenChat).
func (m Model) Init() tea.Cmd { return nil }

// OpenChat resets the viewport for chatID and triggers an initial repo
// fetch. Returns the updated model (with loading=true and chatID
// populated) plus the load command. Calling OpenChat with the same
// chatID is *not* idempotent: it always reloads, which is the desired
// behaviour when the user re-clicks a chat to see its latest state.
//
// Outgoing optimistic state is also reset: pending entries are bound
// to the chat that produced them and switching chats discards stale
// pendings (the SendService still owns the underlying record in the
// outgoing table — the UI just stops rendering it here).
//
// loadGen is bumped before scheduling loadCmd so any in-flight load
// from a prior OpenChat / SwitchTo / pagination (for any chat,
// including this one) is dropped on arrival. Same-chat re-opens
// otherwise race against their own previous loads.
func (m Model) OpenChat(chatID int64) (Model, tea.Cmd) {
	m.loadGen++
	m = m.dropSelection()
	m.chatID = chatID
	m.messages = nil
	m.outgoing = nil
	m.pendingServerIDs = make(map[int64]string)
	m.finalizedLocalIDs = make(map[string]struct{})
	m.oldestID = 0
	m.hasMore = false
	m.hasNewer = false
	m.forwardCursorID = 0
	m.loading = true
	m.viewport.SetContent("")
	if m.repo == nil {
		m.loading = false
		return m, nil
	}
	return m, loadCmd(m.repo, chatID, 0, initialPageSize, m.loadGen)
}

// ReloadAfterJumpFailure schedules a fresh chat-tail load following a
// failed JumpContext. Like OpenChat it bumps loadGen, clears the
// rendered messages and cursors, and fires loadCmd. UNLIKE OpenChat it
// PRESERVES outgoing optimistic state (outgoing / pendingServerIDs /
// finalizedLocalIDs).
//
// The preservation matters because the search-jump path rebinds the
// input pane to the target chat the moment the user picks a hit; the
// user may issue a send during the async JumpContext window. That send
// flows through ApplyDispatched into outgoing before the context query
// returns. Wiping it on the failure-recovery path would silently drop
// the optimistic row — for 1:1 chats Telegram emits only
// UpdateShortSentMessage and never re-publishes a corresponding
// UpdateNewMessage echo, so the user would see the message disappear
// until manual reload. The success path (LoadJumpWindow) already
// preserves outgoing for the same reason; this method makes the
// failure recovery symmetric.
func (m Model) ReloadAfterJumpFailure(chatID int64) (Model, tea.Cmd) {
	m.loadGen++
	m = m.dropSelection()
	m.chatID = chatID
	m.messages = nil
	m.oldestID = 0
	m.hasMore = false
	m.hasNewer = false
	m.forwardCursorID = 0
	m.loading = true
	m.viewport.SetContent("")
	if m.repo == nil {
		m.loading = false
		return m, nil
	}
	return m, loadCmd(m.repo, chatID, 0, initialPageSize, m.loadGen)
}

// ChatID returns the currently displayed chat id (test helper).
func (m Model) ChatID() int64 { return m.chatID }

// Messages returns a copy of the currently-rendered messages, ordered
// oldest-first. Tests use this to assert pagination outcomes without
// re-parsing the rendered View.
func (m Model) Messages() []domain.Message {
	out := make([]domain.Message, len(m.messages))
	copy(out, m.messages)
	return out
}

// HasMore reports whether the model believes more history is available
// beyond what is currently loaded. Test helper; the live UI uses this
// to decide whether a scroll-up should trigger pagination.
func (m Model) HasMore() bool { return m.hasMore }

// HasNewer reports whether forward pagination can still load messages
// newer than the current forward cursor. True only after LoadJumpWindow
// (the symmetric post-jump path); false once forward pagination
// catches up to the chat tail or when no jump is active.
func (m Model) HasNewer() bool { return m.hasNewer }

// ForwardCursorID exposes the upper boundary of contiguously-loaded
// messages from the bottom of the jump window. Test helper.
func (m Model) ForwardCursorID() int64 { return m.forwardCursorID }

// Loading reports whether a fetch is currently in flight.
func (m Model) Loading() bool { return m.loading }

// OldestID returns the smallest-ID message currently rendered, or 0
// when the model is empty. Exposed for tests so they can assert
// pagination updates the cursor.
func (m Model) OldestID() int64 { return m.oldestID }

// YOffset returns the viewport's current y offset (top-most visible
// rendered line). Exposed for tests so ScrollTo can be verified
// without parsing the rendered View.
func (m Model) YOffset() int { return m.viewport.YOffset() }

// SetSize updates the pane dimensions. Called by the app on resize. We
// reserve one row for the focus-aware header rendered by View and clamp
// the viewport to (minViewportWidth, minViewportHeight) so a zero-pane
// app still produces a non-empty View.
func (m Model) SetSize(width, height int) Model {
	m.Width = width
	m.Height = height
	// The app wraps this pane in a lipgloss box of exactly `width` columns with
	// one column of padding on each side, so the body has two fewer columns to
	// work with. Handing the viewport the full width made every long line two
	// columns too wide: lipgloss then wrapped the overflow onto its own line,
	// which pushed the rows below out of the box — text simply vanished off the
	// bottom as the separator was dragged.
	w := width - paneHPadding
	if w < minViewportWidth {
		w = minViewportWidth
	}
	// Reserve one row for the header.
	h := height - 1
	if h < minViewportHeight {
		h = minViewportHeight
	}
	m.viewport.SetWidth(w)
	m.viewport.SetHeight(h)
	if len(m.messages) > 0 {
		m.viewport.SetContent(m.renderAll())
	}
	return m
}

// SetDirectory supplies what the pane needs to name senders: display names
// keyed by sender id (the app takes them from the loaded chat list) and whether
// the open chat is a 1:1 dialog. Called on every chat switch — the names map is
// shared by reference and must not be mutated afterwards.
func (m Model) SetDirectory(names map[int64]string, kind domain.ChatType) Model {
	m.authorNames = names
	m.chatKind = kind
	// Author rendering only cares whether this is a one-to-one dialog; the
	// full kind is kept because deletion updates for private chats and basic
	// groups arrive without a chat id and must not be applied to a channel,
	// which numbers its messages independently.
	m.private = kind == domain.ChatTypePrivate
	m.viewport.SetContent(m.renderAll())
	return m
}

// SetFocus toggles whether the pane is focused. The viewport itself
// does not have a separate focus concept (key handling is ours either
// way) so this only updates the cosmetic flag the View layer uses.
func (m Model) SetFocus(f bool) Model {
	m.Focused = f
	return m
}

// ScrollUp moves the viewport up by one page. Exposed so the app's
// global key handler can route the configurable ScrollUp chord (which
// the viewport's stock keymap does not recognise — pgup/pgdown are
// detected via key strings, but ctrl+b is not) without poking at the
// embedded viewport directly.
func (m Model) ScrollUp() Model {
	m.viewport.PageUp()
	return m
}

// ScrollDown is the symmetric helper for the configurable ScrollDown
// chord. See ScrollUp for the rationale.
func (m Model) ScrollDown() Model {
	m.viewport.PageDown()
	return m
}

// ScrollTo positions the viewport so the message with messageID is
// visible roughly in the middle of the visible window. around hints
// how many rows above and below the target should remain readable —
// when the surrounding context is shorter than the viewport height the
// target is centred; otherwise the target lands one "around" worth of
// rows below the top.
//
// Returns the model unchanged when messageID is not in m.messages
// (the caller is presumed to have loaded the surrounding window
// already via JumpContext or repo.GetMessagesBefore — Stage 3 Task 4
// keeps that orchestration in the app layer so the thread pane stays
// self-contained).
func (m Model) ScrollTo(messageID int64, around int) Model {
	if messageID == 0 || len(m.messages) == 0 {
		return m
	}
	if around < 0 {
		around = 0
	}
	idx := -1
	for i, msg := range m.messages {
		if msg.ID == messageID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return m
	}

	width := m.viewport.Width()
	if width <= 0 {
		width = minViewportWidth
	}

	// Lines occupied by everything strictly before the target.
	linesBefore := countRenderedLines(m.messages[:idx], width)
	// Add the inter-message blank-line separator that countRenderedLines
	// only counts between adjacent messages — when idx > 0 the target's
	// header is preceded by a blank row.
	if idx > 0 {
		linesBefore++
	}

	// Target the row where the target's header lands. Subtracting the
	// "around" hint nudges the viewport up so the user has visible
	// context above the hit. SetYOffset clamps internally so we don't
	// have to handle negative values explicitly — but doing the clamp
	// here keeps the value monotonically non-negative for tests that
	// inspect it.
	offset := linesBefore - around
	if offset < 0 {
		offset = 0
	}
	m.viewport.SetYOffset(offset)
	return m
}

// clock returns the model's time source, falling back to time.Now so
// the zero Model — the one chooseModel hands out when the app wires no
// thread pane — renders separators rather than panicking.
func (m Model) clock() time.Time {
	if m.now == nil {
		return time.Now()
	}
	return m.now()
}

// SetClock replaces the time source behind the "Today" / "Yesterday"
// separators. Test seam only; production uses time.Now.
func (m Model) SetClock(now func() time.Time) Model {
	m.now = now
	m.viewport.SetContent(m.renderAll())
	return m
}

// renderAll concatenates every message with a blank-line separator,
// then appends every still-pending or failed optimistic-UI entry.
// Messages are stored oldest-first so the natural top-to-bottom reading
// order matches the slice index. Optimistic rows are kept after the
// regular history because they are always the most recent thing the
// user did — sticky-bottom rendering matches the user's mental model.
func (m Model) renderAll() string {
	content, spans := m.renderContent()
	if m.sel == nil {
		return content
	}
	return applySelection(content, spans, *m.sel)
}

// renderContent builds the thread body and, alongside it, the line range each
// message occupies. The spans are what turns a pointer position into "this
// message", so they are produced by the renderer itself rather than
// re-derived: a second implementation of the same layout would drift, and the
// drift would show up as selecting the wrong message.
func (m Model) renderContent() (string, []blockSpan) {
	if m.dragCache != nil {
		return m.dragCache.content, m.dragCache.spans
	}
	var b strings.Builder
	spans := make([]blockSpan, 0, len(m.messages)+len(m.outgoing))
	width := m.viewport.Width()
	line := 0
	appendBlock := func(rendered string, id int64, mediaLine int) {
		if b.Len() > 0 {
			b.WriteString("\n\n")
			line += 2
		}
		b.WriteString(rendered)
		height := strings.Count(rendered, "\n") + 1
		if mediaLine >= 0 {
			mediaLine += line
		}
		spans = append(spans, blockSpan{start: line, end: line + height, id: id, mediaLine: mediaLine})
		line += height - 1
	}
	// A separator is written before the first message of each day. It
	// occupies one line and gets no span: it belongs to no message, so a
	// click on it must not select the message below.
	appendSeparator := func(rendered string) {
		if b.Len() > 0 {
			b.WriteString("\n\n")
			line += 2
		}
		b.WriteString(rendered)
	}
	now := m.clock()
	var lastDay time.Time

	cursorID := m.cursorID
	if cursorID == 0 && len(m.messages) > 0 {
		// An unplaced cursor is drawn on the newest message rather than
		// left invisible: the media chords already act there, and a
		// marker that only appears after the first arrow key would make
		// the first press look like it did something else.
		cursorID = m.messages[len(m.messages)-1].ID
	}
	for _, msg := range m.messages {
		if lastDay.IsZero() || !sameDay(lastDay, msg.Date) {
			appendSeparator(renderDaySeparator(msg.Date, now, width))
			lastDay = msg.Date
		}
		rendered, mediaLine := formatMessageBlockMarked(msg, width, nil,
			resolveAuthor(msg, m.chatID, m.private, m.authorNames),
			msg.ID == cursorID, m.marked[msg.ID])
		// A drawn picture is appended to its message's block so it moves
		// with the message and is covered by the same span — a click on it
		// means that message, which is what the user is pointing at.
		if block, rows := m.imageBlock(msg.ID); rows > 0 {
			rendered += "\n" + block
		}
		appendBlock(rendered, msg.ID, mediaLine)
	}
	for _, out := range m.outgoing {
		appendBlock(RenderOptimistic(out), 0, -1)
	}
	return b.String(), spans
}

// Outgoing returns a copy of the optimistic-UI entries currently
// rendered. Test helper: lets unit tests verify state transitions
// (pending → sent → drop, pending → failed) without parsing the
// rendered viewport content.
func (m Model) Outgoing() []OutgoingMessage {
	out := make([]OutgoingMessage, len(m.outgoing))
	copy(out, m.outgoing)
	return out
}

// SwitchTo resets the rendered state and binds the pane to chatID
// without firing a repo load. The search-jump path uses this to
// pre-position the thread on the target chat while a JumpContext
// query runs asynchronously — the result is then applied via
// LoadJumpWindow.
//
// SwitchTo always resets, even when chatID matches the currently
// displayed chat. The reset is required so that LoadJumpWindow's
// "preserve messages newer than the window" merge only retains
// genuine live appends that arrived after the jump kicked off.
// Without the reset, a same-chat jump would keep the entire prior
// tail (e.g. [70..100]) and the loaded ±N window around an older
// hit (e.g. [25..35]) would render alongside it as
// [25..35, 70..100] — a discontinuous thread with a missing gap
// in the middle.
//
// loadGen is bumped so any in-flight initial load / pagination from
// before the jump is dropped on arrival — without the bump, a slow
// OpenChat fetch for the same chat could clobber the freshly-installed
// jump window with the chat's tail.
func (m Model) SwitchTo(chatID int64) Model {
	m.loadGen++
	m = m.dropSelection()
	m.chatID = chatID
	m.messages = nil
	m.outgoing = nil
	m.pendingServerIDs = make(map[int64]string)
	m.finalizedLocalIDs = make(map[string]struct{})
	m.oldestID = 0
	m.hasMore = false
	m.hasNewer = false
	m.forwardCursorID = 0
	m.loading = false
	m.viewport.SetContent("")
	return m
}

// LoadJumpWindow replaces the rendered slice with messages (a context
// window typically produced by search.Service.JumpContext: ±N around a
// hit) and scrolls the viewport to scrollToID. Used by the search-jump
// path so that picking an old hit does not drop the user at the bottom
// of the freshly-loaded chat — the surrounding context loads in one
// step and the viewport lands on the match. hasMore is set to true
// because older history is still reachable via pagination from the
// oldest id in the window.
//
// hasNewer / forwardCursorID are set so the symmetric forward
// pagination is reachable: scroll-down at the bottom of the loaded
// window walks the model toward the present (id > forwardCursorID)
// rather than stranding the user in an isolated ±N slice. The cursor
// pins to the loaded window's max id, NOT the live tail's max id,
// because forward pagination needs to fill the gap between the window
// and any live appends that landed during the async JumpContext.
//
// chatID switches to the supplied id even when the window is empty —
// the caller (app handleSearchJump) has already decided to surface this
// chat. An empty window leaves the thread visually empty until the next
// load, which is acceptable for the missing-target edge case.
//
// Messages already present in the model whose id is strictly greater
// than the loaded window's max id are preserved at the tail. This
// covers the race where a live MessageReceived event lands between
// SwitchTo and LoadJumpWindow: applyIncoming appends the new row to
// m.messages while JumpContext is still running, and a naive "replace
// with the loaded window" would silently drop that row from the UI
// until the next reload. Preserved entries are deduplicated against
// the window so a freshly-persisted live row that the SQL also picked
// up does not render twice.
//
// loadGen is bumped so any older-page pagination still in flight from
// before the jump (the symmetric problem to SwitchTo's bump) is
// dropped on arrival rather than prepending unrelated history into
// the freshly-installed window.
//
// Optimistic-UI state (outgoing / pendingServerIDs / finalizedLocalIDs)
// is *preserved* here. SwitchTo (always called by handleSearchJump
// before this) already cleared the slate, so any rows that exist now
// were inserted by ApplyDispatched / applyOutgoingState while
// JumpContext was running asynchronously — the user issued sends after
// the input was rebound to the target chat but before the window load
// returned. Wiping here would silently drop those rows; for 1:1 chats
// where Telegram emits only UpdateShortSentMessage (no later
// UpdateNewMessage echo) the message would disappear from the UI until
// manual reload.
func (m Model) LoadJumpWindow(chatID int64, messages []domain.Message, scrollToID int64, around int) Model {
	m.loadGen++
	m = m.dropSelection()
	m.chatID = chatID

	var maxWindowID int64
	windowIDs := make(map[int64]struct{}, len(messages))
	for _, msg := range messages {
		windowIDs[msg.ID] = struct{}{}
		if msg.ID > maxWindowID {
			maxWindowID = msg.ID
		}
	}

	var tail []domain.Message
	for _, prev := range m.messages {
		if prev.ID <= maxWindowID {
			continue
		}
		if _, dup := windowIDs[prev.ID]; dup {
			continue
		}
		tail = append(tail, prev)
	}

	merged := append([]domain.Message(nil), messages...)
	merged = append(merged, tail...)
	m.messages = merged

	m.loading = false
	m.hasMore = len(messages) > 0
	m.hasNewer = len(messages) > 0
	m.forwardCursorID = maxWindowID
	m.recomputeOldestID()
	m.viewport.SetContent(m.renderAll())
	if scrollToID == 0 {
		return m
	}
	return m.ScrollTo(scrollToID, around)
}

// loadCmd returns a tea.Cmd that fetches the next page from repo and
// emits messagesLoadedMsg or messagesLoadFailedMsg. We over-fetch by
// one row (limit+1) so the caller can detect hasMore without a second
// COUNT(*) round-trip.
//
// gen is the model's loadGen at scheduling time. The result carries it
// back so applyLoaded can drop the message when the model has moved
// on (chat switch, search jump) — without it a slow load would
// clobber whichever state the user has navigated to since.
func loadCmd(repo Repository, chatID int64, offset, limit int, gen uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		raw, err := repo.GetMessages(ctx, chatID, limit+1, offset)
		if err != nil {
			return messagesLoadFailedMsg{chatID: chatID, gen: gen, err: err}
		}
		hasMore := len(raw) > limit
		if hasMore {
			raw = raw[:limit]
		}
		return messagesLoadedMsg{
			chatID:   chatID,
			gen:      gen,
			messages: reverseMessages(raw),
			hasMore:  hasMore,
		}
	}
}

// paginateCmd is the scroll-up sibling of loadCmd. It drops the result
// into a separate message type so applyPagination knows to *prepend*
// rather than replace the current slice.
//
// Pagination is cursor-based (id < beforeID) rather than offset-based
// because the latter races with applyIncoming: a live append between
// initial load and scroll-up shifts the row count and produces a gap
// at the boundary. The cursor pins the boundary to a concrete id, so
// pagination remains correct regardless of how many messages have
// arrived live in between.
//
// gen mirrors loadCmd's gen — applyPagination drops the message on
// mismatch so a still-in-flight scroll-up from before a search-jump
// does not prepend stray rows into the freshly-installed window.
func paginateCmd(repo Repository, chatID, beforeID int64, limit int, gen uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		raw, err := repo.GetMessagesBefore(ctx, chatID, beforeID, limit+1)
		if err != nil {
			return messagesLoadFailedMsg{chatID: chatID, gen: gen, err: err}
		}
		hasMore := len(raw) > limit
		if hasMore {
			raw = raw[:limit]
		}
		return messagesPaginationLoadedMsg{
			chatID:   chatID,
			gen:      gen,
			messages: reverseMessages(raw),
			hasMore:  hasMore,
		}
	}
}

// paginateNewerCmd is the symmetric scroll-down companion. It fetches
// the next batch of rows newer than afterID via repo.GetMessagesAfter
// (already ASC) and emits messagesPaginationNewerLoadedMsg. The
// initiator is the search-jump path: after LoadJumpWindow lands, the
// user can scroll down to walk the model toward the present rather
// than staying stranded in the isolated ±N window.
//
// gen carries the model's loadGen at scheduling time so a stale forward
// page (e.g. user re-jumped to a different hit while this fetch was in
// flight) is dropped on arrival.
func paginateNewerCmd(repo Repository, chatID, afterID int64, limit int, gen uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		raw, err := repo.GetMessagesAfter(ctx, chatID, afterID, limit+1)
		if err != nil {
			return messagesLoadFailedMsg{chatID: chatID, gen: gen, err: err}
		}
		hasMore := len(raw) > limit
		if hasMore {
			raw = raw[:limit]
		}
		// repo.GetMessagesAfter returns ASC by contract — no reverse.
		return messagesPaginationNewerLoadedMsg{
			chatID:   chatID,
			gen:      gen,
			messages: raw,
			hasMore:  hasMore,
		}
	}
}

// reverseMessages flips a DESC-ordered slice (newest first, what SQL
// returns) into ASC (oldest first, what the viewport reads top-to-
// bottom). Operates on a fresh slice so the caller can keep its own
// reference without aliasing surprises.
func reverseMessages(in []domain.Message) []domain.Message {
	out := make([]domain.Message, len(in))
	for i, m := range in {
		out[len(in)-1-i] = m
	}
	return out
}

// recomputeOldestID scans m.messages and stores the smallest non-zero
// message id as m.oldestID. The cursor is exposed via OldestID() but
// kept in lockstep here so callers don't have to compute it on every
// access.
func (m *Model) recomputeOldestID() {
	var oldest int64
	for _, msg := range m.messages {
		if msg.ID == 0 {
			continue
		}
		if oldest == 0 || msg.ID < oldest {
			oldest = msg.ID
		}
	}
	m.oldestID = oldest
}

// paneHPadding mirrors the chats pane's constant: the app renders both panes in
// a box with one column of padding per side.
const paneHPadding = 2
