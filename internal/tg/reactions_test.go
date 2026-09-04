package tg

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

type stubReactionAPI struct {
	calls []*tg.MessagesSendReactionRequest
	res   tg.UpdatesClass
	err   error
}

func (s *stubReactionAPI) MessagesSendReaction(_ context.Context, r *tg.MessagesSendReactionRequest) (tg.UpdatesClass, error) {
	s.calls = append(s.calls, r)
	if s.err != nil {
		return nil, s.err
	}
	if s.res != nil {
		return s.res, nil
	}
	return &tg.Updates{}, nil
}

func reactionUpdates(msgID int, counts map[string]int, chosen string) tg.UpdatesClass {
	var results []tg.ReactionCount
	for emoticon, n := range counts {
		rc := tg.ReactionCount{Reaction: &tg.ReactionEmoji{Emoticon: emoticon}, Count: n}
		if emoticon == chosen {
			rc.SetChosenOrder(0)
		}
		results = append(results, rc)
	}
	return &tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdateMessageReactions{
			MsgID:     msgID,
			Reactions: tg.MessageReactions{Results: results},
		},
	}}
}

func TestReactor_SendsTheEmoji(t *testing.T) {
	t.Parallel()

	api := &stubReactionAPI{res: reactionUpdates(7, map[string]int{"👍": 3}, "👍")}
	r := NewReactor(api, stubPeers{peer: domain.Peer{ID: 42, Type: domain.ChatTypePrivate, AccessHash: 11}})

	got, err := r.React(context.Background(), 42, 7, "👍")
	if err != nil {
		t.Fatalf("React: %v", err)
	}
	if len(api.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(api.calls))
	}
	req := api.calls[0]
	if req.MsgID != 7 {
		t.Fatalf("MsgID = %d, want 7", req.MsgID)
	}
	if len(req.Reaction) != 1 {
		t.Fatalf("Reaction = %v, want one emoji", req.Reaction)
	}
	emoji, ok := req.Reaction[0].(*tg.ReactionEmoji)
	if !ok || emoji.Emoticon != "👍" {
		t.Fatalf("Reaction[0] = %#v", req.Reaction[0])
	}
	// The new set comes from the server rather than being guessed at:
	// counts belong to everybody, and a client incrementing its own copy
	// drifts the moment two people react at once.
	if len(got) != 1 || got[0].Count != 3 || !got[0].Chosen {
		t.Fatalf("returned reactions = %v", got)
	}
}

// Removal is the same call with nothing in it, because that is how the
// protocol expresses it.
func TestReactor_RemovesWithAnEmptyList(t *testing.T) {
	t.Parallel()

	api := &stubReactionAPI{}
	r := NewReactor(api, stubPeers{peer: domain.Peer{ID: 42, Type: domain.ChatTypePrivate}})

	if _, err := r.React(context.Background(), 42, 7, ""); err != nil {
		t.Fatalf("React: %v", err)
	}
	if got := api.calls[0].Reaction; len(got) != 0 {
		t.Fatalf("removal carried %v", got)
	}
}

// A response that says nothing about the result must produce nothing rather
// than a guess. The push update fills it in.
func TestReactor_NoUpdateInTheResponseYieldsNoReactions(t *testing.T) {
	t.Parallel()

	api := &stubReactionAPI{res: &tg.Updates{}}
	r := NewReactor(api, stubPeers{peer: domain.Peer{ID: 42, Type: domain.ChatTypePrivate}})

	got, err := r.React(context.Background(), 42, 7, "👍")
	if err != nil {
		t.Fatalf("React: %v", err)
	}
	if got != nil {
		t.Fatalf("invented reactions from an empty response: %v", got)
	}
}

// A response can carry updates about other messages entirely.
func TestReactor_MatchesTheMessageItAskedAbout(t *testing.T) {
	t.Parallel()

	api := &stubReactionAPI{res: reactionUpdates(999, map[string]int{"❤": 8}, "")}
	r := NewReactor(api, stubPeers{peer: domain.Peer{ID: 42, Type: domain.ChatTypePrivate}})

	got, err := r.React(context.Background(), 42, 7, "👍")
	if err != nil {
		t.Fatalf("React: %v", err)
	}
	if got != nil {
		t.Fatalf("took another message's reactions: %v", got)
	}
}

func TestReactionsFromMessage_ReadsCountsAndTheChosenOne(t *testing.T) {
	t.Parallel()

	chosen := tg.ReactionCount{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 4}
	chosen.SetChosenOrder(0)
	m := &tg.Message{ID: 7}
	m.SetReactions(tg.MessageReactions{Results: []tg.ReactionCount{
		chosen,
		{Reaction: &tg.ReactionEmoji{Emoticon: "❤"}, Count: 1},
	}})

	got := ReactionsFromMessage(m)
	if len(got) != 2 {
		t.Fatalf("read %d reactions, want 2: %v", len(got), got)
	}
	if got[0].Emoticon != "👍" || got[0].Count != 4 || !got[0].Chosen {
		t.Fatalf("first reaction = %+v", got[0])
	}
	if got[1].Chosen {
		t.Fatalf("second reaction marked as ours: %+v", got[1])
	}
}

// A custom (premium) reaction is a document id rather than a character.
// Showing one means downloading and drawing a sticker; until that exists a
// count attached to nothing is worse than the reaction being absent.
func TestReactionsFromMessage_SkipsCustomReactions(t *testing.T) {
	t.Parallel()

	m := &tg.Message{ID: 7}
	m.SetReactions(tg.MessageReactions{Results: []tg.ReactionCount{
		{Reaction: &tg.ReactionCustomEmoji{DocumentID: 12345}, Count: 9},
		{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 1},
	}})

	got := ReactionsFromMessage(m)
	if len(got) != 1 || got[0].Emoticon != "👍" {
		t.Fatalf("reactions = %v, want only the standard one", got)
	}
}

func TestReactionsFromMessage_NoneWhenTheFieldIsAbsent(t *testing.T) {
	t.Parallel()

	if got := ReactionsFromMessage(&tg.Message{ID: 7}); got != nil {
		t.Fatalf("invented reactions: %v", got)
	}
	if got := ReactionsFromMessage(nil); got != nil {
		t.Fatalf("ReactionsFromMessage(nil) = %v", got)
	}
}
