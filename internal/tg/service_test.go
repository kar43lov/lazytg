package tg

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/tg"

	"github.com/kar43lov/lazytg/internal/core/events"
)

func TestDescribeAction_WordsTheCommonOnes(t *testing.T) {
	t.Parallel()

	call := &tg.MessageActionPhoneCall{Video: true}
	call.SetDuration(125)
	missed := &tg.MessageActionPhoneCall{Reason: &tg.PhoneCallDiscardReasonMissed{}}
	cases := []struct {
		action tg.MessageActionClass
		want   string
	}{
		{&tg.MessageActionChatCreate{Title: "devs"}, "created the group “devs”"},
		{&tg.MessageActionChatEditTitle{Title: "ops"}, "changed the title to “ops”"},
		{&tg.MessageActionChatAddUser{Users: []int64{1, 2}}, "added 2 members"},
		{&tg.MessageActionChatDeleteUser{UserID: 1}, "removed a member"},
		{&tg.MessageActionPinMessage{}, "pinned a message"},
		{call, "video call, 2 minutes"},
		{missed, "missed call"},
		{&tg.MessageActionSetMessagesTTL{Period: 86400}, "set messages to auto-delete after 1 day"},
		{&tg.MessageActionGiftStars{}, "service: GiftStars"},
		{nil, "service message"},
	}
	for _, tc := range cases {
		if got := describeAction(tc.action); got != tc.want {
			t.Errorf("describeAction(%T) = %q, want %q", tc.action, got, tc.want)
		}
	}
}

// A group that was only ever created and joined used to decode to zero
// messages and look identical to an empty chat. Its service lines are rows
// now, with the actor as the sender and the reader's own as outgoing.
func TestUpdatesDispatcher_PublishesServiceMessages(t *testing.T) {
	t.Parallel()
	bus, ch := newTestBus(t)
	d := NewUpdatesDispatcher(bus, nil)

	svc := &tg.MessageService{ID: 3, PeerID: &tg.PeerChat{ChatID: 9}, Date: int(time.Now().Unix()), Action: &tg.MessageActionChatJoinedByLink{}}
	svc.SetFromID(&tg.PeerUser{UserID: 42})
	upd := &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: svc}, &tg.UpdateNewMessage{Message: svc}}}
	if err := d.HandlerFunc().Handle(context.Background(), upd); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, ok := receiveOne(t, ch).(events.MessageReceived)
	if !ok {
		t.Fatal("no message published for the service line")
	}
	if got.ChatID != 9 || got.MessageID != 3 || got.FromID != 42 || got.Text != "joined by invite link" || got.ChatType == "" {
		t.Fatalf("published %+v", got)
	}
	expectNone(t, ch)
}
