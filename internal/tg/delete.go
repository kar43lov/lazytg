package tg

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/kar43lov/lazytg/internal/core/domain"
	coresync "github.com/kar43lov/lazytg/internal/core/sync"
)

// Deleter removes messages from the server. It satisfies
// coresync.MessageDeleter.
//
// Telegram splits this the same way it splits reading, and for the same
// reason: a channel numbers its own messages from one, so ids are only
// meaningful together with the channel they belong to. messages.deleteMessages
// carries bare ids and is therefore only valid for users and basic groups;
// channels.deleteMessages names the channel. Sending the wrong one does not
// fail loudly — it deletes ids in the wrong space, which is why the peer type
// decides and an unknown type is an error rather than a guess.
type Deleter struct {
	api   MessagesDeleteMessagesClient
	peers PeerResolver
}

// MessagesDeleteMessagesClient is the slice of *tg.Client the deleter needs.
type MessagesDeleteMessagesClient interface {
	MessagesDeleteMessages(ctx context.Context, request *tg.MessagesDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error)
	ChannelsDeleteMessages(ctx context.Context, request *tg.ChannelsDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error)
}

// NewDeleter wires a deleter over the MTProto client and the local peer table.
func NewDeleter(api MessagesDeleteMessagesClient, peers PeerResolver) *Deleter {
	return &Deleter{api: api, peers: peers}
}

// Delete removes ids from chatID.
//
// revoke asks Telegram to delete the message for everyone rather than only
// for this account. It is passed through rather than decided here because the
// two are genuinely different acts and only the user knows which one they
// mean; the UI asks. For channels the flag does not exist — a deletion there
// is always for everyone — so it is ignored on that path rather than silently
// changing what the user asked for on the other one.
//
// The server reports how many messages it actually affected; that number is
// returned so a caller can tell "deleted" from "there was nothing to delete",
// which a bare error cannot express.
func (d *Deleter) Delete(ctx context.Context, chatID int64, ids []int64, revoke bool) (int, error) {
	if d == nil || d.api == nil {
		return 0, fmt.Errorf("delete: no MTProto client for chat %d", chatID)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	peer, err := d.peers.Resolve(ctx, chatID)
	if err != nil {
		return 0, fmt.Errorf("delete: resolve peer %d: %w", chatID, err)
	}

	msgIDs := make([]int, 0, len(ids))
	for _, id := range ids {
		msgIDs = append(msgIDs, int(id))
	}

	switch peer.Type {
	case domain.ChatTypeChannel, domain.ChatTypeSupergroup:
		res, err := d.api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: chatID, AccessHash: peer.AccessHash},
			ID:      msgIDs,
		})
		if err != nil {
			return 0, wrapDeleteErr(err, "channels.deleteMessages", chatID)
		}
		return res.PtsCount, nil
	case domain.ChatTypePrivate, domain.ChatTypeGroup:
		req := &tg.MessagesDeleteMessagesRequest{ID: msgIDs}
		req.SetRevoke(revoke)
		res, err := d.api.MessagesDeleteMessages(ctx, req)
		if err != nil {
			return 0, wrapDeleteErr(err, "messages.deleteMessages", chatID)
		}
		return res.PtsCount, nil
	default:
		return 0, fmt.Errorf("delete: unknown peer type %q for chat %d", peer.Type, chatID)
	}
}

func wrapDeleteErr(err error, method string, chatID int64) error {
	if d, ok := tgerr.AsFloodWait(err); ok {
		return &coresync.FloodWaitError{RetryAfter: d}
	}
	return fmt.Errorf("delete: %s chat=%d: %w", method, chatID, err)
}
