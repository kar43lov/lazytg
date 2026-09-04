package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
)

// MessageEditor is the gotd-free contract for rewriting a message that is
// already on the server. Satisfied by *internal/tg.Editor in production.
type MessageEditor interface {
	Edit(ctx context.Context, chatID, messageID int64, text string) error
}

// MessageDeleter is the gotd-free contract for removing messages from the
// server. Satisfied by *internal/tg.Deleter in production. The returned count
// is what the server reported as affected.
type MessageDeleter interface {
	Delete(ctx context.Context, chatID int64, ids []int64, revoke bool) (int, error)
}

// ActionStore is the storage surface ActionService writes through: the same
// SaveMessage the live path uses, because an edit is an upsert of a row that
// already exists.
type ActionStore interface {
	SaveMessage(ctx context.Context, m domain.Message) error
	Message(ctx context.Context, chatID, messageID int64) (domain.Message, error)
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
	editor  MessageEditor
	deleter MessageDeleter
	store   ActionStore
	bus     EventPublisher
	log     *slog.Logger
}

// EventPublisher is the slice of events.Bus this service needs.
type EventPublisher interface {
	Publish(ev events.Event)
}

// NewActionService wires the service. Any of editor, deleter and bus may be
// nil, which is how an offline session is expressed — the corresponding
// operation then reports that it is unavailable rather than panicking.
func NewActionService(editor MessageEditor, deleter MessageDeleter, store ActionStore, bus EventPublisher, log *slog.Logger) *ActionService {
	if log == nil {
		log = slog.New(noopHandler{})
	}
	return &ActionService{
		editor:  editor,
		deleter: deleter,
		store:   store,
		bus:     bus,
		log:     log,
	}
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

	if err := s.editor.Edit(ctx, chatID, messageID, text); err != nil {
		return err
	}

	msg.Text = text
	if err := s.store.SaveMessage(ctx, msg); err != nil {
		s.log.Warn("edit: server accepted the edit but the mirror was not updated",
			"chat_id", chatID, "message_id", messageID, "err", err)
		return nil
	}
	s.publish(events.MessageEdited{ChatID: chatID, MessageID: messageID, Text: text})
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

func (s *ActionService) publish(ev events.Event) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(ev)
}
