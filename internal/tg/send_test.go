package tg

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/kar43lov/lazytg/internal/core/domain"
	coresync "github.com/kar43lov/lazytg/internal/core/sync"
)

// stubSendMessage is the symmetric counterpart to stubGetHistory in
// history_test.go: it captures the request and replays a scripted
// response (or error) so we can exercise Sender without spinning up
// gotd/td/tgtest. The test surface stays narrow on purpose — we only need
// the single-RPC contract.
type stubSendMessage struct {
	resp  tg.UpdatesClass
	err   error
	calls []*tg.MessagesSendMessageRequest
}

func (s *stubSendMessage) MessagesSendMessage(_ context.Context, req *tg.MessagesSendMessageRequest) (tg.UpdatesClass, error) {
	s.calls = append(s.calls, req)
	if s.err != nil {
		return nil, s.err
	}
	return s.resp, nil
}

// MessagesSendMedia satisfies the extended MessagesSendMessageClient
// interface. Tests that exercise media flows use stubMediaSender (in
// files_test.go); this stub returns a not-implemented error so an
// accidental call shows up as a clear test failure.
func (s *stubSendMessage) MessagesSendMedia(_ context.Context, _ *tg.MessagesSendMediaRequest) (tg.UpdatesClass, error) {
	return nil, errors.New("stubSendMessage: MessagesSendMedia not wired")
}

// stubResolver is a fixed PeerResolver: every Resolve returns the same
// configured peer (or error). The chatID is irrelevant for these tests
// because Sender's job is to translate the resolver's output into an
// InputPeer — multi-peer routing belongs to higher layers.
type stubResolver struct {
	peer domain.Peer
	err  error
}

func (s *stubResolver) Resolve(_ context.Context, _ int64) (domain.Peer, error) {
	if s.err != nil {
		return domain.Peer{}, s.err
	}
	return s.peer, nil
}

func TestSender_SendText_Success_PrivateChat(t *testing.T) {
	t.Parallel()
	stub := &stubSendMessage{
		resp: &tg.UpdateShortSentMessage{ID: 1234, Date: int(time.Now().Unix())},
	}
	resolver := &stubResolver{peer: domain.Peer{ID: 99, Type: domain.ChatTypePrivate, AccessHash: 0xCAFE}}
	sender := NewSender(stub, resolver, WithRandomIDFunc(func() (int64, error) {
		return 0xDEADBEEF, nil
	}))

	id, err := sender.SendText(context.Background(), 99, "hi", 0)
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if id != 1234 {
		t.Fatalf("server id = %d, want 1234", id)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(stub.calls))
	}
	req := stub.calls[0]
	if req.Message != "hi" {
		t.Fatalf("Message = %q, want %q", req.Message, "hi")
	}
	if req.RandomID != 0xDEADBEEF {
		t.Fatalf("RandomID = %d, want 0xDEADBEEF", req.RandomID)
	}
	peer, ok := req.Peer.(*tg.InputPeerUser)
	if !ok || peer.UserID != 99 || peer.AccessHash != 0xCAFE {
		t.Fatalf("Peer = %+v, want InputPeerUser{99, 0xCAFE}", req.Peer)
	}
	if _, ok := req.GetReplyTo(); ok {
		t.Fatalf("replyTo unexpectedly set when caller passed 0")
	}
}

func TestSender_SendText_AttachesReplyTo(t *testing.T) {
	t.Parallel()
	stub := &stubSendMessage{resp: &tg.UpdateShortSentMessage{ID: 5}}
	resolver := &stubResolver{peer: domain.Peer{ID: 1, Type: domain.ChatTypePrivate, AccessHash: 1}}
	sender := NewSender(stub, resolver, WithRandomIDFunc(func() (int64, error) { return 1, nil }))

	if _, err := sender.SendText(context.Background(), 1, "re", 42); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	rt, ok := stub.calls[0].GetReplyTo()
	if !ok {
		t.Fatalf("ReplyTo not set")
	}
	itm, ok := rt.(*tg.InputReplyToMessage)
	if !ok {
		t.Fatalf("ReplyTo type = %T", rt)
	}
	if itm.ReplyToMsgID != 42 {
		t.Fatalf("ReplyToMsgID = %d, want 42", itm.ReplyToMsgID)
	}
}

func TestSender_SendText_TranslatesFloodWait(t *testing.T) {
	t.Parallel()
	stub := &stubSendMessage{err: tgerr.New(420, "FLOOD_WAIT_5")}
	resolver := &stubResolver{peer: domain.Peer{ID: 1, Type: domain.ChatTypePrivate, AccessHash: 1}}
	sender := NewSender(stub, resolver)

	_, err := sender.SendText(context.Background(), 1, "x", 0)
	var fw *coresync.FloodWaitError
	if !errors.As(err, &fw) {
		t.Fatalf("expected FloodWaitError, got %T: %v", err, err)
	}
	if fw.RetryAfter != 5*time.Second {
		t.Fatalf("RetryAfter = %v, want 5s", fw.RetryAfter)
	}
}

func TestSender_SendText_TranslatesValidationError(t *testing.T) {
	t.Parallel()
	stub := &stubSendMessage{err: tgerr.New(400, "MESSAGE_TOO_LONG")}
	resolver := &stubResolver{peer: domain.Peer{ID: 1, Type: domain.ChatTypePrivate, AccessHash: 1}}
	sender := NewSender(stub, resolver)

	_, err := sender.SendText(context.Background(), 1, "x", 0)
	reason, ok := coresync.AsValidation(err)
	if !ok {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	if reason != "MESSAGE_TOO_LONG" {
		t.Fatalf("Reason = %q, want MESSAGE_TOO_LONG", reason)
	}
}

func TestSender_SendText_PassesThroughUnknownErrors(t *testing.T) {
	t.Parallel()
	want := errors.New("network blew up")
	stub := &stubSendMessage{err: want}
	resolver := &stubResolver{peer: domain.Peer{ID: 1, Type: domain.ChatTypePrivate, AccessHash: 1}}
	sender := NewSender(stub, resolver)

	_, err := sender.SendText(context.Background(), 1, "x", 0)
	if err == nil {
		t.Fatalf("expected an error")
	}
	if !errors.Is(err, want) {
		t.Fatalf("expected wrapped %v, got %v", want, err)
	}
	// Must not be classified.
	if _, ok := coresync.AsValidation(err); ok {
		t.Fatalf("network error must not be ValidationError")
	}
	if _, ok := coresync.AsFloodWait(err); ok {
		t.Fatalf("network error must not be FloodWaitError")
	}
}

func TestSender_SendText_RejectsEmptyText(t *testing.T) {
	t.Parallel()
	sender := NewSender(&stubSendMessage{}, &stubResolver{peer: domain.Peer{ID: 1, Type: domain.ChatTypePrivate}})
	if _, err := sender.SendText(context.Background(), 1, "", 0); err == nil {
		t.Fatalf("SendText must reject empty text")
	}
}

func TestSender_SendText_PropagatesResolverError(t *testing.T) {
	t.Parallel()
	stub := &stubSendMessage{}
	want := errors.New("peer lookup failed")
	resolver := &stubResolver{err: want}
	sender := NewSender(stub, resolver)

	_, err := sender.SendText(context.Background(), 1, "hi", 0)
	if err == nil || !errors.Is(err, want) {
		t.Fatalf("expected wrapped resolver error, got %v", err)
	}
	if len(stub.calls) != 0 {
		t.Fatalf("RPC must not be called when resolver fails")
	}
}

func TestSender_SendText_SupergroupUsesInputPeerChannel(t *testing.T) {
	t.Parallel()
	stub := &stubSendMessage{resp: &tg.UpdateShortSentMessage{ID: 7}}
	resolver := &stubResolver{peer: domain.Peer{ID: 555, Type: domain.ChatTypeSupergroup, AccessHash: 0xBEEF}}
	sender := NewSender(stub, resolver, WithRandomIDFunc(func() (int64, error) { return 1, nil }))

	if _, err := sender.SendText(context.Background(), 555, "hi", 0); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	peer, ok := stub.calls[0].Peer.(*tg.InputPeerChannel)
	if !ok {
		t.Fatalf("expected InputPeerChannel, got %T", stub.calls[0].Peer)
	}
	if peer.ChannelID != 555 || peer.AccessHash != 0xBEEF {
		t.Fatalf("peer = %+v", peer)
	}
}

func TestSender_SendText_ExtractsIDFromUpdates(t *testing.T) {
	t.Parallel()
	stub := &stubSendMessage{
		resp: &tg.Updates{
			Updates: []tg.UpdateClass{
				&tg.UpdateMessageID{ID: 9876, RandomID: 1},
			},
		},
	}
	resolver := &stubResolver{peer: domain.Peer{ID: 1, Type: domain.ChatTypePrivate, AccessHash: 1}}
	sender := NewSender(stub, resolver, WithRandomIDFunc(func() (int64, error) { return 1, nil }))

	id, err := sender.SendText(context.Background(), 1, "x", 0)
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if id != 9876 {
		t.Fatalf("server id = %d, want 9876", id)
	}
}

func TestSender_SendText_RandomIDGeneratorErrorPropagates(t *testing.T) {
	t.Parallel()
	stub := &stubSendMessage{}
	resolver := &stubResolver{peer: domain.Peer{ID: 1, Type: domain.ChatTypePrivate, AccessHash: 1}}
	want := errors.New("rand exhausted")
	sender := NewSender(stub, resolver, WithRandomIDFunc(func() (int64, error) { return 0, want }))

	_, err := sender.SendText(context.Background(), 1, "x", 0)
	if err == nil || !errors.Is(err, want) {
		t.Fatalf("expected wrapped %v, got %v", want, err)
	}
	if len(stub.calls) != 0 {
		t.Fatalf("RPC must not be called when random_id fails")
	}
}

// Sanity: the default cryptoRandInt64 actually returns a non-negative
// number on every invocation. A regression here would manifest as
// "NEGATIVE_RANDOM_ID" on the wire.
func TestCryptoRandInt64_NonNegative(t *testing.T) {
	t.Parallel()
	for i := 0; i < 64; i++ {
		v, err := cryptoRandInt64()
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if v < 0 {
			t.Fatalf("iter %d: got negative %d", i, v)
		}
	}
}

// Compile-time assertion that fmt.Errorf wrapping survives errors.Is
// inspection — guards against future refactors that drop %w.
var _ = fmt.Errorf

// The composer's markup becomes spans here, at the edge, once. The server
// gets the plain text and the entities, not the asterisks.
func TestSender_SendText_TurnsMarkupIntoEntities(t *testing.T) {
	t.Parallel()
	stub := &stubSendMessage{resp: &tg.UpdateShortSentMessage{ID: 1, Date: int(time.Now().Unix())}}
	resolver := &stubResolver{peer: domain.Peer{ID: 99, Type: domain.ChatTypePrivate, AccessHash: 1}}
	sender := NewSender(stub, resolver, WithRandomIDFunc(func() (int64, error) { return 1, nil }))

	if _, err := sender.SendText(context.Background(), 99, "🚀 **go** `now`", 0); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	req := stub.calls[0]
	if req.Message != "🚀 go now" {
		t.Fatalf("Message = %q, want the markup stripped", req.Message)
	}
	got, ok := req.GetEntities()
	if !ok || len(got) != 2 {
		t.Fatalf("entities = %+v, want bold and code", got)
	}
	bold, isBold := got[0].(*tg.MessageEntityBold)
	if !isBold || bold.Offset != 3 || bold.Length != 2 {
		t.Fatalf("first entity = %+v, want bold at UTF-16 offset 3 (the rocket is two units)", got[0])
	}
	if _, isCode := got[1].(*tg.MessageEntityCode); !isCode {
		t.Fatalf("second entity = %+v, want code", got[1])
	}
}

func TestSender_SendText_PlainTextCarriesNoEntities(t *testing.T) {
	t.Parallel()
	stub := &stubSendMessage{resp: &tg.UpdateShortSentMessage{ID: 1, Date: int(time.Now().Unix())}}
	resolver := &stubResolver{peer: domain.Peer{ID: 99, Type: domain.ChatTypePrivate, AccessHash: 1}}
	sender := NewSender(stub, resolver, WithRandomIDFunc(func() (int64, error) { return 1, nil }))

	if _, err := sender.SendText(context.Background(), 99, "2 * 3 and a_b", 0); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	req := stub.calls[0]
	if req.Message != "2 * 3 and a_b" {
		t.Fatalf("Message = %q, want it untouched", req.Message)
	}
	if _, ok := req.GetEntities(); ok {
		t.Fatalf("entities were set on plain text: %+v", req.Entities)
	}
}
