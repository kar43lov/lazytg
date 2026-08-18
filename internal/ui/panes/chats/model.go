// Package chats hosts the left pane of the lazytg TUI — the dialog list.
//
// The pane is a thin shell over bubbles/list.Model that adds (1) a
// repo-backed loader, (2) deterministic sorting (pinned first, then
// last-message-date desc), (3) a 200ms debounce for DialogUpdated events
// so a flood of metadata changes does not redo the whole sort/render
// cycle on every tick, and (4) an Enter handler that publishes
// ChatSelectedMsg for the thread pane to consume.
//
// Width/Height/Focused fields stay exported so the app's view layer can
// keep rendering each pane in its own lipgloss box without poking at
// internal list state.
package chats

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

// debounceWindow is how long Update waits after the *last* DialogUpdated
// before re-reading the repo. 200ms is short enough that a user-perceived
// "list update" still feels instantaneous, while bunching the bursts that
// gotd's updates.Manager emits during initial sync (dozens of DialogUpdates
// per second) into a single SQL query.
const debounceWindow = 200 * time.Millisecond

// loadTimeout caps how long a repo read can stall the UI. SQLite reads
// against the local DB normally complete in microseconds, but a stuck
// VFS lock should not be allowed to freeze the chats pane forever.
const loadTimeout = 5 * time.Second

// minListHeight keeps the list visible when the app composer is in
// terminal-too-small fallback or has not yet received a WindowSize. The
// bubbles list rejects height <= 0 (renders an empty string) which would
// hide the pane label and confuse the existing app/view tests.
const (
	minListWidth  = 20
	minListHeight = 5
)

// Model is the chats-pane Bubble Tea model. It composes a bubbles/list
// for navigation/filter UI, a repo handle for content, and a generation
// counter for debounced reloads.
//
// Width/Height/Focused are public so the app's view layer can size the
// surrounding lipgloss border. The list itself receives sizes via
// SetSize; the model also persists them so a SetFocus call (which does
// not carry dimensions) does not lose them.
type Model struct {
	Width   int
	Height  int
	Focused bool

	list  list.Model
	repo  Repository
	log   *slog.Logger
	chats []ChatItem

	// itemRows is how many terminal rows one list row occupies, captured
	// from the delegate at construction (bubbles exposes no getter on
	// list.Model). Mouse hit-testing needs it; see mouse.go.
	itemRows int

	// reloadGeneration is incremented every time a DialogUpdated arrives.
	// reloadDebouncedMsg with a stale generation is dropped so only the
	// most recent tick performs the repo read.
	reloadGeneration uint64
}

// New returns a placeholder model with no repo wired. Used by app
// construction in tests and by the early Stage 2 skeleton where wiring
// happens lazily. Calling Init on a no-repo model is a no-op.
func New() Model {
	return newModel(nil, nil)
}

// NewWithRepo returns a model that loads chats from repo on Init. log
// can be nil; the model uses slog.Default in that case.
func NewWithRepo(repo Repository, log *slog.Logger) Model {
	return newModel(repo, log)
}

func newModel(repo Repository, log *slog.Logger) Model {
	if log == nil {
		log = slog.Default()
	}
	delegate := list.NewDefaultDelegate()
	l := list.New(nil, delegate, minListWidth, minListHeight)
	l.SetShowTitle(false)       // we render the focus-aware header in View
	l.SetShowStatusBar(false)   // pane real-estate is tight; defer status to app
	l.SetShowHelp(false)        // help lives in the global overlay (Task 10)
	l.SetShowPagination(false)  // few-dozen-chats lists fit in one page
	l.SetFilteringEnabled(true) // keep the built-in '/' filter
	return Model{
		list:     l,
		repo:     repo,
		log:      log,
		itemRows: delegate.Height() + delegate.Spacing(),
	}
}

// Init kicks off the initial repo load. Returns nil if the model was
// constructed without a repo (placeholder mode).
func (m Model) Init() tea.Cmd {
	if m.repo == nil {
		return nil
	}
	return loadCmd(m.repo)
}

// Items returns the current chat items. Exposed for tests so they can
// assert ordering without rendering and re-parsing the View.
func (m Model) Items() []ChatItem {
	out := make([]ChatItem, len(m.chats))
	copy(out, m.chats)
	return out
}

// SelectedItem returns the currently highlighted ChatItem, or the zero
// value if the list is empty. Tests use this to verify cursor movement
// without inspecting list internals.
func (m Model) SelectedItem() (ChatItem, bool) {
	sel := m.list.SelectedItem()
	if sel == nil {
		return ChatItem{}, false
	}
	item, ok := sel.(ChatItem)
	return item, ok
}

// SetSize updates pane dimensions. Called by the app on resize. We clamp
// to (minListWidth, minListHeight) so the list always has room to render
// at least one row plus the header line.
func (m Model) SetSize(width, height int) Model {
	m.Width = width
	m.Height = height
	// Two columns go to the pane box's padding (see paneHPadding): the bubbles
	// delegate truncates to whatever width it is given, so a list sized to the
	// full pane produced rows two columns too wide, which lipgloss wrapped onto
	// a second line. That turned a two-row item into three, shifted every row
	// below it, and pushed the last chat out of the pane entirely.
	w := width - paneHPadding
	if w < minListWidth {
		w = minListWidth
	}
	// Reserve one row for the focus-aware header rendered by View.
	listHeight := height - 1
	if listHeight < minListHeight {
		listHeight = minListHeight
	}
	m.list.SetSize(w, listHeight)
	return m
}

// SetFocus toggles whether the pane is focused. The list itself does not
// have a separate focus concept (key handling is ours either way) so
// this only updates the cosmetic flag the View layer uses.
func (m Model) SetFocus(f bool) Model {
	m.Focused = f
	return m
}

// loadCmd returns a tea.Cmd that fetches chats from repo and emits
// either chatsLoadedMsg or chatsLoadFailedMsg. Sorting happens here so
// Update never has to re-sort and tests can rely on the message payload
// being already-canonical.
func loadCmd(repo Repository) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		raw, err := repo.GetChats(ctx)
		if err != nil {
			return chatsLoadFailedMsg{err: err}
		}
		items := make([]ChatItem, 0, len(raw))
		for _, c := range raw {
			items = append(items, NewChatItem(c, c.LastMessagePreview))
		}
		sortChatItems(items)
		return chatsLoadedMsg{items: items}
	}
}

// sortChatItems applies the canonical pinned-first / last-message-desc /
// id-asc ordering to items in place. Mirrors what the SQLite repo
// already does in GetChats; we re-sort here so unit tests with fake
// repos that return arbitrary orderings still produce a deterministic
// view, and so future merges (e.g. interleaving pending-only chats from
// the outgoing table) inherit the same rule.
func sortChatItems(items []ChatItem) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.pinned != b.pinned {
			return a.pinned
		}
		if !a.lastMessageDate.Equal(b.lastMessageDate) {
			return a.lastMessageDate.After(b.lastMessageDate)
		}
		return a.id < b.id
	})
}

// paneHPadding is how many columns the app's pane box takes for its own
// horizontal padding (one on each side). Declared here rather than imported
// from the app package: internal/ui/app already imports this package, and the
// value is part of the contract SetSize is given, not a layout decision the
// pane makes.
const paneHPadding = 2
