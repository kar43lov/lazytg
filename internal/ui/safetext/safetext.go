// Package safetext strips the characters that let text sent by somebody
// else drive the terminal rather than merely appear in it.
//
// Every string lazytg renders from Telegram — a message body, a chat
// title, an author name, the filename attached to a document — is chosen
// by whoever sent it, and lazytg draws it straight into a terminal that
// reads escape sequences as commands. That is not a theoretical concern
// here: lazytg copies to the clipboard with OSC 52, which proves the
// terminal it runs in honours OSC, so a filename containing an OSC 52
// sequence would rewrite the user's clipboard the moment the message
// scrolled into view. The same class covers OSC 8 (a hyperlink whose
// visible text and target disagree), CSI (repaint the pane, hide a
// line, move the cursor) and the bidi overrides, which reverse the
// display order of what follows them: a filename ending in a U+202E and
// then "dnammoc.gnp" is drawn as "photo.png" and opened as a .command.
// (Spelled out rather than shown, because a literal override in this
// file would be the same trick played on whoever reads the source.)
//
// The filter runs at render time rather than on the way into storage,
// because storage already holds rows written before it existed, and
// because the search index should keep matching the text the sender
// actually wrote.
package safetext

import "strings"

// Clean returns s with every control character removed except the tab
// and the newline, which the thread pane lays out itself.
//
// Removal, not replacement: a sequence broken by a substituted
// placeholder still leaves its payload on screen as text, which is
// noise, while a filename with the escape taken out reads as the name
// it claims to be.
func Clean(s string) string { return clean(s, true) }

// CleanLine is Clean for a context that must stay on one row — a chat
// title, an author label, a list preview. Newlines and tabs become
// single spaces rather than disappearing, so words do not run together.
func CleanLine(s string) string {
	return strings.Join(strings.Fields(clean(s, false)), " ")
}

func clean(s string, keepBreaks bool) string {
	if !needsClean(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			if keepBreaks {
				b.WriteRune(r)
			} else {
				b.WriteByte(' ')
			}
		case unsafeRune(r):
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// needsClean is the fast path: the overwhelming majority of messages
// carry nothing to strip, and this runs on every rendered row.
func needsClean(s string) bool {
	for _, r := range s {
		if r == '\n' || r == '\t' {
			continue
		}
		if unsafeRune(r) {
			return true
		}
	}
	return false
}

// unsafeRune reports whether r can steer the terminal or the reader.
//
// The zero-width joiner and non-joiner are deliberately absent: they
// carry meaning inside emoji sequences and in several scripts, and
// dropping them would corrupt ordinary text to defend against nothing —
// they cannot move the cursor or reverse a filename.
func unsafeRune(r rune) bool {
	switch {
	case r < 0x20, r == 0x7f: // C0 and DEL — ESC, CSI introducers, BEL
		return true
	case r >= 0x80 && r <= 0x9f: // C1, including the 8-bit CSI and OSC
		return true
	case r == 0x200e || r == 0x200f: // LRM / RLM
		return true
	case r >= 0x202a && r <= 0x202e: // bidi embedding and override
		return true
	case r >= 0x2066 && r <= 0x2069: // bidi isolates
		return true
	case r == 0x2028 || r == 0x2029: // line / paragraph separator
		return true
	}
	return false
}
