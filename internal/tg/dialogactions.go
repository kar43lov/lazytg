package tg

import (
	"context"
	"fmt"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	coresync "github.com/kar43lov/lazytg/internal/core/sync"
)

// DialogAPI is the slice of the MTProto client the dialog actions need.
// An interface so the tests can hand in a stub that records the request.
type DialogAPI interface {
	AccountUpdateNotifySettings(ctx context.Context, request *tg.AccountUpdateNotifySettingsRequest) (bool, error)
	MessagesToggleDialogPin(ctx context.Context, request *tg.MessagesToggleDialogPinRequest) (bool, error)
	MessagesMarkDialogUnread(ctx context.Context, request *tg.MessagesMarkDialogUnreadRequest) (bool, error)
}

// DialogActor changes the three things a person does to a chat from the
// list without opening it: mutes it, pins it, marks it unread. Each is one
// request on one keypress, over a chat that already exists, which is why
// none of them goes through the send guard — nothing here creates a
// message.
type DialogActor struct {
	api   DialogAPI
	peers PeerResolver
}

// NewDialogActor wires an actor over api and peers.
func NewDialogActor(api DialogAPI, peers PeerResolver) *DialogActor {
	return &DialogActor{api: api, peers: peers}
}

// muteForever is the date Telegram's own clients send for "mute forever":
// the last second of a signed 32-bit clock. Sent as it is, so the value
// that comes back on the next sync is the one that went out.
const muteForever = 2147483647

// Mute silences a chat until the given time; the zero time unmutes, and a
// time past 2038 is sent as Telegram's "forever".
func (a *DialogActor) Mute(ctx context.Context, chatID int64, until time.Time) error {
	if a == nil || a.api == nil {
		return fmt.Errorf("mute: no MTProto client for chat %d", chatID)
	}
	input, err := a.inputPeer(ctx, chatID)
	if err != nil {
		return fmt.Errorf("mute: %w", err)
	}
	settings := tg.InputPeerNotifySettings{}
	switch {
	case until.IsZero():
		settings.SetMuteUntil(0)
	case until.Unix() >= muteForever:
		settings.SetMuteUntil(muteForever)
	default:
		settings.SetMuteUntil(int(until.Unix()))
	}
	_, err = a.api.AccountUpdateNotifySettings(ctx, &tg.AccountUpdateNotifySettingsRequest{
		Peer:     &tg.InputNotifyPeer{Peer: input},
		Settings: settings,
	})
	return wrapDialogErr("account.updateNotifySettings", chatID, err)
}

// Pin puts a chat at the top of the list, or takes it back out.
func (a *DialogActor) Pin(ctx context.Context, chatID int64, pinned bool) error {
	if a == nil || a.api == nil {
		return fmt.Errorf("pin: no MTProto client for chat %d", chatID)
	}
	input, err := a.inputPeer(ctx, chatID)
	if err != nil {
		return fmt.Errorf("pin: %w", err)
	}
	_, err = a.api.MessagesToggleDialogPin(ctx, &tg.MessagesToggleDialogPinRequest{
		Pinned: pinned,
		Peer:   &tg.InputDialogPeer{Peer: input},
	})
	return wrapDialogErr("messages.toggleDialogPin", chatID, err)
}

// MarkUnread sets or clears the by-hand unread dot.
func (a *DialogActor) MarkUnread(ctx context.Context, chatID int64, unread bool) error {
	if a == nil || a.api == nil {
		return fmt.Errorf("mark unread: no MTProto client for chat %d", chatID)
	}
	input, err := a.inputPeer(ctx, chatID)
	if err != nil {
		return fmt.Errorf("mark unread: %w", err)
	}
	_, err = a.api.MessagesMarkDialogUnread(ctx, &tg.MessagesMarkDialogUnreadRequest{
		Unread: unread,
		Peer:   &tg.InputDialogPeer{Peer: input},
	})
	return wrapDialogErr("messages.markDialogUnread", chatID, err)
}

func (a *DialogActor) inputPeer(ctx context.Context, chatID int64) (tg.InputPeerClass, error) {
	peer, err := a.peers.Resolve(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("resolve peer %d: %w", chatID, err)
	}
	return buildInputPeer(peer.ID, peer.AccessHash, string(peer.Type))
}

func wrapDialogErr(call string, chatID int64, err error) error {
	if err == nil {
		return nil
	}
	if d, ok := tgerr.AsFloodWait(err); ok {
		return &coresync.FloodWaitError{RetryAfter: d}
	}
	return fmt.Errorf("%s chat=%d: %w", call, chatID, err)
}
