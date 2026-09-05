package search

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/search"
)

// DefaultDebounce is the textinput → service.Search delay. 150ms is
// short enough that the user feels each keystroke produced a result,
// but long enough that typing "hello" produces one query rather than
// five sequential ones.
const DefaultDebounce = 150 * time.Millisecond

// DefaultLimit is the per-search result cap. Matches
// search.DefaultSearchLimit so the overlay shows whatever the service
// is willing to return without trimming further.
const DefaultLimit = 50

// queryTimeout caps how long a single service.Search call can stall
// the UI. SQLite trigram queries normally complete in tens of
// milliseconds; the timeout exists so a stuck VFS lock cannot freeze
// the overlay forever.
const queryTimeout = 5 * time.Second

// remoteTimeout bounds one server search; the network is slower than
// SQLite and a flood wait is reported, not waited out.
const remoteTimeout = 20 * time.Second

// Service is the gotd-/storage-free contract the overlay needs from
// the search pipeline. *search.Service satisfies it via its Search
// method; tests swap in a fake that records the call and produces a
// canned result.
type Service interface {
	Search(ctx context.Context, raw string, limit int) ([]search.Hit, error)
}

// Model is the search overlay's Bubble Tea model. Visible is exported
// so the app's view layer can decide whether to render the overlay on
// top of the main 2-pane layout. Width/Height carry the surrounding
// terminal dimensions used by the overlay's own layout pass; the
// modal sizes itself against them.
type Model struct {
	Width   int
	Height  int
	Visible bool

	input    textinput.Model
	service  Service
	remote   Service
	debounce time.Duration
	log      *slog.Logger

	// remoteShown says the hits on screen came from the server;
	// remoteLoading that a server search is in flight.
	remoteShown   bool
	remoteLoading bool

	// queryGeneration is incremented every time the input value
	// changes. The debounce tick captures the generation it was
	// scheduled with; mismatches drop the tick so only the last
	// keystroke's worth of typing fires a query.
	queryGeneration uint64

	// lastQuery records the query whose results are currently
	// displayed. Used to skip duplicate Search calls when the
	// debounce ticks at a moment the value hasn't changed (e.g.
	// after the user pressed Esc and re-opened the overlay with the
	// same input value).
	lastQuery string

	// hits is the most recently returned slice. Cursor indexes into
	// it.
	hits   []search.Hit
	cursor int
	err    error

	// loading signals an in-flight Search. The view renders a
	// "Searching…" placeholder while loading is true and hits is
	// empty so the user has feedback during the SQL round-trip.
	loading bool
}

// New returns an overlay bound to service. debounce <= 0 falls back
// to DefaultDebounce; log may be nil and gets routed to a discard
// handler so the overlay can be exercised in tests without setting up
// a logger.
func New(service Service, debounce time.Duration, log *slog.Logger) Model {
	if debounce <= 0 {
		debounce = DefaultDebounce
	}
	if log == nil {
		log = slog.New(discardHandler{})
	}
	ti := textinput.New()
	ti.Placeholder = "search…"
	ti.Prompt = "/"
	ti.SetWidth(40)
	ti.SetVirtualCursor(true)
	return Model{
		input:    ti,
		service:  service,
		debounce: debounce,
		log:      log,
	}
}

// WithRemote installs the server-side fallback the Tab key asks. Without
// it the key does nothing and the hint does not mention it.
func (m Model) WithRemote(remote Service) Model {
	m.remote = remote
	return m
}

// RemoteShown reports whether the hits on screen came from the server.
func (m Model) RemoteShown() bool { return m.remoteShown }

// RemoteLoading reports whether a server search is in flight.
func (m Model) RemoteLoading() bool { return m.remoteLoading }

// SetSize records the surrounding terminal dimensions so View can
// position the modal. The overlay does not propagate this into the
// textinput — its width is set independently in renderView.
func (m Model) SetSize(width, height int) Model {
	m.Width = width
	m.Height = height
	if width > 4 {
		// Reserve a few columns for the border + padding and clamp to
		// a sensible maximum so the input does not stretch across a
		// 200-column terminal.
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

// Open shows the overlay and focuses the textinput. Returns the model
// + the focus Cmd so cursor blink scheduling lines up with what the
// rest of the bubbletea tree expects.
func (m Model) Open() (Model, tea.Cmd) {
	m.Visible = true
	m.err = nil
	m.cursor = 0
	cmd := m.input.Focus()
	return m, cmd
}

// Close hides the overlay and blurs the textinput. The input value is
// preserved so re-opening without an explicit Reset shows the previous
// query (mirrors what most "/" search overlays do).
func (m Model) Close() Model {
	m.Visible = false
	m.input.Blur()
	return m
}

// Reset clears the input value and any pending results. Used by
// tests; production currently relies on Close keeping the value so
// the user can re-fire the same query without re-typing.
func (m Model) Reset() Model {
	m.input.SetValue("")
	m.hits = nil
	m.lastQuery = ""
	m.cursor = 0
	m.err = nil
	m.loading = false
	return m
}

// Hits returns a copy of the currently displayed result slice. Test
// helper.
func (m Model) Hits() []search.Hit {
	out := make([]search.Hit, len(m.hits))
	copy(out, m.hits)
	return out
}

// Cursor returns the index of the highlighted result. Test helper.
func (m Model) Cursor() int { return m.cursor }

// Value returns the textinput's current contents. Test helper.
func (m Model) Value() string { return m.input.Value() }

// Loading reports whether a service.Search call is currently in
// flight. Test helper.
func (m Model) Loading() bool { return m.loading }

// Err returns the most recent error from service.Search, or nil.
// Test helper.
func (m Model) Err() error { return m.err }

// QueryGeneration returns the internal generation counter. Test
// helper used by debounce assertions.
func (m Model) QueryGeneration() uint64 { return m.queryGeneration }

// scheduleQuery bumps the generation counter and arms a tea.Tick. The
// debounce drops every tick whose generation does not match the
// current counter so a burst of keystrokes only fires one query.
func (m *Model) scheduleQuery() tea.Cmd {
	m.queryGeneration++
	gen := m.queryGeneration
	d := m.debounce
	return tea.Tick(d, func(time.Time) tea.Msg {
		return debounceTickMsg{generation: gen}
	})
}

// runSearch returns a tea.Cmd that calls service.Search and emits a
// ResultsMsg. Bounded by queryTimeout so a wedged SQLite read
// cannot freeze the overlay.
func (m Model) runSearch(query string) tea.Cmd {
	if m.service == nil {
		return nil
	}
	limit := DefaultLimit
	svc := m.service
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		hits, err := svc.Search(ctx, query, limit)
		return ResultsMsg{Hits: hits, Err: err}
	}
}

// runRemote asks the server for the current query: one request per
// press of the key, never on a timer, never on typing. The answer
// carries the generation so applyResults can drop it if the user
// typed on while waiting.
func (m Model) runRemote() (Model, tea.Cmd) {
	if m.remote == nil {
		return m, nil
	}
	query := trimSpace(m.input.Value())
	if query == "" || m.remoteLoading {
		return m, nil
	}
	m.remoteLoading = true
	m.err = nil
	svc := m.remote
	gen := m.queryGeneration
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), remoteTimeout)
		defer cancel()
		hits, err := svc.Search(ctx, query, search.DefaultRemoteLimit)
		return ResultsMsg{Hits: hits, Err: err, Remote: true, Generation: gen}
	}
}

// trimSpace is a tiny wrapper so call sites stay self-explanatory.
// Whitespace-only queries are rejected by service.Search anyway, but
// the overlay short-circuits empty values in Update before scheduling
// a tick so the placeholder state stays clean.
func trimSpace(s string) string { return strings.TrimSpace(s) }

// debounceTickMsg fires after the debounce window elapses. Carries
// the generation it was scheduled with so stale ticks get dropped.
type debounceTickMsg struct{ generation uint64 }

// discardHandler is a slog.Handler that drops every record. Used as
// the fallback logger when callers pass nil so we don't have to
// import log/slog/internal helpers.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (h discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return h }
func (h discardHandler) WithGroup(string) slog.Handler           { return h }
