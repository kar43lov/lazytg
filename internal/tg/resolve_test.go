package tg

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/kar43lov/lazytg/internal/core/domain"
	coresync "github.com/kar43lov/lazytg/internal/core/sync"
)

type stubResolveAPI struct {
	got string
	res *tg.ContactsResolvedPeer
	err error
}

func (s *stubResolveAPI) ContactsResolveUsername(_ context.Context, req *tg.ContactsResolveUsernameRequest) (*tg.ContactsResolvedPeer, error) {
	s.got = req.Username
	return s.res, s.err
}

func TestUsernameResolver_UserAndChannel(t *testing.T) {
	t.Parallel()

	api := &stubResolveAPI{res: &tg.ContactsResolvedPeer{
		Peer:  &tg.PeerUser{UserID: 42},
		Users: []tg.UserClass{&tg.User{ID: 42, AccessHash: 7, FirstName: "Pavel", Username: "durov"}},
	}}
	chat, peer, err := NewUsernameResolver(api).ResolveUsername(context.Background(), "@Durov")
	if err != nil {
		t.Fatalf("ResolveUsername: %v", err)
	}
	if api.got != "Durov" {
		t.Fatalf("the @ went to the server: %q", api.got)
	}
	if chat.ID != 42 || chat.Type != domain.ChatTypePrivate || chat.Title != "Pavel" || chat.Username != "durov" {
		t.Fatalf("chat = %+v", chat)
	}
	if peer.ID != 42 || peer.AccessHash != 7 || peer.Type != domain.ChatTypePrivate {
		t.Fatalf("peer = %+v", peer)
	}

	api = &stubResolveAPI{res: &tg.ContactsResolvedPeer{
		Peer:  &tg.PeerChannel{ChannelID: 500},
		Chats: []tg.ChatClass{&tg.Channel{ID: 500, AccessHash: 9, Title: "News", Username: "news", Megagroup: true}},
	}}
	chat, peer, err = NewUsernameResolver(api).ResolveUsername(context.Background(), "news")
	if err != nil {
		t.Fatalf("ResolveUsername: %v", err)
	}
	if chat.Type != domain.ChatTypeSupergroup || chat.Title != "News" || peer.AccessHash != 9 {
		t.Fatalf("chat = %+v peer = %+v", chat, peer)
	}
}

func TestUsernameResolver_Errors(t *testing.T) {
	t.Parallel()

	_, _, err := NewUsernameResolver(&stubResolveAPI{err: tgerr.New(400, "USERNAME_NOT_OCCUPIED")}).ResolveUsername(context.Background(), "nobody")
	if !errors.Is(err, coresync.ErrNoSuchUsername) {
		t.Fatalf("unoccupied handle: %v", err)
	}
	_, _, err = NewUsernameResolver(&stubResolveAPI{err: tgerr.New(420, "FLOOD_WAIT_30")}).ResolveUsername(context.Background(), "x_y_z_w")
	var flood *coresync.FloodWaitError
	if !errors.As(err, &flood) {
		t.Fatalf("flood wait not surfaced: %v", err)
	}
	_, _, err = NewUsernameResolver(&stubResolveAPI{res: &tg.ContactsResolvedPeer{Peer: &tg.PeerUser{UserID: 1}}}).ResolveUsername(context.Background(), "ghost")
	if err == nil {
		t.Fatal("a peer the server did not describe was accepted")
	}
	if _, _, err := NewUsernameResolver(&stubResolveAPI{}).ResolveUsername(context.Background(), "@"); err == nil {
		t.Fatal("an empty handle went to the server")
	}
}
