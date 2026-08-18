package tg

import (
	"context"
	"fmt"

	"github.com/kar43lov/lazytg/internal/core/events"
)

// pollingWindow is how many messages one poll pulls per chat. It only has to
// cover what can arrive between two ticks, and everything older is filtered
// out by the watermark anyway — a larger window would buy nothing and cost
// bandwidth on every tick of every polled chat.
const pollingWindow = 20

// PollingFetcher adapts HistoryFetcher to the MessagePollingFetcher contract
// the fallback loop expects: "give me what is newer than this id, and tell me
// the new high-water mark".
//
// It exists so PollingFallback never sees gotd types and never has to know
// that a poll is a plain messages.getHistory from offset zero.
type PollingFetcher struct {
	history *HistoryFetcher
}

// NewPollingFetcher wires a fetcher over an existing HistoryFetcher, so a
// polled chat goes through exactly the same decode path (and the same media
// extraction) as one opened by hand.
func NewPollingFetcher(history *HistoryFetcher) *PollingFetcher {
	return &PollingFetcher{history: history}
}

// Latest returns the messages in chat newer than sinceID, plus the highest id
// observed — which is sinceID itself when the chat has nothing new, so the
// caller's watermark never moves backwards.
//
// A message equal to sinceID is excluded: Telegram returns the window
// inclusive of the newest known message, and republishing it on every tick
// would make the fallback a duplicate generator rather than a safety net.
func (p *PollingFetcher) Latest(ctx context.Context, chat PolledChat, sinceID int64) ([]events.MessageReceived, int64, error) {
	if p.history == nil {
		return nil, sinceID, fmt.Errorf("polling: no history fetcher for chat %d", chat.ChatID)
	}
	msgs, _, err := p.history.Fetch(ctx, chat.ChatID, chat.AccessHash, chat.Type, pollingWindow, 0)
	if err != nil {
		return nil, sinceID, fmt.Errorf("polling: fetch chat %d: %w", chat.ChatID, err)
	}

	newest := sinceID
	out := make([]events.MessageReceived, 0, len(msgs))
	for _, m := range msgs {
		if m.ID <= sinceID {
			continue
		}
		if m.ID > newest {
			newest = m.ID
		}
		out = append(out, events.MessageReceived{
			ChatID:    m.ChatID,
			MessageID: m.ID,
			Text:      m.Text,
			FromID:    m.FromID,
			Date:      m.Date,
			Media:     m.Media,
		})
	}
	return out, newest, nil
}
