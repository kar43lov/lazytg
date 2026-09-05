package tg

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/tg"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

type stubDialogAPI struct {
	notify []*tg.AccountUpdateNotifySettingsRequest
	pin    []*tg.MessagesToggleDialogPinRequest
	unread []*tg.MessagesMarkDialogUnreadRequest
}

func (s *stubDialogAPI) AccountUpdateNotifySettings(_ context.Context, r *tg.AccountUpdateNotifySettingsRequest) (bool, error) {
	s.notify = append(s.notify, r)
	return true, nil
}

func (s *stubDialogAPI) MessagesToggleDialogPin(_ context.Context, r *tg.MessagesToggleDialogPinRequest) (bool, error) {
	s.pin = append(s.pin, r)
	return true, nil
}

func (s *stubDialogAPI) MessagesMarkDialogUnread(_ context.Context, r *tg.MessagesMarkDialogUnreadRequest) (bool, error) {
	s.unread = append(s.unread, r)
	return true, nil
}

func TestDialogActor_SendsEachRequestOnce(t *testing.T) {
	t.Parallel()

	api := &stubDialogAPI{}
	actor := NewDialogActor(api, &stubResolver{peer: domain.Peer{ID: 99, Type: domain.ChatTypePrivate, AccessHash: 5}})
	ctx := context.Background()

	if err := actor.Mute(ctx, 99, time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Mute: %v", err)
	}
	if err := actor.Mute(ctx, 99, time.Time{}); err != nil {
		t.Fatalf("unmute: %v", err)
	}
	if err := actor.Pin(ctx, 99, true); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if err := actor.MarkUnread(ctx, 99, true); err != nil {
		t.Fatalf("MarkUnread: %v", err)
	}

	if len(api.notify) != 2 || len(api.pin) != 1 || len(api.unread) != 1 {
		t.Fatalf("requests: notify=%d pin=%d unread=%d", len(api.notify), len(api.pin), len(api.unread))
	}
	if until, _ := api.notify[0].Settings.GetMuteUntil(); until != muteForever {
		t.Fatalf("a date past 2038 was sent as %d, want Telegram's forever", until)
	}
	if until, _ := api.notify[1].Settings.GetMuteUntil(); until != 0 {
		t.Fatalf("unmute sent %d, want 0", until)
	}
	if p, ok := api.notify[0].Peer.(*tg.InputNotifyPeer); !ok || p.Peer.(*tg.InputPeerUser).UserID != 99 {
		t.Fatalf("mute addressed %+v", api.notify[0].Peer)
	}
	if !api.pin[0].Pinned || api.pin[0].Peer.(*tg.InputDialogPeer).Peer.(*tg.InputPeerUser).AccessHash != 5 {
		t.Fatalf("pin request %+v", api.pin[0])
	}
	if !api.unread[0].Unread {
		t.Fatalf("mark unread request %+v", api.unread[0])
	}
}
