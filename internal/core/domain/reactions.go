package domain

import "encoding/json"

// The stored form of a reaction list.
//
// It lives here rather than in the storage package because two packages have
// to agree on it: the repository writes it, and the search service reads the
// same column back when it builds a jump window. A second copy of the format
// is how one of them ends up rendering a message with its reactions missing —
// which is exactly what happened to the media columns before they were
// centralised.
//
// JSON rather than a packed string because the key is an emoji chosen by
// Telegram, and every separator character anyone might pick is a guess about
// what an emoji cannot contain.

// storedReaction is the on-disk shape. Short keys because the column is
// written on every message upsert, including backfills of hundreds of rows.
type storedReaction struct {
	E string `json:"e"`
	C int    `json:"c"`
	M bool   `json:"m,omitempty"`
}

// EncodeReactions renders reactions for storage. No reactions is the empty
// string rather than "[]": it is what the column defaults to for every row
// written before migration 0012, and one representation of "none" is better
// than two.
func EncodeReactions(rs []Reaction) string {
	if len(rs) == 0 {
		return ""
	}
	out := make([]storedReaction, 0, len(rs))
	for _, r := range rs {
		if r.Emoticon == "" {
			continue
		}
		out = append(out, storedReaction{E: r.Emoticon, C: r.Count, M: r.Chosen})
	}
	if len(out) == 0 {
		return ""
	}
	b, err := json.Marshal(out)
	if err != nil {
		// Marshalling a slice of strings and ints cannot fail. Returning
		// "no reactions" rather than propagating keeps a cosmetic field
		// from failing a message write.
		return ""
	}
	return string(b)
}

// DecodeReactions parses the column back.
//
// Unparseable content returns no reactions rather than an error. The column
// is cosmetic: a message whose reactions cannot be read is still a message,
// and refusing to show the conversation because of it would be the wrong
// trade by a wide margin.
func DecodeReactions(s string) []Reaction {
	if s == "" {
		return nil
	}
	var stored []storedReaction
	if err := json.Unmarshal([]byte(s), &stored); err != nil {
		return nil
	}
	out := make([]Reaction, 0, len(stored))
	for _, r := range stored {
		if r.E == "" {
			continue
		}
		out = append(out, Reaction{Emoticon: r.E, Count: r.C, Chosen: r.M})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
