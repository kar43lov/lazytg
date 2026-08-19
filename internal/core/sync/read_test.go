package sync

import (
	"context"
	"errors"
	stdsync "sync"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
)

// fakeMarker records what was acknowledged to Telegram, and can be told to
// fail so the "do not clear the badge on failure" rule is testable.
type fakeMarker struct {
	mu      stdsync.Mutex
	calls   []markCall
	err     error
	block   chan struct{}
	entered int
}

type markCall struct {
	chatID int64
	maxID  int64
}

func (f *fakeMarker) MarkRead(_ context.Context, chatID, maxID int64) error {
	f.mu.Lock()
	f.entered++
	block := f.block
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, markCall{chatID: chatID, maxID: maxID})
	return f.err
}

func (f *fakeMarker) enteredCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.entered
}

func (f *fakeMarker) snapshot() []markCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]markCall(nil), f.calls...)
}

// fakeReadStore serves a fixed newest message and records unread clears.
type fakeReadStore struct {
	mu       stdsync.Mutex
	newest   []domain.Message
	getErr   error
	cleared  []int64
	clearErr error
}

func (f *fakeReadStore) GetMessages(_ context.Context, _ int64, _, _ int) ([]domain.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.newest, f.getErr
}

func (f *fakeReadStore) ClearUnread(_ context.Context, chatID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleared = append(f.cleared, chatID)
	return f.clearErr
}

func (f *fakeReadStore) clearedSnapshot() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.cleared...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestReadService_MarksAnOpenedChatRead covers the behaviour the client
// simply did not have: opening a conversation left it unread on every other
// device, because nothing ever called readHistory.
func TestReadService_MarksAnOpenedChatRead(t *testing.T) {
	t.Parallel()
	bus := events.New()
	marker := &fakeMarker{}
	store := &fakeReadStore{newest: []domain.Message{{ID: 25, ChatID: 275641346}}}
	svc := NewReadService(marker, store, bus, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := svc.Start(ctx)

	bus.Publish(events.ChatOpened{ChatID: 275641346})

	waitFor(t, "the chat to be acknowledged", func() bool { return len(marker.snapshot()) == 1 })
	call := marker.snapshot()[0]
	if call.chatID != 275641346 || call.maxID != 25 {
		t.Fatalf("MarkRead called with %+v, want the opened chat and its newest message", call)
	}
	waitFor(t, "the local badge to clear", func() bool { return len(store.clearedSnapshot()) == 1 })

	cancel()
	<-done
}

// TestReadService_DoesNotRepeatItself pins the ban-risk half. Re-entering a
// conversation is something a user does constantly, and one outgoing request
// per visit would be a traffic pattern no ordinary client produces.
func TestReadService_DoesNotRepeatItself(t *testing.T) {
	t.Parallel()
	bus := events.New()
	marker := &fakeMarker{}
	store := &fakeReadStore{newest: []domain.Message{{ID: 25}}}
	svc := NewReadService(marker, store, bus, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := svc.Start(ctx)

	for i := 0; i < 3; i++ {
		bus.Publish(events.ChatOpened{ChatID: 1})
	}
	waitFor(t, "the first acknowledgement", func() bool { return len(marker.snapshot()) >= 1 })
	waitFor(t, "all three opens to be processed", func() bool { return len(store.clearedSnapshot()) >= 1 })

	// Give any redundant call time to land before asserting its absence.
	time.Sleep(50 * time.Millisecond)
	if calls := marker.snapshot(); len(calls) != 1 {
		t.Fatalf("MarkRead called %d times for three opens of the same chat, want 1", len(calls))
	}

	cancel()
	<-done
}

// TestReadService_KeepsTheBadgeWhenTheServerRefuses covers the one ordering
// that matters on failure: a chat shown as read while the server still holds
// it unread is worse than a stale badge, because nothing on screen says so.
func TestReadService_KeepsTheBadgeWhenTheServerRefuses(t *testing.T) {
	t.Parallel()
	bus := events.New()
	marker := &fakeMarker{err: errors.New("network is down")}
	store := &fakeReadStore{newest: []domain.Message{{ID: 25}}}
	svc := NewReadService(marker, store, bus, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := svc.Start(ctx)

	bus.Publish(events.ChatOpened{ChatID: 1})
	waitFor(t, "the failing acknowledgement", func() bool { return len(marker.snapshot()) == 1 })

	time.Sleep(50 * time.Millisecond)
	if cleared := store.clearedSnapshot(); len(cleared) != 0 {
		t.Fatalf("the local unread counter was cleared despite the server refusing: %v", cleared)
	}

	cancel()
	<-done
}

// TestReadService_SkipsAnEmptyChat guards against acknowledging a chat with
// nothing in it, which would be a request with no message id to report.
func TestReadService_SkipsAnEmptyChat(t *testing.T) {
	t.Parallel()
	bus := events.New()
	marker := &fakeMarker{}
	store := &fakeReadStore{}
	svc := NewReadService(marker, store, bus, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := svc.Start(ctx)

	bus.Publish(events.ChatOpened{ChatID: 1})
	time.Sleep(50 * time.Millisecond)
	if calls := marker.snapshot(); len(calls) != 0 {
		t.Fatalf("MarkRead called for a chat with no messages: %+v", calls)
	}

	cancel()
	<-done
}

// TestReadService_SurvivesTrafficDuringASlowAcknowledgement pins why the bus
// reader and the network call live in separate goroutines.
//
// events.Bus.Publish is a non-blocking send: an event aimed at a subscriber
// whose buffer is full is dropped silently. Doing the readHistory request
// inside the read loop meant that for the whole round trip, ordinary incoming
// messages filled that buffer — and the next ChatOpened fell on the floor,
// leaving the chat the user was looking at unread on every other device. The
// flood below is what a busy account produces in a second.
func TestReadService_SurvivesTrafficDuringASlowAcknowledgement(t *testing.T) {
	t.Parallel()
	bus := events.New()
	release := make(chan struct{})
	marker := &fakeMarker{block: release}
	store := &fakeReadStore{newest: []domain.Message{{ID: 25}}}
	svc := NewReadService(marker, store, bus, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := svc.Start(ctx)

	bus.Publish(events.ChatOpened{ChatID: 1})
	waitFor(t, "the first acknowledgement to reach the network", func() bool {
		return marker.enteredCount() == 1
	})

	// Far more than one subscriber buffer holds, all while the first request
	// is still in flight.
	floodBus(bus, 500)
	bus.Publish(events.ChatOpened{ChatID: 2})

	close(release)
	waitFor(t, "the second chat to be acknowledged", func() bool {
		for _, c := range marker.snapshot() {
			if c.chatID == 2 {
				return true
			}
		}
		return false
	})

	cancel()
	<-done
}

// floodBus publishes ordinary message traffic in batches, pausing between
// them the way a network stream does. The pacing is the point: Bus.Publish
// drops on a full subscriber buffer no matter what, so a tight loop would
// prove only that a producer can outrun any consumer. Paced, the buffer stays
// clear as long as the subscriber keeps reading — and fills solid the moment
// it stops to wait for a server.
func floodBus(bus *events.Bus, n int) {
	for i := 0; i < n; i++ {
		bus.Publish(events.MessageReceived{ChatID: 1, MessageID: int64(i + 1), Date: time.Now()})
		if (i+1)%25 == 0 {
			time.Sleep(time.Millisecond)
		}
	}
}
