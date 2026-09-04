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

	"github.com/kar43lov/lazytg/internal/core/events"
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

// Upload is the upload-side twin of Download. uploadID is the
// in-process counter UploadService assigns at SendFile time (not the
// Telegram-assigned file id, which the upload pipeline only learns
// after completion); filename / bytes / total drive the same widget
// rendering as Download but with a ⬆ glyph instead of ⬇.
type Upload struct {
	uploadID int64
	filename string
	bytes    int64
	total    int64
}

// NewUpload constructs an Upload row. Twin of NewDownload — exposed so
// app/update.go can build typed values without poking unexported
// fields.
func NewUpload(uploadID int64, filename string, bytes, total int64) Upload {
	return Upload{uploadID: uploadID, filename: filename, bytes: bytes, total: total}
}

// UploadID returns the upload's in-process identifier.
func (u Upload) UploadID() int64 { return u.uploadID }

// Filename returns the user-visible name.
func (u Upload) Filename() string { return u.filename }

// Bytes returns the bytes uploaded so far.
func (u Upload) Bytes() int64 { return u.bytes }

// Total returns the total byte size; 0 when unknown.
func (u Upload) Total() int64 { return u.total }

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

	// DBSizeMB, when > 0, renders an inline "⚠ DB N.N GB" warning chip
	// next to the storage cell. The chip stays visible until the
	// monitor publishes a cleared event (DBSizeMB=0). 0 means "no
	// warning" — the file may still be large, just under the threshold.
	DBSizeMB int

	// downloads tracks active file downloads keyed by FileID. The map
	// is copied on every Upsert/Remove so the value-semantics promise
	// of every other Model setter holds — callers never observe a
	// partially-mutated map across goroutine boundaries.
	downloads map[int64]Download

	// Notice is a one-line answer to something the user just did —
	// "copied 3 messages", "delete failed: …". It takes the place of the
	// chat title, which is the one cell the eye is already on, and stays
	// until something replaces it.
	//
	// No timer: a notice that erases itself after N seconds is a notice
	// the user misses while looking at the message they just deleted, and
	// the next action overwrites it anyway.
	Notice string

	// Typing is what the other side is doing right now — "typing…",
	// "recording a voice message…". Empty almost always, and it is the one
	// field here that expires on its own: the app clears it on a timer,
	// because Telegram's "stopped" notification is not reliably sent.
	Typing string

	// uploads is the upload-side twin of downloads, keyed by the
	// in-process UploadID UploadService assigns. Same copy-on-write
	// discipline so the Model stays value-typed across goroutines.
	uploads map[int64]Upload
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
	if m.Typing != "" {
		// Somebody writing to you right now outranks the chat title,
		// which has not changed and is on screen anyway.
		chat = chat + " · " + m.Typing
	}
	if m.Notice != "" {
		// A notice is the answer to something the user just did, so it
		// outranks both. It is transient; the indicator comes back.
		chat = m.Notice
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

// SetTyping replaces the "somebody is composing something" indicator. An
// empty string clears it.
//
// Held here rather than composed by the caller because the status bar owns
// the budget: the line is truncated to fit the terminal, and a caller
// splicing its own text into the title would be truncated as one blob.
func (m Model) SetTyping(s string) Model {
	m.Typing = s
	return m
}

// SetNotice replaces the notice line. An empty string clears it, which is
// what a chat switch does: the answer to "copied 3 messages" belongs to the
// conversation it happened in.
func (m Model) SetNotice(s string) Model {
	m.Notice = s
	return m
}

// renderRight composes "unread N | conn[: reason] | storage" with colour on
// the conn cell. FloodWait, when non-zero, replaces conn with "floodwait Xs".
//
// When at least one download or upload is active, the conn cell is
// replaced with `⬇ filename N%` (download) or `⬆ filename N%` (upload)
// so the user sees ongoing progress in the bottom strip without a
// separate notification widget. Active uploads take priority over
// downloads — the user just initiated the upload action and feedback
// matters more than passive download progress. Multi-row cases pick
// the row with the smallest in-process id to keep rendering
// deterministic across map re-orders.
func (m Model) renderRight() string {
	unread := fmt.Sprintf("unread %d", m.UnreadTotal)
	cell := m.renderConn()
	if u, ok := m.activeUpload(); ok {
		cell = uploadStyle.Render(formatUploadCell(u))
	} else if d, ok := m.activeDownload(); ok {
		cell = downloadStyle.Render(formatDownloadCell(d))
	}
	storage := m.StorageMode
	if storage == "" {
		storage = events.StorageModeReadWrite
	}
	out := unread + " | " + cell + " | " + storage
	if m.DBSizeMB > 0 {
		out += " | " + dbSizeStyle.Render(formatDBSizeCell(m.DBSizeMB))
	}
	return out
}

// formatDBSizeCell renders the DB-size warning chip. We switch to GB
// once we cross 1024 MB so the bar reads "⚠ DB 1.4 GB" instead of
// "⚠ DB 1430 MB" — humans parse the GB form faster.
func formatDBSizeCell(mb int) string {
	if mb >= 1024 {
		return fmt.Sprintf("⚠ DB %.1f GB", float64(mb)/1024)
	}
	return fmt.Sprintf("⚠ DB %d MB", mb)
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

// UpsertUpload inserts or updates the in-progress upload row keyed
// by u.UploadID. Same preserve-filename behaviour as UpsertDownload —
// a Progress event without a filename keeps the previous one visible.
func (m Model) UpsertUpload(u Upload) Model {
	out := m
	out.uploads = make(map[int64]Upload, len(m.uploads)+1)
	for k, v := range m.uploads {
		out.uploads[k] = v
	}
	if u.filename == "" {
		if prev, ok := m.uploads[u.uploadID]; ok {
			u.filename = prev.filename
			if u.total == 0 {
				u.total = prev.total
			}
		}
	}
	out.uploads[u.uploadID] = u
	return out
}

// RemoveUpload drops the row for uploadID. Twin of RemoveDownload —
// idempotent, used by both completed and failed paths.
func (m Model) RemoveUpload(uploadID int64) Model {
	if _, ok := m.uploads[uploadID]; !ok {
		return m
	}
	out := m
	out.uploads = make(map[int64]Upload, len(m.uploads))
	for k, v := range m.uploads {
		if k == uploadID {
			continue
		}
		out.uploads[k] = v
	}
	if len(out.uploads) == 0 {
		out.uploads = nil
	}
	return out
}

// ActiveUploads returns a snapshot of in-flight uploads. Test helper.
func (m Model) ActiveUploads() map[int64]Upload {
	if len(m.uploads) == 0 {
		return nil
	}
	out := make(map[int64]Upload, len(m.uploads))
	for k, v := range m.uploads {
		out[k] = v
	}
	return out
}

// activeUpload picks one of the running uploads to surface in the
// status bar. Smallest UploadID wins so the rendering is stable
// across map re-orders.
func (m Model) activeUpload() (Upload, bool) {
	if len(m.uploads) == 0 {
		return Upload{}, false
	}
	var (
		out  Upload
		seen bool
	)
	for _, u := range m.uploads {
		if !seen || u.uploadID < out.uploadID {
			out = u
			seen = true
		}
	}
	return out, seen
}

// formatUploadCell mirrors formatDownloadCell but renders ⬆ instead.
func formatUploadCell(u Upload) string {
	name := u.filename
	if name == "" {
		name = "file"
	}
	if u.total > 0 {
		pct := int(u.bytes * 100 / u.total)
		if pct > 100 {
			pct = 100
		}
		return fmt.Sprintf("⬆ %s %d%%", name, pct)
	}
	return "⬆ " + name
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

// uploadStyle paints the upload cell. We re-use the cyan from the
// download cell so the user reads "transient activity" without the bar
// having to spell out the direction in colour — the ⬆ glyph already
// does that.
var uploadStyle = downloadStyle

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
	// dbSizeStyle uses the same yellow as floodwait/connecting so the
	// "warn" semantic is consistent across the bar — the user reads
	// yellow as "look at me but nothing is broken yet".
	dbSizeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
)
