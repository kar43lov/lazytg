package search

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/kar43lov/lazytg/internal/core/search"
)

// snippetBoldOpen / snippetBoldClose are the markers FTS5 wraps every
// matched window with via the snippet() builtin (see the
// search.Service SQL). The overlay rewrites them as ANSI bold spans
// so the matched fragment stands out without us re-parsing the
// underlying message.
const (
	snippetBoldOpen  = "<b>"
	snippetBoldClose = "</b>"
)

// rowStyle / cursorRowStyle are the per-result style passes. The
// cursor row inherits the bold attribute the snippet pass already
// emits, so we only need to flip the foreground colour to make the
// highlighted row stand out.
var (
	resultStyle      = lipgloss.NewStyle().Padding(0, 1)
	resultCursorRow  = lipgloss.NewStyle().Padding(0, 1).Foreground(lipgloss.Color("12"))
	resultMetaStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	resultSnippetEm  = lipgloss.NewStyle().Bold(true)
	overlayBoxStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 2)
	overlayHintStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
)

// View renders the centred search overlay. width and height are the
// surrounding terminal dimensions used by lipgloss.Place; the modal
// sizes itself against its content.
//
// When Visible is false the view returns the empty string so callers
// can append it unconditionally (mirrors the help overlay contract).
func (m Model) View(width, height int) string {
	if !m.Visible {
		return ""
	}

	var body strings.Builder
	body.WriteString(m.input.View())
	body.WriteByte('\n')

	switch {
	case m.err != nil:
		body.WriteString(resultMetaStyle.Render(fmt.Sprintf("error: %v", m.err)))
	case m.loading && len(m.hits) == 0:
		body.WriteString(resultMetaStyle.Render("Searching…"))
	case len(m.hits) == 0:
		hint := overlayHintStyle.Render("type to search; Enter to jump, Esc to close")
		body.WriteString(hint)
	default:
		body.WriteString(m.renderResults())
	}

	box := overlayBoxStyle.Render(body.String())
	if width <= 0 || height <= 0 {
		return box
	}
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// renderResults renders every hit with the cursor row styled. The
// snippet's <b>…</b> markers are converted into bold ANSI runs so
// the matched fragment is visually highlighted without us touching
// the underlying message text.
func (m Model) renderResults() string {
	var b strings.Builder
	for i, hit := range m.hits {
		row := formatHit(hit)
		style := resultStyle
		if i == m.cursor {
			style = resultCursorRow
		}
		b.WriteString(style.Render(row))
		if i < len(m.hits)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// formatHit composes one result row: "chat-id  HH:MM  snippet". The
// chat title is not rendered here because the overlay does not have
// access to the chats list — the wiring layer can layer that on by
// wrapping search.Hit before passing it in (Stage 4). For Stage 3
// the user can correlate the chat-id with the chats pane.
func formatHit(hit search.Hit) string {
	// Local(), for the same reason as the thread header: search hits carry the
	// stored UTC instant, and a result row dated three hours off is worse here
	// than in the thread — the timestamp is the only thing telling two similar
	// hits apart.
	meta := fmt.Sprintf("chat=%d  %s", hit.ChatID, hit.Message.Date.Local().Format("2006-01-02 15:04"))
	snippet := renderSnippet(hit.Snippet)
	if snippet == "" {
		snippet = hit.Message.Text
	}
	return resultMetaStyle.Render(meta) + "  " + snippet
}

// renderSnippet substitutes FTS5's <b>…</b> markers for ANSI bold
// runs. Unmatched openings are preserved verbatim so a stray opening
// does not eat the rest of the snippet.
func renderSnippet(s string) string {
	var out strings.Builder
	for {
		i := strings.Index(s, snippetBoldOpen)
		if i < 0 {
			out.WriteString(s)
			return out.String()
		}
		rest := s[i+len(snippetBoldOpen):]
		j := strings.Index(rest, snippetBoldClose)
		if j < 0 {
			out.WriteString(s)
			return out.String()
		}
		out.WriteString(s[:i])
		out.WriteString(resultSnippetEm.Render(rest[:j]))
		s = rest[j+len(snippetBoldClose):]
	}
}
