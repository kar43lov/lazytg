package sync

import (
	"context"
	"errors"
	stdsync "sync"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/events"
)

// countingSyncer counts dialog syncs and can fail on demand.
type countingSyncer struct {
	mu      stdsync.Mutex
	calls   int
	err     error
	block   chan struct{}
	entered int
}

func (c *countingSyncer) Sync(context.Context) (int, error) {
	c.mu.Lock()
	c.entered++
	block := c.block
	c.mu.Unlock()
	if block != nil {
		<-block
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return 0, c.err
}

func (c *countingSyncer) enteredCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entered
}

func (c *countingSyncer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// TestRediscoverer_RefreshesOnANewChat covers the placeholder the live path
// leaves behind: a chat created from an update has no title, and dialog sync
// is the only thing that can give it one. Before this the placeholder
// survived until the next launch — seen live on 19.08.2026 as
// "chat 275641346" in the list and in the thread.
func TestRediscoverer_RefreshesOnANewChat(t *testing.T) {
	t.Parallel()
	bus := events.New()
	syncer := &countingSyncer{}
	r := NewRediscoverer(syncer, bus, nil, time.Millisecond, time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := r.Start(ctx)

	bus.Publish(events.ChatDiscovered{ChatID: 275641346})
	waitFor(t, "the chat list to be refreshed", func() bool { return syncer.count() == 1 })

	cancel()
	<-done
}

// TestRediscoverer_HonoursTheCooldown is the ban-risk half. Being added to
// several chats at once, or a burst from unknown contacts, would otherwise
// produce one getDialogs per message — a request pattern no human produces.
func TestRediscoverer_HonoursTheCooldown(t *testing.T) {
	t.Parallel()
	bus := events.New()
	syncer := &countingSyncer{}
	r := NewRediscoverer(syncer, bus, nil, time.Hour, time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := r.Start(ctx)

	for i := 0; i < 5; i++ {
		bus.Publish(events.ChatDiscovered{ChatID: int64(i + 1)})
	}
	waitFor(t, "the first refresh", func() bool { return syncer.count() >= 1 })
	time.Sleep(50 * time.Millisecond)

	if got := syncer.count(); got != 1 {
		t.Fatalf("dialog syncs = %d for five discoveries inside the cooldown, want 1", got)
	}

	cancel()
	<-done
}

// TestRediscoverer_IgnoresUnrelatedEvents keeps ordinary traffic from turning
// into dialog fetches: every incoming message crosses this bus.
func TestRediscoverer_IgnoresUnrelatedEvents(t *testing.T) {
	t.Parallel()
	bus := events.New()
	syncer := &countingSyncer{}
	r := NewRediscoverer(syncer, bus, nil, time.Millisecond, time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := r.Start(ctx)

	bus.Publish(events.MessageReceived{ChatID: 1, MessageID: 1, Date: time.Now()})
	bus.Publish(events.ChatOpened{ChatID: 1})
	time.Sleep(50 * time.Millisecond)

	if got := syncer.count(); got != 0 {
		t.Fatalf("dialog syncs = %d for events that discovered nothing, want 0", got)
	}

	cancel()
	<-done
}

// TestRediscoverer_RetriesAfterAFailedRefresh checks that a failed sync does
// not burn the cooldown for good — the next discovery must be able to try
// again once the cooldown elapses.
func TestRediscoverer_RetriesAfterAFailedRefresh(t *testing.T) {
	t.Parallel()
	bus := events.New()
	syncer := &countingSyncer{err: errors.New("network is down")}
	r := NewRediscoverer(syncer, bus, nil, time.Millisecond, time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := r.Start(ctx)

	bus.Publish(events.ChatDiscovered{ChatID: 1})
	waitFor(t, "the first attempt", func() bool { return syncer.count() >= 1 })
	time.Sleep(5 * time.Millisecond)
	bus.Publish(events.ChatDiscovered{ChatID: 2})
	waitFor(t, "the retry", func() bool { return syncer.count() >= 2 })

	cancel()
	<-done
}

// TestRediscoverer_SurvivesTrafficDuringASync is the same lesson as in
// ReadService, and worse here: a dialog sync walks pages with deliberate
// pauses between them, so it holds the goroutine for seconds. Running it
// inside the bus read loop meant every event published during those seconds
// hit a full subscriber buffer and was dropped — including the discovery of
// the next new chat, which is exactly what this service exists to catch.
func TestRediscoverer_SurvivesTrafficDuringASync(t *testing.T) {
	t.Parallel()
	bus := events.New()
	release := make(chan struct{})
	syncer := &countingSyncer{block: release}
	r := NewRediscoverer(syncer, bus, nil, time.Millisecond, time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := r.Start(ctx)

	bus.Publish(events.ChatDiscovered{ChatID: 1})
	waitFor(t, "the first sync to start", func() bool { return syncer.enteredCount() == 1 })

	floodBus(bus, 500)
	bus.Publish(events.ChatDiscovered{ChatID: 2})

	close(release)
	waitFor(t, "the second discovery to trigger a sync", func() bool { return syncer.count() >= 2 })

	cancel()
	<-done
}
