package sync

import (
	"context"
	"log/slog"
	stdsync "sync"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
)

// ReadMarker tells Telegram the user has read a chat up to a message. It is
// the gotd-free contract ReadService drives, satisfied by *internal/tg.Reader
// in production.
type ReadMarker interface {
	MarkRead(ctx context.Context, chatID, maxID int64) error
}

// ReadStore is the storage surface ReadService needs: the newest message it
// can claim to have read, and the local unread counter to clear.
type ReadStore interface {
	GetMessages(ctx context.Context, chatID int64, limit, offset int) ([]domain.Message, error)
	ClearUnread(ctx context.Context, chatID int64) error
}

// ReadService marks a chat read when the user opens it.
//
// Until this existed, lazytg never told the server anything: opening a
// conversation here left it unread on every other device and left the local
// badge in place until the next dialog sync happened to refresh it. A client
// that reads without acknowledging is also a distinctive behavioural pattern
// on an account Telegram already watches for being unofficial — every real
// client marks what the user reads.
//
// The service is deliberately quiet on the wire. It skips a chat whose newest
// message it has already acknowledged in this session, so re-entering a
// conversation costs nothing, and it skips a chat with no messages at all.
// What remains is one request per conversation the user actually opens with
// something new in it — which is exactly what a person reading their messages
// produces.
type ReadService struct {
	marker ReadMarker
	store  ReadStore
	bus    *events.Bus
	log    *slog.Logger

	// The stdlib import is aliased because this package is itself named sync.
	mu stdsync.Mutex
	// acknowledged is the highest message id already reported per chat. It is
	// per-session state on purpose: the server is the authority, and a
	// forgotten entry costs one redundant request after a restart, while a
	// persisted one that drifted would cost a chat that never gets marked.
	acknowledged map[int64]int64
}

// NewReadService wires a service. log may be nil — a no-op logger is used in
// that case so unit tests stay free of plumbing noise.
func NewReadService(marker ReadMarker, store ReadStore, bus *events.Bus, log *slog.Logger) *ReadService {
	if log == nil {
		log = slog.New(noopHandler{})
	}
	return &ReadService{
		marker:       marker,
		store:        store,
		bus:          bus,
		log:          log,
		acknowledged: make(map[int64]int64),
	}
}

// pendingReads bounds the queue between the bus reader and the worker that
// talks to Telegram. Small on purpose: the queue holds chats waiting to be
// acknowledged, and a user cannot open more than a handful in the time one
// request takes.
const pendingReads = 8

// Start subscribes to the bus and returns a channel closed when both its
// goroutines exit, mirroring LiveService so the cmd layer wires them the same
// way.
//
// Two goroutines rather than one, because marking a chat read is a network
// call and Bus.Publish drops events for a subscriber whose buffer is full.
// Doing the RPC inside the read loop means every message arriving during it
// eats buffer, and a ChatOpened landing in that window would be dropped — the
// chat would silently stay unread, which is exactly the bug this service
// exists to fix. The reader now never blocks; the worker does the waiting.
func (s *ReadService) Start(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	if s.bus == nil || s.marker == nil {
		close(done)
		return done
	}
	ch := s.bus.Subscribe(ctx)
	queue := make(chan int64, pendingReads)

	var wg stdsync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer close(queue)
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				opened, ok := ev.(events.ChatOpened)
				if !ok {
					continue
				}
				select {
				case queue <- opened.ChatID:
				default:
					// The worker is behind. Dropping costs one read receipt
					// that the next open of the chat will send anyway, and
					// keeps this loop off the network's critical path.
					s.log.Debug("read: acknowledgement queue full, skipping", "chat_id", opened.ChatID)
				}
			}
		}
	}()
	go func() {
		defer wg.Done()
		for chatID := range queue {
			s.markRead(ctx, chatID)
		}
	}()

	go func() {
		wg.Wait()
		close(done)
	}()
	return done
}

// markRead acknowledges the newest message in a chat, then clears the local
// counter and announces the change so the chat list drops its badge.
//
// A failure to reach the server does not clear the local counter: showing a
// chat as read while the server still holds it unread is the one outcome
// worse than a stale badge, because the user has no way to tell.
func (s *ReadService) markRead(ctx context.Context, chatID int64) {
	if chatID == 0 || s.marker == nil {
		return
	}
	msgs, err := s.store.GetMessages(ctx, chatID, 1, 0)
	if err != nil {
		s.log.Warn("read: newest message lookup failed", "chat_id", chatID, "err", err)
		return
	}
	if len(msgs) == 0 {
		return
	}
	maxID := msgs[0].ID

	s.mu.Lock()
	already := s.acknowledged[chatID]
	s.mu.Unlock()
	if maxID <= already {
		return
	}

	if err := s.marker.MarkRead(ctx, chatID, maxID); err != nil {
		s.log.Warn("read: marking the chat read failed", "chat_id", chatID, "max_id", maxID, "err", err)
		return
	}

	s.mu.Lock()
	s.acknowledged[chatID] = maxID
	s.mu.Unlock()

	if err := s.store.ClearUnread(ctx, chatID); err != nil {
		s.log.Warn("read: clearing the local unread counter failed", "chat_id", chatID, "err", err)
		return
	}
	if s.bus != nil {
		s.bus.Publish(events.DialogUpdated{ChatID: chatID})
	}
}
