package tg

import (
	"container/list"
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"

	"github.com/kar43lov/lazytg/internal/core/events"
)

// UpdatesDispatcher fans MTProto updates out to the in-process event bus.
// It is the only place that imports gotd's `tg.Update*` variants — all
// downstream consumers in core/sync, ui/ and tests work against typed
// events from internal/core/events.
//
// gotd's updates.Manager guarantees ordered delivery per channel and per
// user state, but it does not protect us from the same physical message
// being re-delivered across a reconnect-with-difference cycle. We use a
// small LRU keyed by (chat_id, message_id) so the second delivery is
// silently dropped instead of producing a duplicate UI line and a duplicate
// SQLite UPSERT.
type UpdatesDispatcher struct {
	bus *events.Bus
	log *slog.Logger

	mu    sync.Mutex
	cache *list.List // doubly-linked LRU; back = most-recent
	index map[dedupKey]*list.Element
	cap   int
}

// dedupKey identifies a message uniquely within an account.
type dedupKey struct {
	chatID, messageID int64
}

// dedupCacheCapacity bounds the LRU at 256 entries — enough to absorb a
// burst of updates within a typical reconnect difference window without
// inflating heap usage.
const dedupCacheCapacity = 256

// NewUpdatesDispatcher returns a dispatcher that publishes onto bus. log
// may be nil; a no-op logger is used in that case.
func NewUpdatesDispatcher(bus *events.Bus, log *slog.Logger) *UpdatesDispatcher {
	if log == nil {
		log = slog.New(noopHandler{})
	}
	return &UpdatesDispatcher{
		bus:   bus,
		log:   log,
		cache: list.New(),
		index: make(map[dedupKey]*list.Element, dedupCacheCapacity),
		cap:   dedupCacheCapacity,
	}
}

// Manager constructs a gotd updates.Manager wired through this dispatcher.
// Pass the returned manager into telegram.Options.UpdateHandler to start
// receiving updates with gap recovery and pts/qts state persistence.
func (d *UpdatesDispatcher) Manager(storage updates.StateStorage) *updates.Manager {
	return updates.New(updates.Config{
		Handler: telegram.UpdateHandlerFunc(d.handle),
		Storage: storage,
	})
}

// HandlerFunc returns the telegram.UpdateHandler view of the dispatcher.
// Useful when wiring without updates.Manager (raw updates handler).
func (d *UpdatesDispatcher) HandlerFunc() telegram.UpdateHandlerFunc {
	return d.handle
}

// handle is the gotd UpdateHandler entry point. We unwrap container types
// (UpdatesCombined, Updates, UpdateShortMessage, …) into individual
// UpdateClass elements and dispatch each one.
func (d *UpdatesDispatcher) handle(ctx context.Context, u tg.UpdatesClass) error {
	for _, single := range flattenUpdates(u) {
		d.dispatch(ctx, single)
	}
	return nil
}

// dispatch routes a single update into the bus. Unknown variants are
// dropped after a debug log — the server may add new types over time and
// silently ignoring them is the right default for an unofficial client.
func (d *UpdatesDispatcher) dispatch(_ context.Context, u tg.UpdateClass) {
	switch upd := u.(type) {
	case *tg.UpdateNewMessage:
		d.publishMessage(upd.Message)
	case *tg.UpdateNewChannelMessage:
		d.publishMessage(upd.Message)
	case *tg.UpdateEditMessage, *tg.UpdateEditChannelMessage:
		d.log.Debug("update: edit ignored in v0.1", "type", u.TypeName())
	default:
		d.log.Debug("update: unhandled type", "type", u.TypeName())
	}
}

// publishMessage converts a gotd MessageClass into a domain MessageReceived
// and publishes it after deduplication.
func (d *UpdatesDispatcher) publishMessage(mc tg.MessageClass) {
	m, ok := mc.(*tg.Message)
	if !ok {
		// MessageEmpty / MessageService — nothing useful to render in v0.1.
		return
	}
	chatID := chatIDFromPeer(m.PeerID)
	if chatID == 0 {
		// Unknown peer container. Skip rather than poison the bus.
		d.log.Debug("update: skipping message with unknown peer", "id", m.ID)
		return
	}
	key := dedupKey{chatID: chatID, messageID: int64(m.ID)}
	if d.seen(key) {
		return
	}
	var fromID int64
	if from, ok := m.GetFromID(); ok {
		fromID = peerIDToInt64(from)
	}
	d.bus.Publish(events.MessageReceived{
		ChatID:    chatID,
		MessageID: int64(m.ID),
		Text:      m.Message,
		FromID:    fromID,
		Date:      time.Unix(int64(m.Date), 0).UTC(),
		Media:     MediaFromMessage(m),
	})
}

// seen reports whether key is already in the LRU and records it otherwise.
// Returns true if this is a duplicate that should be skipped.
func (d *UpdatesDispatcher) seen(key dedupKey) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if el, ok := d.index[key]; ok {
		d.cache.MoveToBack(el)
		return true
	}
	el := d.cache.PushBack(key)
	d.index[key] = el
	if d.cache.Len() > d.cap {
		oldest := d.cache.Front()
		if oldest != nil {
			d.cache.Remove(oldest)
			delete(d.index, oldest.Value.(dedupKey))
		}
	}
	return false
}

// chatIDFromPeer returns the local chat id for a Telegram peer. Returns 0
// if the variant is unknown.
func chatIDFromPeer(p tg.PeerClass) int64 {
	switch v := p.(type) {
	case *tg.PeerUser:
		return v.UserID
	case *tg.PeerChat:
		return v.ChatID
	case *tg.PeerChannel:
		return v.ChannelID
	}
	return 0
}

// flattenUpdates collapses gotd's container types into a flat slice of
// UpdateClass entries. Short* variants are synthesised back into
// UpdateNewMessage equivalents so dispatch only needs the canonical
// variants.
func flattenUpdates(u tg.UpdatesClass) []tg.UpdateClass {
	switch v := u.(type) {
	case *tg.Updates:
		return v.Updates
	case *tg.UpdatesCombined:
		return v.Updates
	case *tg.UpdateShort:
		return []tg.UpdateClass{v.Update}
	case *tg.UpdateShortMessage:
		return []tg.UpdateClass{shortToUpdateMessage(v)}
	case *tg.UpdateShortChatMessage:
		return []tg.UpdateClass{shortChatToUpdateMessage(v)}
	case *tg.UpdateShortSentMessage:
		// Sent confirmation; ignored at v0.1 (covered by send.go path
		// in Task 3, where the local optimistic record is patched up).
		return nil
	case *tg.UpdatesTooLong:
		// gotd will trigger a getDifference; nothing to dispatch here.
		return nil
	}
	return nil
}

// shortToUpdateMessage rebuilds a full UpdateNewMessage from an
// UpdateShortMessage so the dispatcher only deals with one variant.
func shortToUpdateMessage(s *tg.UpdateShortMessage) tg.UpdateClass {
	m := &tg.Message{
		ID:      s.ID,
		Date:    s.Date,
		Message: s.Message,
		Out:     s.Out,
	}
	// Short messages always describe a private chat: peer = self when
	// outgoing, peer = sender otherwise. For our dedup purposes either
	// choice is stable as long as it matches what the chat list uses,
	// which is always the *other* user's ID.
	peer := &tg.PeerUser{UserID: s.UserID}
	m.PeerID = peer
	m.SetFromID(peer)
	return &tg.UpdateNewMessage{Message: m, Pts: s.Pts, PtsCount: s.PtsCount}
}

// shortChatToUpdateMessage rebuilds a full UpdateNewMessage from an
// UpdateShortChatMessage.
func shortChatToUpdateMessage(s *tg.UpdateShortChatMessage) tg.UpdateClass {
	m := &tg.Message{
		ID:      s.ID,
		Date:    s.Date,
		Message: s.Message,
	}
	m.PeerID = &tg.PeerChat{ChatID: s.ChatID}
	m.SetFromID(&tg.PeerUser{UserID: s.FromID})
	return &tg.UpdateNewMessage{Message: m, Pts: s.Pts, PtsCount: s.PtsCount}
}
