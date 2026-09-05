package tg

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

type stubForwardAPI struct {
	calls []*tg.MessagesForwardMessagesRequest
	err   error
}

func (s *stubForwardAPI) MessagesForwardMessages(_ context.Context, r *tg.MessagesForwardMessagesRequest) (tg.UpdatesClass, error) {
	s.calls = append(s.calls, r)
	if s.err != nil {
		return nil, s.err
	}
	return &tg.Updates{}, nil
}

// peersByID answers differently per chat, which forwarding needs and nothing
// before it did: the source and the target are two different peers and the
// request names both.
type peersByID map[int64]domain.Peer

func (p peersByID) Resolve(_ context.Context, id int64) (domain.Peer, error) {
	peer, ok := p[id]
	if !ok {
		return domain.Peer{}, errors.New("unknown peer")
	}
	return peer, nil
}

func twoPeers() peersByID {
	return peersByID{
		42: {ID: 42, Type: domain.ChatTypePrivate, AccessHash: 11},
		99: {ID: 99, Type: domain.ChatTypeChannel, AccessHash: 22},
	}
}

func TestForwarder_NamesBothPeers(t *testing.T) {
	t.Parallel()

	api := &stubForwardAPI{}
	f := NewForwarder(api, twoPeers())

	if err := f.Forward(context.Background(), 42, 99, []int64{7, 8}, false); err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if len(api.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(api.calls))
	}
	req := api.calls[0]
	if _, ok := req.FromPeer.(*tg.InputPeerUser); !ok {
		t.Fatalf("FromPeer = %T, want InputPeerUser", req.FromPeer)
	}
	ch, ok := req.ToPeer.(*tg.InputPeerChannel)
	if !ok {
		t.Fatalf("ToPeer = %T, want InputPeerChannel", req.ToPeer)
	}
	if ch.ChannelID != 99 || ch.AccessHash != 22 {
		t.Fatalf("ToPeer = %+v, want channel 99 with its access hash", ch)
	}
	if len(req.ID) != 2 || req.ID[0] != 7 || req.ID[1] != 8 {
		t.Fatalf("ids = %v, want [7 8]", req.ID)
	}
}

// Telegram deduplicates sends by random id. One id for two messages means the
// server drops the second copy, and the user sees a forward that half
// happened.
func TestForwarder_GivesEveryMessageItsOwnRandomID(t *testing.T) {
	t.Parallel()

	api := &stubForwardAPI{}
	f := NewForwarder(api, twoPeers())

	if err := f.Forward(context.Background(), 42, 99, []int64{1, 2, 3, 4}, false); err != nil {
		t.Fatalf("Forward: %v", err)
	}
	req := api.calls[0]
	if len(req.RandomID) != 4 {
		t.Fatalf("random ids = %d, want one per message", len(req.RandomID))
	}
	seen := make(map[int64]bool, 4)
	for _, id := range req.RandomID {
		if id == 0 {
			t.Fatal("a random id came out zero")
		}
		if seen[id] {
			t.Fatalf("random id %d used twice", id)
		}
		seen[id] = true
	}
}

func TestForwarder_PassesDropAuthorThrough(t *testing.T) {
	t.Parallel()

	api := &stubForwardAPI{}
	f := NewForwarder(api, twoPeers())

	if err := f.Forward(context.Background(), 42, 99, []int64{1}, true); err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if !api.calls[0].DropAuthor {
		t.Fatal("drop_author was not set")
	}
}

func TestForwarder_NothingToDoIsNotARequest(t *testing.T) {
	t.Parallel()

	api := &stubForwardAPI{}
	f := NewForwarder(api, twoPeers())

	if err := f.Forward(context.Background(), 42, 99, nil, false); err != nil {
		t.Fatalf("Forward(nil): %v", err)
	}
	if len(api.calls) != 0 {
		t.Fatal("an empty forward reached the wire")
	}
}

func TestForwarder_ReportsAnUnknownTarget(t *testing.T) {
	t.Parallel()

	api := &stubForwardAPI{}
	f := NewForwarder(api, peersByID{42: {ID: 42, Type: domain.ChatTypePrivate}})

	err := f.Forward(context.Background(), 42, 99, []int64{1}, false)
	if err == nil {
		t.Fatal("forwarding to an unresolvable chat succeeded")
	}
	if len(api.calls) != 0 {
		t.Fatal("the request went out despite an unresolved target")
	}
}
