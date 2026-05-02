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
func (m Model) renderRight() string {
	unread := fmt.Sprintf("unread %d", m.UnreadTotal)
	conn := m.renderConn()
	storage := m.StorageMode
	if storage == "" {
		storage = events.StorageModeReadWrite
	}
	return unread + " | " + conn + " | " + storage
}

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
