package palette

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Normalize folds s for case- and diacritic-insensitive fuzzy
// matching. The pipeline is:
//
//  1. NFKD: decompose composed characters so combining marks become
//     standalone runes (e.g. "Алёна" → "Але" + U+0308 + "на",
//     "Café" → "Cafe" + U+0301).
//  2. Drop combining marks (Unicode category Mn) so the diacritic
//     vanishes and the base letter remains. After NFKD, the diacritic
//     is always a separate Mn rune so this is a single pass.
//  3. ToLower so the trigram tokenizer behaviour is mirrored at the
//     fuzzy layer ("Алёна" and "алёна" rank identically).
//
// Critically, this lets the user type "Алена" (post-Soviet Russian
// writing convention) and find the chat titled "Алёна" — the FTS
// search service relies on the same property at the SQLite level via
// case_sensitive=0; the palette enforces it at the Go level so the
// fuzzy ranker (which has no knowledge of the FTS tokenizer) behaves
// the same way.
//
// Emoji and other non-letter runes pass through unchanged because
// they are not Mn-category and Unicode lowercase is a no-op for
// them.
func Normalize(s string) string {
	if s == "" {
		return s
	}
	decomposed := norm.NFKD.String(s)
	var b strings.Builder
	b.Grow(len(decomposed))
	for _, r := range decomposed {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}
