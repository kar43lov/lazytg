package tg

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	coresync "github.com/kar43lov/lazytg/internal/core/sync"
)

// BotAPI is the one call pressing a bot's button needs.
type BotAPI interface {
	MessagesGetBotCallbackAnswer(ctx context.Context, request *tg.MessagesGetBotCallbackAnswerRequest) (*tg.MessagesBotCallbackAnswer, error)
}

// BotActor presses the callback buttons under a bot's messages: one
// request per explicit press, over a message that already exists, which
// is why it is not behind the send guard — nothing here creates a message.
type BotActor struct {
	api   BotAPI
	peers PeerResolver
}

// NewBotActor wires an actor over api and peers.
func NewBotActor(api BotAPI, peers PeerResolver) *BotActor {
	return &BotActor{api: api, peers: peers}
}

// PressButton sends the button's data to the bot and returns what it said
// back. A callback the bot does not answer within Telegram's own timeout
// comes back as BOT_RESPONSE_TIMEOUT, which is worded rather than raw:
// it is the ordinary way a bot that only edits the message replies.
func (a *BotActor) PressButton(ctx context.Context, chatID, messageID int64, data []byte) (coresync.CallbackAnswer, error) {
	if a == nil || a.api == nil {
		return coresync.CallbackAnswer{}, fmt.Errorf("press button: no MTProto client for chat %d", chatID)
	}
	peer, err := a.peers.Resolve(ctx, chatID)
	if err != nil {
		return coresync.CallbackAnswer{}, fmt.Errorf("press button: resolve peer %d: %w", chatID, err)
	}
	input, err := buildInputPeer(peer.ID, peer.AccessHash, string(peer.Type))
	if err != nil {
		return coresync.CallbackAnswer{}, fmt.Errorf("press button: %w", err)
	}
	req := &tg.MessagesGetBotCallbackAnswerRequest{Peer: input, MsgID: int(messageID)}
	req.SetData(data)
	res, err := a.api.MessagesGetBotCallbackAnswer(ctx, req)
	if err != nil {
		if d, ok := tgerr.AsFloodWait(err); ok {
			return coresync.CallbackAnswer{}, &coresync.FloodWaitError{RetryAfter: d}
		}
		if tgerr.Is(err, "BOT_RESPONSE_TIMEOUT") {
			return coresync.CallbackAnswer{}, nil
		}
		return coresync.CallbackAnswer{}, fmt.Errorf("messages.getBotCallbackAnswer chat=%d msg=%d: %w", chatID, messageID, err)
	}
	answer := coresync.CallbackAnswer{Alert: res.Alert}
	if msg, ok := res.GetMessage(); ok {
		answer.Message = msg
	}
	if u, ok := res.GetURL(); ok {
		answer.URL = u
	}
	return answer, nil
}
