package tg

import (
	"context"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"

	"github.com/gotd/td/tg"

	"github.com/kar43lov/lazytg/internal/core/events"
)

// receiveOne drains a single event from ch with a generous timeout. The
// helper exists so tests can express "I expect exactly one event" without
// shouting at goroutines that race the bus delivery.
func receiveOne(t *testing.T, ch <-chan events.Event) events.Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatalf("bus channel closed before event")
		}
		return ev
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for event")
		return nil
	}
}

// expectNone asserts the bus stays silent for a short window. We use a
// 50 ms ceiling so tests stay fast yet large enough to outrun the bus
// scheduler on shared CI runners.
func expectNone(t *testing.T, ch <-chan events.Event) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event: %T %+v", ev, ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func newTestBus(t *testing.T) (*events.Bus, <-chan events.Event) {
	t.Helper()
	bus := events.New()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return bus, bus.Subscribe(ctx)
}

func makeUpdateNewMessage(id int, peerUserID int64, text string, date time.Time) *tg.UpdateNewMessage {
	m := &tg.Message{
		ID:      id,
		PeerID:  &tg.PeerUser{UserID: peerUserID},
		Date:    int(date.Unix()),
		Message: text,
	}
	m.SetFromID(&tg.PeerUser{UserID: peerUserID})
	return &tg.UpdateNewMessage{Message: m, Pts: 1, PtsCount: 1}
}

func TestUpdatesDispatcher_PublishesNewMessage(t *testing.T) {
	t.Parallel()
	bus, ch := newTestBus(t)
	d := NewUpdatesDispatcher(bus, nil)

	date := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	upd := &tg.Updates{Updates: []tg.UpdateClass{makeUpdateNewMessage(101, 555, "hi", date)}}
	if err := d.HandlerFunc().Handle(context.Background(), upd); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	ev := receiveOne(t, ch)
	got, ok := ev.(events.MessageReceived)
	if !ok {
		t.Fatalf("event type = %T, want MessageReceived", ev)
	}
	if got.ChatID != 555 || got.MessageID != 101 || got.Text != "hi" || got.FromID != 555 {
		t.Fatalf("event mismatch: %+v", got)
	}
	if !got.Date.Equal(date) {
		t.Fatalf("date mismatch: got=%v want=%v", got.Date, date)
	}
}

func TestUpdatesDispatcher_DedupsRepeatedMessage(t *testing.T) {
	t.Parallel()
	bus, ch := newTestBus(t)
	d := NewUpdatesDispatcher(bus, nil)

	upd := &tg.Updates{Updates: []tg.UpdateClass{
		makeUpdateNewMessage(1, 42, "hi", time.Now().UTC()),
	}}
	if err := d.HandlerFunc().Handle(context.Background(), upd); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	receiveOne(t, ch)

	if err := d.HandlerFunc().Handle(context.Background(), upd); err != nil {
		t.Fatalf("second Handle: %v", err)
	}
	expectNone(t, ch)
}

func TestUpdatesDispatcher_HandlesChannelMessage(t *testing.T) {
	t.Parallel()
	bus, ch := newTestBus(t)
	d := NewUpdatesDispatcher(bus, nil)

	m := &tg.Message{
		ID:      77,
		PeerID:  &tg.PeerChannel{ChannelID: 9999},
		Date:    int(time.Now().Unix()),
		Message: "channel post",
	}
	upd := &tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdateNewChannelMessage{Message: m, Pts: 1, PtsCount: 1},
	}}
	if err := d.HandlerFunc().Handle(context.Background(), upd); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := receiveOne(t, ch).(events.MessageReceived)
	if got.ChatID != 9999 || got.MessageID != 77 {
		t.Fatalf("event mismatch: %+v", got)
	}
}

func TestUpdatesDispatcher_FlattensShortMessage(t *testing.T) {
	t.Parallel()
	bus, ch := newTestBus(t)
	d := NewUpdatesDispatcher(bus, nil)

	short := &tg.UpdateShortMessage{
		ID:      55,
		UserID:  333,
		Date:    int(time.Now().Unix()),
		Message: "short hi",
		Pts:     1,
	}
	if err := d.HandlerFunc().Handle(context.Background(), short); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := receiveOne(t, ch).(events.MessageReceived)
	if got.ChatID != 333 || got.MessageID != 55 || got.Text != "short hi" {
		t.Fatalf("event mismatch: %+v", got)
	}
}

func TestUpdatesDispatcher_IgnoresUnknown(t *testing.T) {
	t.Parallel()
	bus, ch := newTestBus(t)
	d := NewUpdatesDispatcher(bus, nil)

	upd := &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateConfig{}}}
	if err := d.HandlerFunc().Handle(context.Background(), upd); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	expectNone(t, ch)
}

func TestUpdatesDispatcher_DedupCacheBoundedSize(t *testing.T) {
	t.Parallel()
	bus := events.New()
	d := NewUpdatesDispatcher(bus, nil)
	// Fill the LRU past its capacity and assert the index size is bounded.
	for i := 1; i <= dedupCacheCapacity+50; i++ {
		updates := []tg.UpdateClass{makeUpdateNewMessage(i, 1, "x", time.Unix(int64(i), 0))}
		if err := d.HandlerFunc().Handle(context.Background(), &tg.Updates{Updates: updates}); err != nil {
			t.Fatalf("Handle %d: %v", i, err)
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if got := d.cache.Len(); got != dedupCacheCapacity {
		t.Fatalf("cache.Len = %d, want %d", got, dedupCacheCapacity)
	}
	if got := len(d.index); got != dedupCacheCapacity {
		t.Fatalf("len(index) = %d, want %d", got, dedupCacheCapacity)
	}
}

// An edit is the same message id arriving again by design, so it must get
// past the cache that stops one arrival rendering twice — and it must say
// it is an edit, so nothing downstream counts it as new.
func TestUpdatesDispatcher_PublishesEditsPastTheDedup(t *testing.T) {
	t.Parallel()
	bus, ch := newTestBus(t)
	d := NewUpdatesDispatcher(bus, nil)

	when := time.Now().UTC()
	if err := d.HandlerFunc().Handle(context.Background(), &tg.Updates{Updates: []tg.UpdateClass{
		makeUpdateNewMessage(1, 42, "hi", when),
	}}); err != nil {
		t.Fatalf("new: %v", err)
	}
	receiveOne(t, ch)

	edited := &tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: 42}, Date: int(when.Unix()), Message: "hi, fixed"}
	edited.SetEditDate(int(when.Unix()) + 60)
	edited.SetEntities([]tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 4, Length: 5}})
	if err := d.HandlerFunc().Handle(context.Background(), &tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdateEditMessage{Message: edited},
	}}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	ev := receiveOne(t, ch)
	got, ok := ev.(events.MessageReceived)
	if !ok {
		t.Fatalf("published %T, want MessageReceived", ev)
	}
	if !got.Edited || got.Text != "hi, fixed" || got.MessageID != 1 {
		t.Fatalf("edit published as %+v", got)
	}
	if got.EditDate.IsZero() || len(got.Entities) != 1 || got.Entities[0].Kind != domain.EntityBold {
		t.Fatalf("edit lost its date or spans: %+v", got)
	}
}
