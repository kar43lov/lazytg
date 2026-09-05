// Package markdown turns the markup people type into the spans Telegram
// carries, and back.
//
// The dialect is the one Telegram Desktop applies on send, because that is
// what this program's users already have in their fingers: **bold**,
// __italic__, ~~strike~~, ||spoiler||, `code`, ```pre``` with an optional
// language after the opening fence, and [text](url). Nothing else — no
// headings, no lists, no escapes — for the same reason: the official client
// has none, and a message that formats differently here than there is a
// message that surprises somebody on the other end.
//
// Parse produces plain text plus rune-indexed entities; Render is its
// inverse, used to put a formatted message back into the composer for an
// edit. Offsets count runes, never bytes and never UTF-16 units — the wire
// conversion is the edge's job.
package markdown

import (
	"strings"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// marker is one paired delimiter and the kind it produces.
type marker struct {
	token string
	kind  domain.EntityKind
}

// markers in the order they are tried at a position. Longer tokens first
// where one is a prefix of another; there are none today, but the order
// is the contract.
var markers = []marker{
	{"**", domain.EntityBold},
	{"__", domain.EntityItalic},
	{"~~", domain.EntityStrike},
	{"||", domain.EntitySpoiler},
}

// Parse strips the markup out of src and reports what it meant. Text that
// looks like markup but is not — a lone `**`, a backtick with no partner,
// a bracket without a parenthesis — is left exactly as typed, because
// eating characters somebody wrote is worse than leaving a marker visible.
func Parse(src string) (string, []domain.Entity) {
	if src == "" || !strings.ContainsAny(src, "*_~|`[") {
		return src, nil
	}
	p := parser{in: []rune(src)}
	p.parse()
	if len(p.out) == 0 {
		return src, nil
	}
	if len(p.entities) == 0 {
		return string(p.out), nil
	}
	domain.SortEntities(p.entities)
	return string(p.out), p.entities
}

type parser struct {
	in       []rune
	pos      int
	out      []rune
	entities []domain.Entity
}

func (p *parser) parse() {
	for p.pos < len(p.in) {
		if p.tryFence() || p.tryCode() || p.tryLink() || p.tryPair() {
			continue
		}
		p.out = append(p.out, p.in[p.pos])
		p.pos++
	}
}

// has reports whether tok starts at the cursor.
func (p *parser) has(tok string) bool {
	return p.hasAt(p.pos, tok)
}

func (p *parser) hasAt(i int, tok string) bool {
	t := []rune(tok)
	if i+len(t) > len(p.in) {
		return false
	}
	for k, r := range t {
		if p.in[i+k] != r {
			return false
		}
	}
	return true
}

// find returns the index of the next tok at or after from, or -1.
func (p *parser) find(from int, tok string) int {
	for i := from; i < len(p.in); i++ {
		if p.hasAt(i, tok) {
			return i
		}
	}
	return -1
}

// tryFence handles ```lang\ncode```. The language is whatever follows the
// opening fence on its own line, as on GitHub and in Telegram Desktop; a
// fence with the code on the same line has no language.
func (p *parser) tryFence() bool {
	if !p.has("```") {
		return false
	}
	start := p.pos + 3
	end := p.find(start, "```")
	if end < 0 {
		return false
	}
	body := p.in[start:end]
	lang := ""
	if nl := indexRune(body, '\n'); nl >= 0 {
		head := string(body[:nl])
		if head != "" && !strings.ContainsAny(head, " \t") {
			lang = head
			body = body[nl+1:]
		}
	}
	body = trimEdges(body)
	if len(body) == 0 {
		return false
	}
	p.entities = append(p.entities, domain.Entity{Kind: domain.EntityPre, Offset: len(p.out), Length: len(body), Language: lang})
	p.out = append(p.out, body...)
	p.pos = end + 3
	return true
}

// tryCode handles `inline code`. Nothing inside is parsed, which is the
// point of a code span.
func (p *parser) tryCode() bool {
	if !p.has("`") {
		return false
	}
	end := p.find(p.pos+1, "`")
	if end < 0 || end == p.pos+1 {
		return false
	}
	body := p.in[p.pos+1 : end]
	if indexRune(body, '\n') >= 0 {
		return false
	}
	p.entities = append(p.entities, domain.Entity{Kind: domain.EntityCode, Offset: len(p.out), Length: len(body)})
	p.out = append(p.out, body...)
	p.pos = end + 1
	return true
}

// tryLink handles [text](url). The text is parsed for nested markup; the
// url is taken verbatim.
func (p *parser) tryLink() bool {
	if !p.has("[") {
		return false
	}
	closeAt := p.find(p.pos+1, "](")
	if closeAt < 0 || closeAt == p.pos+1 {
		return false
	}
	end := p.find(closeAt+2, ")")
	if end < 0 || end == closeAt+2 {
		return false
	}
	url := strings.TrimSpace(string(p.in[closeAt+2 : end]))
	if url == "" || strings.ContainsAny(url, " \n") {
		return false
	}
	start := len(p.out)
	inner := &parser{in: p.in[p.pos+1 : closeAt]}
	inner.parse()
	if len(inner.out) == 0 {
		return false
	}
	p.out = append(p.out, inner.out...)
	for _, e := range inner.entities {
		e.Offset += start
		p.entities = append(p.entities, e)
	}
	p.entities = append(p.entities, domain.Entity{Kind: domain.EntityTextURL, Offset: start, Length: len(inner.out), URL: url})
	p.pos = end + 1
	return true
}

// tryPair handles the symmetric markers. The span may itself contain
// markup, so the body is parsed recursively; a marker whose partner never
// comes is written out as text.
func (p *parser) tryPair() bool {
	for _, m := range markers {
		if !p.has(m.token) {
			continue
		}
		bodyStart := p.pos + len([]rune(m.token))
		end := p.find(bodyStart, m.token)
		if end < 0 || end == bodyStart {
			return false
		}
		start := len(p.out)
		inner := &parser{in: p.in[bodyStart:end]}
		inner.parse()
		if len(inner.out) == 0 {
			return false
		}
		p.out = append(p.out, inner.out...)
		for _, e := range inner.entities {
			e.Offset += start
			p.entities = append(p.entities, e)
		}
		p.entities = append(p.entities, domain.Entity{Kind: m.kind, Offset: start, Length: len(inner.out)})
		p.pos = end + len([]rune(m.token))
		return true
	}
	return false
}

func indexRune(rs []rune, r rune) int {
	for i, x := range rs {
		if x == r {
			return i
		}
	}
	return -1
}

// trimEdges drops one leading and one trailing newline, which is where the
// fences sit when the code is written on its own lines.
func trimEdges(rs []rune) []rune {
	if len(rs) > 0 && rs[0] == '\n' {
		rs = rs[1:]
	}
	if len(rs) > 0 && rs[len(rs)-1] == '\n' {
		rs = rs[:len(rs)-1]
	}
	return rs
}

// Render writes text with its spans back as markup, so a formatted message
// can be edited the way it was written. Spans without a marker in this
// dialect — a mention, a hashtag, an auto-linked url — are the text they
// cover and need nothing. Overlapping spans that are not nested come out
// with their markers interleaved, which reads oddly but parses back to the
// same overlap.
func Render(text string, es []domain.Entity) string {
	if len(es) == 0 {
		return text
	}
	runes := []rune(text)
	n := len(runes)
	type edge struct {
		at    int
		open  bool
		order int
		s     string
	}
	var edges []edge
	sorted := append([]domain.Entity(nil), es...)
	domain.SortEntities(sorted)
	for i, e := range sorted {
		open, closeTok, ok := tokensFor(e)
		if !ok || e.Length <= 0 || e.Offset < 0 || e.Offset >= n {
			continue
		}
		end := e.End()
		if end > n {
			end = n
		}
		edges = append(edges, edge{at: e.Offset, open: true, order: i, s: open})
		edges = append(edges, edge{at: end, open: false, order: -i, s: closeTok})
	}
	if len(edges) == 0 {
		return text
	}
	// Stable order at one position: closers before openers, and among
	// closers the innermost (latest-opened) first.
	for i := 1; i < len(edges); i++ {
		for j := i; j > 0 && edgeBefore(edges[j], edges[j-1]); j-- {
			edges[j-1], edges[j] = edges[j], edges[j-1]
		}
	}
	var b strings.Builder
	k := 0
	for i := 0; i <= n; i++ {
		for k < len(edges) && edges[k].at == i {
			b.WriteString(edges[k].s)
			k++
		}
		if i < n {
			b.WriteRune(runes[i])
		}
	}
	return b.String()
}

func edgeBefore(a, b struct {
	at    int
	open  bool
	order int
	s     string
}) bool {
	if a.at != b.at {
		return a.at < b.at
	}
	if a.open != b.open {
		return !a.open
	}
	if a.open {
		return a.order < b.order
	}
	return a.order < b.order
}

func tokensFor(e domain.Entity) (string, string, bool) {
	switch e.Kind {
	case domain.EntityBold:
		return "**", "**", true
	case domain.EntityItalic:
		return "__", "__", true
	case domain.EntityStrike:
		return "~~", "~~", true
	case domain.EntitySpoiler:
		return "||", "||", true
	case domain.EntityCode:
		return "`", "`", true
	case domain.EntityPre:
		return "```" + e.Language + "\n", "\n```", true
	case domain.EntityTextURL:
		return "[", "](" + e.URL + ")", true
	default:
		return "", "", false
	}
}
