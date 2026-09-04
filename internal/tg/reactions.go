package tg

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/kar43lov/lazytg/internal/core/domain"
	coresync "github.com/kar43lov/lazytg/internal/core/sync"
)

// Reactions, both directions: reading what a message carries and sending
// this account's own.
//
// Only standard emoji reactions are handled. Telegram also has custom
// (premium) reactions, which are a document id rather than a character —
// showing one means downloading and drawing a sticker, and a count attached
// to nothing is worse than the reaction being absent. They are skipped on the
// way in and cannot be sent.

// ReactionsFromMessage reads the reactions a message carries.
//
// The chosen bit comes from chosen_order, which Telegram sets on the
// reaction this account sent (the value orders several; the presence is what
// matters here). It decides whether pressing the key adds a reaction or takes
// one back, so a client that ignored it would make the gesture a coin flip.
func ReactionsFromMessage(m *tg.Message) []domain.Reaction {
	if m == nil {
		return nil
	}
	raw, ok := m.GetReactions()
	if !ok {
		return nil
	}
	return decodeReactions(raw)
}

func decodeReactions(raw tg.MessageReactions) []domain.Reaction {
	out := make([]domain.Reaction, 0, len(raw.Results))
	for _, r := range raw.Results {
		emoji, ok := r.Reaction.(*tg.ReactionEmoji)
		if !ok {
			continue
		}
		if emoji.Emoticon == "" {
			continue
		}
		_, chosen := r.GetChosenOrder()
		out = append(out, domain.Reaction{
			Emoticon: emoji.Emoticon,
			Count:    r.Count,
			Chosen:   chosen,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Reactor sends this account's reaction. It satisfies coresync.MessageReactor.
type Reactor struct {
	api   MessagesSendReactionClient
	peers PeerResolver
}

// MessagesSendReactionClient is the slice of *tg.Client the reactor needs.
type MessagesSendReactionClient interface {
	MessagesSendReaction(ctx context.Context, request *tg.MessagesSendReactionRequest) (tg.UpdatesClass, error)
}

// NewReactor wires a reactor over the MTProto client and the local peer table.
func NewReactor(api MessagesSendReactionClient, peers PeerResolver) *Reactor {
	return &Reactor{api: api, peers: peers}
}

// React sets this account's reaction on a message, or removes it when
// emoticon is empty, and returns the message's reactions as the server now
// has them.
//
// Removal is the same call with an empty reaction list rather than a separate
// method, because that is how the protocol expresses it: a reaction is a
// value the account holds on a message, and clearing it is setting it to
// nothing. Modelling it as add/remove here would invent a state machine the
// server does not have.
//
// The new set is read out of the response rather than guessed at locally.
// Counts belong to everybody, not just this account: adding a 👍 to a message
// that already has four means the row now reads five, and a client that
// incremented its own copy would drift from the truth the moment two people
// reacted at once. The response carries the authoritative list, so it is used
// — and when it does not, the caller gets nothing rather than a guess, and
// the push update fills it in.
func (r *Reactor) React(ctx context.Context, chatID, messageID int64, emoticon string) ([]domain.Reaction, error) {
	if r == nil || r.api == nil {
		return nil, fmt.Errorf("react: no MTProto client for chat %d", chatID)
	}
	peer, err := r.peers.Resolve(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("react: resolve peer %d: %w", chatID, err)
	}
	input, err := buildInputPeer(chatID, peer.AccessHash, string(peer.Type))
	if err != nil {
		return nil, fmt.Errorf("react: chat %d: %w", chatID, err)
	}

	req := &tg.MessagesSendReactionRequest{
		Peer:  input,
		MsgID: int(messageID),
	}
	if emoticon != "" {
		req.SetReaction([]tg.ReactionClass{&tg.ReactionEmoji{Emoticon: emoticon}})
		// add_to_recent is what puts a reaction into the row Telegram
		// offers next time. Set because the alternative is a client whose
		// suggestions never learn anything, which is a behavioural
		// difference from every official client rather than a feature.
		req.SetAddToRecent(true)
	}

	res, err := r.api.MessagesSendReaction(ctx, req)
	if err != nil {
		if d, ok := tgerr.AsFloodWait(err); ok {
			return nil, &coresync.FloodWaitError{RetryAfter: d}
		}
		return nil, fmt.Errorf("react: messages.sendReaction chat=%d msg=%d: %w", chatID, messageID, err)
	}
	return reactionsFromUpdates(res, messageID), nil
}

// reactionsFromUpdates digs the new reaction set out of an RPC response.
//
// Telegram answers sendReaction with the updates the change produced, one of
// which is the updateMessageReactions for the message just reacted to. The
// message id is matched rather than taking the first one, because a response
// may carry updates about other things entirely.
func reactionsFromUpdates(res tg.UpdatesClass, messageID int64) []domain.Reaction {
	var list []tg.UpdateClass
	switch u := res.(type) {
	case *tg.Updates:
		list = u.Updates
	case *tg.UpdatesCombined:
		list = u.Updates
	case *tg.UpdateShort:
		list = []tg.UpdateClass{u.Update}
	default:
		return nil
	}
	for _, upd := range list {
		reactions, ok := upd.(*tg.UpdateMessageReactions)
		if !ok || int64(reactions.MsgID) != messageID {
			continue
		}
		return decodeReactions(reactions.Reactions)
	}
	return nil
}
