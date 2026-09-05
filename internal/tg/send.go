package tg

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/markdown"
	coresync "github.com/kar43lov/lazytg/internal/core/sync"
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
	MessagesSendMedia(ctx context.Context, request *tg.MessagesSendMediaRequest) (tg.UpdatesClass, error)
}

// Sender is the gotd-aware adapter for messages.sendMessage. The struct
// stays small on purpose: every retry / state transition lives in
// internal/core/sync.SendService, which owns the optimistic record and
// the bus event. Sender just speaks MTProto.
type Sender struct {
	api     MessagesSendMessageClient
	peers   PeerResolver
	randInt func() (int64, error)
	// echo is where a sent message is announced to the rest of the
	// program. Telegram does not push a message back to the session that
	// sent it — the response to messages.sendMessage is the whole
	// acknowledgement — so without this the mirror learns about the
	// account's own messages only when the chat is next opened and its
	// history re-fetched: they were missing from search, from the chat
	// list preview, and from the thread after a restart.
	echo *UpdatesDispatcher
}

// SenderOption tweaks an optional knob on Sender. Using functional options
// keeps the constructor signature stable as we add knobs (e.g. silent
// sends, scheduled messages) without breaking existing call sites.
type SenderOption func(*Sender)

// WithEcho routes every sent message through the dispatcher, as if the
// server had pushed it. The dispatcher's duplicate filter sees it, so a
// copy arriving by any other path is dropped rather than shown twice.
func WithEcho(d *UpdatesDispatcher) SenderOption {
	return func(s *Sender) {
		s.echo = d
	}
}

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
	// The outbox stores what was typed, markup included, so a retry sends
	// the same thing; the markup becomes spans here, at the edge, once.
	plain, entities := markdown.Parse(text)
	req := &tg.MessagesSendMessageRequest{
		Peer:     inputPeer,
		Message:  plain,
		RandomID: randomID,
	}
	if wire := entitiesToWire(plain, entities); len(wire) > 0 {
		req.SetEntities(wire)
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
	s.announce(ctx, updates, peer, plain, replyTo)
	return extractMessageID(updates), nil
}

// announce hands the sent message to the dispatcher. A full Updates
// payload carries the message itself and goes through as it is; the short
// acknowledgement carries only the id, the date and the entities the
// server settled on, so the message is rebuilt around them from what was
// sent — which is exactly what the official clients do with it.
func (s *Sender) announce(ctx context.Context, updates tg.UpdatesClass, peer domain.Peer, plain string, replyTo int) {
	if s.echo == nil || updates == nil {
		return
	}
	short, ok := updates.(*tg.UpdateShortSentMessage)
	if !ok {
		_ = s.echo.handle(ctx, updates)
		return
	}
	m := &tg.Message{
		ID:      short.ID,
		Date:    short.Date,
		Message: plain,
		Out:     true,
		PeerID:  peerToWire(peer),
	}
	if ents, ok := short.GetEntities(); ok && len(ents) > 0 {
		m.SetEntities(ents)
	}
	if media, ok := short.GetMedia(); ok {
		m.SetMedia(media)
	}
	if replyTo > 0 {
		header := &tg.MessageReplyHeader{}
		header.SetReplyToMsgID(replyTo)
		m.SetReplyTo(header)
	}
	s.echo.publishMessage(m, false, nil)
}

// peerToWire names a chat the way a message's peer_id does.
func peerToWire(p domain.Peer) tg.PeerClass {
	switch p.Type {
	case domain.ChatTypeGroup:
		return &tg.PeerChat{ChatID: p.ID}
	case domain.ChatTypeChannel, domain.ChatTypeSupergroup:
		return &tg.PeerChannel{ChannelID: p.ID}
	default:
		return &tg.PeerUser{UserID: p.ID}
	}
}

// SendMedia delivers a previously-uploaded file as a document message
// with optional caption to chatID. file is the gotd InputFile handle
// returned by Uploader.Upload (small files: *tg.InputFile, big files:
// *tg.InputFileBig — both satisfy InputFileClass). filename is sent in
// the document attribute so recipients see the original on-disk name;
// mimeType drives the server-side preview pipeline (image/* enables
// thumbnails, video/* enables playback). caption is optional.
//
// All v0.1 uploads go out as InputMediaUploadedDocument with no
// ForceFile flag — Telegram still picks an appropriate envelope (photo,
// video, audio) when the mime type matches the well-known set. A future
// task can switch image/* uploads to InputMediaUploadedPhoto for the
// "render in chat as image" UX.
//
// FLOOD_WAIT errors are translated to *coresync.FloodWaitError;
// 400-class errors land as *coresync.ValidationError so the retry
// policy in core/sync gives up immediately.
func (s *Sender) SendMedia(ctx context.Context, chatID int64, file tg.InputFileClass, filename, mimeType, caption string, replyTo int) (int64, error) {
	if file == nil {
		return 0, errors.New("send: file is nil")
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
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	var media tg.InputMediaClass = &tg.InputMediaUploadedDocument{
		File:     file,
		MimeType: mimeType,
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeFilename{FileName: filename},
		},
	}
	if sendsAsPhoto(file, mimeType) {
		// A picture goes as a picture, the way the official clients send
		// one: it draws in the chat on the other end rather than sitting
		// there as a file to open. Telegram re-encodes it, which is the
		// trade a photo makes; anything that must arrive byte for byte is
		// not a jpeg, and a gif is an animation, not a photo.
		media = &tg.InputMediaUploadedPhoto{File: file}
	}
	plainCaption, captionEntities := markdown.Parse(caption)
	req := &tg.MessagesSendMediaRequest{
		Peer:     inputPeer,
		Media:    media,
		Message:  plainCaption,
		RandomID: randomID,
	}
	if wire := entitiesToWire(plainCaption, captionEntities); len(wire) > 0 {
		req.SetEntities(wire)
	}
	if replyTo > 0 {
		req.SetReplyTo(&tg.InputReplyToMessage{ReplyToMsgID: replyTo})
	}
	updates, err := s.api.MessagesSendMedia(ctx, req)
	if err != nil {
		if d, ok := tgerr.AsFloodWait(err); ok {
			return 0, &coresync.FloodWaitError{RetryAfter: d}
		}
		if rpc, ok := tgerr.As(err); ok && rpc.IsCode(400) {
			return 0, &coresync.ValidationError{Reason: rpc.Type}
		}
		return 0, fmt.Errorf("messages.sendMedia chat=%d: %w", chatID, err)
	}
	s.announce(ctx, updates, peer, plainCaption, replyTo)
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

// sendsAsPhoto reports whether an upload goes out as a photo rather than a
// document: an image that is not a gif, small enough to have come through
// the small-file path — Telegram caps photos at 10 MiB, and a big-file
// handle means the picture is past it.
func sendsAsPhoto(file tg.InputFileClass, mimeType string) bool {
	if _, big := file.(*tg.InputFileBig); big {
		return false
	}
	switch strings.ToLower(mimeType) {
	case "image/jpeg", "image/png", "image/webp", "image/heic", "image/heif", "image/bmp", "image/tiff":
		return true
	}
	return false
}
