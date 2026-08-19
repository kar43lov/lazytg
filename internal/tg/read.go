package tg

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// Reader acknowledges messages the user has read. It satisfies
// coresync.ReadMarker.
//
// Two RPCs cover the cases because Telegram splits them: messages.readHistory
// for users and basic groups, channels.readHistory for anything channel-like.
// Sending the wrong one is not a soft failure — the server rejects it — so the
// peer type decides, and an unknown type is an error rather than a guess.
type Reader struct {
	api   *tg.Client
	peers PeerResolver
}

// NewReader wires a reader over the MTProto client and the local peer table.
// The peer table is where the access hash lives, and without it a chat cannot
// be addressed at all.
func NewReader(api *tg.Client, peers PeerResolver) *Reader {
	return &Reader{api: api, peers: peers}
}

// MarkRead reports every message up to maxID as read.
//
// The call is fire-and-check: Telegram answers with the affected-messages
// bookkeeping (pts and count) for the messages variant and a plain bool for
// channels, and neither tells us anything the caller can act on beyond
// success. What matters is that a failure is returned rather than swallowed —
// ReadService keeps the local unread counter until the server has agreed.
func (r *Reader) MarkRead(ctx context.Context, chatID, maxID int64) error {
	if r == nil || r.api == nil {
		return fmt.Errorf("read: no MTProto client for chat %d", chatID)
	}
	peer, err := r.peers.Resolve(ctx, chatID)
	if err != nil {
		return fmt.Errorf("read: resolve peer %d: %w", chatID, err)
	}

	switch peer.Type {
	case domain.ChatTypeChannel, domain.ChatTypeSupergroup:
		if _, err := r.api.ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{
			Channel: &tg.InputChannel{ChannelID: chatID, AccessHash: peer.AccessHash},
			MaxID:   int(maxID),
		}); err != nil {
			return fmt.Errorf("read: channels.readHistory chat=%d max=%d: %w", chatID, maxID, err)
		}
		return nil
	case domain.ChatTypePrivate, domain.ChatTypeGroup:
		input, err := buildInputPeer(chatID, peer.AccessHash, string(peer.Type))
		if err != nil {
			return fmt.Errorf("read: input peer %d: %w", chatID, err)
		}
		if _, err := r.api.MessagesReadHistory(ctx, &tg.MessagesReadHistoryRequest{
			Peer:  input,
			MaxID: int(maxID),
		}); err != nil {
			return fmt.Errorf("read: messages.readHistory chat=%d max=%d: %w", chatID, maxID, err)
		}
		return nil
	default:
		return fmt.Errorf("read: unknown peer type %q for chat %d", peer.Type, chatID)
	}
}
