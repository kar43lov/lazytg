package sqlite

import (
	"encoding/json"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// Reactions live in one JSON column on the message. See migration 0012 for
// why they are not a table: the thread reads messages a page at a time and
// renders each row whole, so a side table would put a join on the one query
// whose latency the user feels, in order to normalise a list nothing ever
// queries across messages.
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

// encodeReactions renders reactions for storage. No reactions is the empty
// string rather than "[]": it is what the column defaults to for every row
// written before migration 0012, and one representation of "none" is better
// than two.
func encodeReactions(rs []domain.Reaction) string {
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

// decodeReactions parses the column back.
//
// Unparseable content returns no reactions rather than an error. The column
// is cosmetic: a message whose reactions cannot be read is still a message,
// and refusing to show the conversation because of it would be the wrong
// trade by a wide margin.
func decodeReactions(s string) []domain.Reaction {
	if s == "" {
		return nil
	}
	var stored []storedReaction
	if err := json.Unmarshal([]byte(s), &stored); err != nil {
		return nil
	}
	out := make([]domain.Reaction, 0, len(stored))
	for _, r := range stored {
		if r.E == "" {
			continue
		}
		out = append(out, domain.Reaction{Emoticon: r.E, Count: r.C, Chosen: r.M})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
