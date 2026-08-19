package thread

import (
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
)

// TestApplyIncoming_OrdersByDateNotArrival covers the smoke finding of
// 19.08.2026: two messages sent a second apart during a network outage were
// rendered swapped once the link came back.
//
// The thread appended live messages unconditionally, which is correct only
// while a single producer exists. Since Stage 4 the live update path and the
// polling fallback both publish MessageReceived, they race, and a reconnect
// hands over a backlog through whichever wins — so arrival order stopped
// being chronological order.
func TestApplyIncoming_OrdersByDateNotArrival(t *testing.T) {
	t.Parallel()

	const chatID int64 = 275641346
	m := sized(New())
	m = m.SwitchTo(chatID)

	base := time.Date(2026, 8, 19, 11, 18, 20, 0, time.UTC)
	// Delivered newest-first, which is what a history-backed poll returns.
	m, _ = m.applyIncoming(events.MessageReceived{
		ChatID: chatID, MessageID: 21, Date: base.Add(time.Second), Text: "789",
	})
	m, _ = m.applyIncoming(events.MessageReceived{
		ChatID: chatID, MessageID: 20, Date: base, Text: "567",
	})

	got := m.Messages()
	if len(got) != 2 {
		t.Fatalf("messages = %d, want 2", len(got))
	}
	if got[0].ID != 20 || got[1].ID != 21 {
		t.Fatalf("thread order = [%d %d], want [20 21] — rendered in arrival order, not by date",
			got[0].ID, got[1].ID)
	}
}

// TestApplyIncoming_BreaksTiesByID pins the same-second case. Telegram stamps
// message dates in whole seconds, so a burst shares a timestamp and only the
// server's own id says which came first.
func TestApplyIncoming_BreaksTiesByID(t *testing.T) {
	t.Parallel()

	const chatID int64 = 1
	m := sized(New())
	m = m.SwitchTo(chatID)

	same := time.Date(2026, 8, 19, 11, 18, 20, 0, time.UTC)
	for _, id := range []int64{33, 31, 32} {
		m, _ = m.applyIncoming(events.MessageReceived{ChatID: chatID, MessageID: id, Date: same})
	}

	got := m.Messages()
	if len(got) != 3 {
		t.Fatalf("messages = %d, want 3", len(got))
	}
	if got[0].ID != 31 || got[1].ID != 32 || got[2].ID != 33 {
		t.Fatalf("thread order = [%d %d %d], want [31 32 33]", got[0].ID, got[1].ID, got[2].ID)
	}
}

// TestApplyIncoming_AppendsTheCommonCaseAtTheTail guards against the fix
// turning every live message into a scan of the whole thread: a message newer
// than everything on screen must land at the end, which is the shape the
// sticky-scroll behaviour and the pagination cursors both assume.
func TestApplyIncoming_AppendsTheCommonCaseAtTheTail(t *testing.T) {
	t.Parallel()

	const chatID int64 = 1
	m := sized(New())
	m = m.SwitchTo(chatID)

	start := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	for i := int64(1); i <= 5; i++ {
		m, _ = m.applyIncoming(events.MessageReceived{
			ChatID: chatID, MessageID: i, Date: start.Add(time.Duration(i) * time.Minute),
		})
	}

	got := m.Messages()
	for i, msg := range got {
		if msg.ID != int64(i+1) {
			t.Fatalf("message %d has id %d, want %d", i, msg.ID, i+1)
		}
	}
}

// TestInsertByDate_LeavesOlderHistoryAlone checks the insertion point itself
// against a thread that already holds loaded history: a live message must not
// disturb the rows above it.
func TestInsertByDate_LeavesOlderHistoryAlone(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 19, 9, 0, 0, 0, time.UTC)
	history := []domain.Message{
		{ID: 1, Date: base},
		{ID: 2, Date: base.Add(time.Hour)},
		{ID: 4, Date: base.Add(3 * time.Hour)},
	}
	got := insertByDate(history, domain.Message{ID: 3, Date: base.Add(2 * time.Hour)})

	want := []int64{1, 2, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("position %d = id %d, want %d (full order %v)", i, got[i].ID, id, got)
		}
	}
}
