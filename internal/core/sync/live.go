package sync

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
)

// LiveStore is the storage surface LiveService relies on. Defining it as
// an interface (rather than reaching for *sqlite.Repo directly) keeps the
// service unit-testable with an in-memory fake and lets future storage
// implementations slot in without churning the call sites.
type LiveStore interface {
	SaveMessage(ctx context.Context, m domain.Message) error
	// DeleteMessages removes messages Telegram reported as deleted. A zero
	// chat id means the update named no chat, which is how deletions in
	// private chats and basic groups arrive.
	DeleteMessages(ctx context.Context, chatID int64, ids []int64) (int64, error)
	// SetReactions replaces the reactions stored against one message. It
	// touches nothing else about the row, because a reaction update says
	// nothing about the message body and rewriting the whole row from an
	// update that does not carry it would blank the text.
	SetReactions(ctx context.Context, chatID, messageID int64, rs []domain.Reaction) error
	// The chat-list facts. Each changes one column of the chat row, because
	// each arrives on its own update that says nothing about the rest.
	SetUnread(ctx context.Context, chatID int64, count int) error
	SetPinned(ctx context.Context, chatID int64, pinned bool) error
	SetMutedUntil(ctx context.Context, chatID int64, until time.Time) error
	SetUnreadMark(ctx context.Context, chatID int64, marked bool) error
	SetPresence(ctx context.Context, userID int64, online bool, lastSeen time.Time) error
	// EnsureChat creates the parent chats row when the peer is unknown and
	// leaves an existing row untouched. Without it a message from a chat
	// outside the synced dialog window fails its foreign key and is lost.
	// The date orders the new row in the chat list; see the implementation.
	EnsureChat(ctx context.Context, id int64, t domain.ChatType, lastMessageDate time.Time) (bool, error)
	// IncrementUnread raises a chat's unread counter for a message that
	// arrived while the user was reading something else.
	IncrementUnread(ctx context.Context, chatID int64) error
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
	// openChat is the conversation the user is looking at, tracked from
	// ChatOpened. Messages landing there must not raise the unread counter:
	// they are being read as they arrive, and a badge on the chat you are
	// reading is noise every other client knows to suppress.
	openChat atomic.Int64
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
			switch typed := ev.(type) {
			case events.MessageReceived:
				s.persist(ctx, typed)
			case events.MessagesDeleted:
				s.forget(ctx, typed)
			case events.MessageReactionsChanged:
				s.applyReactions(ctx, typed)
			case events.ChatOpened:
				s.openChat.Store(typed.ChatID)
			case events.ChatReadInbox:
				s.applyChatFact(ctx, typed.ChatID, "read", s.store.SetUnread(ctx, typed.ChatID, typed.StillUnread))
			case events.ChatPinned:
				s.applyChatFact(ctx, typed.ChatID, "pin", s.store.SetPinned(ctx, typed.ChatID, typed.Pinned))
			case events.ChatMuted:
				s.applyChatFact(ctx, typed.ChatID, "mute", s.store.SetMutedUntil(ctx, typed.ChatID, typed.Until))
			case events.ChatUnreadMark:
				s.applyChatFact(ctx, typed.ChatID, "unread mark", s.store.SetUnreadMark(ctx, typed.ChatID, typed.Unread))
			case events.PeerPresence:
				s.applyChatFact(ctx, typed.UserID, "presence", s.store.SetPresence(ctx, typed.UserID, typed.Online, typed.LastSeen))
			}
		}
	}
}

// forget removes messages another device deleted. The mirror is the only
// copy the user has once the server has dropped them, so this is the one
// path that can make a deletion stick — and equally the one that could lose
// data it should not, which is why the store filters channel ids out of the
// no-chat case rather than deleting by bare id.
//
// A count is logged only when it differs from what was asked for: deleting
// messages that were never mirrored is the ordinary case (they were outside
// the fetched history), not a problem worth a line each time.
func (s *LiveService) forget(ctx context.Context, ev events.MessagesDeleted) {
	removed, err := s.store.DeleteMessages(ctx, ev.ChatID, ev.MessageIDs)
	if err != nil {
		s.log.Error("live: delete messages failed",
			"chat_id", ev.ChatID, "ids", len(ev.MessageIDs), "err", err)
		return
	}
	if removed != int64(len(ev.MessageIDs)) {
		s.log.Debug("live: deleted fewer messages than reported",
			"chat_id", ev.ChatID, "reported", len(ev.MessageIDs), "removed", removed)
	}
}

// applyReactions writes a reaction change into the mirror.
//
// A message the mirror does not hold is the ordinary case rather than an
// error: reactions arrive for the whole account, including chats whose
// history was never fetched. The store reports it and nothing is logged at a
// level anybody watches.
func (s *LiveService) applyReactions(ctx context.Context, ev events.MessageReactionsChanged) {
	if err := s.store.SetReactions(ctx, ev.ChatID, ev.MessageID, ev.Reactions); err != nil {
		s.log.Debug("live: reactions not stored",
			"chat_id", ev.ChatID, "message_id", ev.MessageID, "err", err)
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
	// The parent row first: messages.chat_id references chats(id), so a
	// message from a peer dialog sync has not reached yet would otherwise be
	// rejected and dropped. A failure here is logged and the save is still
	// attempted — if the chat does exist, the message is fine, and if it does
	// not, SaveMessage reports the foreign key error as before rather than
	// hiding it behind this one.
	if ev.ChatType != "" {
		created, err := s.store.EnsureChat(ctx, ev.ChatID, ev.ChatType, ev.Date)
		switch {
		case err != nil:
			s.log.Warn("live: ensure chat failed",
				"chat_id", ev.ChatID, "type", ev.ChatType, "err", err)
		case created && s.bus != nil:
			// A row the live path invented shows as "chat <id>" with no
			// unread count until the server describes it. Nothing else asks
			// for that: dialog sync runs at startup, so without this the
			// placeholder survives until the next launch.
			s.bus.Publish(events.ChatDiscovered{ChatID: ev.ChatID})
		}
	}
	if err := s.store.SaveMessage(ctx, domain.Message{
		ID:        ev.MessageID,
		ChatID:    ev.ChatID,
		FromID:    ev.FromID,
		Date:      ev.Date,
		Text:      ev.Text,
		Media:     ev.Media,
		Outgoing:  ev.Outgoing,
		ReplyTo:   ev.ReplyTo,
		Entities:  ev.Entities,
		EditDate:  ev.EditDate,
		Reactions: ev.Reactions,
	}); err != nil {
		s.log.Error("live: save message failed",
			"chat_id", ev.ChatID, "message_id", ev.MessageID, "err", err)
		return
	}
	s.countUnread(ctx, ev)
	latency := s.now().Sub(start)
	if latency < 0 {
		latency = 0
	}
	s.lastIngestLatency.Store(latency.Milliseconds())
}

// countUnread raises the chat's badge for a message the user has not seen.
//
// Three things are excluded, and each of them would otherwise show a badge for
// something already read: the user's own messages (sent from another device or
// echoed back after lazytg sent them), messages in the chat currently open,
// and — implicitly, by running after SaveMessage — anything whose save failed.
//
// The chats pane reloads off MessageReceived directly, so the new count is on
// screen without a DialogUpdated of its own; publishing one here would land in
// this service's own subscriber buffer, which is the fan-out amplification
// persist's comment warns about.
//
// The increment is not idempotent, and deliberately relies on the dispatcher's
// duplicate filter rather than checking storage: the same message delivered
// twice would count twice. That filter is an in-memory LRU of 256 entries
// covering both the live path and the polling fallback, so the only way past
// it is a redelivery separated by more than 256 messages — after which the
// count is corrected by the next dialog sync or by opening the chat. Making it
// idempotent means having SaveMessage report whether the row was new, which is
// a wider contract change than an over-count that heals itself is worth.
func (s *LiveService) countUnread(ctx context.Context, ev events.MessageReceived) {
	// An edit is the same message again, not one more to read.
	if ev.Edited || ev.Outgoing || ev.ChatID == 0 || ev.ChatID == s.openChat.Load() {
		return
	}
	if err := s.store.IncrementUnread(ctx, ev.ChatID); err != nil {
		s.log.Warn("live: unread counter not raised", "chat_id", ev.ChatID, "err", err)
	}
}

// applyChatFact reports the outcome of one list-fact write and, when it
// took, tells the chat list to reload. A chat the mirror does not hold is
// a no-op at the store, which is right: the facts describe a row, and a
// row that does not exist has nothing to describe.
func (s *LiveService) applyChatFact(_ context.Context, chatID int64, what string, err error) {
	if err != nil {
		s.log.Warn("live: chat "+what+" not recorded", "chat_id", chatID, "err", err)
		return
	}
	if s.bus != nil {
		s.bus.Publish(events.DialogUpdated{ChatID: chatID})
	}
}
