package tg

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
)

func pollingChat() PolledChat {
	return PolledChat{ChatID: 7, AccessHash: 99, Type: "private"}
}

// TestPollingFetcher_SkipsWhatIsAlreadySeen is the property that makes the
// fallback a safety net instead of a duplicate generator: Telegram returns
// the window inclusive of the newest known message, so a fetcher that
// published everything it received would republish the same message on every
// tick, forever.
func TestPollingFetcher_SkipsWhatIsAlreadySeen(t *testing.T) {
	t.Parallel()
	stub := &stubGetHistory{responses: map[int]tg.MessagesMessagesClass{
		0: &tg.MessagesMessages{Messages: []tg.MessageClass{
			makeMsg(12, 7, "newest", 0),
			makeMsg(11, 7, "middle", 0),
			makeMsg(10, 7, "already seen", 0),
		}},
	}}

	got, newest, err := NewPollingFetcher(NewHistoryFetcher(stub)).
		Latest(context.Background(), pollingChat(), 10)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("published %d messages want 2: %+v", len(got), got)
	}
	for _, m := range got {
		if m.MessageID <= 10 {
			t.Fatalf("republished message %d at or below the watermark", m.MessageID)
		}
		if m.ChatID != 7 {
			t.Fatalf("chat id = %d want 7", m.ChatID)
		}
	}
	if newest != 12 {
		t.Fatalf("watermark = %d want 12", newest)
	}
}

// TestPollingFetcher_WatermarkNeverGoesBackwards covers the quiet tick and the
// failed one. Either must leave the caller's watermark where it was: a
// fetcher that answered 0 would make the next tick republish the entire
// window it just decided to skip.
func TestPollingFetcher_WatermarkNeverGoesBackwards(t *testing.T) {
	t.Parallel()
	t.Run("nothing new", func(t *testing.T) {
		t.Parallel()
		stub := &stubGetHistory{responses: map[int]tg.MessagesMessagesClass{
			0: &tg.MessagesMessages{Messages: []tg.MessageClass{makeMsg(10, 7, "already seen", 0)}},
		}}
		got, newest, err := NewPollingFetcher(NewHistoryFetcher(stub)).
			Latest(context.Background(), pollingChat(), 10)
		if err != nil {
			t.Fatalf("Latest: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("published %d messages want 0: %+v", len(got), got)
		}
		if newest != 10 {
			t.Fatalf("watermark = %d want 10", newest)
		}
	})

	t.Run("fetch fails", func(t *testing.T) {
		t.Parallel()
		stub := &stubGetHistory{err: errors.New("network down")}
		got, newest, err := NewPollingFetcher(NewHistoryFetcher(stub)).
			Latest(context.Background(), pollingChat(), 10)
		if err == nil {
			t.Fatal("Latest returned nil error for a failed fetch")
		}
		if got != nil {
			t.Fatalf("messages returned alongside an error: %+v", got)
		}
		if newest != 10 {
			t.Fatalf("watermark = %d want 10 — a failed poll must not rewind it", newest)
		}
	})
}
