package domain

import (
	"encoding/json"
	"sort"
)

// EntityKind names one kind of formatting Telegram attaches to a span of a
// message. The set mirrors the wire's MessageEntity constructors that a
// terminal can do something with; a kind this build does not know is kept
// as the string Telegram used, so a newer server never turns a span into
// nothing.
type EntityKind string

// The kinds, named after the wire constructors with the MessageEntity
// prefix dropped.
const (
	EntityBold        EntityKind = "bold"
	EntityItalic      EntityKind = "italic"
	EntityUnderline   EntityKind = "underline"
	EntityStrike      EntityKind = "strike"
	EntityCode        EntityKind = "code"
	EntityPre         EntityKind = "pre"
	EntitySpoiler     EntityKind = "spoiler"
	EntityBlockquote  EntityKind = "blockquote"
	EntityURL         EntityKind = "url"
	EntityTextURL     EntityKind = "text_url"
	EntityMention     EntityKind = "mention"
	EntityMentionName EntityKind = "mention_name"
	EntityHashtag     EntityKind = "hashtag"
	EntityCashtag     EntityKind = "cashtag"
	EntityBotCommand  EntityKind = "bot_command"
	EntityEmail       EntityKind = "email"
	EntityPhone       EntityKind = "phone"
	EntityCustomEmoji EntityKind = "custom_emoji"
)

// Entity is one formatted span of a message.
//
// Offset and Length count runes, not the UTF-16 code units Telegram's wire
// format counts in. The conversion happens once, at the edge that talks to
// Telegram, so that nothing on this side of it — the renderer, the markdown
// parser, the composer — has to know that "😀" is two units on the wire and
// one character everywhere else. An offset stored in wire units would be
// wrong for every message with an emoji in it, which is most of them.
type Entity struct {
	Kind   EntityKind
	Offset int
	Length int
	// URL is the target of a text_url span, and empty for every other kind.
	URL string
	// Language is the code fence language of a pre span, when the sender
	// gave one.
	Language string
	// UserID is the user a mention_name span points at.
	UserID int64
	// DocumentID is the custom emoji document of a custom_emoji span. Kept
	// so an edit round trip does not strip premium emoji from a message
	// this account wrote; nothing here draws it.
	DocumentID int64
}

// End is the rune index just past the span.
func (e Entity) End() int { return e.Offset + e.Length }

// The stored form of an entity list. Lives here for the same reason the
// reaction codec does: the repository writes the column and the search
// service reads it back when it builds a jump window, and one of them
// keeping a private copy of the format is how a message reaches the pane
// with its formatting stripped.

type storedEntity struct {
	K string `json:"k"`
	O int    `json:"o"`
	L int    `json:"l"`
	U string `json:"u,omitempty"`
	G string `json:"g,omitempty"`
	I int64  `json:"i,omitempty"`
	D int64  `json:"d,omitempty"`
}

// EncodeEntities renders entities for storage. No entities is the empty
// string rather than "[]": it is the column default for every row written
// before migration 0014, and one representation of "none" is better than
// two.
func EncodeEntities(es []Entity) string {
	if len(es) == 0 {
		return ""
	}
	out := make([]storedEntity, 0, len(es))
	for _, e := range es {
		if e.Kind == "" || e.Length <= 0 || e.Offset < 0 {
			continue
		}
		out = append(out, storedEntity{K: string(e.Kind), O: e.Offset, L: e.Length, U: e.URL, G: e.Language, I: e.UserID, D: e.DocumentID})
	}
	if len(out) == 0 {
		return ""
	}
	b, err := json.Marshal(out)
	if err != nil {
		// Strings and ints cannot fail to marshal. "No formatting" rather
		// than an error keeps a cosmetic field from failing a message write.
		return ""
	}
	return string(b)
}

// DecodeEntities parses the column back. Unparseable content is "no
// formatting" rather than an error: a message whose styling cannot be read
// is still a message.
func DecodeEntities(s string) []Entity {
	if s == "" {
		return nil
	}
	var stored []storedEntity
	if err := json.Unmarshal([]byte(s), &stored); err != nil {
		return nil
	}
	out := make([]Entity, 0, len(stored))
	for _, e := range stored {
		if e.K == "" || e.L <= 0 || e.O < 0 {
			continue
		}
		out = append(out, Entity{Kind: EntityKind(e.K), Offset: e.O, Length: e.L, URL: e.U, Language: e.G, UserID: e.I, DocumentID: e.D})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SortEntities orders spans by where they start, longer first among those
// that start together, which is the order a renderer and a serialiser both
// want: an outer span opens before the inner one it contains.
func SortEntities(es []Entity) {
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].Offset != es[j].Offset {
			return es[i].Offset < es[j].Offset
		}
		return es[i].Length > es[j].Length
	})
}
