package thread

import (
	"net/url"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/ui/safetext"
)

// Formatting a message the way Telegram's own clients do — bold as bold,
// code in a fixed shade, a spoiler hidden until asked for — comes down to
// one problem: the spans overlap. Telegram lets a word be bold and italic
// and a link all at once, and it sends three entities for it. Rather than
// nesting styles, the body is cut at every span boundary into segments
// whose style set is constant, each segment is styled once with everything
// that applies to it, and the pieces are joined back. A wrap afterwards
// keeps the escapes with their text.

var (
	// hiddenStyle draws a spoiler that has not been revealed: the same grey
	// front and back, so the shape of the text is there and the text is not.
	hiddenStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Background(lipgloss.Color("8"))
	// revealedStyle is a spoiler with the cursor on its message. Kept
	// visibly marked so the reader knows this is what was hidden.
	revealedStyle = lipgloss.NewStyle().Background(lipgloss.Color("8"))
	quoteBar      = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("▎ ")
	urlHintStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// renderEntities lays the spans over text. reveal shows spoilers in the
// clear; the pane passes the cursor state, so moving onto a message is how a
// spoiler is read — the same gesture graphical clients bind to a click, and
// the only one a keyboard has that means "this one, now".
//
// The text is cleaned segment by segment rather than up front, because the
// cleaner removes characters and the offsets index the text as Telegram
// sent it; cleaning first would shift every span after the first stripped
// control character.
func renderEntities(text string, es []domain.Entity, reveal bool) string {
	if len(es) == 0 {
		return renderInlineMarkdown(safetext.Clean(text))
	}
	runes := []rune(text)
	n := len(runes)

	// Every boundary at which the active set may change.
	cuts := map[int]struct{}{0: {}, n: {}}
	for _, e := range es {
		start, end := clampSpan(e, n)
		if end <= start {
			continue
		}
		cuts[start] = struct{}{}
		cuts[end] = struct{}{}
	}
	bounds := make([]int, 0, len(cuts))
	for b := range cuts {
		bounds = append(bounds, b)
	}
	sortInts(bounds)

	var out strings.Builder
	for i := 0; i+1 < len(bounds); i++ {
		start, end := bounds[i], bounds[i+1]
		active := activeAt(es, start, end, n)
		seg := safetext.Clean(string(runes[start:end]))
		if seg == "" {
			continue
		}
		out.WriteString(styleSegment(seg, active, reveal))
		// A text_url shows where it goes. Placed after the last segment of
		// the span, where the eye lands after reading the words.
		for _, e := range active {
			if e.Kind == domain.EntityTextURL && e.End() == end {
				if host := linkHost(e.URL); host != "" {
					out.WriteString(urlHintStyle.Render(" ⟨" + host + "⟩"))
				}
			}
		}
	}
	return out.String()
}

// styleSegment applies every kind in active to one run of text with a
// constant style set. Spoiler wins over everything, because whatever else
// the text is, hidden means hidden.
func styleSegment(seg string, active []domain.Entity, reveal bool) string {
	var (
		style   = lipgloss.NewStyle()
		spoiler bool
		quote   bool
		code    bool
	)
	for _, e := range active {
		switch e.Kind {
		case domain.EntityBold:
			style = style.Bold(true)
		case domain.EntityItalic:
			style = style.Italic(true)
		case domain.EntityUnderline:
			style = style.Underline(true)
		case domain.EntityStrike:
			style = style.Strikethrough(true)
		case domain.EntityCode, domain.EntityPre:
			code = true
		case domain.EntitySpoiler:
			spoiler = true
		case domain.EntityBlockquote:
			quote = true
		case domain.EntityURL, domain.EntityTextURL, domain.EntityEmail, domain.EntityPhone:
			style = style.Foreground(lipgloss.Color("4")).Underline(true)
		case domain.EntityMention, domain.EntityMentionName, domain.EntityHashtag,
			domain.EntityCashtag, domain.EntityBotCommand:
			style = style.Foreground(lipgloss.Color("6"))
		}
	}
	if code {
		style = style.Foreground(lipgloss.Color("8"))
	}
	if quote {
		seg = quoteBar + strings.ReplaceAll(seg, "\n", "\n"+quoteBar)
	}
	switch {
	case spoiler && !reveal:
		return hiddenStyle.Render(seg)
	case spoiler:
		return style.Inherit(revealedStyle).Render(seg)
	default:
		return style.Render(seg)
	}
}

// activeAt lists the spans covering [start, end). Spans are cut at every
// boundary, so covering start is covering the whole segment.
func activeAt(es []domain.Entity, start, end, n int) []domain.Entity {
	var active []domain.Entity
	for _, e := range es {
		s, t := clampSpan(e, n)
		if s <= start && t >= end && t > s {
			active = append(active, e)
		}
	}
	return active
}

func clampSpan(e domain.Entity, n int) (int, int) {
	start, end := e.Offset, e.End()
	if start < 0 {
		start = 0
	}
	if end > n {
		end = n
	}
	return start, end
}

// linkHost is the part of a link a reader needs before following it. The
// scheme and the path are what a phisher varies; the host is what they
// cannot fake. Cleaned like everything else the sender wrote.
func linkHost(raw string) string {
	raw = safetext.CleanLine(strings.TrimSpace(raw))
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return truncRunes(raw, 40)
	}
	return u.Host
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}
