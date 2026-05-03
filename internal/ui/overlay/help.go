// Package overlay provides modal overlays drawn on top of the main TUI
// layout. The Help overlay lists every action-level binding from the active
// keymap; full implementation lands in Stage 2 Task 10. This stub gives the
// app skeleton (Task 6) something to compose against and keeps the toggle
// behaviour testable end-to-end.
package overlay

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"

	tea "charm.land/bubbletea/v2"

	"github.com/pgmac/lazytg/internal/ui/keymap"
)

// Help is the immutable view-state of the help overlay.
//
// Visibility is owned by the app model — the overlay only knows how to render
// itself and respond to dismiss keys (Esc/q/?). When Visible is false, View
// returns the empty string so callers can unconditionally append it.
type Help struct {
	Visible bool
	Keymap  keymap.Keymap
}

// New returns a hidden help overlay bound to the given keymap.
func New(km keymap.Keymap) Help {
	return Help{Keymap: km}
}

// Update reacts to dismiss keys. The overlay is modal: when visible it
// swallows every key event so the underlying focus target does not also act
// on the same keypress (e.g. typing "q" in the input field while help is open
// must not quit the app).
func (h Help) Update(msg tea.Msg) (Help, tea.Cmd) {
	if !h.Visible {
		return h, nil
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc", "q", "?":
			h.Visible = false
		}
	}
	return h, nil
}

// View renders a centred modal listing every action and its key chord. width
// and height are the surrounding terminal dimensions, used to position the
// overlay; the overlay itself sizes to its content.
func (h Help) View(width, height int) string {
	if !h.Visible {
		return ""
	}

	rows := h.rows()
	body := renderTable(rows)
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(1, 2).
		Render(body)

	if width <= 0 || height <= 0 {
		return box
	}
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// row pairs an action description with its current chord.
type row struct {
	action string
	chord  string
}

// rows pulls every binding off the keymap into a deterministic slice. Sort is
// alphabetical by action so the overlay is stable across reorderings of the
// Keymap struct.
func (h Help) rows() []row {
	bindings := []struct {
		action string
		bind   key.Binding
	}{
		{"send", h.Keymap.Send},
		{"newline", h.Keymap.Newline},
		{"reply", h.Keymap.Reply},
		{"open editor", h.Keymap.OpenEditor},
		{"toggle help", h.Keymap.ToggleHelp},
		{"focus next", h.Keymap.FocusNext},
		{"focus prev", h.Keymap.FocusPrev},
		{"scroll up", h.Keymap.ScrollUp},
		{"scroll down", h.Keymap.ScrollDown},
		{"search", h.Keymap.Search},
		{"quit", h.Keymap.Quit},
	}
	out := make([]row, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, row{action: b.action, chord: chordFromBinding(b.bind)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].action < out[j].action })
	return out
}

// chordFromBinding picks the user-friendly chord representation. Prefer the
// Help string set via key.WithHelp (it is what we want users to memorise);
// fall back to the first concrete key when help is empty.
func chordFromBinding(b key.Binding) string {
	if h := b.Help(); h.Key != "" {
		return h.Key
	}
	if keys := b.Keys(); len(keys) > 0 {
		return keys[0]
	}
	return "-"
}

// renderTable builds a fixed two-column "Action | Binding" listing. Manual
// padding keeps the overlay free of extra deps; widths are computed from data
// so renames don't break alignment.
func renderTable(rows []row) string {
	const (
		hdrAction = "Action"
		hdrChord  = "Binding"
	)

	actionW, chordW := lipgloss.Width(hdrAction), lipgloss.Width(hdrChord)
	for _, r := range rows {
		if w := lipgloss.Width(r.action); w > actionW {
			actionW = w
		}
		if w := lipgloss.Width(r.chord); w > chordW {
			chordW = w
		}
	}

	var b strings.Builder
	headerStyle := lipgloss.NewStyle().Bold(true)
	fmt.Fprintf(&b, "%s  %s\n",
		headerStyle.Render(padRight(hdrAction, actionW)),
		headerStyle.Render(padRight(hdrChord, chordW)),
	)
	b.WriteString(strings.Repeat("-", actionW+chordW+2))
	b.WriteByte('\n')
	for _, r := range rows {
		fmt.Fprintf(&b, "%s  %s\n", padRight(r.action, actionW), padRight(r.chord, chordW))
	}
	return strings.TrimRight(b.String(), "\n")
}

func padRight(s string, width int) string {
	pad := width - lipgloss.Width(s)
	if pad <= 0 {
		return s
	}
	return s + strings.Repeat(" ", pad)
}
