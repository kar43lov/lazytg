package tg

import (
	"context"
	"fmt"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/kar43lov/lazytg/internal/core/domain"
	coresync "github.com/kar43lov/lazytg/internal/core/sync"
)

// MessagesGetHistoryClient is the subset of *tg.Client that HistoryFetcher
// needs. Declaring it here lets unit tests substitute a stub without spinning
// up tgtest, and keeps the dependency surface explicit.
type MessagesGetHistoryClient interface {
	MessagesGetHistory(ctx context.Context, request *tg.MessagesGetHistoryRequest) (tg.MessagesMessagesClass, error)
}

// HistoryFetcher loads message history from Telegram via MTProto. It exists
// so that internal/core/sync can satisfy its HistoryProvider interface
// against a concrete gotd-aware implementation without importing gotd itself.
type HistoryFetcher struct {
	api  MessagesGetHistoryClient
	self *Self
}

// NewHistoryFetcher returns a HistoryFetcher that talks to api. Call sites
// pass either Client.API() in production or a stub in tests.
func NewHistoryFetcher(api MessagesGetHistoryClient) *HistoryFetcher {
	return &HistoryFetcher{api: api}
}

// WithSelf tells the fetcher which chat is the account's own, so messages
// there come back as the account's rather than as the peer's.
func (h *HistoryFetcher) WithSelf(self *Self) *HistoryFetcher {
	h.self = self
	return h
}

// Fetch retrieves up to limit messages older than offsetID from the given
// peer. peerType matches domain.ChatType ("private", "group", "supergroup",
// "channel"); accessHash is read from the local peers cache (it is ignored
// for plain groups). hasMore reports whether the server indicated additional
// messages exist beyond the returned slice — callers use it to drive
// pagination.
//
// MessageEmpty entries are dropped — there is nothing in them. Service
// messages become rows with Telegram's own sentence as their text.
func (h *HistoryFetcher) Fetch(ctx context.Context, peerID, accessHash int64, peerType string, limit, offsetID int) (msgs []domain.Message, hasMore bool, err error) {
	peer, err := buildInputPeer(peerID, accessHash, peerType)
	if err != nil {
		return nil, false, err
	}
	res, err := h.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer:     peer,
		Limit:    limit,
		OffsetID: offsetID,
	})
	if err != nil {
		if d, ok := tgerr.AsFloodWait(err); ok {
			return nil, false, &coresync.FloodWaitError{RetryAfter: d}
		}
		return nil, false, fmt.Errorf("messages.getHistory peer=%d: %w", peerID, err)
	}
	return decodeHistory(res, peerID, limit, h.self), describesPartialResult(res, limit), nil
}

// buildInputPeer maps domain peer metadata to the gotd InputPeer variant.
// AccessHash is required for users and channels and ignored for plain groups.
func buildInputPeer(peerID, accessHash int64, peerType string) (tg.InputPeerClass, error) {
	switch peerType {
	case string(domain.ChatTypePrivate):
		return &tg.InputPeerUser{UserID: peerID, AccessHash: accessHash}, nil
	case string(domain.ChatTypeGroup):
		return &tg.InputPeerChat{ChatID: peerID}, nil
	case string(domain.ChatTypeChannel), string(domain.ChatTypeSupergroup):
		return &tg.InputPeerChannel{ChannelID: peerID, AccessHash: accessHash}, nil
	default:
		return nil, fmt.Errorf("unknown peer type %q", peerType)
	}
}

// decodeHistory converts the gotd response to domain messages, dropping
// non-Message entries.
func decodeHistory(res tg.MessagesMessagesClass, chatID int64, limit int, self *Self) []domain.Message {
	mod, ok := res.AsModified()
	if !ok {
		return nil
	}
	raw := mod.GetMessages()
	out := make([]domain.Message, 0, len(raw))
	for _, mc := range raw {
		switch m := mc.(type) {
		case *tg.Message:
			out = append(out, convertMessage(m, chatID, self))
		case *tg.MessageService:
			out = append(out, convertService(m, chatID, self))
		}
	}
	_ = limit
	return out
}

func convertMessage(m *tg.Message, chatID int64, self *Self) domain.Message {
	replyTo := replyToOf(m)
	return domain.Message{
		ID:        int64(m.ID),
		ChatID:    chatID,
		FromID:    senderOf(m, chatID),
		Date:      time.Unix(int64(m.Date), 0).UTC(),
		Text:      messageText(m),
		ReplyTo:   replyTo,
		Media:     MediaFromMessage(m),
		Outgoing:  m.Out || self.Owns(chatID),
		Reactions: ReactionsFromMessage(m),
		Entities:  EntitiesFromMessage(m),
		EditDate:  editDateOf(m),
		Buttons:   ButtonsFromMessage(m),
	}
}

// editDateOf is when the message was last rewritten, or the zero time.
func editDateOf(m *tg.Message) time.Time {
	if d, ok := m.GetEditDate(); ok && d != 0 {
		return time.Unix(int64(d), 0).UTC()
	}
	return time.Time{}
}

// replyToOf names the message m answers, or 0.
//
// Pulled out of the history converter because the live path needs the same
// answer: a reply that arrived as an update had no parent until the chat was
// reopened, so the quoted line and the "go to what this answers" gesture
// both went missing on exactly the messages a user is most likely to try
// them on — the ones that just came in.
func replyToOf(m *tg.Message) int64 {
	if m == nil {
		return 0
	}
	rt, ok := m.GetReplyTo()
	if !ok {
		return 0
	}
	hdr, ok := rt.(*tg.MessageReplyHeader)
	if !ok {
		return 0
	}
	id, ok := hdr.GetReplyToMsgID()
	if !ok {
		return 0
	}
	return int64(id)
}

// senderOf names the sender of m, filling in what the wire format leaves out.
//
// Telegram sends from_id only when it adds something: in a group or a channel
// it says which member wrote, but in a 1:1 dialog the sender follows from the
// out flag and the peer, so the field is simply absent. Reading it blindly
// yields 0 — which the thread pane renders as "system" and the mirror then
// stored, so a private conversation displayed as a wall of service messages.
// The bug was invisible until a chat was re-opened: a live update sometimes
// arrives in the short form, where gotd fills from_id in, and the next
// messages.getHistory overwrote that row with NULL (seen live 19.08.2026).
//
// An outgoing message keeps id 0 deliberately. Naming the reader needs a
// self-id lookup this layer would have to plumb through every constructor,
// and it buys nothing: domain.Message.Outgoing already says whose message it
// is, and that is what the pane labels from.
func senderOf(m *tg.Message, chatID int64) int64 {
	if from, ok := m.GetFromID(); ok {
		return peerIDToInt64(from)
	}
	if _, private := m.PeerID.(*tg.PeerUser); private && !m.Out {
		return chatID
	}
	return 0
}

func peerIDToInt64(p tg.PeerClass) int64 {
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

// describesPartialResult reports whether the response leaves room for more
// pages. MessagesMessages is the full result by definition, so we say no;
// for slice/channel-messages we report true when the page filled up — the
// server may still have older messages even if its declared Count is
// inexact, so length-based heuristic is the safer default.
func describesPartialResult(res tg.MessagesMessagesClass, limit int) bool {
	mod, ok := res.AsModified()
	if !ok {
		return false
	}
	if _, full := res.(*tg.MessagesMessages); full {
		// MessagesMessages is the full result by definition — no more pages.
		return false
	}
	return limit > 0 && len(mod.GetMessages()) >= limit
}
