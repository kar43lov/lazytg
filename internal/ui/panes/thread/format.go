package thread

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// replyPreviewMax caps how many runes of the parent message are echoed
// inside the "↳ replying to:" hint. 50 is what WeeChat uses for its
// inline-reply badge and keeps the line readable on a 30-col thread pane
// after the prefix is added.
const replyPreviewMax = 50

// minBodyWidth is the smallest column count we will ask lipgloss to wrap
// to. Anything tighter forces lipgloss into single-character columns
// which renders most messages as vertical stripes — better to floor at
// 4 (still readable for short emoji replies).
const minBodyWidth = 4

// timeStyle paints the "[15:04]" timestamp grey. The colour is an ANSI
// index (8 = bright black / grey) so the choice survives terminals that
// only support the basic 8-colour palette and golden snapshots stay
// stable across machines.
var timeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

// nameStyle bolds the author label so the eye can find message
// boundaries when scanning a long thread without colour.
var nameStyle = lipgloss.NewStyle().Bold(true)

// replyStyle is italic + grey for the "↳ replying to: …" hint so it sits
// visually below the headline without competing with the body text.
var replyStyle = lipgloss.NewStyle().Italic(true).Foreground(lipgloss.Color("8"))

// boldStyle / italicStyle / codeStyle drive the simple-markdown inline
// pass. Code uses dim grey on the default background — a real background
// fill would clash with the surrounding pane lipgloss border.
var (
	boldStyle   = lipgloss.NewStyle().Bold(true)
	italicStyle = lipgloss.NewStyle().Italic(true)
	codeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// FormatMessage renders msg into a multi-line string sized for width.
// replyTo, when non-nil, adds the inline "↳ replying to:" hint above the
// body; it is the parent message and only its text is consulted.
//
// The output ends with a trailing newline so callers can concatenate
// messages with a blank-line separator without bookkeeping.
func FormatMessage(msg domain.Message, width int, replyTo *domain.Message) string {
	return formatMessageAs(msg, width, replyTo, authorLabel(msg.FromID))
}

// formatMessageAs is FormatMessage with the author label decided by the caller,
// which is how the pane substitutes a real name for the numeric id.
func formatMessageAs(msg domain.Message, width int, replyTo *domain.Message, author string) string {
	if width < minBodyWidth {
		width = minBodyWidth
	}

	var b strings.Builder
	b.WriteString(renderHeader(msg.Date, author, ""))
	b.WriteByte('\n')

	if replyTo != nil {
		b.WriteString(formatReplyHint(*replyTo))
		b.WriteByte('\n')
	}

	if badge := mediaBadge(msg.Media); badge != "" {
		b.WriteString(badge)
		if msg.Text != "" {
			b.WriteByte('\n')
		}
	}

	body := renderInlineMarkdown(msg.Text)
	body = wrapText(body, width-2)
	b.WriteString(body)
	return b.String()
}

// mediaBadge formats a one-line marker like "[📎 report.pdf, 234 KB]" so
// the user can see at a glance that the message has an attachment.
// Returns an empty string when the message has no media; callers can
// safely concat without nil checks.
//
// The byte size is rendered with 1024-based units (KiB / MiB) because
// Telegram itself uses base-2 in the official clients — staying
// consistent matches user mental model. The rendered prefix is grey
// italic to signal "metadata, not body content" while the filename
// stays in normal weight so the eye is drawn to it.
func mediaBadge(m *domain.MediaInfo) string {
	if m == nil {
		return ""
	}
	icon := "📎"
	if m.Kind == domain.MediaKindPhoto {
		icon = "🖼"
	}
	name := m.Filename
	if name == "" {
		name = "(no name)"
	}
	if m.Size > 0 {
		return mediaStyle.Render("["+icon+" "+name+", "+formatBytes(m.Size)+"]") + " " + hintStyle.Render("ctrl+d to save")
	}
	return mediaStyle.Render("["+icon+" "+name+"]") + " " + hintStyle.Render("ctrl+d to save")
}

// formatBytes renders n as a base-2 size string ("234 KiB", "1.4 MiB").
// Tiny inputs stay in plain bytes for surprise-free output.
func formatBytes(n int64) string {
	const (
		kib = 1 << 10
		mib = 1 << 20
		gib = 1 << 30
	)
	switch {
	case n < kib:
		return fmt.Sprintf("%d B", n)
	case n < mib:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(kib))
	case n < gib:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(mib))
	default:
		return fmt.Sprintf("%.2f GiB", float64(n)/float64(gib))
	}
}

// mediaStyle and hintStyle paint the attachment badge so it sits
// visually separate from the message body but does not steal focus
// from text.
var (
	mediaStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	hintStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
)

// renderHeader is the one place a message header is composed, shared by stored
// messages and by the optimistic rows of messages still in flight. Before this
// was shared, an outgoing message rendered as bare text: no time, no author,
// visually unlike every line around it, until the server echo replaced it —
// which can take the whole session if live updates never deliver the echo.
//
// suffix carries an optional state glyph (⏳ / ✗) so an in-flight row says so
// in its header instead of prefixing the body and shifting the text column.
//
// Local() is load-bearing: every layer below the UI keeps time in UTC on
// purpose (storage, tg mapping, search), and formatting that instant as-is
// prints UTC to a user sitting in another zone. Found on the first live smoke —
// a message sent at 19:32 MSK rendered as [16:32].
func renderHeader(ts time.Time, author, suffix string) string {
	head := timeStyle.Render(fmt.Sprintf("[%s]", ts.Local().Format("15:04"))) + " " + nameStyle.Render(author)
	if suffix != "" {
		head += " " + suffix
	}
	return head
}

// authorLabel maps a from_id to a display string with no directory to consult.
// Zero means "service message / system" (Telegram uses this for join/leave
// notifications); anything else falls back to the numeric id.
func authorLabel(fromID int64) string {
	if fromID == 0 {
		return "system"
	}
	return fmt.Sprintf("user-%d", fromID)
}

// resolveAuthor names the sender of msg as well as the pane can.
//
// A raw "user-8385473863" is what the thread used to print for every line,
// including the reader's own messages — unreadable, and the id is not something
// a human can map back to a person. Three sources, in order:
//
//  1. names — titles from the chat list, which covers the other party of a
//     private chat and any group/channel that has its own dialog row.
//  2. the private-chat rule: in a 1:1 dialog every message is either from the
//     peer (from_id == chat id) or from the account itself, so a sender that is
//     not the peer is the reader. No RPC and no self-id plumbing needed.
//  3. the numeric fallback, for group members with no dialog of their own —
//     a proper member directory needs peer names in storage, which v0.1 does
//     not keep.
func resolveAuthor(msg domain.Message, chatID int64, private bool, names map[int64]string) string {
	if msg.FromID == 0 {
		return "system"
	}
	if name, ok := names[msg.FromID]; ok && name != "" {
		return name
	}
	if private && msg.FromID != chatID {
		return selfAuthorLabel
	}
	return authorLabel(msg.FromID)
}

// formatReplyHint truncates the parent message preview to replyPreviewMax
// runes so a long quoted block does not blow up the visible row count
// for a single reply.
func formatReplyHint(replyTo domain.Message) string {
	preview := truncRunes(replyTo.Text, replyPreviewMax)
	return replyStyle.Render("↳ replying to: " + preview)
}

// renderInlineMarkdown applies a tiny subset of Markdown: ` `code` `,
// `**bold**`, `*italic*`. Order matters: backticks first so the inner
// text is not re-styled, then double-star (greedy `**`) before single-
// star — and the single-star pass is `**`-aware so an unmatched
// `**bold` does not get mis-decoded as two adjacent italics.
//
// The full entity-aware pass (Telegram bold/italic/strike/code/link
// entities) lands in Stage 3; this helper exists so v0.1 plaintext
// with a few markdown markers still looks like the user expects.
func renderInlineMarkdown(s string) string {
	s = applyDelim(s, "`", func(in string) string { return codeStyle.Render(in) })
	s = applyDelim(s, "**", func(in string) string { return boldStyle.Render(in) })
	s = applyStarItalic(s)
	return s
}

// applyDelim replaces every delim..delim pair in s with style(content).
// Unmatched openings are preserved verbatim so a stray opening does
// not eat the rest of the message.
func applyDelim(s, delim string, style func(string) string) string {
	var out strings.Builder
	for {
		i := strings.Index(s, delim)
		if i < 0 {
			out.WriteString(s)
			return out.String()
		}
		rest := s[i+len(delim):]
		j := strings.Index(rest, delim)
		if j < 0 {
			out.WriteString(s)
			return out.String()
		}
		out.WriteString(s[:i])
		out.WriteString(style(rest[:j]))
		s = rest[j+len(delim):]
	}
}

// applyStarItalic finds single-star italic spans while leaving every
// `**` token alone. The bold pass runs first; if a `**` opening had no
// matching closing the bytes survive into here unchanged, and a naive
// `*…*` scan would mis-pair them. Walking byte-by-byte and explicitly
// skipping `**` pairs keeps `**bold` (no close) literal in the output.
func applyStarItalic(s string) string {
	var out strings.Builder
	i := 0
	for i < len(s) {
		if s[i] != '*' {
			out.WriteByte(s[i])
			i++
			continue
		}
		// `**` token — preserve verbatim, do not treat as two italics.
		if i+1 < len(s) && s[i+1] == '*' {
			out.WriteString("**")
			i += 2
			continue
		}
		// Look for the matching closing single-star, also skipping `**`.
		end := -1
		j := i + 1
		for j < len(s) {
			if s[j] == '*' {
				if j+1 < len(s) && s[j+1] == '*' {
					j += 2
					continue
				}
				end = j
				break
			}
			j++
		}
		if end < 0 {
			// Unmatched opening — preserve the rest verbatim.
			out.WriteString(s[i:])
			return out.String()
		}
		out.WriteString(italicStyle.Render(s[i+1 : end]))
		i = end + 1
	}
	return out.String()
}

// wrapText word-wraps s to width via lipgloss. lipgloss's Width strategy
// preserves ANSI escapes so styled spans survive the wrap untouched.
func wrapText(s string, width int) string {
	if width <= 0 {
		return s
	}
	return lipgloss.NewStyle().Width(width).Render(s)
}

// truncRunes shortens s to at most n runes, appending "…" when it
// dropped any tail. Counted in runes (not bytes) so Cyrillic / CJK
// content is never sliced mid-codepoint.
func truncRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}
