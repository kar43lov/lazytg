package input

import (
	"strings"
	"unicode"

	"github.com/kar43lov/lazytg/internal/ui/emoji"
)

// Emoji, the way a keyboard-first client should offer them.
//
// Typing `:rocket` and pressing Tab is the fast path, and it is deliberately
// readline-shaped rather than a popup with its own key handling: no mode to
// enter, no mode to leave, and nothing to dismiss if the user changes their
// mind mid-word. The completion is the same gesture people already use for
// filenames in a shell.
//
// Repeated presses cycle the candidates, replacing the character inserted by
// the previous press. That is what makes the ranking bearable — the first
// answer for "fire" is 🔥 and the second is 🎆, so being wrong costs one more
// keystroke rather than sending the wrong picture.

// emojiCompletion is the state of a cycle in progress: what was typed, what
// matched it, and which candidate is currently sitting in the composer.
type emojiCompletion struct {
	query string
	hits  []emoji.Entry
	idx   int
}

// emojiPrefix returns the `:word` the cursor is sitting behind, without the
// colon, or "" when there is none.
//
// The colon must open a word — `10:30` is a time, not a shortcode, and a
// client that eats it the moment somebody presses Tab is worse than one with
// no completion at all.
func emojiPrefix(line string, col int) string {
	runes := []rune(line)
	if col > len(runes) {
		col = len(runes)
	}
	head := runes[:col]

	i := len(head) - 1
	for i >= 0 && isShortcodeRune(head[i]) {
		i--
	}
	if i < 0 || head[i] != ':' {
		return ""
	}
	if i > 0 && !unicode.IsSpace(head[i-1]) {
		return ""
	}
	word := string(head[i+1:])
	if word == "" {
		return ""
	}
	return word
}

func isShortcodeRune(r rune) bool {
	return r == '_' || r == '+' || r == '-' ||
		(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// EmojiPrefix reports the shortcode being typed, for the app to decide
// whether Tab belongs to the composer or to the focus cycler.
func (m Model) EmojiPrefix() string {
	if m.emoji != nil {
		return m.emoji.query
	}
	line, col := m.cursorLine()
	return emojiPrefix(line, col)
}

// completeEmoji replaces the shortcode under the cursor with an emoji, or
// advances to the next candidate when the last press already put one there.
// Reports whether anything happened.
func (m *Model) completeEmoji() bool {
	line, col := m.cursorLine()

	if m.emoji != nil && len(m.emoji.hits) > 1 {
		current := m.emoji.hits[m.emoji.idx].Char
		if strings.HasSuffix(string([]rune(line)[:col]), current) {
			m.emoji.idx = (m.emoji.idx + 1) % len(m.emoji.hits)
			m.replaceBeforeCursor(len([]rune(current)), m.emoji.hits[m.emoji.idx].Char)
			return true
		}
	}

	query := emojiPrefix(line, col)
	if query == "" {
		m.emoji = nil
		return false
	}
	hits := emoji.Search(query)
	if len(hits) == 0 {
		m.emoji = nil
		return false
	}
	if len(hits) > emojiCandidates {
		hits = hits[:emojiCandidates]
	}
	m.emoji = &emojiCompletion{query: query, hits: hits}
	m.replaceBeforeCursor(len(query)+1, hits[0].Char)
	return true
}

// emojiCandidates caps how many matches a cycle walks through. Past a dozen
// the user is faster opening the picker, and an unbounded cycle over a
// thousand entries is a way to lose the draft.
const emojiCandidates = 12

// clearEmojiCompletion ends a cycle. Called on any key that is not another
// completion press: the candidate in the box is now part of the message.
func (m *Model) clearEmojiCompletion() { m.emoji = nil }

// cursorLine returns the line the cursor is on and its column, in runes.
func (m Model) cursorLine() (string, int) {
	lines := strings.Split(m.textarea.Value(), "\n")
	row := m.textarea.Line()
	if row < 0 || row >= len(lines) {
		return "", 0
	}
	return lines[row], m.textarea.Column()
}

// replaceBeforeCursor swaps the n runes before the cursor for s, leaving the
// cursor after the replacement.
//
// It goes through SetValue rather than through the textarea's own editing
// commands because there is no API for "delete the previous n runes" that
// does not also fire the key bindings; and SetValue drops the cursor at the
// end of the text, so the position has to be walked back by hand.
func (m *Model) replaceBeforeCursor(n int, s string) {
	lines := strings.Split(m.textarea.Value(), "\n")
	row := m.textarea.Line()
	if row < 0 || row >= len(lines) {
		return
	}
	runes := []rune(lines[row])
	col := m.textarea.Column()
	if col > len(runes) {
		col = len(runes)
	}
	if n > col {
		n = col
	}
	head := string(runes[:col-n])
	tail := string(runes[col:])
	lines[row] = head + s + tail
	newCol := len([]rune(head)) + len([]rune(s))

	m.textarea.SetValue(strings.Join(lines, "\n"))
	m.moveCursorTo(row, newCol, len(lines))
}

// moveCursorTo walks the cursor from the end of the text (where SetValue
// leaves it) back to row/col.
func (m *Model) moveCursorTo(row, col, total int) {
	m.textarea.MoveToEnd()
	for i := total - 1; i > row; i-- {
		m.textarea.CursorUp()
	}
	m.textarea.SetCursorColumn(col)
}

// EmojiHint is the line shown under the composer while a completion is
// cycling: what was typed and what pressing Tab again would offer.
func (m Model) EmojiHint() string {
	if m.emoji == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(":" + m.emoji.query + " → ")
	for i, e := range m.emoji.hits {
		if i > 0 {
			b.WriteString(" ")
		}
		if i == m.emoji.idx {
			b.WriteString("[" + e.Char + "]")
			continue
		}
		b.WriteString(e.Char)
	}
	b.WriteString("   tab for the next one")
	return b.String()
}
