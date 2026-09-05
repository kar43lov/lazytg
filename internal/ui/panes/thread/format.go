package thread

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/ui/safetext"
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

// dayStyle paints the date separator: grey, like the other metadata in
// the thread, so it separates without competing with the conversation.
var dayStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

// renderDaySeparator draws the "─── August 19 ───" rule that marks where
// one day's messages end and the next day's begin.
//
// The thread prints times and only times, which is right inside a day
// and actively misleading across days: a live run showed 16:35 followed
// by 15:10 and read as a broken sort, when in fact a fortnight had
// passed between them. Every chat client draws this line for the same
// reason, and the cost of not drawing it is that the user distrusts the
// ordering.
func renderDaySeparator(day time.Time, now time.Time, width int) string {
	label := " " + dayLabel(day, now) + " "
	if width < minBodyWidth {
		width = minBodyWidth
	}
	rule := width - lipgloss.Width(label)
	if rule < 2 {
		return dayStyle.Render(label)
	}
	left := rule / 2
	return dayStyle.Render(strings.Repeat("─", left) + label + strings.Repeat("─", rule-left))
}

// dayLabel names a day the way a reader thinks of it: the two most
// recent days by name, everything else by date, and the year only when
// it is not the current one.
func dayLabel(day, now time.Time) string {
	d := day.Local()
	n := now.Local()
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, n.Location())
	switch {
	case !d.Before(today):
		return "Today"
	case !d.Before(today.AddDate(0, 0, -1)):
		return "Yesterday"
	case d.Year() == n.Year():
		return d.Format("January 2")
	default:
		return d.Format("January 2, 2006")
	}
}

// sameDay reports whether two instants fall on the same local calendar
// day. Local, because the separator answers "what day was this for me",
// and the timestamps beside it are printed in local time too.
func sameDay(a, b time.Time) bool {
	al, bl := a.Local(), b.Local()
	ay, am, ad := al.Date()
	by, bm, bd := bl.Date()
	return ay == by && am == bm && ad == bd
}

// cursorMark and cursorStyle draw the per-message cursor. A glyph rather
// than a highlighted row: the thread already uses reverse video for the
// text selection, and two different things wearing the same paint is how
// a user stops trusting either. Cyan (ANSI 6) survives an 8-colour
// terminal and does not collide with the grey used for metadata.
const cursorMark = "▸"

var cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)

// markMark and markStyle draw a message the user has picked out for an
// action that takes several. It sits in the same column as the cursor
// glyph and replaces it while both apply, because they answer different
// questions — "where am I" and "what have I chosen" — and the cursor is
// recoverable at a glance from the arrow keys while the marks are not.
// Yellow (ANSI 3) rather than cyan so the two are distinguishable on a
// monochrome-ish theme by shape as well as colour.
const markMark = "✓"

var markStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)

// reactionStyle is the ordinary reaction; chosenReactionStyle is the one this
// account sent, which the user has to be able to pick out at a glance because
// it decides what the react key does next.
// waveformStyle paints the voice shape. Cyan rather than the metadata grey:
// it is the only part of the badge line that carries the message itself.
var waveformStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))

var (
	reactionStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	chosenReactionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("4")).Bold(true)
)

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
	block, _ := formatMessageBlock(msg, width, replyTo, author, false)
	return block
}

// formatMessageBlock renders one message and reports which of its lines
// carries the media badge, counting from the top of the block, or -1 when
// there is none.
//
// The line number is returned rather than re-derived by the caller because
// only this function knows the layout: whether a reply hint took a line above
// the badge, whether the badge is there at all. A caller that counted lines
// itself would be a second copy of the layout, and the drift would show up as
// a click opening the wrong thing — or nothing.
//
// cursor draws the marker that says "this is the message the keyboard is
// pointing at". It sits in front of the header rather than indenting the whole
// block: the body stays where it was, so nothing about wrapping, selection
// columns or the golden snapshots moves when the cursor passes over a message.
func formatMessageBlock(msg domain.Message, width int, replyTo *domain.Message, author string, cursor bool) (string, int) {
	return formatMessageBlockMarked(msg, width, replyTo, author, cursor, false)
}

// formatMessageBlockMarked is formatMessageBlock with the mark state the
// multi-select needs. The two are separate so every existing caller — and
// every existing golden test — keeps its signature: a mark is an addition
// to the row, not a change to how a row is built.
func formatMessageBlockMarked(msg domain.Message, width int, replyTo *domain.Message, author string, cursor, marked bool) (string, int) {
	if width < minBodyWidth {
		width = minBodyWidth
	}

	var b strings.Builder
	switch {
	case marked:
		b.WriteString(markStyle.Render(markMark) + " ")
	case cursor:
		b.WriteString(cursorStyle.Render(cursorMark) + " ")
	}
	b.WriteString(renderHeader(msg.Date, author, editedSuffix(msg)))
	b.WriteByte('\n')
	line := 1

	if replyTo != nil {
		b.WriteString(formatReplyHint(*replyTo))
		b.WriteByte('\n')
		line++
	}

	mediaLine := -1
	if badge := mediaBadge(msg.Media); badge != "" {
		mediaLine = line
		b.WriteString(badge)
		if msg.Text != "" {
			b.WriteByte('\n')
		}
	}

	body := renderEntities(msg.Text, msg.Entities, cursor)
	body = wrapText(body, width-2)
	b.WriteString(body)

	if row := reactionRow(msg.Reactions); row != "" {
		b.WriteByte('\n')
		b.WriteString(row)
	}
	return b.String(), mediaLine
}

// reactionRow renders the reactions under a message, or "" when there are
// none — which is the case for almost every message, so it costs a line only
// where there is something to show.
//
// The one this account sent is boxed. That is the bit the user needs before
// pressing the key: reacting again with the same emoji takes it back, and
// without a mark on screen the gesture is a coin flip.
//
// A count of one is left off. "👍" already says one person did it, and a
// column of "👍 1  ❤ 1" is noise where "👍 ❤" is a sentence.
func reactionRow(rs []domain.Reaction) string {
	if len(rs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(rs))
	hidden := 0
	for _, r := range rs {
		if r.Emoticon == "" {
			continue
		}
		if len(parts) == maxShownReactions {
			hidden = len(rs) - maxShownReactions
			break
		}
		// Both caps are about a string somebody else chose. The count is
		// capped because a message can carry more reactions than a line
		// can hold, and the emoticon is truncated because the field is a
		// string on the wire, not a character — nothing stops a server
		// putting a paragraph in it. Control characters are already gone
		// by then; length is the part CleanLine does not answer.
		label := truncRunes(safetext.CleanLine(r.Emoticon), maxReactionRunes)
		if r.Count > 1 {
			label += " " + strconv.Itoa(r.Count)
		}
		if r.Chosen {
			parts = append(parts, chosenReactionStyle.Render("["+label+"]"))
			continue
		}
		parts = append(parts, reactionStyle.Render(" "+label+" "))
	}
	if len(parts) == 0 {
		return ""
	}
	if hidden > 0 {
		parts = append(parts, reactionStyle.Render(fmt.Sprintf(" +%d ", hidden)))
	}
	return strings.Join(parts, " ")
}

// maxShownReactions caps how many reactions a message shows before the rest
// become a count, and maxReactionRunes how long one may be.
const (
	maxShownReactions = 8
	maxReactionRunes  = 4
)

// mediaBadge formats a one-line marker like "[📎 report.pdf, 234 KiB]" so
// the user can see at a glance that the message has an attachment.
// Returns an empty string when the message has no media; callers can
// safely concat without nil checks.
//
// The byte size is rendered with 1024-based units (KiB / MiB) because
// Telegram itself uses base-2 in the official clients — staying
// consistent matches user mental model. The rendered prefix is grey
// italic to signal "metadata, not body content" while the filename
// stays in normal weight so the eye is drawn to it.
//
// What the badge names depends on the kind. A document or a photo has a
// filename the sender chose, and that is the useful thing to show. A
// voice message, a video note or a sticker does not: Telegram sends no
// filename for those, so the name would be one lazytg invented
// ("voice_5123.ogg"), which says less than the word "voice" and buries
// the duration behind it. Those show the kind and how long they run.
func mediaBadge(m *domain.MediaInfo) string {
	if m == nil {
		return ""
	}
	if fileless(m.Kind) {
		return filelessBadge(m)
	}
	var parts []string
	parts = append(parts, mediaLabel(m))
	if d := formatDuration(m.Duration); d != "" {
		parts = append(parts, d)
	}
	if m.Size > 0 {
		parts = append(parts, formatBytes(m.Size))
	}
	badge := mediaStyle.Render("[" + mediaIcon(m.Kind) + " " + strings.Join(parts, ", ") + "]")
	// The waveform goes outside the brackets, between the badge and the
	// hint: it is the one part of the line that is the message rather than
	// a description of it.
	if wave := renderWaveform(m.Waveform); wave != "" {
		badge += " " + waveformStyle.Render(wave)
	}
	return badge + " " + hintStyle.Render("o to open, ctrl+d to save")
}

// mediaIcon picks the glyph that opens the badge. One per kind, because
// the icon is what the eye catches when scrolling a thread — a wall of
// identical paperclips is the state this replaced.
// fileless reports the kinds that carry no file: nothing to download,
// nothing to open but a map.
func fileless(kind domain.MediaKind) bool {
	switch kind {
	case domain.MediaKindLocation, domain.MediaKindContact, domain.MediaKindPoll, domain.MediaKindDice:
		return true
	}
	return false
}

// filelessBadge draws an attachment that is not a file: a place, a card, a
// poll, a dice. No size, no download hint; a map for the place.
func filelessBadge(m *domain.MediaInfo) string {
	label := safetext.CleanLine(m.Filename)
	switch m.Kind {
	case domain.MediaKindLocation:
		if venue := safetext.CleanLine(m.MimeType); venue != "" {
			label = venue + ", " + label
		}
		return mediaStyle.Render("[📍 "+label+"]") + " " + hintStyle.Render("o to open the map")
	case domain.MediaKindContact:
		return mediaStyle.Render("[👤 " + label + "]")
	case domain.MediaKindPoll:
		return mediaStyle.Render("[📊 poll]")
	case domain.MediaKindDice:
		return mediaStyle.Render("[🎲 " + label + "]")
	default:
		return mediaStyle.Render("[" + mediaIcon(m.Kind) + " " + label + "]")
	}
}

func mediaIcon(kind domain.MediaKind) string {
	switch kind {
	case domain.MediaKindPhoto:
		return "🖼"
	case domain.MediaKindVideo:
		return "🎬"
	case domain.MediaKindVideoNote:
		return "⏺"
	case domain.MediaKindVoice:
		return "🎤"
	case domain.MediaKindAudio:
		return "🎵"
	case domain.MediaKindSticker:
		return "🙂"
	case domain.MediaKindAnimation:
		return "🎞"
	default:
		return "📎"
	}
}

// mediaLabel returns what the badge should call this attachment: the
// sender's filename where one exists, and the kind where the filename
// would be one lazytg made up.
func mediaLabel(m *domain.MediaInfo) string {
	switch m.Kind {
	case domain.MediaKindVideoNote:
		return "video note"
	case domain.MediaKindVoice:
		return "voice"
	case domain.MediaKindSticker:
		return "sticker"
	case domain.MediaKindAnimation:
		return "animation"
	}
	// The filename is chosen by the sender and drawn into a terminal, so
	// it is cleaned on the way to the screen — see internal/ui/safetext.
	name := safetext.CleanLine(m.Filename)
	if name == "" {
		return "(no name)"
	}
	return name
}

// formatDuration renders whole seconds as m:ss, or h:mm:ss once it runs
// past an hour. Zero returns an empty string so the caller can leave the
// field out entirely rather than print "0:00" against a PDF.
func formatDuration(seconds int) string {
	if seconds <= 0 {
		return ""
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
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
	head := timeStyle.Render(fmt.Sprintf("[%s]", ts.Local().Format("15:04"))) + " " + nameStyle.Render(safetext.CleanLine(author))
	if suffix != "" {
		head += " " + suffix
	}
	return head
}

// editedSuffix is the word every client puts on a message that changed
// after it was read. Dim, because it is a footnote; present, because a
// sentence that says something different from what it said an hour ago
// should admit it.
func editedSuffix(msg domain.Message) string {
	if msg.EditDate.IsZero() {
		return ""
	}
	return timeStyle.Render("edited")
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
// a human can map back to a person. Four sources, in order:
//
//  1. the stored direction: a message the account sent is the reader's, and in
//     a 1:1 dialog that is the only thing distinguishing it — Telegram sends no
//     from_id there at all.
//  2. names — titles from the chat list, which covers the other party of a
//     private chat and any group/channel that has its own dialog row.
//  3. the private-chat rule: in a 1:1 dialog every message is either from the
//     peer (from_id == chat id) or from the account itself, so a sender that is
//     not the peer is the reader. It still earns its place: rows written before
//     migration 0010 have no direction recorded.
//  4. the numeric fallback, for group members with no dialog of their own —
//     a proper member directory needs peer names in storage, which v0.1 does
//     not keep.
func resolveAuthor(msg domain.Message, chatID int64, private bool, names map[int64]string) string {
	if msg.Outgoing {
		return selfAuthorLabel
	}
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
	preview := truncRunes(safetext.CleanLine(replyTo.Text), replyPreviewMax)
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
