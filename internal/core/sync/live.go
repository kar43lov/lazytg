package sync

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/pgmac/lazytg/internal/core/domain"
	"github.com/pgmac/lazytg/internal/core/events"
)

// LiveStore is the storage surface LiveService relies on. Defining it as
// an interface (rather than reaching for *sqlite.Repo directly) keeps the
// service unit-testable with an in-memory fake and lets future storage
// implementations slot in without churning the call sites.
type LiveStore interface {
	SaveMessage(ctx context.Context, m domain.Message) error
}

// LiveService persists incoming MessageReceived events into the local
// SQLite mirror so the UI sees the same state offline as online. The
// service does not re-publish anything onto the bus — the UI subscribes
// to the same MessageReceived stream directly, which avoids fan-out
// amplification and keeps the latency measurement honest.
//
// LastIngestLatency exposes the most recent end-to-end latency between
// receiving the bus event and finishing the SaveMessage call. It is
// stored in milliseconds (int64) so call sites and benchmarks can read
// the value lock-free via atomic.LoadInt64.
type LiveService struct {
	store             LiveStore
	bus               *events.Bus
	log               *slog.Logger
	now               func() time.Time
	lastIngestLatency atomic.Int64
}

// NewLiveService wires a service. log may be nil — a no-op logger is used
// in that case so unit tests stay free of plumbing noise.
func NewLiveService(store LiveStore, bus *events.Bus, log *slog.Logger) *LiveService {
	if log == nil {
		log = slog.New(noopHandler{})
	}
	return &LiveService{
		store: store,
		bus:   bus,
		log:   log,
		now:   time.Now,
	}
}

// LastIngestLatency returns the most recent end-to-end latency between
// receiving a MessageReceived event and writing it to storage. The value
// is the wall-clock duration on the goroutine that processed the event;
// callers should treat it as a snapshot, not an aggregate.
func (s *LiveService) LastIngestLatency() time.Duration {
	return time.Duration(s.lastIngestLatency.Load()) * time.Millisecond
}

// Run subscribes to the bus and persists every MessageReceived event
// until ctx is cancelled. Subscription is done synchronously before the
// drain loop starts so that a Publish on the same goroutine that called
// Run cannot race ahead of the subscription. Returns ctx.Err once the
// loop unwinds so call sites can wire it into errgroup-style supervisors.
//
// Errors from store.SaveMessage are logged and otherwise swallowed —
// dropping a single event must never take the whole loop down. Storage
// outages surface separately via the StorageStateChanged event in
// Task 4 (degradation detector).
func (s *LiveService) Run(ctx context.Context) error {
	ch := s.bus.Subscribe(ctx)
	return s.drain(ctx, ch)
}

// Start is the non-blocking sibling of Run: it subscribes to the bus on
// the calling goroutine (so a follow-up Publish is guaranteed to be
// observed) and spawns the drain loop in the background. The returned
// channel is closed once the loop exits, mirroring BackfillService.Start.
//
// Tests use this entry point to avoid the publish-before-subscribe race
// that `go svc.Run(ctx)` exhibits on cold goroutine schedules.
func (s *LiveService) Start(ctx context.Context) <-chan struct{} {
	ch := s.bus.Subscribe(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.drain(ctx, ch)
	}()
	return done
}

func (s *LiveService) drain(ctx context.Context, ch <-chan events.Event) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-ch:
			if !ok {
				return ctx.Err()
			}
			msg, ok := ev.(events.MessageReceived)
			if !ok {
				continue
			}
			s.persist(ctx, msg)
		}
	}
}

// persist saves a single message and records the ingest latency. The
// chats pane reorders the dialog list by subscribing directly to
// MessageReceived (same event the UI already routes through the bus
// fan-in), so there is no need to republish a DialogUpdated here.
//
// Earlier revisions of this method emitted a DialogUpdated to drive the
// chats pane reload, but that re-publish landed in LiveService's own
// subscriber buffer (Bus.Publish fans out to every subscriber, including
// the producer). Under bursty traffic the self-delivered event halved
// the effective buffer capacity and could cause real MessageReceived
// events to drop. Routing the reload directly off MessageReceived keeps
// the bus single-producer-per-event-type and removes the back-pressure
// surprise.
func (s *LiveService) persist(ctx context.Context, ev events.MessageReceived) {
	start := s.now()
	if err := s.store.SaveMessage(ctx, domain.Message{
		ID:     ev.MessageID,
		ChatID: ev.ChatID,
		FromID: ev.FromID,
		Date:   ev.Date,
		Text:   ev.Text,
		Media:  ev.Media,
	}); err != nil {
		s.log.Error("live: save message failed",
			"chat_id", ev.ChatID, "message_id", ev.MessageID, "err", err)
		return
	}
	latency := s.now().Sub(start)
	if latency < 0 {
		latency = 0
	}
	s.lastIngestLatency.Store(latency.Milliseconds())
}
