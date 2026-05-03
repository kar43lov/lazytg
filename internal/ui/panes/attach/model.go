package attach

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// Mode tracks which textinput currently has focus inside the overlay.
// The overlay has a path field on top and a caption field below; Tab
// flips between them.
type Mode int

const (
	// ModePath puts the path input in focus. Up/Down navigate the
	// directory listing instead of moving the cursor inside the input.
	ModePath Mode = iota
	// ModeCaption puts the caption input in focus. The directory
	// listing freezes — Up/Down forward to the textinput's history
	// chord, but our caption input is single-line so they are no-ops.
	ModeCaption
)

// Entry is a single row in the directory listing. Name is the basename;
// IsDir distinguishes directories (rendered with a trailing "/") from
// regular files; Path is the absolute path used by Submit.
type Entry struct {
	Name  string
	Path  string
	IsDir bool
}

// Model is the attachment overlay's Bubble Tea model. Visible mirrors
// the search/palette pattern so the app's view layer can pick which
// overlay to render. ChatID is captured at OpenedMsg time and echoed
// back in SubmitMsg.
type Model struct {
	Width   int
	Height  int
	Visible bool

	pathInput    textinput.Model
	captionInput textinput.Model

	mode    Mode
	entries []Entry
	cursor  int
	chatID  int64
	loadErr error

	log *slog.Logger
}

// New returns an unfocused overlay rooted at the user's home directory
// (or "." when the home dir cannot be resolved). The overlay does not
// list anything until Open() fires its load Cmd — keeping the
// constructor synchronous lets the app build the model in tests
// without a running event loop.
func New(log *slog.Logger) Model {
	if log == nil {
		log = slog.New(discardHandler{})
	}
	pi := textinput.New()
	pi.Placeholder = "/path/to/file"
	pi.Prompt = "📁 "
	pi.SetWidth(40)
	pi.SetVirtualCursor(true)
	pi.SetValue(initialDir())

	ci := textinput.New()
	ci.Placeholder = "caption (optional)"
	ci.Prompt = "✎ "
	ci.SetWidth(40)
	ci.SetVirtualCursor(true)

	return Model{
		pathInput:    pi,
		captionInput: ci,
		mode:         ModePath,
		log:          log,
	}
}

// initialDir returns the directory the path input starts in. We pick
// the user's home so the picker lands in a familiar place; falling
// back to "." keeps the overlay functional in containerised
// environments where HOME is unset.
func initialDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return "."
}

// SetSize records the surrounding terminal dimensions so View can
// position the modal. Same logic as the search overlay's SetSize.
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
		m.pathInput.SetWidth(w)
		m.captionInput.SetWidth(w)
	}
	return m
}

// Open shows the overlay, focuses the path input, captures the chatID
// the file should land in, and returns a Cmd that lists the current
// directory. ChatID == 0 still opens the overlay but Submit will
// no-op — the app should never emit OpenedMsg with a zero chatID, the
// guard is defensive.
func (m Model) Open(chatID int64) (Model, tea.Cmd) {
	m.Visible = true
	m.chatID = chatID
	m.mode = ModePath
	m.cursor = 0
	m.loadErr = nil
	m.captionInput.SetValue("")
	cmd := m.pathInput.Focus()
	return m, tea.Batch(cmd, m.loadDir())
}

// Close hides the overlay and blurs both inputs. The path value
// survives so re-opening lands in the same directory the user was
// browsing.
func (m Model) Close() Model {
	m.Visible = false
	m.pathInput.Blur()
	m.captionInput.Blur()
	return m
}

// Entries returns a copy of the current directory listing. Test
// helper.
func (m Model) Entries() []Entry {
	out := make([]Entry, len(m.entries))
	copy(out, m.entries)
	return out
}

// Cursor returns the highlighted row index. Test helper.
func (m Model) Cursor() int { return m.cursor }

// PathValue returns the path-input contents. Test helper.
func (m Model) PathValue() string { return m.pathInput.Value() }

// CaptionValue returns the caption-input contents. Test helper.
func (m Model) CaptionValue() string { return m.captionInput.Value() }

// CurrentMode returns which input has focus. Test helper.
func (m Model) CurrentMode() Mode { return m.mode }

// ChatID returns the chat id captured at Open time. Test helper.
func (m Model) ChatID() int64 { return m.chatID }

// LoadErr returns the most recent directory-load error or nil. Test
// helper.
func (m Model) LoadErr() error { return m.loadErr }

// loadDir returns a Cmd that reads the current directory and emits a
// dirLoadedMsg. The actual ReadDir runs in a goroutine so a slow
// filesystem (network mount, etc.) does not block the program loop.
func (m Model) loadDir() tea.Cmd {
	dir := m.pathInput.Value()
	if dir == "" {
		dir = "."
	}
	return func() tea.Msg {
		// Stat first so we can distinguish "user typed a directory
		// path" from "user typed a file path" — the latter goes into
		// the listing as a single-row pre-selected entry.
		info, err := os.Stat(dir)
		if err != nil {
			return dirLoadedMsg{err: err}
		}
		if !info.IsDir() {
			abs, _ := filepath.Abs(dir)
			return dirLoadedMsg{
				dir: filepath.Dir(abs),
				entries: []Entry{{
					Name: filepath.Base(abs),
					Path: abs,
				}},
			}
		}
		return readDir(dir)
	}
}

// readDir runs the os.ReadDir + sort + Entry-mapping pipeline. Pulled
// out so the load Cmd stays a thin wrapper and tests can call it
// directly with a tmp dir.
func readDir(dir string) dirLoadedMsg {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dirLoadedMsg{err: err}
	}
	raw, err := os.ReadDir(abs)
	if err != nil {
		return dirLoadedMsg{err: err}
	}
	entries := make([]Entry, 0, len(raw)+1)
	if abs != "/" {
		entries = append(entries, Entry{Name: "..", Path: filepath.Dir(abs), IsDir: true})
	}
	dirs := make([]Entry, 0, len(raw))
	files := make([]Entry, 0, len(raw))
	for _, e := range raw {
		name := e.Name()
		if len(name) > 0 && name[0] == '.' {
			continue
		}
		entry := Entry{
			Name:  name,
			Path:  filepath.Join(abs, name),
			IsDir: e.IsDir(),
		}
		if e.IsDir() {
			dirs = append(dirs, entry)
		} else {
			files = append(files, entry)
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	entries = append(entries, dirs...)
	entries = append(entries, files...)
	return dirLoadedMsg{dir: abs, entries: entries}
}

// dirLoadedMsg lands the result of a directory listing. Internal —
// the app does not see it directly.
type dirLoadedMsg struct {
	dir     string
	entries []Entry
	err     error
}

// discardHandler is a slog.Handler that drops every record. Same
// pattern as the search/palette overlays.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (h discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return h }
func (h discardHandler) WithGroup(string) slog.Handler           { return h }
