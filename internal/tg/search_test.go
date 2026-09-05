package tg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/kar43lov/lazytg/internal/core/domain"
	coresearch "github.com/kar43lov/lazytg/internal/core/search"
	coresync "github.com/kar43lov/lazytg/internal/core/sync"
)

type stubSearchAPI struct {
	got *tg.MessagesSearchGlobalRequest
	res tg.MessagesMessagesClass
	err error
}

func (s *stubSearchAPI) MessagesSearchGlobal(_ context.Context, req *tg.MessagesSearchGlobalRequest) (tg.MessagesMessagesClass, error) {
	s.got = req
	return s.res, s.err
}

func TestSearcher_ConvertsHitsAndDescribesTheirChats(t *testing.T) {
	t.Parallel()

	inChannel := &tg.Message{ID: 7, PeerID: &tg.PeerChannel{ChannelID: 500}, Date: 1_700_000_000, Message: "budget is late"}
	inChannel.SetFromID(&tg.PeerUser{UserID: 42})
	api := &stubSearchAPI{res: &tg.MessagesMessagesSlice{
		Count: 2,
		Messages: []tg.MessageClass{
			inChannel,
			&tg.Message{ID: 9, PeerID: &tg.PeerUser{UserID: 42}, Date: 1_700_000_100, Message: "budget again"},
			&tg.MessageService{ID: 10, PeerID: &tg.PeerChannel{ChannelID: 500}},
		},
		Users: []tg.UserClass{&tg.User{ID: 42, AccessHash: 7, FirstName: "Pavel"}},
		Chats: []tg.ChatClass{&tg.Channel{ID: 500, AccessHash: 9, Title: "Team", Megagroup: true}},
	}}
	after := time.Unix(1_600_000_000, 0)
	res, err := NewSearcher(api, nil).SearchGlobal(context.Background(), coresearch.RemoteQuery{Text: "budget", After: after, Limit: 5})
	if err != nil {
		t.Fatalf("SearchGlobal: %v", err)
	}
	if api.got.Q != "budget" || api.got.Limit != 5 || api.got.MinDate != int(after.Unix()) || api.got.MaxDate != 0 {
		t.Fatalf("request = %+v", api.got)
	}
	if len(res.Messages) != 2 {
		t.Fatalf("messages = %+v", res.Messages)
	}
	if m := res.Messages[0]; m.ID != 7 || m.ChatID != 500 || m.FromID != 42 || m.Text != "budget is late" {
		t.Fatalf("channel hit = %+v", m)
	}
	if m := res.Messages[1]; m.ID != 9 || m.ChatID != 42 || m.FromID != 42 {
		t.Fatalf("private hit = %+v", m)
	}
	if len(res.Chats) != 2 || res.Chats[0].ID != 500 || res.Chats[0].Title != "Team" || res.Chats[0].Type != domain.ChatTypeSupergroup {
		t.Fatalf("chats = %+v", res.Chats)
	}
	if res.Chats[1].ID != 42 || res.Chats[1].Title != "Pavel" {
		t.Fatalf("chats = %+v", res.Chats)
	}
	if len(res.Peers) != 2 || res.Peers[0].AccessHash != 9 || res.Peers[1].AccessHash != 7 {
		t.Fatalf("peers = %+v", res.Peers)
	}
}

func TestSearcher_Errors(t *testing.T) {
	t.Parallel()

	api := &stubSearchAPI{err: tgerr.New(420, "FLOOD_WAIT_30")}
	_, err := NewSearcher(api, nil).SearchGlobal(context.Background(), coresearch.RemoteQuery{Text: "x", Limit: 1})
	var flood *coresync.FloodWaitError
	if !errors.As(err, &flood) || flood.RetryAfter != 30*time.Second {
		t.Fatalf("flood wait not mapped: %v", err)
	}

	api = &stubSearchAPI{err: errors.New("boom")}
	if _, err := NewSearcher(api, nil).SearchGlobal(context.Background(), coresearch.RemoteQuery{Text: "x", Limit: 1}); err == nil {
		t.Fatal("a failed request must be reported")
	}

	api = &stubSearchAPI{res: &tg.MessagesMessagesNotModified{}}
	res, err := NewSearcher(api, nil).SearchGlobal(context.Background(), coresearch.RemoteQuery{Text: "x", Limit: 1})
	if err != nil || len(res.Messages) != 0 {
		t.Fatalf("not-modified = %+v, %v", res, err)
	}
}
