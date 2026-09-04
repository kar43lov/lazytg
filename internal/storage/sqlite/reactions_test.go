package sqlite

import (
	"testing"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

func TestReactions_RoundTripThroughTheColumn(t *testing.T) {
	t.Parallel()

	rs := []domain.Reaction{
		{Emoticon: "👍", Count: 3, Chosen: true},
		{Emoticon: "❤", Count: 1},
	}
	got := decodeReactions(encodeReactions(rs))
	if len(got) != 2 {
		t.Fatalf("decoded %d reactions, want 2: %v", len(got), got)
	}
	if got[0] != rs[0] || got[1] != rs[1] {
		t.Fatalf("round trip changed the reactions: %v", got)
	}
}

// No reactions is the empty string, which is also what every row written
// before migration 0012 holds. One representation of "none" beats two.
func TestReactions_NoneIsTheEmptyString(t *testing.T) {
	t.Parallel()

	if got := encodeReactions(nil); got != "" {
		t.Fatalf("encodeReactions(nil) = %q", got)
	}
	if got := encodeReactions([]domain.Reaction{{Emoticon: ""}}); got != "" {
		t.Fatalf("a reaction with no emoji encoded to %q", got)
	}
	if got := decodeReactions(""); got != nil {
		t.Fatalf("decodeReactions(\"\") = %v", got)
	}
}

// The column is cosmetic. A message whose reactions cannot be read is still a
// message, and refusing to show the conversation over it would be the wrong
// trade by a wide margin.
func TestReactions_UnreadableColumnIsNotAnError(t *testing.T) {
	t.Parallel()

	if got := decodeReactions("{not json"); got != nil {
		t.Fatalf("garbage decoded to %v", got)
	}
}
