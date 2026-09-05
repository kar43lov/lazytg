package tg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/kar43lov/lazytg/internal/core/domain"
	coresync "github.com/kar43lov/lazytg/internal/core/sync"
)

// stubGetHistory replays a script of MessagesGetHistory responses. The map
// is keyed by OffsetID so the test can express "first call returns batch A,
// the next page (offsetID = last id) returns batch B".
//
// We deliberately avoid spinning up gotd/td/tgtest here — that test server
// is geared towards exchange-level scenarios (handshake, encrypted message
// framing) and adds dozens of seconds of setup per test. For verifying that
// our wrapper consumes a *tg.MessagesMessages response correctly, mocking
// the high-level Client interface is both more focused and faster.
type stubGetHistory struct {
	responses map[int]tg.MessagesMessagesClass
	err       error
	calls     []*tg.MessagesGetHistoryRequest
}

func (s *stubGetHistory) MessagesGetHistory(_ context.Context, req *tg.MessagesGetHistoryRequest) (tg.MessagesMessagesClass, error) {
	s.calls = append(s.calls, req)
	if s.err != nil {
		return nil, s.err
	}
	if r, ok := s.responses[req.OffsetID]; ok {
		return r, nil
	}
	return &tg.MessagesMessages{}, nil
}

func makeMsg(id int, peerUserID int64, text string, replyToID int) tg.MessageClass {
	m := &tg.Message{
		ID:      id,
		PeerID:  &tg.PeerUser{UserID: peerUserID},
		Date:    int(time.Date(2026, 1, 1, 12, 0, id, 0, time.UTC).Unix()),
		Message: text,
	}
	// FromID is a flag-conditional field; setting the struct field alone is
	// not enough — Flags.Has(8) must also be set, which the Set* helper does.
	m.SetFromID(&tg.PeerUser{UserID: peerUserID})
	if replyToID > 0 {
		hdr := &tg.MessageReplyHeader{}
		hdr.SetReplyToMsgID(replyToID)
		m.SetReplyTo(hdr)
	}
	return m
}

func TestHistoryFetcher_Fetch_ConvertsBatch(t *testing.T) {
	t.Parallel()
	const userID = 1001
	resp := &tg.MessagesMessagesSlice{
		Count: 12,
		Messages: []tg.MessageClass{
			makeMsg(5, userID, "hello", 0),
			makeMsg(4, userID, "world", 5),
			&tg.MessageEmpty{ID: 3}, // must be skipped
			&tg.MessageService{ID: 2, PeerID: &tg.PeerUser{UserID: userID}, Action: &tg.MessageActionPinMessage{}},
			makeMsg(1, userID, "first", 0),
		},
	}
	stub := &stubGetHistory{responses: map[int]tg.MessagesMessagesClass{0: resp}}
	fetcher := NewHistoryFetcher(stub)

	msgs, hasMore, err := fetcher.Fetch(context.Background(), userID, 0xCAFE, string(domain.ChatTypePrivate), 5, 0)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if hasMore != true {
		t.Fatalf("hasMore=false; expected true (slice + len==limit)")
	}
	if got := len(msgs); got != 4 {
		t.Fatalf("converted %d messages, want 4 (empty skipped, service kept)", got)
	}
	want := []domain.Message{
		{ID: 5, ChatID: userID, FromID: userID, Text: "hello", ReplyTo: 0},
		{ID: 4, ChatID: userID, FromID: userID, Text: "world", ReplyTo: 5},
		{ID: 2, ChatID: userID, FromID: userID, Text: "pinned a message", ReplyTo: 0},
		{ID: 1, ChatID: userID, FromID: userID, Text: "first", ReplyTo: 0},
	}
	for i, w := range want {
		got := msgs[i]
		if got.ID != w.ID || got.ChatID != w.ChatID || got.FromID != w.FromID || got.Text != w.Text || got.ReplyTo != w.ReplyTo {
			t.Fatalf("message[%d]: got %+v, want %+v", i, got, w)
		}
		if got.Date.IsZero() {
			t.Fatalf("message[%d]: Date is zero", i)
		}
	}
	// Verify the request the wrapper built.
	if len(stub.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(stub.calls))
	}
	req := stub.calls[0]
	if req.Limit != 5 || req.OffsetID != 0 {
		t.Fatalf("request limit/offset: %+v", req)
	}
	peer, ok := req.Peer.(*tg.InputPeerUser)
	if !ok || peer.UserID != userID || peer.AccessHash != 0xCAFE {
		t.Fatalf("peer: got %+v, want InputPeerUser{UserID:%d, AccessHash:0xCAFE}", req.Peer, userID)
	}
}

func TestHistoryFetcher_Fetch_PaginationOffsetID(t *testing.T) {
	t.Parallel()
	const userID = 2002
	first := &tg.MessagesMessagesSlice{
		Count: 4,
		Messages: []tg.MessageClass{
			makeMsg(10, userID, "j", 0),
			makeMsg(9, userID, "i", 0),
		},
	}
	second := &tg.MessagesMessagesSlice{
		Count: 4,
		Messages: []tg.MessageClass{
			makeMsg(8, userID, "h", 0),
			makeMsg(7, userID, "g", 0),
		},
	}
	stub := &stubGetHistory{responses: map[int]tg.MessagesMessagesClass{
		0: first,
		8: second, // offsetID == oldest of first batch
	}}
	fetcher := NewHistoryFetcher(stub)

	page1, hasMore, err := fetcher.Fetch(context.Background(), userID, 1, string(domain.ChatTypePrivate), 2, 0)
	if err != nil || !hasMore {
		t.Fatalf("page1: hasMore=%v err=%v", hasMore, err)
	}
	if len(page1) != 2 || page1[len(page1)-1].ID != 9 {
		t.Fatalf("page1 unexpected: %+v", page1)
	}
	oldest := int(page1[len(page1)-1].ID) - 1 // emulate caller advancing offset

	page2, _, err := fetcher.Fetch(context.Background(), userID, 1, string(domain.ChatTypePrivate), 2, oldest)
	if err != nil {
		t.Fatalf("page2 err: %v", err)
	}
	if len(page2) != 2 || page2[0].ID != 8 {
		t.Fatalf("page2 unexpected: %+v", page2)
	}
	if got := stub.calls[1].OffsetID; got != oldest {
		t.Fatalf("page2 request OffsetID: want %d, got %d", oldest, got)
	}
}

func TestHistoryFetcher_Fetch_FullResultSetsHasMoreFalse(t *testing.T) {
	t.Parallel()
	const userID = 3003
	resp := &tg.MessagesMessages{
		Messages: []tg.MessageClass{makeMsg(1, userID, "only", 0)},
	}
	stub := &stubGetHistory{responses: map[int]tg.MessagesMessagesClass{0: resp}}
	fetcher := NewHistoryFetcher(stub)

	msgs, hasMore, err := fetcher.Fetch(context.Background(), userID, 1, string(domain.ChatTypePrivate), 50, 0)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if hasMore {
		t.Fatalf("MessagesMessages should signal no more pages")
	}
	if len(msgs) != 1 {
		t.Fatalf("len(msgs) = %d, want 1", len(msgs))
	}
}

func TestHistoryFetcher_Fetch_RejectsUnknownPeerType(t *testing.T) {
	t.Parallel()
	stub := &stubGetHistory{}
	fetcher := NewHistoryFetcher(stub)
	if _, _, err := fetcher.Fetch(context.Background(), 1, 0, "frobnicate", 10, 0); err == nil {
		t.Fatalf("Fetch must reject unknown peer type")
	}
	if len(stub.calls) != 0 {
		t.Fatalf("API must not be called when peer is invalid, got %d calls", len(stub.calls))
	}
}

func TestHistoryFetcher_Fetch_TranslatesFloodWait(t *testing.T) {
	t.Parallel()
	floodErr := tgerr.New(420, "FLOOD_WAIT_3")
	stub := &stubGetHistory{err: floodErr}
	fetcher := NewHistoryFetcher(stub)

	_, _, err := fetcher.Fetch(context.Background(), 1, 0, string(domain.ChatTypeGroup), 10, 0)
	if err == nil {
		t.Fatalf("Fetch should fail when API returns FLOOD_WAIT")
	}
	var fw *coresync.FloodWaitError
	if !errors.As(err, &fw) {
		t.Fatalf("error should be FloodWaitError, got %T: %v", err, err)
	}
	if fw.RetryAfter != 3*time.Second {
		t.Fatalf("RetryAfter = %v, want 3s", fw.RetryAfter)
	}
}

func TestHistoryFetcher_Fetch_GroupUsesInputPeerChat(t *testing.T) {
	t.Parallel()
	stub := &stubGetHistory{responses: map[int]tg.MessagesMessagesClass{0: &tg.MessagesMessages{}}}
	fetcher := NewHistoryFetcher(stub)
	if _, _, err := fetcher.Fetch(context.Background(), 444, 0, string(domain.ChatTypeGroup), 10, 0); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, ok := stub.calls[0].Peer.(*tg.InputPeerChat); !ok {
		t.Fatalf("group chat should map to InputPeerChat, got %T", stub.calls[0].Peer)
	}
}

func TestHistoryFetcher_Fetch_ChannelUsesInputPeerChannel(t *testing.T) {
	t.Parallel()
	stub := &stubGetHistory{responses: map[int]tg.MessagesMessagesClass{0: &tg.MessagesMessages{}}}
	fetcher := NewHistoryFetcher(stub)
	if _, _, err := fetcher.Fetch(context.Background(), 555, 0xBEEF, string(domain.ChatTypeChannel), 10, 0); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	peer, ok := stub.calls[0].Peer.(*tg.InputPeerChannel)
	if !ok {
		t.Fatalf("channel should map to InputPeerChannel, got %T", stub.calls[0].Peer)
	}
	if peer.ChannelID != 555 || peer.AccessHash != 0xBEEF {
		t.Fatalf("channel peer: got %+v", peer)
	}
}

// Telegram marks nothing in Saved Messages as outgoing — the flag means
// "sent to somebody else" — so without the account's own id every message
// there reads as the peer's: labelled with the chat's name and refused by
// the editor. Seen live on 05.09.2026 on the first send into it.
func TestConvertMessage_SavedMessagesAreYours(t *testing.T) {
	t.Parallel()

	self := &Self{}
	self.Set(8385)
	m := &tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: 8385}, Date: 1, Message: "note to self"}
	m.FromID = &tg.PeerUser{UserID: 8385}
	m.Flags.Set(8)
	if got := convertMessage(m, 8385, self); !got.Outgoing {
		t.Fatalf("a message in the self chat is not the account's: %+v", got)
	}
	if got := convertMessage(m, 8385, nil); got.Outgoing {
		t.Fatalf("without the id, the flag alone decided: %+v", got)
	}
	other := &tg.Message{ID: 2, PeerID: &tg.PeerUser{UserID: 42}, Date: 1, Message: "from a friend"}
	if got := convertMessage(other, 42, self); got.Outgoing {
		t.Fatalf("a message in another chat became the account's: %+v", got)
	}
}
