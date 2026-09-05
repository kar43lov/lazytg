package tg

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/kar43lov/lazytg/internal/core/domain"
	coresync "github.com/kar43lov/lazytg/internal/core/sync"
)

// Editor rewrites a message that is already on the server. It satisfies
// coresync.MessageEditor.
//
// Unlike reading and deleting, editing needs no channel-specific variant:
// messages.editMessage takes an InputPeer and covers every dialog kind, so
// the peer type only decides how that InputPeer is built. Telegram enforces
// the rest — who may edit what, and for how long — and answers with an error
// this passes through rather than second-guessing. In particular it does not
// try to decide locally whether a message is still editable: the 48-hour
// window is server policy, it has exceptions (a chat admin, saved messages),
// and a client that guessed would refuse edits the server would have allowed.
type Editor struct {
	api   MessagesEditMessageClient
	peers PeerResolver
}

// MessagesEditMessageClient is the slice of *tg.Client the editor needs,
// declared as an interface so tests need no tgtest harness — the same
// pattern the sender and the history fetcher use.
type MessagesEditMessageClient interface {
	MessagesEditMessage(ctx context.Context, request *tg.MessagesEditMessageRequest) (tg.UpdatesClass, error)
}

// NewEditor wires an editor over the MTProto client and the local peer table.
func NewEditor(api MessagesEditMessageClient, peers PeerResolver) *Editor {
	return &Editor{api: api, peers: peers}
}

// Edit replaces the text of messageID in chatID.
//
// An empty text is rejected here rather than sent: Telegram treats
// messages.editMessage with no message and no media as a malformed request,
// and the resulting MESSAGE_EMPTY reads like a bug in lazytg rather than what
// it is — an edit that would have deleted the message if it were allowed to.
// Deleting is a separate, deliberate gesture.
//
// FLOOD_WAIT is translated to *coresync.FloodWaitError so the calling service
// stays free of gotd, matching Sender.SendText.
func (e *Editor) Edit(ctx context.Context, chatID, messageID int64, text string, entities []domain.Entity) error {
	if e == nil || e.api == nil {
		return fmt.Errorf("edit: no MTProto client for chat %d", chatID)
	}
	if text == "" {
		return fmt.Errorf("edit: empty text for message %d — delete it instead", messageID)
	}
	peer, err := e.peers.Resolve(ctx, chatID)
	if err != nil {
		return fmt.Errorf("edit: resolve peer %d: %w", chatID, err)
	}
	input, err := buildInputPeer(chatID, peer.AccessHash, string(peer.Type))
	if err != nil {
		return fmt.Errorf("edit: input peer %d: %w", chatID, err)
	}

	req := &tg.MessagesEditMessageRequest{
		Peer: input,
		ID:   int(messageID),
	}
	req.SetMessage(text)
	if wire := entitiesToWire(text, entities); len(wire) > 0 {
		req.SetEntities(wire)
	}

	if _, err := e.api.MessagesEditMessage(ctx, req); err != nil {
		if d, ok := tgerr.AsFloodWait(err); ok {
			return &coresync.FloodWaitError{RetryAfter: d}
		}
		return fmt.Errorf("edit: messages.editMessage chat=%d id=%d: %w", chatID, messageID, err)
	}
	return nil
}
