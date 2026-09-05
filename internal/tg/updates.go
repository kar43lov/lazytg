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

	"github.com/kar43lov/lazytg/internal/core/domain"
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
	// self names the account's own chat; see Self.
	self *Self

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

// maxChannelDifferenceConcurrency bounds how many
// updates.getChannelDifference calls the manager may have in flight at once.
// Recovering a gap means one such call per channel, and an account in many
// active channels would otherwise fire all of them the instant the
// connection comes back — a burst that is both rate-limit bait and exactly
// the traffic shape a human client never produces. Four keeps recovery
// prompt while leaving the pattern unremarkable.
const maxChannelDifferenceConcurrency = 4

// Manager constructs a gotd updates.Manager wired through this dispatcher.
// Pass the returned manager into telegram.Options.UpdateHandler, then call
// Client.RunGapRecovery once the session is authorised: the handler alone
// forwards updates as they arrive, and it is Run that adds ordering and
// getDifference recovery on top.
//
// hasher may be nil, in which case channel access hashes live in memory and
// are lost on exit — the manager then skips every channel on the next start
// and recovers only the common (user and basic-group) sequence.
func (d *UpdatesDispatcher) Manager(storage updates.StateStorage, hasher updates.ChannelAccessHasher) *updates.Manager {
	return updates.New(updates.Config{
		Handler:                         telegram.UpdateHandlerFunc(d.handle),
		Storage:                         storage,
		AccessHasher:                    hasher,
		MaxChannelDifferenceConcurrency: maxChannelDifferenceConcurrency,
		// OnTooLong fires when the gap is too wide to recover through
		// getDifference. Nothing to do beyond saying so: the next dialog
		// sync refreshes last_message_date, and the freshness check then
		// pulls the affected chats' history the next time they are opened.
		OnTooLong: func() {
			d.log.Warn("updates: gap too long to recover — chats refresh on next open")
		},
	})
}

// WithSelf tells the dispatcher which chat is the account's own.
func (d *UpdatesDispatcher) WithSelf(self *Self) *UpdatesDispatcher {
	d.self = self
	return d
}

// SeenMessage reports whether this message has already been published, and
// records it when it has not. It is the same LRU the live path dedupes
// against, exported so the polling fallback shares one filter with it rather
// than keeping a second, blind one.
//
// The two paths overlap by design — polling is a net under push, not a
// replacement — and the watermarks alone cannot close the overlap: the
// polling source reads the store's newest id, then makes a network call, and
// a live update landing in that window is newer than the watermark the call
// was made with. One filter across both paths closes it wherever it happens.
func (d *UpdatesDispatcher) SeenMessage(chatID, messageID int64) bool {
	return d.seen(dedupKey{chatID: chatID, messageID: messageID})
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
		d.publishMessage(upd.Message, false)
	case *tg.UpdateNewChannelMessage:
		d.publishMessage(upd.Message, false)
	case *tg.UpdateEditMessage:
		d.publishMessage(upd.Message, true)
	case *tg.UpdateEditChannelMessage:
		d.publishMessage(upd.Message, true)
	case *tg.UpdateDeleteMessages:
		// No peer in this variant, by design on Telegram's side: message ids
		// are unique across all private chats and basic groups for one
		// account, so the ids alone say what to remove. Consumers must not
		// apply them to a channel, which numbers its own messages.
		d.publishDeleted(0, upd.Messages)
	case *tg.UpdateDeleteChannelMessages:
		d.publishDeleted(upd.ChannelID, upd.Messages)
	case *tg.UpdateUserTyping:
		// A private dialog is named by the user typing in it: there is
		// only one other person, so they are both the chat and the typer.
		d.publishTyping(upd.UserID, upd.UserID, upd.Action, time.Now())
	case *tg.UpdateChatUserTyping:
		d.publishTyping(upd.ChatID, chatIDFromPeer(upd.FromID), upd.Action, time.Now())
	case *tg.UpdateChannelUserTyping:
		d.publishTyping(upd.ChannelID, chatIDFromPeer(upd.FromID), upd.Action, time.Now())
	case *tg.UpdateMessageReactions:
		d.publishReactions(upd)
	case *tg.UpdateReadHistoryInbox:
		d.publish(events.ChatReadInbox{ChatID: chatIDFromPeer(upd.Peer), MaxID: int64(upd.MaxID), StillUnread: upd.StillUnreadCount})
	case *tg.UpdateReadChannelInbox:
		d.publish(events.ChatReadInbox{ChatID: upd.ChannelID, MaxID: int64(upd.MaxID), StillUnread: upd.StillUnreadCount})
	case *tg.UpdateDraftMessage:
		d.publish(events.DraftChanged{ChatID: chatIDFromPeer(upd.Peer), Text: draftText(upd.Draft)})
	case *tg.UpdateReadHistoryOutbox:
		d.publish(events.ChatReadOutbox{ChatID: chatIDFromPeer(upd.Peer), MaxID: int64(upd.MaxID)})
	case *tg.UpdateReadChannelOutbox:
		d.publish(events.ChatReadOutbox{ChatID: upd.ChannelID, MaxID: int64(upd.MaxID)})
	case *tg.UpdateDialogPinned:
		if id := dialogPeerID(upd.Peer); id != 0 {
			d.publish(events.ChatPinned{ChatID: id, Pinned: upd.Pinned})
		}
	case *tg.UpdateDialogUnreadMark:
		if id := dialogPeerID(upd.Peer); id != 0 {
			d.publish(events.ChatUnreadMark{ChatID: id, Unread: upd.Unread})
		}
	case *tg.UpdateNotifySettings:
		// Only a setting on one chat is a fact about that chat. The
		// class-wide defaults (all users, all groups) are a policy this
		// client does not model; a chat inherits nothing from them here.
		if np, ok := upd.Peer.(*tg.NotifyPeer); ok {
			d.publish(events.ChatMuted{ChatID: chatIDFromPeer(np.Peer), Until: muteUntilOf(upd.NotifySettings)})
		}
	case *tg.UpdateUserStatus:
		if d.self.Owns(upd.UserID) {
			// Your own presence is not news, and the chat with yourself
			// does not show it.
			return
		}
		online, lastSeen := presenceOf(upd.Status)
		d.publish(events.PeerPresence{UserID: upd.UserID, Online: online, LastSeen: lastSeen})
	default:
		d.log.Debug("update: unhandled type", "type", u.TypeName())
	}
}

// publishMessage converts a gotd MessageClass into a domain MessageReceived
// and publishes it after deduplication.
//
// edited marks a rewrite of a message already delivered. It skips the
// dedup on purpose: the cache exists to stop one arrival rendering twice,
// and an edit is a second arrival of the same id by design — the same
// message, changed. Telegram also sends an edit update when a reaction
// lands on a message; the reactions come along on the message here, which
// is fine, and the row is replaced rather than appended by every consumer.
func (d *UpdatesDispatcher) publishMessage(mc tg.MessageClass, edited bool) {
	m, ok := mc.(*tg.Message)
	if !ok {
		if svc, isService := mc.(*tg.MessageService); isService {
			d.publishService(svc)
		}
		return
	}
	chatID := chatIDFromPeer(m.PeerID)
	if chatID == 0 {
		// Unknown peer container. Skip rather than poison the bus.
		d.log.Debug("update: skipping message with unknown peer", "id", m.ID)
		return
	}
	key := dedupKey{chatID: chatID, messageID: int64(m.ID)}
	if !edited && d.seen(key) {
		return
	}
	d.bus.Publish(events.MessageReceived{
		ChatID:    chatID,
		MessageID: int64(m.ID),
		Text:      messageText(m),
		FromID:    senderOf(m, chatID),
		Date:      time.Unix(int64(m.Date), 0).UTC(),
		Media:     MediaFromMessage(m),
		ChatType:  chatTypeFromPeer(m.PeerID),
		Outgoing:  m.Out || d.self.Owns(chatID),
		ReplyTo:   replyToOf(m),
		Reactions: ReactionsFromMessage(m),
		Entities:  EntitiesFromMessage(m),
		Edited:    edited,
		EditDate:  editDateOf(m),
	})
}

// publishReactions converts a reaction update into a bus event.
//
// Not deduplicated against the message LRU: that cache stops the same arrival
// being rendered twice, and reactions on one message change repeatedly by
// design — a second update for the same message is the point, not a repeat.
func (d *UpdatesDispatcher) publishReactions(upd *tg.UpdateMessageReactions) {
	chatID := chatIDFromPeer(upd.Peer)
	if chatID == 0 {
		return
	}
	d.bus.Publish(events.MessageReactionsChanged{
		ChatID:    chatID,
		MessageID: int64(upd.MsgID),
		Reactions: decodeReactions(upd.Reactions),
	})
}

// publishDeleted converts a deletion update into a bus event. Deletions are
// not deduplicated against the message LRU: that cache exists to stop the
// same arrival being rendered twice, while a repeated delete is harmless —
// the second one removes nothing.
func (d *UpdatesDispatcher) publishDeleted(channelID int64, ids []int) {
	if len(ids) == 0 {
		return
	}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		out = append(out, int64(id))
	}
	d.bus.Publish(events.MessagesDeleted{ChatID: channelID, MessageIDs: out})
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

// chatTypeFromPeer maps a Telegram peer container to the chat kind stored in
// chats.type. Returns an empty kind for an unknown variant, which callers read
// as "do not create a chat row for this".
func chatTypeFromPeer(p tg.PeerClass) domain.ChatType {
	switch p.(type) {
	case *tg.PeerUser:
		return domain.ChatTypePrivate
	case *tg.PeerChat:
		return domain.ChatTypeGroup
	case *tg.PeerChannel:
		// Supergroup and broadcast channel are the same peer container here;
		// dialog sync corrects the distinction when it next runs. Guessing
		// supergroup keeps a live-discovered chat in the list the user reads
		// rather than filing it under broadcasts.
		return domain.ChatTypeSupergroup
	}
	return ""
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

// publish is the one place list-fact events leave the dispatcher; a zero
// chat id names nothing and is dropped here rather than in every consumer.
func (d *UpdatesDispatcher) publish(ev events.Event) {
	switch typed := ev.(type) {
	case events.ChatReadInbox:
		if typed.ChatID == 0 {
			return
		}
	case events.ChatMuted:
		if typed.ChatID == 0 {
			return
		}
	case events.PeerPresence:
		if typed.UserID == 0 {
			return
		}
	}
	d.bus.Publish(ev)
}

// dialogPeerID names the chat a dialog-level update is about, or 0 for the
// archive folder pseudo-peer.
func dialogPeerID(p tg.DialogPeerClass) int64 {
	dp, ok := p.(*tg.DialogPeer)
	if !ok {
		return 0
	}
	return chatIDFromPeer(dp.Peer)
}

// publishService is publishMessage for Telegram's own lines: somebody
// joined, a message was pinned, a call ended. Same dedup, same event.
func (d *UpdatesDispatcher) publishService(m *tg.MessageService) {
	chatID := chatIDFromPeer(m.PeerID)
	if chatID == 0 {
		return
	}
	if d.seen(dedupKey{chatID: chatID, messageID: int64(m.ID)}) {
		return
	}
	row := convertService(m, chatID, d.self)
	d.bus.Publish(events.MessageReceived{
		ChatID:    chatID,
		MessageID: row.ID,
		Text:      row.Text,
		FromID:    row.FromID,
		Date:      row.Date,
		ChatType:  chatTypeFromPeer(m.PeerID),
		Outgoing:  row.Outgoing,
		ReplyTo:   row.ReplyTo,
	})
}
