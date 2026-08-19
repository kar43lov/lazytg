package sync

import (
	"context"
	"log/slog"
	stdsync "sync"
	"sync/atomic"

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

// ReadService tells Telegram what the user has read: the chat they open, and
// anything arriving into it while it stays open.
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
// Acknowledgements for an ongoing conversation coalesce to the newest one
// pending, because marking message 40 read covers everything below it. What
// remains is one request per conversation the user actually opens, plus one
// per burst of messages they sit and read — which is exactly what a person
// reading their messages produces.
type ReadService struct {
	marker ReadMarker
	store  ReadStore
	bus    *events.Bus
	log    *slog.Logger

	// openChat is the conversation the user is looking at, tracked from
	// ChatOpened so a message arriving into it can be acknowledged as it
	// lands rather than waiting for the next open.
	openChat atomic.Int64

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

// readRequest is one acknowledgement waiting to be sent. maxID is the message
// to report as read, or zero to mean "whatever the mirror says is newest" —
// the form an open carries, since the user has just seen everything in the
// chat. A message arriving into an open chat names its own id instead, which
// keeps the acknowledgement correct even if the live save has not landed yet.
type readRequest struct {
	chatID int64
	maxID  int64
}

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
//
// The two kinds of acknowledgement get their own queues because they behave
// differently under load. An open is a discrete user action and each one
// matters, so those are queued. A message arriving into the open chat is a
// stream, and only its newest entry matters — an acknowledgement of message
// 40 covers 31 through 39 as well. Sharing one queue would let a busy
// conversation fill all eight slots with acknowledgements that supersede each
// other and push out the open of another chat, and would spend one request
// per message on the way.
func (s *ReadService) Start(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	if s.bus == nil || s.marker == nil {
		close(done)
		return done
	}
	ch := s.bus.Subscribe(ctx)
	opens := make(chan readRequest, pendingReads)
	acks := make(chan readRequest, 1)

	var wg stdsync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer close(opens)
		defer close(acks)
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				req, ok := s.requestFor(ev)
				if !ok {
					continue
				}
				if req.maxID != 0 {
					replaceAck(acks, req)
					continue
				}
				select {
				case opens <- req:
				default:
					// The worker is behind. Dropping costs one read receipt
					// that the next open of the chat will send anyway, and
					// keeps this loop off the network's critical path.
					s.log.Debug("read: acknowledgement queue full, skipping", "chat_id", req.chatID)
				}
			}
		}
	}()
	go func() {
		defer wg.Done()
		for opens != nil || acks != nil {
			select {
			case req, ok := <-opens:
				if !ok {
					opens = nil
					continue
				}
				s.markRead(ctx, req)
			case req, ok := <-acks:
				if !ok {
					acks = nil
					continue
				}
				s.markRead(ctx, req)
			}
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
func (s *ReadService) markRead(ctx context.Context, req readRequest) {
	chatID := req.chatID
	if chatID == 0 || s.marker == nil {
		return
	}
	maxID := req.maxID
	if maxID == 0 {
		msgs, err := s.store.GetMessages(ctx, chatID, 1, 0)
		if err != nil {
			s.log.Warn("read: newest message lookup failed", "chat_id", chatID, "err", err)
			return
		}
		if len(msgs) == 0 {
			return
		}
		maxID = msgs[0].ID
	}

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

// requestFor turns a bus event into an acknowledgement, or reports that this
// event asks for none.
//
// Two events do. Opening a chat acknowledges everything in it. A message
// arriving into the chat already open acknowledges itself — without that, a
// conversation the user is reading in real time stays unread on their phone
// for as long as it lasts, because the open happened before the messages did.
// The reader's own messages are skipped: Telegram has nothing to mark.
func (s *ReadService) requestFor(ev events.Event) (readRequest, bool) {
	switch typed := ev.(type) {
	case events.ChatOpened:
		s.openChat.Store(typed.ChatID)
		return readRequest{chatID: typed.ChatID}, typed.ChatID != 0
	case events.MessageReceived:
		if typed.Outgoing || typed.ChatID == 0 || typed.ChatID != s.openChat.Load() {
			return readRequest{}, false
		}
		return readRequest{chatID: typed.ChatID, maxID: typed.MessageID}, true
	default:
		return readRequest{}, false
	}
}

// replaceAck puts req into a depth-one channel, displacing whatever is
// already waiting there.
//
// Displacing is the point: acknowledgements supersede one another, so the
// pending entry is stale the moment a newer message arrives, and holding it
// would spend a request on a message the next one covers anyway. The drain is
// best-effort in both directions — the worker may take the old entry between
// the two operations, which simply means the send finds the slot empty.
func replaceAck(acks chan readRequest, req readRequest) {
	select {
	case acks <- req:
		return
	default:
	}
	select {
	case <-acks:
	default:
	}
	select {
	case acks <- req:
	default:
	}
}
