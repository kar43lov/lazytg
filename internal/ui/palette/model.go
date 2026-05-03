package palette

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// loadTimeout caps how long the synchronous Open() candidate-list
// refresh can stall the UI. SQLite reads against the local DB
// normally complete in microseconds; this is a guard against a
// stuck VFS lock freezing the palette forever.
const loadTimeout = 5 * time.Second

// ChatLister is the slice of the chats backend the palette consumes
// to fill in titles for the frecency-ranked ids and to seed the
// first-time-user list (when no visits have been recorded yet).
// Defined as an interface so tests can swap a fake without the
// SQLite driver.
type ChatLister interface {
	GetChats(ctx context.Context) ([]domain.Chat, error)
}

// Item is one row in the palette's candidate list. NormalizedTitle
// is precomputed at load time so the per-keystroke fuzzy pass does
// not have to fold every candidate on every match call.
type Item struct {
	ChatID          int64
	Title           string
	NormalizedTitle string
}

// Model is the palette's Bubble Tea model. Visible is exported so
// the app's view layer can decide whether to render the overlay on
// top of the main 2-pane layout. Width/Height carry the surrounding
// terminal dimensions used by the overlay's own layout pass.
type Model struct {
	Width   int
	Height  int
	Visible bool

	input    textinput.Model
	frecency FrecencyStore
	chats    ChatLister
	log      *slog.Logger

	// items is the full candidate list seeded by Open. filtered
	// holds the indices into items that survive the current fuzzy
	// query; the cursor indexes into filtered, not items, so the
	// View layer can iterate over filtered in O(matches) without a
	// re-scan.
	items    []Item
	filtered []int
	cursor   int
	loadErr  error
}

// New returns a palette bound to frecency and chats. log may be nil
// and gets routed to a discard handler so the palette can be
// exercised in tests without setting up a logger. Both backends may
// also be nil — Open() then loads an empty list and the View shows
// the "no candidates yet" placeholder.
func New(frecency FrecencyStore, chats ChatLister, log *slog.Logger) Model {
	if log == nil {
		log = slog.New(discardHandler{})
	}
	ti := textinput.New()
	ti.Placeholder = "switch chat…"
	ti.Prompt = "› "
	ti.SetWidth(40)
	ti.SetVirtualCursor(true)
	return Model{
		input:    ti,
		frecency: frecency,
		chats:    chats,
		log:      log,
	}
}

// SetSize records the surrounding terminal dimensions so View can
// position the modal. The overlay does not propagate this into the
// textinput — its width is set independently in renderView.
func (m Model) SetSize(width, height int) Model {
	m.Width = width
	m.Height = height
	if width > 4 {
		w := width - 8
		if w > 80 {
			w = 80
		}
		if w < 10 {
			w = 10
		}
		m.input.SetWidth(w)
	}
	return m
}

// Open shows the palette and focuses the textinput. Returns the
// model + the focus Cmd (cursor blink) and a load Cmd that refreshes
// the candidate list from FrecencyStore + ChatLister. The two cmds
// are batched so callers can hand the result straight to bubbletea.
func (m Model) Open() (Model, tea.Cmd) {
	m.Visible = true
	m.cursor = 0
	m.loadErr = nil
	m.input.SetValue("")
	focusCmd := m.input.Focus()
	loadCmd := m.loadCandidates()
	return m, tea.Batch(focusCmd, loadCmd)
}

// Close hides the palette and blurs the textinput. The candidate
// list is preserved so a subsequent Open without intervening data
// changes can short-circuit the reload (currently the load fires
// every Open — Stage 4 may add invalidation).
func (m Model) Close() Model {
	m.Visible = false
	m.input.Blur()
	return m
}

// Items returns a copy of the current candidate list. Test helper.
func (m Model) Items() []Item {
	out := make([]Item, len(m.items))
	copy(out, m.items)
	return out
}

// Filtered returns the indices into Items() that pass the current
// fuzzy query. Test helper.
func (m Model) Filtered() []int {
	out := make([]int, len(m.filtered))
	copy(out, m.filtered)
	return out
}

// Cursor returns the index into Filtered() of the highlighted row.
// Test helper.
func (m Model) Cursor() int { return m.cursor }

// Value returns the textinput's current contents. Test helper.
func (m Model) Value() string { return m.input.Value() }

// LoadErr returns the most recent error from the candidate-list
// refresh, or nil. Test helper.
func (m Model) LoadErr() error { return m.loadErr }

// loadCandidates schedules a tea.Cmd that reads the frecency hot-set
// + the chat list, merges them into Items, and emits a LoadedMsg.
// The merge logic prefers the frecency order for known chats and
// appends the rest of the chat list (sorted by title) so a brand-new
// account with zero visits still has something to type against.
func (m Model) loadCandidates() tea.Cmd {
	frecency := m.frecency
	chats := m.chats
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()

		var (
			topIDs   []int64
			chatList []domain.Chat
			loadErr  error
		)
		if frecency != nil {
			ids, err := frecency.Top(ctx, HotSetLimit)
			if err != nil {
				loadErr = err
			}
			topIDs = ids
		}
		if chats != nil {
			cs, err := chats.GetChats(ctx)
			if err != nil && loadErr == nil {
				loadErr = err
			}
			chatList = cs
		}

		items := mergeCandidates(topIDs, chatList)
		return LoadedMsg{Items: items, Err: loadErr}
	}
}

// mergeCandidates folds the frecency hot-set with the full chat
// list. The result is ordered: first every chat that appears in
// topIDs (in topIDs order), then every remaining chat from chatList
// sorted alphabetically by title for a deterministic tail. Chats
// without a title are skipped entirely — they cannot be visualised
// in the palette and the user cannot fuzzy-match an empty string.
//
// If chatList is nil but topIDs has entries, those chats are
// returned with empty Title (the palette will fall back to
// rendering "chat N" in that case). This corresponds to the rare
// race where the frecency table has rows for chats the chats table
// has not yet learned about; not blocking the palette behind the
// chats reload keeps the open-time predictable.
func mergeCandidates(topIDs []int64, chatList []domain.Chat) []Item {
	byID := make(map[int64]domain.Chat, len(chatList))
	for _, c := range chatList {
		byID[c.ID] = c
	}
	seen := make(map[int64]bool, len(topIDs)+len(chatList))
	out := make([]Item, 0, len(topIDs)+len(chatList))

	for _, id := range topIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, itemFor(id, byID[id]))
	}

	rest := make([]domain.Chat, 0, len(chatList))
	for _, c := range chatList {
		if seen[c.ID] {
			continue
		}
		if c.Title == "" {
			continue
		}
		rest = append(rest, c)
	}
	sort.SliceStable(rest, func(i, j int) bool { return rest[i].Title < rest[j].Title })
	for _, c := range rest {
		out = append(out, itemFor(c.ID, c))
	}
	return out
}

// itemFor turns a chat (possibly zero-valued when chatList didn't
// know it) into an Item with a precomputed normalized title for
// fuzzy matching.
func itemFor(id int64, c domain.Chat) Item {
	title := c.Title
	if title == "" {
		// fallback so the user can still see and select the chat
		// even though the title hasn't synced yet.
		title = formatFallbackTitle(id)
	}
	return Item{
		ChatID:          id,
		Title:           title,
		NormalizedTitle: Normalize(title),
	}
}

// formatFallbackTitle is the rendering for a chat the palette knows
// the id of but not the title. Kept short so the row width stays
// predictable.
func formatFallbackTitle(id int64) string {
	return "chat " + itoa(id)
}

// itoa is a tiny strconv-free integer formatter used by
// formatFallbackTitle. Avoids pulling strconv into a hot path that
// only ever sees positive int64s; the fallback path is itself rare
// (frecency-known but chats-unknown), but the helper keeps the call
// site obvious.
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// discardHandler is a slog.Handler that drops every record. Used as
// the fallback logger when callers pass nil so we don't have to
// import log/slog/internal helpers.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (h discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return h }
func (h discardHandler) WithGroup(string) slog.Handler           { return h }
