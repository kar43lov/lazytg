package tg

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/tg"

	"github.com/kar43lov/lazytg/internal/core/events"
)

// The distinctions are kept rather than flattened to "typing" because they
// change what the reader should do: recording means wait, typing means keep
// the conversation going.
func TestTypingLabel_NamesTheActivity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		action tg.SendMessageActionClass
		want   string
	}{
		{&tg.SendMessageTypingAction{}, "typing"},
		{&tg.SendMessageRecordAudioAction{}, "recording a voice message"},
		{&tg.SendMessageRecordRoundAction{}, "recording a video message"},
		{&tg.SendMessageUploadPhotoAction{}, "sending a photo"},
		{&tg.SendMessageChooseStickerAction{}, "choosing a sticker"},
		// Not an activity, and the one every client ignores: the cancel
		// notification is not reliably sent, so the indicator expires on
		// a timer instead.
		{&tg.SendMessageCancelAction{}, ""},
		// A kind Telegram adds later lands here. Showing nothing is the
		// right answer for a label this client does not know.
		{&tg.SendMessageHistoryImportAction{}, ""},
	}
	for _, c := range cases {
		if got := typingLabel(c.action); got != c.want {
			t.Errorf("typingLabel(%T) = %q, want %q", c.action, got, c.want)
		}
	}
}

// A private dialog is named by the user typing in it: there is only one other
// person, so they are both the chat and the typer. A group carries both.
func TestDispatcher_PublishesTyping(t *testing.T) {
	t.Parallel()
	bus, ch := newTestBus(t)
	d := NewUpdatesDispatcher(bus, nil)

	upd := &tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdateUserTyping{UserID: 555, Action: &tg.SendMessageTypingAction{}},
	}}
	if err := d.HandlerFunc().Handle(context.Background(), upd); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	select {
	case ev := <-ch:
		typed, ok := ev.(events.PeerTyping)
		if !ok {
			t.Fatalf("published %T, want PeerTyping", ev)
		}
		if typed.ChatID != 555 || typed.FromID != 555 || typed.Action != "typing" {
			t.Fatalf("event = %+v", typed)
		}
		if typed.At.IsZero() {
			t.Fatal("the event carries no time, so it can never expire")
		}
	case <-time.After(time.Second):
		t.Fatal("no typing event published")
	}
}

func TestDispatcher_PublishesGroupTyping(t *testing.T) {
	t.Parallel()
	bus, ch := newTestBus(t)
	d := NewUpdatesDispatcher(bus, nil)

	upd := &tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdateChatUserTyping{
			ChatID: 42,
			FromID: &tg.PeerUser{UserID: 7},
			Action: &tg.SendMessageRecordAudioAction{},
		},
	}}
	if err := d.HandlerFunc().Handle(context.Background(), upd); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	select {
	case ev := <-ch:
		typed := ev.(events.PeerTyping)
		if typed.ChatID != 42 || typed.FromID != 7 {
			t.Fatalf("event = %+v, want chat 42 and person 7", typed)
		}
		if typed.Action != "recording a voice message" {
			t.Fatalf("action = %q", typed.Action)
		}
	case <-time.After(time.Second):
		t.Fatal("no typing event published")
	}
}

// The cancel notification is the one every client ignores: it is not reliably
// sent, so the indicator expires on a timer instead.
func TestDispatcher_SaysNothingForACancel(t *testing.T) {
	t.Parallel()
	bus, ch := newTestBus(t)
	d := NewUpdatesDispatcher(bus, nil)

	upd := &tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdateUserTyping{UserID: 555, Action: &tg.SendMessageCancelAction{}},
	}}
	if err := d.HandlerFunc().Handle(context.Background(), upd); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	select {
	case ev := <-ch:
		t.Fatalf("published %#v for a cancel", ev)
	case <-time.After(50 * time.Millisecond):
	}
}
