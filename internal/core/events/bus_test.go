package events

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestBus_FanOutDeliversToAllSubscribers(t *testing.T) {
	defer goleak.VerifyNone(t)

	bus := New()

	const n = 3
	ctxs := make([]context.Context, n)
	cancels := make([]context.CancelFunc, n)
	chans := make([]<-chan Event, n)
	for i := 0; i < n; i++ {
		ctxs[i], cancels[i] = context.WithCancel(context.Background())
		chans[i] = bus.Subscribe(ctxs[i])
	}

	want := MessageReceived{ChatID: 42, MessageID: 7, Text: "hello", FromID: 1, Date: time.Unix(1700000000, 0).UTC()}
	bus.Publish(want)

	for i, ch := range chans {
		select {
		case got := <-ch:
			gotMsg, ok := got.(MessageReceived)
			if !ok {
				t.Fatalf("subscriber %d: expected MessageReceived, got %T", i, got)
			}
			if gotMsg != want {
				t.Fatalf("subscriber %d: got %+v, want %+v", i, gotMsg, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timed out waiting for event", i)
		}
	}

	for i := 0; i < n; i++ {
		cancels[i]()
		drainUntilClosed(t, chans[i])
	}
}

func TestBus_SubscriberRemovedAfterContextCancel(t *testing.T) {
	defer goleak.VerifyNone(t)

	bus := New()

	keepCtx, keepCancel := context.WithCancel(context.Background())
	defer keepCancel()
	keep := bus.Subscribe(keepCtx)

	dropCtx, dropCancel := context.WithCancel(context.Background())
	drop := bus.Subscribe(dropCtx)

	dropCancel()
	drainUntilClosed(t, drop)

	want := DialogUpdated{ChatID: 99}
	bus.Publish(want)

	select {
	case got, ok := <-keep:
		if !ok {
			t.Fatal("active subscriber channel closed unexpectedly")
		}
		gotMsg, isDialog := got.(DialogUpdated)
		if !isDialog || gotMsg != want {
			t.Fatalf("unexpected event on active subscriber: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("active subscriber did not receive event")
	}

	select {
	case ev, ok := <-drop:
		if ok {
			t.Fatalf("cancelled subscriber unexpectedly received event: %+v", ev)
		}
	default:
	}

	keepCancel()
	drainUntilClosed(t, keep)
}

func TestBus_ConcurrentPublishAndSubscribe(t *testing.T) {
	defer goleak.VerifyNone(t)

	bus := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
	}()

	const subscribers = 5
	chans := make([]<-chan Event, subscribers)
	for i := 0; i < subscribers; i++ {
		chans[i] = bus.Subscribe(ctx)
	}

	var wg sync.WaitGroup
	const publishers = 4
	const perPublisher = 50
	for p := 0; p < publishers; p++ {
		wg.Add(1)
		go func(pid int) {
			defer wg.Done()
			for i := 0; i < perPublisher; i++ {
				bus.Publish(ConnectionStateChanged{State: "tick"})
			}
		}(p)
	}
	wg.Wait()

	for _, ch := range chans {
		select {
		case ev := <-ch:
			if _, ok := ev.(ConnectionStateChanged); !ok {
				t.Fatalf("expected ConnectionStateChanged, got %T", ev)
			}
		case <-time.After(time.Second):
			t.Fatal("subscriber received no events under concurrent publish")
		}
	}

	cancel()
	for _, ch := range chans {
		drainUntilClosed(t, ch)
	}
}

func drainUntilClosed(t *testing.T, ch <-chan Event) {
	t.Helper()
	timeout := time.After(time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("channel was not closed within timeout")
		}
	}
}
