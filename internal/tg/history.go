package tg

import (
	"context"
	"fmt"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/pgmac/lazytg/internal/core/domain"
	coresync "github.com/pgmac/lazytg/internal/core/sync"
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
	api MessagesGetHistoryClient
}

// NewHistoryFetcher returns a HistoryFetcher that talks to api. Call sites
// pass either Client.API() in production or a stub in tests.
func NewHistoryFetcher(api MessagesGetHistoryClient) *HistoryFetcher {
	return &HistoryFetcher{api: api}
}

// Fetch retrieves up to limit messages older than offsetID from the given
// peer. peerType matches domain.ChatType ("private", "group", "supergroup",
// "channel"); accessHash is read from the local peers cache (it is ignored
// for plain groups). hasMore reports whether the server indicated additional
// messages exist beyond the returned slice — callers use it to drive
// pagination.
//
// MessageEmpty and MessageService entries are silently dropped in v0.1: the
// UI cannot render service messages yet, and including them would only inflate
// the local index.
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
	return decodeHistory(res, peerID, limit), describesPartialResult(res, limit), nil
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
func decodeHistory(res tg.MessagesMessagesClass, chatID int64, limit int) []domain.Message {
	mod, ok := res.AsModified()
	if !ok {
		return nil
	}
	raw := mod.GetMessages()
	out := make([]domain.Message, 0, len(raw))
	for _, mc := range raw {
		m, ok := mc.(*tg.Message)
		if !ok {
			continue
		}
		out = append(out, convertMessage(m, chatID))
	}
	_ = limit
	return out
}

func convertMessage(m *tg.Message, chatID int64) domain.Message {
	var replyTo int64
	if rt, ok := m.GetReplyTo(); ok {
		if hdr, ok := rt.(*tg.MessageReplyHeader); ok {
			if id, ok := hdr.GetReplyToMsgID(); ok {
				replyTo = int64(id)
			}
		}
	}
	var fromID int64
	if from, ok := m.GetFromID(); ok {
		fromID = peerIDToInt64(from)
	}
	return domain.Message{
		ID:      int64(m.ID),
		ChatID:  chatID,
		FromID:  fromID,
		Date:    time.Unix(int64(m.Date), 0).UTC(),
		Text:    m.Message,
		ReplyTo: replyTo,
	}
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
