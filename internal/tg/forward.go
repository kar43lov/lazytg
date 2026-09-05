package tg

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	coresync "github.com/kar43lov/lazytg/internal/core/sync"
)

// Forwarder passes messages from one chat to another. It satisfies
// coresync.MessageForwarder.
//
// Unlike deleting, this needs no split by peer kind: messages.forwardMessages
// names both peers explicitly, so the ids are never ambiguous. What it does
// need is a random id per message — Telegram deduplicates sends by it, and
// reusing one silently drops the second copy of a forward.
type Forwarder struct {
	api   MessagesForwardClient
	peers PeerResolver
}

// MessagesForwardClient is the slice of *tg.Client the forwarder needs.
type MessagesForwardClient interface {
	MessagesForwardMessages(ctx context.Context, request *tg.MessagesForwardMessagesRequest) (tg.UpdatesClass, error)
}

// NewForwarder wires a forwarder over the MTProto client and the local peer
// table.
func NewForwarder(api MessagesForwardClient, peers PeerResolver) *Forwarder {
	return &Forwarder{api: api, peers: peers}
}

// Forward copies ids from one chat into another.
//
// dropAuthor is Telegram's "forward without quoting the author" — the same
// checkbox the official clients show. It is passed through rather than
// decided here: hiding who wrote something is a choice about the other
// person, and only the user can make it.
func (f *Forwarder) Forward(ctx context.Context, fromChatID, toChatID int64, ids []int64, dropAuthor bool) error {
	if f == nil || f.api == nil {
		return fmt.Errorf("forward: no MTProto client for chat %d", fromChatID)
	}
	if len(ids) == 0 {
		return nil
	}

	from, err := f.peers.Resolve(ctx, fromChatID)
	if err != nil {
		return fmt.Errorf("forward: resolve source %d: %w", fromChatID, err)
	}
	to, err := f.peers.Resolve(ctx, toChatID)
	if err != nil {
		return fmt.Errorf("forward: resolve target %d: %w", toChatID, err)
	}
	fromPeer, err := buildInputPeer(fromChatID, from.AccessHash, string(from.Type))
	if err != nil {
		return fmt.Errorf("forward: source %d: %w", fromChatID, err)
	}
	toPeer, err := buildInputPeer(toChatID, to.AccessHash, string(to.Type))
	if err != nil {
		return fmt.Errorf("forward: target %d: %w", toChatID, err)
	}

	msgIDs := make([]int, 0, len(ids))
	randomIDs := make([]int64, 0, len(ids))
	for _, id := range ids {
		msgIDs = append(msgIDs, int(id))
		rnd, err := randomID()
		if err != nil {
			return fmt.Errorf("forward: %w", err)
		}
		randomIDs = append(randomIDs, rnd)
	}

	req := &tg.MessagesForwardMessagesRequest{
		FromPeer: fromPeer,
		ToPeer:   toPeer,
		ID:       msgIDs,
		RandomID: randomIDs,
	}
	req.SetDropAuthor(dropAuthor)

	if _, err := f.api.MessagesForwardMessages(ctx, req); err != nil {
		if d, ok := tgerr.AsFloodWait(err); ok {
			return &coresync.FloodWaitError{RetryAfter: d}
		}
		return fmt.Errorf("forward: messages.forwardMessages %d→%d: %w", fromChatID, toChatID, err)
	}
	return nil
}

// randomID draws the per-message deduplication id Telegram requires.
//
// crypto/rand rather than math/rand, and an error rather than a fallback: a
// predictable or repeated id means the server treats a genuine second forward
// as a duplicate and drops it, which looks to the user like the client
// silently ignored them.
func randomID() (int64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("random id: %w", err)
	}
	return int64(binary.LittleEndian.Uint64(b[:])), nil //nolint:gosec // a random 64-bit token, not a measurement
}
