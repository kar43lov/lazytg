package domain

import "testing"

func TestReactionCodec_RoundTrip(t *testing.T) {
	t.Parallel()

	rs := []Reaction{
		{Emoticon: "👍", Count: 3, Chosen: true},
		{Emoticon: "❤", Count: 1},
	}
	got := DecodeReactions(EncodeReactions(rs))
	if len(got) != 2 || got[0] != rs[0] || got[1] != rs[1] {
		t.Fatalf("round trip changed the reactions: %v", got)
	}
}

// One representation of "none", because that is what the column defaults to
// for every row written before the reactions migration.
func TestReactionCodec_NoneIsTheEmptyString(t *testing.T) {
	t.Parallel()

	if got := EncodeReactions(nil); got != "" {
		t.Fatalf("EncodeReactions(nil) = %q", got)
	}
	if got := EncodeReactions([]Reaction{{Emoticon: ""}}); got != "" {
		t.Fatalf("a reaction with no emoji encoded to %q", got)
	}
	if got := DecodeReactions(""); got != nil {
		t.Fatalf(`DecodeReactions("") = %v`, got)
	}
}

// The column is cosmetic: a message whose reactions cannot be read is still a
// message, and refusing to show the conversation over it would be the wrong
// trade by a wide margin.
func TestReactionCodec_UnreadableIsNotAnError(t *testing.T) {
	t.Parallel()

	if got := DecodeReactions("{not json"); got != nil {
		t.Fatalf("garbage decoded to %v", got)
	}
}

func TestChosenReaction_NamesOurOwn(t *testing.T) {
	t.Parallel()

	m := Message{Reactions: []Reaction{
		{Emoticon: "👍", Count: 3},
		{Emoticon: "🔥", Count: 1, Chosen: true},
	}}
	if got := m.ChosenReaction(); got != "🔥" {
		t.Fatalf("ChosenReaction = %q", got)
	}
	if got := (Message{}).ChosenReaction(); got != "" {
		t.Fatalf("a message with no reactions reports %q", got)
	}
}
