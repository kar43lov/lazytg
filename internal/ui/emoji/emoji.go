// Package emoji is the emoji table behind the picker and the composer's
// `:shortcode` completion.
//
// Two ways in, because they answer different questions. Someone who knows
// what they want types `:rocket` and presses Tab; someone who wants to look
// opens the picker and browses. Both read the same table, so a name that
// works in one works in the other.
//
// The table is generated from the Unicode data (see scripts/gen_emoji.py)
// rather than written by hand. A hand-written list is wrong in a way nobody
// can audit: the names drift from the official ones and the coverage stops
// wherever the author lost interest. The generated names are the formal
// Unicode ones, which are accurate but occasionally stiff — "black
// right-pointing double triangle" is nobody's idea of fast-forward — so a
// short hand-written alias table sits on top for the shortcodes people
// actually type.
package emoji

import (
	"sort"
	"strings"
)

// Entry is one emoji: the character, its official Unicode name, the
// shortcode derived from that name, and the group the picker files it under.
type Entry struct {
	Char     string
	Name     string
	Code     string
	Category string
}

// aliases are the shortcodes people type, mapped to the character.
//
// These exist because the Unicode name and the word in common use often
// disagree — nobody types `:face_with_tears_of_joy:`, they type `:joy:` —
// and because every other client in the world speaks the GitHub/Slack
// vocabulary. Hand-written on purpose: this is the one part of the table
// where a person's judgement beats the data.
var aliases = map[string]string{
	"+1": "👍", "thumbsup": "👍", "like": "👍",
	"-1": "👎", "thumbsdown": "👎",
	"joy": "😂", "lol": "😂", "rofl": "🤣",
	"smile": "😄", "grin": "😁", "wink": "😉", "blush": "😊",
	"heart": "❤", "love": "😍", "kiss": "😘",
	"sad": "😢", "cry": "😢", "sob": "😭", "angry": "😠", "rage": "😡",
	"think": "🤔", "thinking": "🤔", "shrug": "🤷", "facepalm": "🤦",
	"fire": "🔥", "100": "💯", "tada": "🎉", "party": "🎉",
	"rocket": "🚀", "ship": "🚀", "eyes": "👀", "pray": "🙏", "thanks": "🙏",
	"clap": "👏", "wave": "👋", "hi": "👋", "ok": "👌", "muscle": "💪",
	"check": "✅", "x": "❌", "warning": "⚠", "bug": "🐛", "star": "⭐",
	"sparkles": "✨", "boom": "💥", "zap": "⚡", "bulb": "💡",
	"coffee": "☕", "beer": "🍺", "cake": "🎂", "pizza": "🍕",
	"cool": "😎", "sleep": "😴", "sick": "🤒", "vomit": "🤮",
	"poop": "💩", "ghost": "👻", "alien": "👽", "robot": "🤖",
	"cat": "🐱", "dog": "🐶", "monkey": "🐵", "unicorn": "🦄",
	"snake": "🐍", "gopher": "🐹", "penguin": "🐧",
	"clock": "⏰", "hourglass": "⌛", "lock": "🔒", "key": "🔑",
	"mag": "🔍", "search": "🔍", "wrench": "🔧", "hammer": "🔨",
	"computer": "💻", "phone": "📱", "mail": "📧", "memo": "📝",
	"chart": "📈", "money": "💰", "gift": "🎁", "trophy": "🏆",
	"cross": "❌", "done": "✅", "no": "🚫", "stop": "🛑",
	"up": "⬆", "down": "⬇", "left": "⬅", "right": "➡",
	"green": "🟢", "red": "🔴", "yellow": "🟡", "blue": "🔵",
}

// All returns every entry, in table order.
func All() []Entry {
	out := make([]Entry, len(entries))
	copy(out, entries)
	return out
}

// categoryOrder is the order the picker shows the groups in.
//
// Fixed here rather than taken from the order they appear in the table: the
// table is in codepoint order, which starts at U+231A ⌚ and would open the
// picker on "Objects". People come to an emoji picker for faces.
var categoryOrder = []string{
	"Smileys", "People", "Nature", "Food", "Activity", "Travel", "Objects", "Symbols",
}

// Categories lists the groups in the order the picker shows them, skipping
// any the table turned out not to fill.
func Categories() []string {
	var out []string
	for _, name := range categoryOrder {
		for _, e := range entries {
			if e.Category == name {
				out = append(out, name)
				break
			}
		}
	}
	return out
}

// InCategory returns the entries of one group.
func InCategory(name string) []Entry {
	var out []Entry
	for _, e := range entries {
		if e.Category == name {
			out = append(out, e)
		}
	}
	return out
}

// Search returns the entries matching a query, best first.
//
// The ranking is the whole point of the function. Typing "hear" must offer
// ❤ before "hear no evil monkey", and typing "fire" must offer 🔥 before
// "fire engine" — otherwise the completion is a lottery and people stop
// using it. So: an alias somebody deliberately wrote wins outright, then a
// shortcode that starts with the query, then a name whose *first* word does,
// then any word, then a bare substring. Ties keep table order, which is
// codepoint order, and that puts the older and more common characters first
// for free.
func Search(query string) []Entry {
	q := strings.ToLower(strings.TrimSpace(query))
	q = strings.Trim(q, ":")
	if q == "" {
		return All()
	}

	type scored struct {
		e    Entry
		rank int
		idx  int
	}
	var hits []scored
	aliasChar := aliases[q]
	aliasFound := false
	for i, e := range entries {
		rank := -1
		switch {
		case aliasChar != "" && e.Char == aliasChar:
			rank = 0
			aliasFound = true
		case e.Code == q:
			rank = 1
		case strings.HasPrefix(e.Code, q):
			rank = 2
		case strings.HasPrefix(firstWord(e.Name), q):
			rank = 3
		case hasWordPrefix(e.Name, q):
			rank = 4
		case strings.Contains(e.Name, q):
			rank = 5
		}
		if rank >= 0 {
			hits = append(hits, scored{e: e, rank: rank, idx: i})
		}
	}
	// An alias may point at a character the generated table does not carry
	// (a newer codepoint than the Unicode data this was built from). It is
	// still the answer the user asked for, so it goes in rather than being
	// dropped for bookkeeping reasons.
	if aliasChar != "" && !aliasFound {
		hits = append(hits, scored{
			e:    Entry{Char: aliasChar, Name: q, Code: q, Category: "Symbols"},
			rank: 0,
			idx:  -1,
		})
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].rank != hits[j].rank {
			return hits[i].rank < hits[j].rank
		}
		return hits[i].idx < hits[j].idx
	})
	out := make([]Entry, len(hits))
	for i, h := range hits {
		out[i] = h.e
	}
	return out
}

func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i]
	}
	return s
}

func hasWordPrefix(name, q string) bool {
	for _, w := range strings.FieldsFunc(name, func(r rune) bool {
		return r == ' ' || r == '-'
	}) {
		if strings.HasPrefix(w, q) {
			return true
		}
	}
	return false
}
