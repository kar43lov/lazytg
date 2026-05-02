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

	"github.com/pgmac/lazytg/internal/core/domain"
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
type Model struct {
	Width   int
	Height  int
	Focused bool

	viewport viewport.Model
	chatID   int64
	messages []domain.Message
	repo     Repository
	provider HistoryProvider
	log      *slog.Logger

	loading bool
	hasMore bool
	// oldestID tracks the first (smallest-ID) message currently
	// rendered. Pagination uses len(messages) as the SQL offset for
	// simplicity, but oldestID is exposed so future callers (cursor-
	// based MTProto backfill) can ask for "older than X".
	oldestID int64
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
		viewport: vp,
		repo:     repo,
		provider: provider,
		log:      log,
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
func (m Model) OpenChat(chatID int64) (Model, tea.Cmd) {
	m.chatID = chatID
	m.messages = nil
	m.oldestID = 0
	m.hasMore = false
	m.loading = true
	m.viewport.SetContent("")
	if m.repo == nil {
		m.loading = false
		return m, nil
	}
	return m, loadCmd(m.repo, chatID, 0, initialPageSize)
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

// Loading reports whether a fetch is currently in flight.
func (m Model) Loading() bool { return m.loading }

// OldestID returns the smallest-ID message currently rendered, or 0
// when the model is empty. Exposed for tests so they can assert
// pagination updates the cursor.
func (m Model) OldestID() int64 { return m.oldestID }

// SetSize updates the pane dimensions. Called by the app on resize. We
// reserve one row for the focus-aware header rendered by View and clamp
// the viewport to (minViewportWidth, minViewportHeight) so a zero-pane
// app still produces a non-empty View.
func (m Model) SetSize(width, height int) Model {
	m.Width = width
	m.Height = height
	w := width
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

// SetFocus toggles whether the pane is focused. The viewport itself
// does not have a separate focus concept (key handling is ours either
// way) so this only updates the cosmetic flag the View layer uses.
func (m Model) SetFocus(f bool) Model {
	m.Focused = f
	return m
}

// renderAll concatenates every message with a blank-line separator.
// Messages are stored oldest-first so the natural top-to-bottom reading
// order matches the slice index.
func (m Model) renderAll() string {
	var b strings.Builder
	width := m.viewport.Width()
	for i, msg := range m.messages {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(FormatMessage(msg, width, nil))
	}
	return b.String()
}

// loadCmd returns a tea.Cmd that fetches the next page from repo and
// emits messagesLoadedMsg or messagesLoadFailedMsg. We over-fetch by
// one row (limit+1) so the caller can detect hasMore without a second
// COUNT(*) round-trip.
func loadCmd(repo Repository, chatID int64, offset, limit int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		raw, err := repo.GetMessages(ctx, chatID, limit+1, offset)
		if err != nil {
			return messagesLoadFailedMsg{chatID: chatID, err: err}
		}
		hasMore := len(raw) > limit
		if hasMore {
			raw = raw[:limit]
		}
		return messagesLoadedMsg{
			chatID:   chatID,
			messages: reverseMessages(raw),
			hasMore:  hasMore,
		}
	}
}

// paginateCmd is the scroll-up sibling of loadCmd. It drops the result
// into a separate message type so applyPagination knows to *prepend*
// rather than replace the current slice.
func paginateCmd(repo Repository, chatID int64, offset, limit int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		raw, err := repo.GetMessages(ctx, chatID, limit+1, offset)
		if err != nil {
			return messagesLoadFailedMsg{chatID: chatID, err: err}
		}
		hasMore := len(raw) > limit
		if hasMore {
			raw = raw[:limit]
		}
		return messagesPaginationLoadedMsg{
			chatID:   chatID,
			messages: reverseMessages(raw),
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
