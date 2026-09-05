package sync

import (
	"context"
	"errors"
	"reflect"
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
	entities  []domain.Entity
}

func (f *fakeEditor) Edit(_ context.Context, chatID, messageID int64, text string, entities []domain.Entity) error {
	f.calls = append(f.calls, editCall{chatID, messageID, text, entities})
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
	msg       domain.Message
	readEr    error
	saved     []domain.Message
	saveEr    error
	reactions []domain.Reaction
	reactEr   error
}

func (f *fakeActionStore) SetReactions(_ context.Context, _, _ int64, rs []domain.Reaction) error {
	f.reactions = append([]domain.Reaction(nil), rs...)
	return f.reactEr
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
	svc := NewActionService(editor, nil, nil, nil, store, nil, nil)

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
	svc := NewActionService(editor, nil, nil, nil, store, bus, nil)

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
	svc := NewActionService(editor, nil, nil, nil, store, bus, nil)

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
		svc := NewActionService(nil, deleter, nil, nil, &fakeActionStore{}, bus, nil)

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
		svc := NewActionService(nil, deleter, nil, nil, &fakeActionStore{}, bus, nil)

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
		svc := NewActionService(nil, deleter, nil, nil, &fakeActionStore{}, nil, nil)
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

	svc := NewActionService(nil, nil, nil, nil, &fakeActionStore{}, nil, nil)
	if err := svc.Edit(context.Background(), 1, 2, "x"); err == nil {
		t.Fatal("Edit with no editor should report that it is not connected")
	}
	if err := svc.Delete(context.Background(), 1, []int64{2}, true); err == nil {
		t.Fatal("Delete with no deleter should report that it is not connected")
	}
}

// fakeForwarder records what reached the wire.
type fakeForwarder struct {
	calls []fwdCall
	err   error
}

type fwdCall struct {
	from, to   int64
	ids        []int64
	dropAuthor bool
}

func (f *fakeForwarder) Forward(_ context.Context, from, to int64, ids []int64, dropAuthor bool) error {
	f.calls = append(f.calls, fwdCall{from, to, append([]int64(nil), ids...), dropAuthor})
	return f.err
}

func TestActionService_ForwardPassesThrough(t *testing.T) {
	t.Parallel()

	fwd := &fakeForwarder{}
	svc := NewActionService(nil, nil, fwd, nil, &fakeActionStore{}, nil, nil)

	if err := svc.Forward(context.Background(), 42, 99, []int64{1, 2}, true); err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if len(fwd.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(fwd.calls))
	}
	if got := fwd.calls[0]; got.from != 42 || got.to != 99 || !got.dropAuthor {
		t.Fatalf("call = %+v", got)
	}
}

// The forwarded copies arrive through the live update path like any other
// message. Announcing them here would produce a second copy the moment that
// update landed.
func TestActionService_ForwardAnnouncesNothing(t *testing.T) {
	t.Parallel()

	bus := &recordingBus{}
	svc := NewActionService(nil, nil, &fakeForwarder{}, nil, &fakeActionStore{}, bus, nil)

	if err := svc.Forward(context.Background(), 42, 99, []int64{1}, false); err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if got := len(bus.events); got != 0 {
		t.Fatalf("%d events published for a forward: %v", got, bus.events)
	}
}

func TestActionService_ForwardOfflineSaysSo(t *testing.T) {
	t.Parallel()

	svc := NewActionService(nil, nil, nil, nil, &fakeActionStore{}, nil, nil)
	if err := svc.Forward(context.Background(), 42, 99, []int64{1}, false); err == nil {
		t.Fatal("forwarding with no client succeeded")
	}
}

type fakeReactor struct {
	calls []reactCall
	out   []domain.Reaction
	err   error
}

type reactCall struct {
	chatID    int64
	messageID int64
	emoticon  string
}

func (f *fakeReactor) React(_ context.Context, chatID, messageID int64, emoticon string) ([]domain.Reaction, error) {
	f.calls = append(f.calls, reactCall{chatID, messageID, emoticon})
	return f.out, f.err
}

func TestActionService_React_StoresWhatTheServerReturned(t *testing.T) {
	t.Parallel()

	reactor := &fakeReactor{out: []domain.Reaction{{Emoticon: "👍", Count: 5, Chosen: true}}}
	store := &fakeActionStore{}
	bus := &recordingBus{}
	svc := NewActionService(nil, nil, nil, reactor, store, bus, nil)

	if err := svc.React(context.Background(), 42, 7, "👍"); err != nil {
		t.Fatalf("React: %v", err)
	}
	// The count is the server's, not an increment of the local copy: counts
	// belong to everybody and drift the moment two people react at once.
	if len(store.reactions) != 1 || store.reactions[0].Count != 5 {
		t.Fatalf("stored %v", store.reactions)
	}
	if len(bus.events) != 1 {
		t.Fatalf("published %v, want one MessageReactionsChanged", bus.events)
	}
	ev, ok := bus.events[0].(events.MessageReactionsChanged)
	if !ok || ev.MessageID != 7 || len(ev.Reactions) != 1 {
		t.Fatalf("event = %#v", bus.events[0])
	}
}

// Announcing before the server agreed would put a count on screen that never
// happened — and a channel refusing reactions is an ordinary outcome.
func TestActionService_React_SaysNothingWhenTheServerRefuses(t *testing.T) {
	t.Parallel()

	reactor := &fakeReactor{err: errors.New("REACTION_INVALID")}
	bus := &recordingBus{}
	svc := NewActionService(nil, nil, nil, reactor, &fakeActionStore{}, bus, nil)

	if err := svc.React(context.Background(), 42, 7, "👍"); err == nil {
		t.Fatal("React swallowed the refusal")
	}
	if len(bus.events) != 0 {
		t.Fatalf("published %v after a refusal", bus.events)
	}
}

// An empty emoticon is a removal, and the empty result that comes back with
// it is the truth rather than a missing answer.
func TestActionService_React_RemovalStoresTheEmptySet(t *testing.T) {
	t.Parallel()

	reactor := &fakeReactor{out: nil}
	store := &fakeActionStore{reactions: []domain.Reaction{{Emoticon: "👍", Count: 1, Chosen: true}}}
	bus := &recordingBus{}
	svc := NewActionService(nil, nil, nil, reactor, store, bus, nil)

	if err := svc.React(context.Background(), 42, 7, ""); err != nil {
		t.Fatalf("React: %v", err)
	}
	if len(store.reactions) != 0 {
		t.Fatalf("the removal left %v behind", store.reactions)
	}
	if len(bus.events) != 1 {
		t.Fatalf("a removal published %v", bus.events)
	}
}

// The server accepted it but told us nothing. Announcing a guess is the one
// way to put a wrong count on screen; the push update will say.
func TestActionService_React_SilentResponseAnnouncesNothing(t *testing.T) {
	t.Parallel()

	reactor := &fakeReactor{out: nil}
	store := &fakeActionStore{}
	bus := &recordingBus{}
	svc := NewActionService(nil, nil, nil, reactor, store, bus, nil)

	if err := svc.React(context.Background(), 42, 7, "👍"); err != nil {
		t.Fatalf("React: %v", err)
	}
	if len(bus.events) != 0 {
		t.Fatalf("published %v from a silent response", bus.events)
	}
}

func TestActionService_React_OfflineSaysSo(t *testing.T) {
	t.Parallel()

	svc := NewActionService(nil, nil, nil, nil, &fakeActionStore{}, nil, nil)
	if err := svc.React(context.Background(), 42, 7, "👍"); err == nil {
		t.Fatal("reacting with no client succeeded")
	}
}

// blockingLimiter refuses, which is how a test tells "the guard was consulted"
// from "the guard exists".
type blockingLimiter struct{ calls int }

func (l *blockingLimiter) Wait(context.Context) error {
	l.calls++
	return errors.New("rate limited")
}

// Forwarding creates messages in another chat. It is the third way this
// client can create one, and the guard is documented as covering sends — a
// path around it makes the documentation wrong.
func TestActionService_ForwardPassesTheSendGuard(t *testing.T) {
	t.Parallel()

	limiter := &blockingLimiter{}
	fwd := &fakeForwarder{}
	svc := NewActionService(nil, nil, fwd, nil, &fakeActionStore{}, nil, nil).WithRateLimiter(limiter)

	if err := svc.Forward(context.Background(), 42, 99, []int64{1}, false); err == nil {
		t.Fatal("the forward went out despite a refusing limiter")
	}
	if limiter.calls != 1 {
		t.Fatalf("the limiter was consulted %d times", limiter.calls)
	}
	if len(fwd.calls) != 0 {
		t.Fatal("the request reached the wire")
	}
}

// Reacting acts on a message that already exists and is one request per
// deliberate keypress; a token bucket in front of "undo my reaction" would
// make the interface feel broken without changing what a human produces.
func TestActionService_ReactIsNotRateLimited(t *testing.T) {
	t.Parallel()

	limiter := &blockingLimiter{}
	svc := NewActionService(nil, nil, nil, &fakeReactor{}, &fakeActionStore{}, nil, nil).WithRateLimiter(limiter)

	if err := svc.React(context.Background(), 42, 7, "👍"); err != nil {
		t.Fatalf("React: %v", err)
	}
	if limiter.calls != 0 {
		t.Fatalf("the limiter was consulted for a reaction")
	}
}

// The composer hands over markup. The server and the mirror get the plain
// text with its spans, so an edited message stores the same shape a fresh
// one does — and says when it was edited.
func TestActionService_Edit_TurnsMarkupIntoSpans(t *testing.T) {
	t.Parallel()

	editor := &fakeEditor{}
	store := &fakeActionStore{msg: domain.Message{ID: 7, ChatID: 1, Outgoing: true, Text: "before"}}
	bus := &recordingBus{}
	svc := NewActionService(editor, nil, nil, nil, store, bus, nil)

	if err := svc.Edit(context.Background(), 1, 7, "**after** all"); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	want := []domain.Entity{{Kind: domain.EntityBold, Offset: 0, Length: 5}}
	call := editor.calls[0]
	if call.text != "after all" || !reflect.DeepEqual(call.entities, want) {
		t.Fatalf("server call = %+v, want the plain text with a bold span", call)
	}
	saved := store.saved[0]
	if saved.Text != "after all" || !reflect.DeepEqual(saved.Entities, want) || saved.EditDate.IsZero() {
		t.Fatalf("mirror = %+v, want text, spans and an edit date", saved)
	}
	ev, ok := bus.events[0].(events.MessageEdited)
	if !ok || ev.Text != "after all" || !reflect.DeepEqual(ev.Entities, want) || ev.EditDate.IsZero() {
		t.Fatalf("published %+v, want the spans and the date", bus.events[0])
	}
}
