package tg

import (
	"unicode/utf16"

	"github.com/gotd/td/tg"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// Telegram counts entity offsets in UTF-16 code units, the way its first
// clients' string types did. Everything on this side of the wire counts
// runes. The two agree on ASCII and disagree on every emoji and every
// character outside the Basic Multilingual Plane, which is where an offset
// carried over unchanged lands a bold span one character to the left of
// each emoji before it — the more emoji, the further off. So the conversion
// happens here, once, in both directions, and nothing else has to know.

// EntitiesFromMessage reads the formatting spans off a wire message, with
// offsets converted to runes. Kinds this build does not name are kept under
// the constructor's own name so a future server never turns a span into
// nothing.
func EntitiesFromMessage(m *tg.Message) []domain.Entity {
	if m == nil {
		return nil
	}
	raw, ok := m.GetEntities()
	if !ok || len(raw) == 0 {
		return nil
	}
	return entitiesFromWire(m.Message, raw)
}

func entitiesFromWire(text string, raw []tg.MessageEntityClass) []domain.Entity {
	if len(raw) == 0 {
		return nil
	}
	runeAt := unitToRune(text)
	out := make([]domain.Entity, 0, len(raw))
	for _, e := range raw {
		d, ok := entityFromWire(e)
		if !ok {
			continue
		}
		start, end := runeAt(e.GetOffset()), runeAt(e.GetOffset()+e.GetLength())
		if end <= start {
			continue
		}
		d.Offset, d.Length = start, end-start
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil
	}
	domain.SortEntities(out)
	return out
}

// entityFromWire maps one constructor to its kind and payload. Offsets are
// filled in by the caller, which owns the text the units are counted in.
func entityFromWire(e tg.MessageEntityClass) (domain.Entity, bool) {
	switch v := e.(type) {
	case *tg.MessageEntityBold:
		return domain.Entity{Kind: domain.EntityBold}, true
	case *tg.MessageEntityItalic:
		return domain.Entity{Kind: domain.EntityItalic}, true
	case *tg.MessageEntityUnderline:
		return domain.Entity{Kind: domain.EntityUnderline}, true
	case *tg.MessageEntityStrike:
		return domain.Entity{Kind: domain.EntityStrike}, true
	case *tg.MessageEntityCode:
		return domain.Entity{Kind: domain.EntityCode}, true
	case *tg.MessageEntityPre:
		return domain.Entity{Kind: domain.EntityPre, Language: v.Language}, true
	case *tg.MessageEntitySpoiler:
		return domain.Entity{Kind: domain.EntitySpoiler}, true
	case *tg.MessageEntityBlockquote:
		return domain.Entity{Kind: domain.EntityBlockquote}, true
	case *tg.MessageEntityURL:
		return domain.Entity{Kind: domain.EntityURL}, true
	case *tg.MessageEntityTextURL:
		return domain.Entity{Kind: domain.EntityTextURL, URL: v.URL}, true
	case *tg.MessageEntityMention:
		return domain.Entity{Kind: domain.EntityMention}, true
	case *tg.MessageEntityMentionName:
		return domain.Entity{Kind: domain.EntityMentionName, UserID: v.UserID}, true
	case *tg.MessageEntityHashtag:
		return domain.Entity{Kind: domain.EntityHashtag}, true
	case *tg.MessageEntityCashtag:
		return domain.Entity{Kind: domain.EntityCashtag}, true
	case *tg.MessageEntityBotCommand:
		return domain.Entity{Kind: domain.EntityBotCommand}, true
	case *tg.MessageEntityEmail:
		return domain.Entity{Kind: domain.EntityEmail}, true
	case *tg.MessageEntityPhone:
		return domain.Entity{Kind: domain.EntityPhone}, true
	case *tg.MessageEntityCustomEmoji:
		return domain.Entity{Kind: domain.EntityCustomEmoji, DocumentID: v.DocumentID}, true
	case nil:
		return domain.Entity{}, false
	default:
		return domain.Entity{Kind: domain.EntityKind(e.TypeName())}, true
	}
}

// entitiesToWire converts rune-indexed spans back to the wire's units for
// text. Spans this client cannot express on the wire — a mention by user id
// needs an access hash it does not carry — are dropped; the text they cover
// is sent as it is, which is what the official clients do with a mention
// pasted from elsewhere.
func entitiesToWire(text string, es []domain.Entity) []tg.MessageEntityClass {
	if len(es) == 0 {
		return nil
	}
	unitAt := runeToUnit(text)
	out := make([]tg.MessageEntityClass, 0, len(es))
	for _, e := range es {
		if e.Length <= 0 || e.Offset < 0 {
			continue
		}
		off, length := unitAt(e.Offset), unitAt(e.End())-unitAt(e.Offset)
		if length <= 0 {
			continue
		}
		var w tg.MessageEntityClass
		switch e.Kind {
		case domain.EntityBold:
			w = &tg.MessageEntityBold{Offset: off, Length: length}
		case domain.EntityItalic:
			w = &tg.MessageEntityItalic{Offset: off, Length: length}
		case domain.EntityUnderline:
			w = &tg.MessageEntityUnderline{Offset: off, Length: length}
		case domain.EntityStrike:
			w = &tg.MessageEntityStrike{Offset: off, Length: length}
		case domain.EntityCode:
			w = &tg.MessageEntityCode{Offset: off, Length: length}
		case domain.EntityPre:
			w = &tg.MessageEntityPre{Offset: off, Length: length, Language: e.Language}
		case domain.EntitySpoiler:
			w = &tg.MessageEntitySpoiler{Offset: off, Length: length}
		case domain.EntityBlockquote:
			w = &tg.MessageEntityBlockquote{Offset: off, Length: length}
		case domain.EntityTextURL:
			w = &tg.MessageEntityTextURL{Offset: off, Length: length, URL: e.URL}
		case domain.EntityCustomEmoji:
			w = &tg.MessageEntityCustomEmoji{Offset: off, Length: length, DocumentID: e.DocumentID}
		default:
			// url, mention, hashtag, bot_command, email, phone, cashtag:
			// the server derives these from the text itself and rejects
			// or ignores a client's copy; mention_name needs an input user.
			continue
		}
		out = append(out, w)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// unitToRune returns a function mapping a UTF-16 offset into text to the
// rune index at or after it. An offset past the end maps to the rune count,
// and one that lands inside a surrogate pair rounds up to the rune after it,
// so a malformed span shrinks rather than growing into its neighbour.
func unitToRune(text string) func(unit int) int {
	units := make([]int, 0, len(text)+1) // units[i] = UTF-16 offset of rune i
	total := 0
	for _, r := range text {
		units = append(units, total)
		total += utf16Len(r)
	}
	units = append(units, total)
	return func(unit int) int {
		if unit <= 0 {
			return 0
		}
		if unit >= total {
			return len(units) - 1
		}
		// Binary search for the first rune whose offset is >= unit.
		lo, hi := 0, len(units)-1
		for lo < hi {
			mid := (lo + hi) / 2
			if units[mid] < unit {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		return lo
	}
}

// runeToUnit is the inverse: rune index to UTF-16 offset.
func runeToUnit(text string) func(index int) int {
	units := make([]int, 0, len(text)+1)
	total := 0
	for _, r := range text {
		units = append(units, total)
		total += utf16Len(r)
	}
	units = append(units, total)
	return func(i int) int {
		if i <= 0 {
			return 0
		}
		if i >= len(units) {
			return total
		}
		return units[i]
	}
}

func utf16Len(r rune) int {
	if utf16.IsSurrogate(r) || r > 0xFFFF {
		return 2
	}
	return 1
}
