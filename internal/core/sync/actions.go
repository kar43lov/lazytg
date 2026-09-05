package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
	"github.com/kar43lov/lazytg/internal/core/markdown"
)

// MessageEditor is the gotd-free contract for rewriting a message that is
// already on the server. Satisfied by *internal/tg.Editor in production.
type MessageEditor interface {
	Edit(ctx context.Context, chatID, messageID int64, text string, entities []domain.Entity) error
}

// MessageDeleter is the gotd-free contract for removing messages from the
// server. Satisfied by *internal/tg.Deleter in production. The returned count
// is what the server reported as affected.
type MessageDeleter interface {
	Delete(ctx context.Context, chatID int64, ids []int64, revoke bool) (int, error)
}

// MessageForwarder is the gotd-free contract for passing messages to another
// chat. Satisfied by *internal/tg.Forwarder in production.
type MessageForwarder interface {
	Forward(ctx context.Context, fromChatID, toChatID int64, ids []int64, dropAuthor bool) error
}

// MessageReactor is the gotd-free contract for this account's own reaction on
// a message. It returns the message's reactions as the server now has them —
// counts belong to everybody, so the authoritative list is the only one worth
// storing. Satisfied by *internal/tg.Reactor in production.
type MessageReactor interface {
	React(ctx context.Context, chatID, messageID int64, emoticon string) ([]domain.Reaction, error)
}

// ActionStore is the storage surface ActionService writes through: the same
// SaveMessage the live path uses, because an edit is an upsert of a row that
// already exists.
type ActionStore interface {
	SaveMessage(ctx context.Context, m domain.Message) error
	Message(ctx context.Context, chatID, messageID int64) (domain.Message, error)
	SetReactions(ctx context.Context, chatID, messageID int64, rs []domain.Reaction) error
}

// DialogActions is the gotd-free contract for what a person does to a chat
// from the list. Satisfied by *internal/tg.DialogActor in production.
type DialogActions interface {
	Mute(ctx context.Context, chatID int64, until time.Time) error
	Pin(ctx context.Context, chatID int64, pinned bool) error
	MarkUnread(ctx context.Context, chatID int64, unread bool) error
}

// ErrNoSuchUsername is the answer to a handle nobody holds.
var ErrNoSuchUsername = errors.New("no such username")

// UsernameResolver turns a public handle into a chat and the peer that
// addresses it. Satisfied by *internal/tg.UsernameResolver.
type UsernameResolver interface {
	ResolveUsername(ctx context.Context, name string) (domain.Chat, domain.Peer, error)
}

// ChatSaver stores a chat row when there is none: the resolved chat has
// to exist locally before the thread can open it, and one that is already
// listed keeps its dialog facts, which a resolved object does not carry.
type ChatSaver interface {
	SaveChatIfMissing(ctx context.Context, c domain.Chat) error
}

// ChatStateStore is where the list facts land once the server has agreed.
// The same setters the live path uses, so the row looks the same whichever
// side changed it.
type ChatStateStore interface {
	GetMessages(ctx context.Context, chatID int64, limit, offset int) ([]domain.Message, error)
	SetUnread(ctx context.Context, chatID int64, count int) error
	SetPinned(ctx context.Context, chatID int64, pinned bool) error
	SetMutedUntil(ctx context.Context, chatID int64, until time.Time) error
	SetUnreadMark(ctx context.Context, chatID int64, marked bool) error
}

// ErrNotEditable is returned when the user asks to edit a message the client
// can tell up front they cannot: one somebody else wrote. It exists as a
// sentinel so the UI can say why rather than surfacing an RPC error the user
// can do nothing about.
//
// Deliberately narrow. Telegram has more rules than this — a time window, and
// exceptions to it — and they are the server's to enforce: a client that
// guessed the window would refuse edits the server would have allowed, which
// is worse than a rejected request.
var ErrNotEditable = errors.New("only your own messages can be edited")

// ActionService performs the operations a user does *to* messages that
// already exist: editing one, and deleting some.
//
// It exists as a service rather than as calls from the UI for the reason the
// rest of core does: the UI must not speak MTProto, and both operations have
// a local half that has to happen in a particular order relative to the
// remote one. That order is the interesting part of this file, and it differs
// between the two operations:
//
//   - An edit writes the server first and the mirror second. If the server
//     refuses (too old, not yours, a flood wait), nothing local changed and
//     the user sees the original text — which is the truth.
//   - A delete also writes the server first, but announces the result on the
//     bus rather than touching storage itself. LiveService already owns
//     "messages disappeared" and does it correctly, including the channel id
//     space; duplicating that here would be a second implementation of a
//     deletion path, and the two would drift.
type ActionService struct {
	resolver  UsernameResolver
	chatSaver ChatSaver
	peerStore PeerStore
	editor    MessageEditor
	deleter   MessageDeleter
	forwarder MessageForwarder
	reactor   MessageReactor
	limiter   RateLimiter
	store     ActionStore
	bus       EventPublisher
	log       *slog.Logger

	// The chat-level actions. Nil until WithDialogs, which is how an
	// offline build behaves: the keys say "not connected".
	dialogs DialogActions
	marker  ReadMarker
	chats   ChatStateStore
}

// EventPublisher is the slice of events.Bus this service needs.
type EventPublisher interface {
	Publish(ev events.Event)
}

// NewActionService wires the service. Any of editor, deleter and bus may be
// nil, which is how an offline session is expressed — the corresponding
// operation then reports that it is unavailable rather than panicking.
func NewActionService(editor MessageEditor, deleter MessageDeleter, forwarder MessageForwarder, reactor MessageReactor, store ActionStore, bus EventPublisher, log *slog.Logger) *ActionService {
	if log == nil {
		log = slog.New(noopHandler{})
	}
	return &ActionService{
		editor:    editor,
		deleter:   deleter,
		forwarder: forwarder,
		reactor:   reactor,
		store:     store,
		bus:       bus,
		log:       log,
	}
}

// Forward passes messages to another chat.
//
// Nothing is written locally and nothing is announced on the bus. The
// forwarded copies are new messages in the target chat, and they arrive the
// same way any other message does — through the live update path, which
// stores them and tells the panes. Writing them here would produce a second
// copy the moment the update landed, and writing them *instead* would leave
// the mirror holding messages the server never confirmed.
func (s *ActionService) Forward(ctx context.Context, fromChatID, toChatID int64, ids []int64, dropAuthor bool) error {
	if s == nil || s.forwarder == nil {
		return errors.New("forward: not connected")
	}
	if len(ids) == 0 {
		return nil
	}
	if s.limiter != nil {
		if err := s.limiter.Wait(ctx); err != nil {
			return fmt.Errorf("forward: %w", err)
		}
	}
	return s.forwarder.Forward(ctx, fromChatID, toChatID, ids, dropAuthor)
}

// Edit rewrites a message and updates the mirror to match.
//
// The mirror update is a best-effort second step: once the server has
// accepted the edit, the new text is the truth whether or not the local write
// succeeds, and the next backfill of the chat brings it in anyway. Failing
// the whole operation on a storage error would tell the user their edit did
// not happen when it did.
func (s *ActionService) Edit(ctx context.Context, chatID, messageID int64, text string) error {
	if s == nil || s.editor == nil {
		return errors.New("edit: not connected")
	}
	msg, err := s.store.Message(ctx, chatID, messageID)
	if err != nil {
		return fmt.Errorf("edit: read message %d: %w", messageID, err)
	}
	// Direction, not identity: migration 0010 stores whether the account
	// sent the message, which is exactly the question here and needs no
	// self-id lookup plumbed through every constructor. Telegram omits
	// from_id in a 1:1 dialog, so comparing ids would answer "unknown" in
	// the most common chat there is.
	if !msg.Outgoing {
		return ErrNotEditable
	}

	// The composer hands over what was typed, markup and all; what the
	// server and the mirror get is the text with its spans, so the edit
	// stores the same shape a fresh message would.
	plain, entities := markdown.Parse(text)
	if err := s.editor.Edit(ctx, chatID, messageID, plain, entities); err != nil {
		return err
	}

	msg.Text = plain
	msg.Entities = entities
	msg.EditDate = time.Now().UTC()
	if err := s.store.SaveMessage(ctx, msg); err != nil {
		s.log.Warn("edit: server accepted the edit but the mirror was not updated",
			"chat_id", chatID, "message_id", messageID, "err", err)
		return nil
	}
	s.publish(events.MessageEdited{ChatID: chatID, MessageID: messageID, Text: plain, Entities: entities, EditDate: msg.EditDate})
	return nil
}

// Delete removes messages from the server and announces the result.
//
// revoke asks for "delete for everyone". The caller decides: the UI asks the
// user, because deleting only your own copy and deleting the other person's
// copy are different acts and no default is right for both.
//
// The announcement happens only after the server has agreed. A local-first
// order would look faster and be wrong in the case that matters — a refused
// deletion would leave the message gone from the screen and present on every
// other device, with nothing on screen saying so.
func (s *ActionService) Delete(ctx context.Context, chatID int64, ids []int64, revoke bool) error {
	if s == nil || s.deleter == nil {
		return errors.New("delete: not connected")
	}
	if len(ids) == 0 {
		return nil
	}
	if _, err := s.deleter.Delete(ctx, chatID, ids, revoke); err != nil {
		return err
	}
	// LiveService owns the local half, including the channel id space.
	s.publish(events.MessagesDeleted{ChatID: chatID, MessageIDs: ids})
	return nil
}

// React sets or clears this account's reaction on a message.
//
// The emoticon the caller passes is what the account should end up holding;
// an empty string means "none". The toggle — pressing the same emoji twice to
// take it back — is decided by the caller, which is the only place that knows
// what the user was looking at when they pressed the key.
//
// The mirror is updated from the server's answer, then announced. Doing it
// the other way round would show a count that never happened if the request
// was refused, and reactions are exactly the kind of thing a channel refuses.
func (s *ActionService) React(ctx context.Context, chatID, messageID int64, emoticon string) error {
	if s == nil || s.reactor == nil {
		return errors.New("react: not connected")
	}
	reactions, err := s.reactor.React(ctx, chatID, messageID, emoticon)
	if err != nil {
		return err
	}
	if reactions == nil && emoticon != "" {
		// The server accepted it but told us nothing about the result.
		// The push update will; announcing a guess here would be the one
		// way to put a wrong count on screen.
		s.log.Debug("react: no reaction list in the response",
			"chat_id", chatID, "message_id", messageID)
		return nil
	}
	if err := s.store.SetReactions(ctx, chatID, messageID, reactions); err != nil {
		s.log.Warn("react: server accepted the reaction but the mirror was not updated",
			"chat_id", chatID, "message_id", messageID, "err", err)
	}
	s.publish(events.MessageReactionsChanged{
		ChatID:    chatID,
		MessageID: messageID,
		Reactions: reactions,
	})
	return nil
}

// WithRateLimiter installs the send-side throttle in front of forwarding.
//
// Forwarding is a send: it creates messages in another chat, and it is the
// third way this client can do that after text and media. The guard is
// documented as covering sends, so a path that creates messages without
// passing through it makes the documentation wrong — which is the specific
// failure mode this project has been bitten by before.
//
// Editing, deleting and reacting are deliberately not gated. They act on
// messages that already exist, each is one request per deliberate keypress,
// and putting a token bucket in front of "undo my reaction" would make the
// interface feel broken without changing the traffic a human produces.
func (s *ActionService) WithRateLimiter(limiter RateLimiter) *ActionService {
	s.limiter = limiter
	return s
}

func (s *ActionService) publish(ev events.Event) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(ev)
}

// WithDialogs enables the chat-level actions: mute, pin, mark unread and
// mark read. marker is the same read-receipt sender the ReadService uses,
// chats is where the facts land locally once the server has agreed.
func (s *ActionService) WithDialogs(dialogs DialogActions, marker ReadMarker, chats ChatStateStore) *ActionService {
	s.dialogs = dialogs
	s.marker = marker
	s.chats = chats
	return s
}

// WithResolver enables opening a chat by its public handle: the resolved
// chat and peer are stored, so the thread can load and address it.
func (s *ActionService) WithResolver(resolver UsernameResolver, chats ChatSaver, peers PeerStore) *ActionService {
	s.resolver = resolver
	s.chatSaver = chats
	s.peerStore = peers
	return s
}

// OpenByUsername resolves a public handle, stores what came back and
// returns the chat id to open. The peer goes in first: a chat row without
// its peer is one the history loader cannot address.
func (s *ActionService) OpenByUsername(ctx context.Context, name string) (int64, error) {
	if s.resolver == nil || s.chatSaver == nil || s.peerStore == nil {
		return 0, errors.New("open by username: not connected")
	}
	chat, peer, err := s.resolver.ResolveUsername(ctx, name)
	if err != nil {
		return 0, err
	}
	if err := s.peerStore.Save(ctx, peer); err != nil {
		return 0, fmt.Errorf("open @%s: save peer: %w", name, err)
	}
	if err := s.chatSaver.SaveChatIfMissing(ctx, chat); err != nil {
		return 0, fmt.Errorf("open @%s: save chat: %w", name, err)
	}
	if s.bus != nil {
		s.bus.Publish(events.DialogUpdated{ChatID: chat.ID})
	}
	return chat.ID, nil
}

// Mute silences a chat until the given time; the zero time unmutes.
func (s *ActionService) Mute(ctx context.Context, chatID int64, until time.Time) error {
	if s == nil || s.dialogs == nil {
		return errors.New("mute: not connected")
	}
	if err := s.dialogs.Mute(ctx, chatID, until); err != nil {
		return err
	}
	s.recordChatFact(ctx, chatID, "mute", s.chats.SetMutedUntil(ctx, chatID, until))
	return nil
}

// Pin puts a chat at the top of the list, or takes it back out.
func (s *ActionService) Pin(ctx context.Context, chatID int64, pinned bool) error {
	if s == nil || s.dialogs == nil {
		return errors.New("pin: not connected")
	}
	if err := s.dialogs.Pin(ctx, chatID, pinned); err != nil {
		return err
	}
	s.recordChatFact(ctx, chatID, "pin", s.chats.SetPinned(ctx, chatID, pinned))
	return nil
}

// MarkUnread puts the by-hand dot on a chat, the way a person flags a
// conversation to come back to.
func (s *ActionService) MarkUnread(ctx context.Context, chatID int64) error {
	if s == nil || s.dialogs == nil {
		return errors.New("mark unread: not connected")
	}
	if err := s.dialogs.MarkUnread(ctx, chatID, true); err != nil {
		return err
	}
	s.recordChatFact(ctx, chatID, "unread mark", s.chats.SetUnreadMark(ctx, chatID, true))
	return nil
}

// MarkRead acknowledges everything in a chat without opening it: the read
// receipt up to the newest message the mirror holds, and the by-hand dot
// cleared when it was set. Two requests at most, and the second only when
// there is a dot to clear.
func (s *ActionService) MarkRead(ctx context.Context, chatID int64, marked bool) error {
	if s == nil || s.dialogs == nil || s.marker == nil {
		return errors.New("mark read: not connected")
	}
	newest, err := s.chats.GetMessages(ctx, chatID, 1, 0)
	if err != nil {
		return fmt.Errorf("mark read: newest message: %w", err)
	}
	if len(newest) > 0 {
		if err := s.marker.MarkRead(ctx, chatID, newest[0].ID); err != nil {
			return err
		}
		s.recordChatFact(ctx, chatID, "read", s.chats.SetUnread(ctx, chatID, 0))
	}
	if marked {
		if err := s.dialogs.MarkUnread(ctx, chatID, false); err != nil {
			return err
		}
		s.recordChatFact(ctx, chatID, "unread mark", s.chats.SetUnreadMark(ctx, chatID, false))
	}
	return nil
}

func (s *ActionService) recordChatFact(_ context.Context, chatID int64, what string, err error) {
	if err != nil {
		s.log.Warn("actions: server accepted the "+what+" but the mirror was not updated", "chat_id", chatID, "err", err)
		return
	}
	s.publish(events.DialogUpdated{ChatID: chatID})
}
