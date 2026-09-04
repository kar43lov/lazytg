package sync

import (
	"context"
	"errors"
	"testing"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
)

type fakeEditor struct {
	calls []editCall
	err   error
}

type editCall struct {
	chatID    int64
	messageID int64
	text      string
}

func (f *fakeEditor) Edit(_ context.Context, chatID, messageID int64, text string) error {
	f.calls = append(f.calls, editCall{chatID, messageID, text})
	return f.err
}

type fakeDeleter struct {
	calls  []deleteCall
	err    error
	report int
}

type deleteCall struct {
	chatID int64
	ids    []int64
	revoke bool
}

func (f *fakeDeleter) Delete(_ context.Context, chatID int64, ids []int64, revoke bool) (int, error) {
	f.calls = append(f.calls, deleteCall{chatID, ids, revoke})
	return f.report, f.err
}

type fakeActionStore struct {
	msg    domain.Message
	readEr error
	saved  []domain.Message
	saveEr error
}

func (f *fakeActionStore) Message(_ context.Context, _, _ int64) (domain.Message, error) {
	return f.msg, f.readEr
}

func (f *fakeActionStore) SaveMessage(_ context.Context, m domain.Message) error {
	f.saved = append(f.saved, m)
	return f.saveEr
}

type recordingBus struct{ events []events.Event }

func (b *recordingBus) Publish(ev events.Event) { b.events = append(b.events, ev) }

func TestActionService_Edit_RefusesSomebodyElsesMessage(t *testing.T) {
	t.Parallel()

	editor := &fakeEditor{}
	store := &fakeActionStore{msg: domain.Message{ID: 7, ChatID: 1, Outgoing: false}}
	svc := NewActionService(editor, nil, store, nil, nil)

	err := svc.Edit(context.Background(), 1, 7, "rewritten")
	if !errors.Is(err, ErrNotEditable) {
		t.Fatalf("Edit of an incoming message = %v, want ErrNotEditable", err)
	}
	// The point of the local check is that it costs no round trip.
	if len(editor.calls) != 0 {
		t.Fatalf("refused edit still called the server: %+v", editor.calls)
	}
	if len(store.saved) != 0 {
		t.Fatalf("refused edit still wrote the mirror: %+v", store.saved)
	}
}

// The direction flag is the whole basis of the ownership check: Telegram
// omits from_id in a 1:1 dialog, so an id comparison would answer "unknown"
// in the most common chat there is.
func TestActionService_Edit_AllowsAnOutgoingMessageWithNoFromID(t *testing.T) {
	t.Parallel()

	editor := &fakeEditor{}
	store := &fakeActionStore{msg: domain.Message{ID: 7, ChatID: 1, FromID: 0, Outgoing: true, Text: "before"}}
	bus := &recordingBus{}
	svc := NewActionService(editor, nil, store, bus, nil)

	if err := svc.Edit(context.Background(), 1, 7, "after"); err != nil {
		t.Fatalf("Edit of own message: %v", err)
	}
	if len(editor.calls) != 1 || editor.calls[0].text != "after" {
		t.Fatalf("server call = %+v, want one call carrying the new text", editor.calls)
	}
	if len(store.saved) != 1 || store.saved[0].Text != "after" {
		t.Fatalf("mirror = %+v, want the row rewritten", store.saved)
	}
	if len(bus.events) != 1 {
		t.Fatalf("bus = %+v, want one MessageEdited", bus.events)
	}
}

// The server writes first. A refused edit must leave the mirror showing what
// the message actually says — anything else is a client that lies about the
// state of the conversation.
func TestActionService_Edit_LeavesTheMirrorAloneWhenTheServerRefuses(t *testing.T) {
	t.Parallel()

	editor := &fakeEditor{err: errors.New("MESSAGE_EDIT_TIME_EXPIRED")}
	store := &fakeActionStore{msg: domain.Message{ID: 7, ChatID: 1, Outgoing: true, Text: "before"}}
	bus := &recordingBus{}
	svc := NewActionService(editor, nil, store, bus, nil)

	if err := svc.Edit(context.Background(), 1, 7, "after"); err == nil {
		t.Fatal("Edit should surface the server's refusal")
	}
	if len(store.saved) != 0 {
		t.Fatalf("mirror was written despite the refusal: %+v", store.saved)
	}
	if len(bus.events) != 0 {
		t.Fatalf("bus announced an edit that did not happen: %+v", bus.events)
	}
}

func TestActionService_Delete_AnnouncesOnlyAfterTheServerAgrees(t *testing.T) {
	t.Parallel()

	t.Run("refused", func(t *testing.T) {
		t.Parallel()
		deleter := &fakeDeleter{err: errors.New("MESSAGE_DELETE_FORBIDDEN")}
		bus := &recordingBus{}
		svc := NewActionService(nil, deleter, &fakeActionStore{}, bus, nil)

		if err := svc.Delete(context.Background(), 1, []int64{7, 8}, true); err == nil {
			t.Fatal("Delete should surface the refusal")
		}
		// This is the case that matters: announcing here would remove the
		// rows locally while they stay on every other device, with nothing
		// on screen saying so.
		if len(bus.events) != 0 {
			t.Fatalf("bus announced a deletion the server refused: %+v", bus.events)
		}
	})

	t.Run("accepted", func(t *testing.T) {
		t.Parallel()
		deleter := &fakeDeleter{report: 2}
		bus := &recordingBus{}
		svc := NewActionService(nil, deleter, &fakeActionStore{}, bus, nil)

		if err := svc.Delete(context.Background(), 1, []int64{7, 8}, true); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if len(bus.events) != 1 {
			t.Fatalf("bus = %+v, want one MessagesDeleted", bus.events)
		}
		ev, ok := bus.events[0].(events.MessagesDeleted)
		if !ok {
			t.Fatalf("event type = %T, want events.MessagesDeleted", bus.events[0])
		}
		if ev.ChatID != 1 || len(ev.MessageIDs) != 2 {
			t.Fatalf("event = %+v, want chat 1 and both ids", ev)
		}
	})
}

// revoke is carried through rather than decided in core: "delete for me" and
// "delete for everyone" are different acts and only the user knows which.
func TestActionService_Delete_PassesTheRevokeChoiceThrough(t *testing.T) {
	t.Parallel()

	for _, revoke := range []bool{true, false} {
		deleter := &fakeDeleter{}
		svc := NewActionService(nil, deleter, &fakeActionStore{}, nil, nil)
		if err := svc.Delete(context.Background(), 1, []int64{7}, revoke); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if len(deleter.calls) != 1 || deleter.calls[0].revoke != revoke {
			t.Fatalf("revoke=%v was not passed through: %+v", revoke, deleter.calls)
		}
	}
}

func TestActionService_ReportsBeingOffline(t *testing.T) {
	t.Parallel()

	svc := NewActionService(nil, nil, &fakeActionStore{}, nil, nil)
	if err := svc.Edit(context.Background(), 1, 2, "x"); err == nil {
		t.Fatal("Edit with no editor should report that it is not connected")
	}
	if err := svc.Delete(context.Background(), 1, []int64{2}, true); err == nil {
		t.Fatal("Delete with no deleter should report that it is not connected")
	}
}
