package tg

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

type stubActionAPI struct {
	editCalls    []*tg.MessagesEditMessageRequest
	deleteCalls  []*tg.MessagesDeleteMessagesRequest
	channelCalls []*tg.ChannelsDeleteMessagesRequest
}

func (s *stubActionAPI) MessagesEditMessage(_ context.Context, r *tg.MessagesEditMessageRequest) (tg.UpdatesClass, error) {
	s.editCalls = append(s.editCalls, r)
	return &tg.Updates{}, nil
}

func (s *stubActionAPI) MessagesDeleteMessages(_ context.Context, r *tg.MessagesDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error) {
	s.deleteCalls = append(s.deleteCalls, r)
	return &tg.MessagesAffectedMessages{PtsCount: len(r.ID)}, nil
}

func (s *stubActionAPI) ChannelsDeleteMessages(_ context.Context, r *tg.ChannelsDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error) {
	s.channelCalls = append(s.channelCalls, r)
	return &tg.MessagesAffectedMessages{PtsCount: len(r.ID)}, nil
}

type stubPeers struct {
	peer domain.Peer
	err  error
}

func (s stubPeers) Resolve(context.Context, int64) (domain.Peer, error) { return s.peer, s.err }

func TestEditor_SendsTheNewTextForTheResolvedPeer(t *testing.T) {
	t.Parallel()

	api := &stubActionAPI{}
	ed := NewEditor(api, stubPeers{peer: domain.Peer{ID: 42, Type: domain.ChatTypePrivate, AccessHash: 99}})

	if err := ed.Edit(context.Background(), 42, 7, "rewritten"); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if len(api.editCalls) != 1 {
		t.Fatalf("edit calls = %d, want 1", len(api.editCalls))
	}
	req := api.editCalls[0]
	if got, ok := req.GetMessage(); !ok || got != "rewritten" {
		t.Fatalf("message = %q (set=%v), want the new text", got, ok)
	}
	if req.ID != 7 {
		t.Fatalf("id = %d, want 7", req.ID)
	}
	if _, ok := req.Peer.(*tg.InputPeerUser); !ok {
		t.Fatalf("peer = %T, want InputPeerUser for a private chat", req.Peer)
	}
}

// An empty edit is a deletion wearing the wrong gesture. Telegram answers
// MESSAGE_EMPTY, which reads like a bug in the client rather than like the
// user having asked for something they did not mean.
func TestEditor_RefusesAnEmptyEditBeforeSending(t *testing.T) {
	t.Parallel()

	api := &stubActionAPI{}
	ed := NewEditor(api, stubPeers{peer: domain.Peer{Type: domain.ChatTypePrivate}})

	if err := ed.Edit(context.Background(), 1, 2, ""); err == nil {
		t.Fatal("empty edit should be refused")
	}
	if len(api.editCalls) != 0 {
		t.Fatalf("empty edit reached the server: %+v", api.editCalls)
	}
}

// The id space is the whole reason this splits: a channel numbers its own
// messages from one, so id 42 exists in several places and means something
// different in each. Sending the bare-id variant for a channel would delete
// in the wrong space.
func TestDeleter_PicksTheRPCByPeerKind(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		kind       domain.ChatType
		wantBare   int
		wantByChan int
	}{
		{"private", domain.ChatTypePrivate, 1, 0},
		{"basic group", domain.ChatTypeGroup, 1, 0},
		{"supergroup", domain.ChatTypeSupergroup, 0, 1},
		{"channel", domain.ChatTypeChannel, 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			api := &stubActionAPI{}
			d := NewDeleter(api, stubPeers{peer: domain.Peer{ID: 5, Type: tc.kind, AccessHash: 7}})

			if _, err := d.Delete(context.Background(), 5, []int64{42}, true); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if len(api.deleteCalls) != tc.wantBare {
				t.Fatalf("messages.deleteMessages calls = %d, want %d", len(api.deleteCalls), tc.wantBare)
			}
			if len(api.channelCalls) != tc.wantByChan {
				t.Fatalf("channels.deleteMessages calls = %d, want %d", len(api.channelCalls), tc.wantByChan)
			}
		})
	}
}

func TestDeleter_CarriesTheRevokeFlag(t *testing.T) {
	t.Parallel()

	for _, revoke := range []bool{true, false} {
		api := &stubActionAPI{}
		d := NewDeleter(api, stubPeers{peer: domain.Peer{Type: domain.ChatTypePrivate}})
		if _, err := d.Delete(context.Background(), 1, []int64{2}, revoke); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		got := api.deleteCalls[0].GetRevoke()
		if got != revoke {
			t.Fatalf("revoke = %v, want %v", got, revoke)
		}
	}
}

func TestDeleter_EmptyListIsANoOp(t *testing.T) {
	t.Parallel()

	api := &stubActionAPI{}
	d := NewDeleter(api, stubPeers{peer: domain.Peer{Type: domain.ChatTypePrivate}})
	n, err := d.Delete(context.Background(), 1, nil, true)
	if err != nil || n != 0 {
		t.Fatalf("Delete(nil) = (%d, %v), want (0, nil)", n, err)
	}
	if len(api.deleteCalls) != 0 || len(api.channelCalls) != 0 {
		t.Fatal("an empty list should not reach the server")
	}
}

func TestDeleter_UnknownPeerKindIsAnErrorRatherThanAGuess(t *testing.T) {
	t.Parallel()

	api := &stubActionAPI{}
	d := NewDeleter(api, stubPeers{peer: domain.Peer{Type: domain.ChatType("kind-from-the-future")}})
	if _, err := d.Delete(context.Background(), 1, []int64{2}, true); err == nil {
		t.Fatal("an unknown peer kind must not be guessed at")
	}
	if len(api.deleteCalls) != 0 || len(api.channelCalls) != 0 {
		t.Fatal("a guess reached the server")
	}
}
