package events

import (
	"context"
	"sync"
)

// defaultBufferSize is the per-subscriber channel buffer. Events that arrive while a
// subscriber's buffer is full are dropped (Publish is non-blocking).
const defaultBufferSize = 64

// Bus is an in-process pub/sub bus that fans out Events to every active subscriber.
//
// Subscribe returns a channel scoped to a context: when the context is cancelled the
// subscription is removed and the channel is closed. Publish performs a non-blocking
// send to every subscriber, dropping events when a slow consumer's buffer is full so
// that one slow consumer cannot stall the producer.
type Bus struct {
	mu   sync.Mutex
	subs []*subscriber
}

type subscriber struct {
	ch chan Event
}

// New returns a ready-to-use Bus with no subscribers.
func New() *Bus {
	return &Bus{}
}

// Subscribe registers a new subscriber bound to ctx. The returned channel receives
// every event published while the subscription is active; it is closed when ctx is
// cancelled. Each Subscribe call spawns a single goroutine that exits as soon as
// ctx.Done fires.
func (b *Bus) Subscribe(ctx context.Context) <-chan Event {
	sub := &subscriber{ch: make(chan Event, defaultBufferSize)}

	b.mu.Lock()
	b.subs = append(b.subs, sub)
	b.mu.Unlock()

	go func() {
		<-ctx.Done()
		b.mu.Lock()
		for i, s := range b.subs {
			if s == sub {
				b.subs = append(b.subs[:i], b.subs[i+1:]...)
				break
			}
		}
		close(sub.ch)
		b.mu.Unlock()
	}()

	return sub.ch
}

// Publish delivers e to every active subscriber. Delivery is non-blocking: if a
// subscriber's buffer is full the event is dropped for that subscriber only.
func (b *Bus) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.subs {
		select {
		case s.ch <- e:
		default:
		}
	}
}
