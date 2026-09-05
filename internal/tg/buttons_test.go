package tg

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
	coresync "github.com/kar43lov/lazytg/internal/core/sync"
)

func TestButtonsFromMessage(t *testing.T) {
	t.Parallel()

	inline := &tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: 1}}
	inline.SetReplyMarkup(&tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{
		{Buttons: []tg.KeyboardButtonClass{
			&tg.KeyboardButtonCallback{Text: "Yes", Data: []byte("yes")},
			&tg.KeyboardButtonURL{Text: "Docs", URL: "https://example.com"},
		}},
		{Buttons: []tg.KeyboardButtonClass{
			&tg.KeyboardButtonCopy{Text: "Copy", CopyText: "token"},
			&tg.KeyboardButtonBuy{Text: "Pay"},
			&tg.KeyboardButton{Text: "Label"},
		}},
	}})
	want := [][]domain.Button{
		{{Text: "Yes", Kind: domain.ButtonCallback, Data: []byte("yes")}, {Text: "Docs", Kind: domain.ButtonURL, URL: "https://example.com"}},
		{{Text: "Copy", Kind: domain.ButtonCopy, URL: "token"}, {Text: "Pay", Kind: domain.ButtonOther}, {Text: "Label", Kind: domain.ButtonOther}},
	}
	if got := ButtonsFromMessage(inline); !reflect.DeepEqual(got, want) {
		t.Fatalf("inline keyboard:\n got %+v\nwant %+v", got, want)
	}

	reply := &tg.Message{ID: 2, PeerID: &tg.PeerUser{UserID: 1}}
	reply.SetReplyMarkup(&tg.ReplyKeyboardMarkup{Rows: []tg.KeyboardButtonRow{
		{Buttons: []tg.KeyboardButtonClass{&tg.KeyboardButton{Text: "/start"}, &tg.KeyboardButton{Text: "/help"}}},
	}})
	wantReply := [][]domain.Button{{{Text: "/start", Kind: domain.ButtonText}, {Text: "/help", Kind: domain.ButtonText}}}
	if got := ButtonsFromMessage(reply); !reflect.DeepEqual(got, wantReply) {
		t.Fatalf("reply keyboard: %+v", got)
	}

	hide := &tg.Message{ID: 3, PeerID: &tg.PeerUser{UserID: 1}}
	hide.SetReplyMarkup(&tg.ReplyKeyboardHide{})
	if ButtonsFromMessage(hide) != nil || ButtonsFromMessage(&tg.Message{ID: 4}) != nil {
		t.Fatal("a hidden keyboard and no keyboard both read as none")
	}
}

type stubBotAPI struct {
	got *tg.MessagesGetBotCallbackAnswerRequest
	res *tg.MessagesBotCallbackAnswer
	err error
}

func (s *stubBotAPI) MessagesGetBotCallbackAnswer(_ context.Context, req *tg.MessagesGetBotCallbackAnswerRequest) (*tg.MessagesBotCallbackAnswer, error) {
	s.got = req
	return s.res, s.err
}

func TestBotActor_PressButton(t *testing.T) {
	t.Parallel()

	resolver := &stubResolver{peer: domain.Peer{ID: 99, Type: domain.ChatTypePrivate, AccessHash: 5}}
	res := &tg.MessagesBotCallbackAnswer{Alert: true}
	res.SetMessage("Done!")
	api := &stubBotAPI{res: res}
	answer, err := NewBotActor(api, resolver).PressButton(context.Background(), 99, 41, []byte("yes"))
	if err != nil {
		t.Fatalf("PressButton: %v", err)
	}
	if answer.Message != "Done!" || !answer.Alert || answer.URL != "" {
		t.Fatalf("answer = %+v", answer)
	}
	if api.got.MsgID != 41 {
		t.Fatalf("msg id = %d", api.got.MsgID)
	}
	if data, ok := api.got.GetData(); !ok || string(data) != "yes" {
		t.Fatalf("data = %q ok=%v", data, ok)
	}
	if user, ok := api.got.Peer.(*tg.InputPeerUser); !ok || user.UserID != 99 || user.AccessHash != 5 {
		t.Fatalf("peer = %#v", api.got.Peer)
	}

	withURL := &tg.MessagesBotCallbackAnswer{}
	withURL.SetURL("https://example.com/next")
	answer, err = NewBotActor(&stubBotAPI{res: withURL}, resolver).PressButton(context.Background(), 99, 41, nil)
	if err != nil || answer.URL != "https://example.com/next" {
		t.Fatalf("url answer = %+v, %v", answer, err)
	}

	answer, err = NewBotActor(&stubBotAPI{err: tgerr.New(400, "BOT_RESPONSE_TIMEOUT")}, resolver).PressButton(context.Background(), 99, 41, nil)
	if err != nil || answer != (coresync.CallbackAnswer{}) {
		t.Fatalf("a bot that only edits the message is not an error: %+v, %v", answer, err)
	}
	_, err = NewBotActor(&stubBotAPI{err: tgerr.New(420, "FLOOD_WAIT_7")}, resolver).PressButton(context.Background(), 99, 41, nil)
	var flood *coresync.FloodWaitError
	if !errors.As(err, &flood) {
		t.Fatalf("flood wait not surfaced: %v", err)
	}
	if _, err := NewBotActor(api, &stubResolver{err: errors.New("unknown peer")}).PressButton(context.Background(), 1, 1, nil); err == nil {
		t.Fatal("an unresolvable peer was pressed")
	}
}

// The keyboard rides on the message from the dialog history and from a
// live update alike.
func TestKeyboard_ArrivesWithHistoryAndUpdates(t *testing.T) {
	t.Parallel()

	m, _ := makeMsg(1, 42, "pick one", 0).(*tg.Message)
	m.SetReplyMarkup(&tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{Buttons: []tg.KeyboardButtonClass{&tg.KeyboardButtonCallback{Text: "A", Data: []byte("a")}}}}})
	msgs := decodeHistory(&tg.MessagesMessages{Messages: []tg.MessageClass{m}}, 42, 10, nil)
	if len(msgs) != 1 || len(msgs[0].Buttons) != 1 || msgs[0].Buttons[0][0].Text != "A" {
		t.Fatalf("history dropped the keyboard: %+v", msgs)
	}

	bus, ch := newTestBus(t)
	d := NewUpdatesDispatcher(bus, nil)
	if err := d.HandlerFunc().Handle(context.Background(), &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: m}}}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	ev, ok := receiveOne(t, ch).(events.MessageReceived)
	if !ok || len(ev.Buttons) != 1 || ev.Buttons[0][0].Kind != domain.ButtonCallback {
		t.Fatalf("update dropped the keyboard: %+v", ev)
	}
}
