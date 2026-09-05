package tg

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/tg"

	"github.com/kar43lov/lazytg/internal/core/events"
)

// A forwarded message says who wrote it: the name the response gave for
// the origin, the header's own name when the origin hid their account,
// the channel plus the post's author for a channel post.
func TestForwardOf(t *testing.T) {
	t.Parallel()

	dir := directoryOf(
		[]tg.UserClass{&tg.User{ID: 7, FirstName: "Ann", LastName: "Lee"}},
		[]tg.ChatClass{&tg.Channel{ID: 500, Title: "News"}, &tg.Chat{ID: 9, Title: "Group"}},
	)
	at := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)

	fromUser := &tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: 1}}
	hdr := tg.MessageFwdHeader{Date: int(at.Unix())}
	hdr.SetFromID(&tg.PeerUser{UserID: 7})
	fromUser.SetFwdFrom(hdr)
	if f := forwardOf(fromUser, dir); f == nil || f.From != "Ann Lee" || f.FromID != 7 || !f.Date.Equal(at) {
		t.Fatalf("from a user: %+v", f)
	}

	hidden := &tg.Message{ID: 2, PeerID: &tg.PeerUser{UserID: 1}}
	hh := tg.MessageFwdHeader{}
	hh.SetFromName("Somebody")
	hidden.SetFwdFrom(hh)
	if f := forwardOf(hidden, dir); f == nil || f.From != "Somebody" || f.FromID != 0 {
		t.Fatalf("hidden origin: %+v", f)
	}

	post := &tg.Message{ID: 3, PeerID: &tg.PeerUser{UserID: 1}}
	ph := tg.MessageFwdHeader{}
	ph.SetFromID(&tg.PeerChannel{ChannelID: 500})
	ph.SetPostAuthor("Editor")
	post.SetFwdFrom(ph)
	if f := forwardOf(post, dir); f == nil || f.From != "News (Editor)" || f.FromID != 500 {
		t.Fatalf("channel post: %+v", f)
	}

	unknown := &tg.Message{ID: 4, PeerID: &tg.PeerUser{UserID: 1}}
	uh := tg.MessageFwdHeader{}
	uh.SetFromID(&tg.PeerUser{UserID: 99})
	unknown.SetFwdFrom(uh)
	if f := forwardOf(unknown, nil); f == nil || f.From != "" || f.FromID != 99 {
		t.Fatalf("origin the response did not name: %+v", f)
	}
	if forwardOf(&tg.Message{ID: 5}, dir) != nil {
		t.Fatal("a message written where it stands has an origin")
	}
}

// The origin and the pinned flag arrive from the history page and from a
// live update container alike, named from the objects that came along.
func TestOriginAndPinned_ArriveWithHistoryAndUpdates(t *testing.T) {
	t.Parallel()

	m, _ := makeMsg(1, 42, "forwarded words", 0).(*tg.Message)
	hdr := tg.MessageFwdHeader{}
	hdr.SetFromID(&tg.PeerChannel{ChannelID: 500})
	m.SetFwdFrom(hdr)
	m.Pinned = true
	chats := []tg.ChatClass{&tg.Channel{ID: 500, Title: "News"}}

	msgs := decodeHistory(&tg.MessagesMessages{Messages: []tg.MessageClass{m}, Chats: chats}, 42, 10, nil)
	if len(msgs) != 1 || msgs[0].Forwarded == nil || msgs[0].Forwarded.From != "News" || !msgs[0].Pinned {
		t.Fatalf("history: %+v", msgs)
	}

	bus, ch := newTestBus(t)
	d := NewUpdatesDispatcher(bus, nil)
	if err := d.HandlerFunc().Handle(context.Background(), &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: m}},
		Chats:   chats,
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	ev, ok := receiveOne(t, ch).(events.MessageReceived)
	if !ok || ev.Forwarded == nil || ev.Forwarded.From != "News" || !ev.Pinned {
		t.Fatalf("update: %+v", ev)
	}

	if err := d.HandlerFunc().Handle(context.Background(), &tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdatePinnedMessages{Pinned: true, Peer: &tg.PeerUser{UserID: 42}, Messages: []int{1, 2}},
		&tg.UpdatePinnedChannelMessages{Pinned: false, ChannelID: 500, Messages: []int{9}},
	}}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	p, ok := receiveOne(t, ch).(events.MessagesPinned)
	if !ok || p.ChatID != 42 || !p.Pinned || len(p.IDs) != 2 || p.IDs[1] != 2 {
		t.Fatalf("pinned = %+v", p)
	}
	p, ok = receiveOne(t, ch).(events.MessagesPinned)
	if !ok || p.ChatID != 500 || p.Pinned || len(p.IDs) != 1 || p.IDs[0] != 9 {
		t.Fatalf("channel unpinned = %+v", p)
	}
}
