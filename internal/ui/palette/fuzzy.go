package palette

import "github.com/sahilm/fuzzy"

// Match returns the indices into items whose normalized title
// fuzzy-matches the (non-empty) query, ordered by the sahilm/fuzzy
// scorer (best match first). Empty or whitespace-only queries are a
// caller responsibility — Match returns the original 0..N-1 index
// slice in that case so the View pass keeps showing the candidate
// list ordered by frecency.
//
// The query itself is normalized through the same pipeline as the
// titles so a "Алена" query matches an "Алёна" title (the headline
// invariant of the palette — see normalize.go).
func Match(query string, items []Item) []int {
	if query == "" {
		out := make([]int, len(items))
		for i := range items {
			out[i] = i
		}
		return out
	}
	normalized := Normalize(query)
	if normalized == "" {
		// Pure-whitespace query → identical fallback to empty.
		out := make([]int, len(items))
		for i := range items {
			out[i] = i
		}
		return out
	}
	source := titleSource(items)
	matches := fuzzy.FindFrom(normalized, source)
	out := make([]int, len(matches))
	for i, m := range matches {
		out[i] = m.Index
	}
	return out
}

// titleSource adapts a []Item into the fuzzy.Source contract
// (Len/String) so the fuzzy library can iterate without us
// allocating an intermediate []string. The String method must
// return the *normalized* title — the query is normalized too, so
// the tokenizer (sahilm scans byte-by-byte against the source) sees
// like-vs-like.
type titleSource []Item

func (s titleSource) Len() int            { return len(s) }
func (s titleSource) String(i int) string { return s[i].NormalizedTitle }
