// Package statusbar renders the bottom 1-line status strip of the lazytg TUI.
//
// The bar carries enough state to answer "is anything wrong right now?" at a
// glance: account alias, current chat title, unread total, MTProto connection
// state, optional flood-wait countdown, and storage mode. Layout splits left
// (account context) from right (system state); ANSI colour is reserved for
// the connection state because that is the field most likely to demand
// attention mid-task.
package statusbar

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/pgmac/lazytg/internal/core/events"
)

// Connection-state strings rendered in the bar. They map 1:1 onto
// events.ConnectionStateChanged.State, so producers and the UI agree on
// spelling and so screenshots / golden files stay stable.
const (
	StateConnecting = "connecting"
	StateOnline     = "online"
	StateOffline    = "offline"
	StateFloodWait  = "floodwait"
)

// Download is one in-flight (or just-finished) download row the status
// bar can show as `⬇ filename 47%`. The status bar holds an internal
// map keyed by FileID so concurrent downloads each get their own row;
// the renderer picks the most-recent one for display because a
// 1-line status bar cannot show many at once.
//
// Fields are exported so app/update.go can construct values without an
// extra adapter layer; the sb model itself never mutates them.
type Download struct {
	fileID   int64
	filename string
	bytes    int64
	total    int64
}

// NewDownload constructs a Download row. Exposed so app/update.go can
// build typed values without poking at unexported fields. fileID is
// the dedup key the status bar uses to merge the same download's
// progress events; filename / bytes / total drive the on-screen
// rendering.
func NewDownload(fileID int64, filename string, bytes, total int64) Download {
	return Download{fileID: fileID, filename: filename, bytes: bytes, total: total}
}

// FileID returns the Telegram file id this download row tracks. It
// doubles as the dedup key inside Model.downloads. Together with the
// other Download accessors below, the unexported fields stay
// invariant from the consumer's perspective — UpsertDownload is the
// only legitimate path to mutate status-bar state.
func (d Download) FileID() int64 { return d.fileID }

// Filename returns the user-visible name shown in the status bar.
func (d Download) Filename() string { return d.filename }

// Bytes returns the bytes downloaded so far.
func (d Download) Bytes() int64 { return d.bytes }

// Total returns the total byte size; 0 when unknown.
func (d Download) Total() int64 { return d.total }

// Model is the immutable view-state of the status bar.
//
// Mutations happen by returning a new Model from each setter so callers can
// freely assign without worrying about pointer aliasing across tea.Cmd
// boundaries. FloodWait > 0 forces the connection cell into "floodwait Xs"
// regardless of ConnState — this matches user mental model (a flood-wait is a
// kind of forced offline) and keeps the renderer single-pass.
type Model struct {
	AccountAlias string
	ChatTitle    string
	UnreadTotal  int
	ConnState    string
	StorageMode  string
	FloodWait    time.Duration

	// downloads tracks active file downloads keyed by FileID. The map
	// is copied on every Upsert/Remove so the value-semantics promise
	// of every other Model setter holds — callers never observe a
	// partially-mutated map across goroutine boundaries.
	downloads map[int64]Download
}

// New returns a Model with sensible "no data yet" defaults — placeholder
// dashes in user-visible cells, "connecting" for the connection state so the
// first frame doesn't lie about being online, and read-write storage mode.
func New() Model {
	return Model{
		AccountAlias: "-",
		ChatTitle:    "-",
		ConnState:    StateConnecting,
		StorageMode:  events.StorageModeReadWrite,
	}
}

// View renders the bar at exactly width cells. It never wraps and never
// truncates the right segment (system state is the priority); if the terminal
// is too narrow, the chat title is truncated first, then the account alias.
//
// width<=0 returns an empty string — callers can pass the raw terminal width
// from tea.WindowSizeMsg without guarding.
func (m Model) View(width int) string {
	if width <= 0 {
		return ""
	}

	right := m.renderRight()
	left := m.renderLeft(width - lipgloss.Width(right) - 1)

	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}

	return left + strings.Repeat(" ", gap) + right
}

// renderLeft builds the "alias | chat" segment, truncating chat title (then
// alias) so the rendered cell width never exceeds the budget.
func (m Model) renderLeft(budget int) string {
	if budget < 0 {
		budget = 0
	}

	alias := m.AccountAlias
	if alias == "" {
		alias = "-"
	}
	chat := m.ChatTitle
	if chat == "" {
		chat = "-"
	}

	full := alias + " | " + chat
	if lipgloss.Width(full) <= budget {
		return full
	}

	// Truncate the chat title first; it is the more dynamic/longer field.
	const ellipsis = "…"
	chatBudget := budget - lipgloss.Width(alias) - len(" | ") - lipgloss.Width(ellipsis)
	if chatBudget > 0 {
		return alias + " | " + truncate(chat, chatBudget) + ellipsis
	}

	// Even the alias does not fit comfortably; truncate alias and drop chat.
	aliasBudget := budget - lipgloss.Width(ellipsis)
	if aliasBudget > 0 {
		return truncate(alias, aliasBudget) + ellipsis
	}
	return truncate(alias, budget)
}

// renderRight composes "unread N | conn[: reason] | storage" with colour on
// the conn cell. FloodWait, when non-zero, replaces conn with "floodwait Xs".
//
// When at least one download is active, the conn cell is replaced with
// `⬇ filename N%` so the user sees ongoing progress in the bottom strip
// without a separate notification widget. Multi-download cases pick the
// download with the smallest fileID to keep the rendering deterministic
// across re-orders of the underlying map.
func (m Model) renderRight() string {
	unread := fmt.Sprintf("unread %d", m.UnreadTotal)
	connOrDl := m.renderConn()
	if d, ok := m.activeDownload(); ok {
		connOrDl = downloadStyle.Render(formatDownloadCell(d))
	}
	storage := m.StorageMode
	if storage == "" {
		storage = events.StorageModeReadWrite
	}
	return unread + " | " + connOrDl + " | " + storage
}

// UpsertDownload inserts or updates the in-progress download row keyed
// by d.FileID. The previous filename is preserved when d.filename is
// empty so a Progress event (which does not carry a filename) does not
// blank the status row mid-flight.
func (m Model) UpsertDownload(d Download) Model {
	out := m
	out.downloads = make(map[int64]Download, len(m.downloads)+1)
	for k, v := range m.downloads {
		out.downloads[k] = v
	}
	if d.filename == "" {
		if prev, ok := m.downloads[d.fileID]; ok {
			d.filename = prev.filename
			if d.total == 0 {
				d.total = prev.total
			}
		}
	}
	out.downloads[d.fileID] = d
	return out
}

// RemoveDownload drops the row for fileID. Idempotent — used by both
// completed and failed paths so the status bar stops showing finished
// downloads.
func (m Model) RemoveDownload(fileID int64) Model {
	if _, ok := m.downloads[fileID]; !ok {
		return m
	}
	out := m
	out.downloads = make(map[int64]Download, len(m.downloads))
	for k, v := range m.downloads {
		if k == fileID {
			continue
		}
		out.downloads[k] = v
	}
	if len(out.downloads) == 0 {
		out.downloads = nil
	}
	return out
}

// ActiveDownloads returns a snapshot of in-flight downloads. Test
// helper: lets unit tests assert on the map without a separate
// renderer round-trip.
func (m Model) ActiveDownloads() map[int64]Download {
	if len(m.downloads) == 0 {
		return nil
	}
	out := make(map[int64]Download, len(m.downloads))
	for k, v := range m.downloads {
		out[k] = v
	}
	return out
}

// activeDownload picks one of the currently-running downloads to
// surface in the status bar. Multi-download UX is intentionally
// minimal in v0.1 (one cell, one row); v0.2 plans to render an
// expanded `⬇ 3 files` chip the user can drill into.
func (m Model) activeDownload() (Download, bool) {
	if len(m.downloads) == 0 {
		return Download{}, false
	}
	var (
		out  Download
		seen bool
	)
	for _, d := range m.downloads {
		if !seen || d.fileID < out.fileID {
			out = d
			seen = true
		}
	}
	return out, seen
}

// formatDownloadCell renders a single Download into the
// "⬇ filename 47%" form. When total bytes are unknown (gotd has not
// yet seen the file size) the percentage drops out and the cell shows
// only "⬇ filename".
func formatDownloadCell(d Download) string {
	name := d.filename
	if name == "" {
		name = "file"
	}
	if d.total > 0 {
		pct := int(d.bytes * 100 / d.total)
		if pct > 100 {
			pct = 100
		}
		return fmt.Sprintf("⬇ %s %d%%", name, pct)
	}
	return "⬇ " + name
}

// downloadStyle paints the download cell so it reads as a
// "transient activity" indicator rather than the steady
// connection/floodwait colours.
var downloadStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6")) // ANSI cyan

// renderConn returns the colourised connection cell. Colour values are
// ANSI-256 indices so the output renders identically across truecolor /
// 256-color terminals — important for golden-file stability.
func (m Model) renderConn() string {
	if m.FloodWait > 0 {
		secs := int(m.FloodWait.Round(time.Second) / time.Second)
		if secs < 1 {
			secs = 1
		}
		return floodwaitStyle.Render(fmt.Sprintf("floodwait %ds", secs))
	}

	state := m.ConnState
	if state == "" {
		state = StateConnecting
	}

	switch state {
	case StateOnline:
		return onlineStyle.Render(state)
	case StateOffline:
		return offlineStyle.Render(state)
	case StateConnecting:
		return connectingStyle.Render(state)
	case StateFloodWait:
		return floodwaitStyle.Render(state)
	default:
		return state
	}
}

// truncate returns s clipped to at most width display cells. When clipping
// happens the last cell is left to the caller (typically replaced with "…")
// so this helper does not append an ellipsis itself.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	runes := []rune(s)
	for i := len(runes); i > 0; i-- {
		candidate := string(runes[:i])
		if lipgloss.Width(candidate) <= width {
			return candidate
		}
	}
	return ""
}

// Style cells are package-level so that golden tests do not need to recompute
// them per render and so that consumers can inspect the colours via
// reflection if a snapshot ever needs justification.
var (
	onlineStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("2")) // ANSI green
	offlineStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1")) // ANSI red
	connectingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3")) // ANSI yellow
	floodwaitStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("3")) // ANSI yellow
)
