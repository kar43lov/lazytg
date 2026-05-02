package tg

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/pgmac/lazytg/internal/core/domain"
	coresync "github.com/pgmac/lazytg/internal/core/sync"
)

// PeerResolver resolves a chat's MTProto access metadata from its local id.
// It is the gotd-free contract Sender depends on; production wires it to
// sqlite.PeerRepo, tests substitute a fake. Returning ErrPeerUnknown lets
// callers distinguish "transient lookup failure" from "we never saw this
// chat" without inspecting the wrapped error.
type PeerResolver interface {
	Resolve(ctx context.Context, chatID int64) (domain.Peer, error)
}

// MessagesSendMessageClient is the slice of *tg.Client that Sender needs.
// Declaring it as an interface keeps unit tests free of tgtest plumbing —
// the same pattern as MessagesGetHistoryClient in history.go.
type MessagesSendMessageClient interface {
	MessagesSendMessage(ctx context.Context, request *tg.MessagesSendMessageRequest) (tg.UpdatesClass, error)
}

// Sender is the gotd-aware adapter for messages.sendMessage. The struct
// stays small on purpose: every retry / state transition lives in
// internal/core/sync.SendService, which owns the optimistic record and
// the bus event. Sender just speaks MTProto.
type Sender struct {
	api     MessagesSendMessageClient
	peers   PeerResolver
	randInt func() (int64, error)
}

// SenderOption tweaks an optional knob on Sender. Using functional options
// keeps the constructor signature stable as we add knobs (e.g. silent
// sends, scheduled messages) without breaking existing call sites.
type SenderOption func(*Sender)

// WithRandomIDFunc overrides the RandomID generator. Tests use it to make
// the generated ID deterministic; production keeps the crypto/rand default.
func WithRandomIDFunc(f func() (int64, error)) SenderOption {
	return func(s *Sender) {
		if f != nil {
			s.randInt = f
		}
	}
}

// NewSender wires a Sender. peers must be non-nil; api must be non-nil.
// log is omitted on purpose — Sender does not log; SendService does.
func NewSender(api MessagesSendMessageClient, peers PeerResolver, opts ...SenderOption) *Sender {
	s := &Sender{
		api:     api,
		peers:   peers,
		randInt: cryptoRandInt64,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// SendText delivers text to chatID. replyTo > 0 attaches the message as a
// reply to that local message id. On success the function returns the
// server-assigned message id (0 when the server only acks via PTS without
// surfacing an id — rare for messages.sendMessage but defensive).
//
// FLOOD_WAIT errors are translated to *coresync.FloodWaitError so the
// retry policy in SendService stays gotd-free. Other gotd errors are
// returned as-is so SendService can apply tgerr.Is checks if needed.
func (s *Sender) SendText(ctx context.Context, chatID int64, text string, replyTo int) (int64, error) {
	if text == "" {
		return 0, errors.New("send: text is empty")
	}
	peer, err := s.peers.Resolve(ctx, chatID)
	if err != nil {
		return 0, fmt.Errorf("send: resolve peer %d: %w", chatID, err)
	}
	inputPeer, err := buildInputPeer(peer.ID, peer.AccessHash, string(peer.Type))
	if err != nil {
		return 0, fmt.Errorf("send: build input peer %d: %w", chatID, err)
	}
	randomID, err := s.randInt()
	if err != nil {
		return 0, fmt.Errorf("send: generate random_id: %w", err)
	}
	req := &tg.MessagesSendMessageRequest{
		Peer:     inputPeer,
		Message:  text,
		RandomID: randomID,
	}
	if replyTo > 0 {
		req.SetReplyTo(&tg.InputReplyToMessage{ReplyToMsgID: replyTo})
	}
	updates, err := s.api.MessagesSendMessage(ctx, req)
	if err != nil {
		if d, ok := tgerr.AsFloodWait(err); ok {
			return 0, &coresync.FloodWaitError{RetryAfter: d}
		}
		if rpc, ok := tgerr.As(err); ok && rpc.IsCode(400) {
			// 400-class errors are client-side validation failures
			// (MESSAGE_TOO_LONG, MESSAGE_EMPTY, PEER_ID_INVALID, …).
			// Retrying cannot help — surface a typed sentinel so the
			// retry policy in core/sync gives up immediately.
			return 0, &coresync.ValidationError{Reason: rpc.Type}
		}
		return 0, fmt.Errorf("messages.sendMessage chat=%d: %w", chatID, err)
	}
	return extractMessageID(updates), nil
}

// extractMessageID pulls the server-assigned id out of the typical
// messages.sendMessage response. UpdateShortSentMessage is the common
// shape; full Updates payloads contain UpdateMessageID which holds the
// (random_id → real_id) mapping.
func extractMessageID(u tg.UpdatesClass) int64 {
	switch v := u.(type) {
	case *tg.UpdateShortSentMessage:
		return int64(v.ID)
	case *tg.Updates:
		return findMessageID(v.Updates)
	case *tg.UpdatesCombined:
		return findMessageID(v.Updates)
	case *tg.UpdateShort:
		return findMessageID([]tg.UpdateClass{v.Update})
	}
	return 0
}

// findMessageID walks an update slice looking for either the explicit
// MessageID mapping or a freshly-inserted message we can use as a fallback.
func findMessageID(updates []tg.UpdateClass) int64 {
	for _, upd := range updates {
		switch u := upd.(type) {
		case *tg.UpdateMessageID:
			return int64(u.ID)
		case *tg.UpdateNewMessage:
			if m, ok := u.Message.(*tg.Message); ok {
				return int64(m.ID)
			}
		case *tg.UpdateNewChannelMessage:
			if m, ok := u.Message.(*tg.Message); ok {
				return int64(m.ID)
			}
		}
	}
	return 0
}

// cryptoRandInt64 returns an unbiased 63-bit integer from crypto/rand.
// The high bit is forced to zero so callers that re-encode the value as
// signed JSON (debug-bundle dumps) do not flip into a negative number.
func cryptoRandInt64() (int64, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0, err
	}
	v := int64(binary.BigEndian.Uint64(buf[:]) >> 1)
	return v, nil
}
