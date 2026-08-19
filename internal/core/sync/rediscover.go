package sync

import (
	"context"
	"log/slog"
	stdsync "sync"
	"time"

	"github.com/kar43lov/lazytg/internal/core/events"
)

// DialogSyncer is the piece Rediscoverer drives — *DialogsService in
// production, a counter in tests.
type DialogSyncer interface {
	Sync(ctx context.Context) (int, error)
}

// defaultRediscoverCooldown is the shortest gap between two dialog syncs
// triggered by a discovery. A new conversation is a rare, human-paced event,
// and one dialog fetch per occurrence is what an ordinary client does; the
// cooldown exists for the case that is not human-paced — being added to
// several chats at once, or a burst from contacts the mirror does not know.
const defaultRediscoverCooldown = 30 * time.Second

// defaultRediscoverTimeout bounds one triggered sync so a hung request cannot
// hold the cooldown stamp hostage.
const defaultRediscoverTimeout = 30 * time.Second

// Rediscoverer refreshes the chat list when the live path invents a chat.
//
// A chat created from an update carries what an update knows: an id and a
// peer kind. No title, no unread count, no username. Dialog sync is the only
// source of those and it runs once at startup — so before this existed, a
// conversation that started while lazytg was open showed as "chat 275641346"
// in the list, and as the sender's name in the thread, until the next launch.
// Observed live on 19.08.2026.
type Rediscoverer struct {
	syncer   DialogSyncer
	bus      *events.Bus
	log      *slog.Logger
	cooldown time.Duration
	timeout  time.Duration
	now      func() time.Time

	mu   stdsync.Mutex
	last time.Time
}

// NewRediscoverer wires the service. Zero cooldown or timeout means the
// package default; log may be nil.
func NewRediscoverer(syncer DialogSyncer, bus *events.Bus, log *slog.Logger, cooldown, timeout time.Duration) *Rediscoverer {
	if log == nil {
		log = slog.New(noopHandler{})
	}
	if cooldown <= 0 {
		cooldown = defaultRediscoverCooldown
	}
	if timeout <= 0 {
		timeout = defaultRediscoverTimeout
	}
	return &Rediscoverer{
		syncer:   syncer,
		bus:      bus,
		log:      log,
		cooldown: cooldown,
		timeout:  timeout,
		now:      time.Now,
	}
}

// Start subscribes to the bus and returns a channel closed when the loop
// exits, matching LiveService and ReadService so the cmd layer wires all
// three the same way.
func (r *Rediscoverer) Start(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	if r.syncer == nil || r.bus == nil {
		close(done)
		return done
	}
	ch := r.bus.Subscribe(ctx)
	// Depth one: a dialog sync fetches the whole list, so one queued refresh
	// covers every discovery that arrives while it runs. The split matters
	// because a sync takes seconds (it paces itself between pages) and
	// Bus.Publish drops events for a subscriber whose buffer is full — doing
	// it inside the read loop would throw away unrelated events for as long
	// as the sync lasted.
	trigger := make(chan struct{}, 1)
	var wg stdsync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer close(trigger)
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				if _, ok := ev.(events.ChatDiscovered); !ok {
					continue
				}
				select {
				case trigger <- struct{}{}:
				default:
				}
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range trigger {
			r.refresh(ctx)
		}
	}()

	go func() {
		wg.Wait()
		close(done)
	}()
	return done
}

// refresh runs a dialog sync unless one ran recently.
//
// The cooldown is stamped before the call rather than after: a sync that
// takes ten seconds should not let a discovery landing at second nine start
// a second one alongside it.
func (r *Rediscoverer) refresh(ctx context.Context) {
	now := r.now()
	r.mu.Lock()
	if !r.last.IsZero() && now.Sub(r.last) < r.cooldown {
		r.mu.Unlock()
		return
	}
	r.last = now
	r.mu.Unlock()

	syncCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	stored, err := r.syncer.Sync(syncCtx)
	if err != nil {
		r.log.Warn("rediscover: chat list refresh failed", "err", err)
		return
	}
	r.log.Info("rediscover: chat list refreshed after a new chat appeared", "chats", stored)
}
