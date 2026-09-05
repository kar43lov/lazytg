package tg

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/tg"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
)

// privateMsg builds a 1:1 message the way Telegram actually sends one: no
// from_id at all. The field is flag-conditional, and in a private dialog it
// carries no information — the sender follows from `out` and the peer — so the
// wire format omits it. makeMsg in history_test.go sets it explicitly, which
// is why the defect below survived every existing test.
func privateMsg(id int, peerUserID int64, text string, out bool) *tg.Message {
	m := &tg.Message{
		ID:      id,
		PeerID:  &tg.PeerUser{UserID: peerUserID},
		Date:    int(time.Date(2026, 8, 19, 15, 12, id, 0, time.UTC).Unix()),
		Message: text,
	}
	m.Out = out
	return m
}

// TestConvertMessage_PrivateChatKeepsItsSender is the regression for the
// second live run (19.08.2026): opening a private chat pulled its history
// through messages.getHistory, every row came back with from_id 0, and the
// thread pane relabelled messages the user had just read by name as "system".
// Re-opening the chat overwrote the good rows a live short-form update had
// written, so the conversation degraded the more it was used.
func TestConvertMessage_PrivateChatKeepsItsSender(t *testing.T) {
	t.Parallel()
	const peer = 275641346

	in := convertMessage(privateMsg(28, peer, "йцу", false), peer, nil)
	if in.FromID != peer {
		t.Errorf("incoming FromID = %d, want the peer %d", in.FromID, peer)
	}
	if in.Outgoing {
		t.Errorf("incoming message marked outgoing")
	}

	out := convertMessage(privateMsg(29, peer, "asd", true), peer, nil)
	if !out.Outgoing {
		t.Errorf("outgoing message not marked outgoing")
	}
	if out.FromID == peer {
		t.Errorf("outgoing FromID = %d, must not be attributed to the peer", out.FromID)
	}
}

// TestConvertMessage_GroupSenderIsUntouched guards the other half: in a group
// from_id is present and meaningful, and the private-chat fallback must not
// overwrite it with the chat id.
func TestConvertMessage_GroupSenderIsUntouched(t *testing.T) {
	t.Parallel()
	const chatID = -1001234
	const member = 555

	m := &tg.Message{
		ID:      7,
		PeerID:  &tg.PeerChat{ChatID: 1234},
		Date:    int(time.Date(2026, 8, 19, 15, 12, 0, 0, time.UTC).Unix()),
		Message: "in a group",
	}
	m.SetFromID(&tg.PeerUser{UserID: member})

	got := convertMessage(m, chatID, nil)
	if got.FromID != member {
		t.Errorf("FromID = %d, want the member %d", got.FromID, member)
	}
}

// TestUpdatesDispatcher_PrivateMessageCarriesSenderAndDirection covers the
// live path, which has the same hole: a full-form UpdateNewMessage in a
// private chat has no from_id either, so the event reached storage with a
// zero sender and the row was written that way.
func TestUpdatesDispatcher_PrivateMessageCarriesSenderAndDirection(t *testing.T) {
	t.Parallel()
	const peer = 275641346

	bus, ch := newTestBus(t)
	d := NewUpdatesDispatcher(bus, nil)

	upd := &tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdateNewMessage{Message: privateMsg(31, peer, "123123123", false), Pts: 1, PtsCount: 1},
	}}
	if err := d.HandlerFunc().Handle(context.Background(), upd); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	ev, ok := receiveOne(t, ch).(events.MessageReceived)
	if !ok {
		t.Fatalf("wrong event type")
	}
	if ev.FromID != peer {
		t.Errorf("FromID = %d, want the peer %d", ev.FromID, peer)
	}
	if ev.ChatType != domain.ChatTypePrivate {
		t.Errorf("ChatType = %q, want private", ev.ChatType)
	}
	if ev.Outgoing {
		t.Errorf("incoming message marked outgoing")
	}
}
