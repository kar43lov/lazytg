package thread

import (
	"strings"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// fixedNow is the "current time" the separator tests run against, so
// "Today" and "Yesterday" mean something stable.
func fixedNow() time.Time {
	return time.Date(2026, 9, 4, 17, 30, 0, 0, time.Local)
}

// threadAcrossDays builds a pane holding one message per given day.
func threadAcrossDays(t *testing.T, days ...time.Time) Model {
	t.Helper()
	msgs := make([]domain.Message, len(days))
	for i, day := range days {
		msgs[i] = domain.Message{
			ID: int64(i + 1), ChatID: 7, FromID: 7, Date: day,
			Text: "message " + string(rune('a'+i)),
		}
	}
	m, _ := sized(New()).OpenChat(7)
	m = m.SetClock(fixedNow)
	m, _ = m.Update(messagesLoadedMsg{chatID: 7, gen: m.loadGen, messages: msgs})
	return m
}

// The thread prints times and only times. Inside one day that is right;
// across days it actively misleads — a live run on 04.09.2026 showed
// 16:35 followed by 15:10 and read as a broken sort, when in fact a
// fortnight had passed. One rule per day is what every chat client draws
// and what makes the ordering legible.
func TestDaySeparator_MarksEveryChangeOfDay(t *testing.T) {
	t.Parallel()

	m := threadAcrossDays(t,
		time.Date(2026, 8, 19, 16, 35, 0, 0, time.Local),
		time.Date(2026, 8, 19, 16, 36, 0, 0, time.Local),
		time.Date(2026, 9, 3, 15, 10, 0, 0, time.Local),
		time.Date(2026, 9, 4, 15, 10, 0, 0, time.Local),
	)

	content, _ := m.renderContent()
	var seen []string
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "─") {
			seen = append(seen, strings.TrimSpace(stripANSI(line)))
		}
	}
	if len(seen) != 3 {
		t.Fatalf("drew %d separators for three days, want 3: %q", len(seen), seen)
	}
	want := []string{"August 19", "Yesterday", "Today"}
	for i, w := range want {
		if !strings.Contains(seen[i], w) {
			t.Errorf("separator %d = %q, want it to name %q", i, seen[i], w)
		}
	}
}

// Two messages on the same day get one rule, not two.
func TestDaySeparator_OnePerDayNotPerMessage(t *testing.T) {
	t.Parallel()

	day := time.Date(2026, 8, 19, 10, 0, 0, 0, time.Local)
	m := threadAcrossDays(t, day, day.Add(time.Hour), day.Add(2*time.Hour))

	content, _ := m.renderContent()
	if got := strings.Count(content, "─"); got == 0 {
		t.Fatalf("no separator drawn at all")
	}
	var rules int
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "─") {
			rules++
		}
	}
	if rules != 1 {
		t.Fatalf("drew %d separators for one day, want 1", rules)
	}
}

// A separator belongs to no message: clicking it must not select the
// message below, and a drag across it must not pick up a phantom row.
func TestDaySeparator_IsNotPartOfAnyMessage(t *testing.T) {
	t.Parallel()

	m := threadAcrossDays(t,
		time.Date(2026, 8, 19, 16, 35, 0, 0, time.Local),
		time.Date(2026, 9, 4, 15, 10, 0, 0, time.Local),
	)
	content, spans := m.renderContent()
	if len(spans) != 2 {
		t.Fatalf("built %d spans for two messages, want 2", len(spans))
	}
	for i, line := range strings.Split(content, "\n") {
		if !strings.Contains(line, "─") {
			continue
		}
		if idx := spanIndexAt(spans, i); idx >= 0 {
			t.Fatalf("separator line %d resolves to message %d; it must belong to none", i, spans[idx].id)
		}
	}
}

// The label answers "what day was this for me": the two most recent days
// by name, older ones by date, and the year only when it is not this one.
func TestDayLabel(t *testing.T) {
	t.Parallel()

	now := fixedNow()
	cases := []struct {
		day  time.Time
		want string
	}{
		{time.Date(2026, 9, 4, 0, 1, 0, 0, time.Local), "Today"},
		{time.Date(2026, 9, 4, 23, 59, 0, 0, time.Local), "Today"},
		{time.Date(2026, 9, 3, 12, 0, 0, 0, time.Local), "Yesterday"},
		{time.Date(2026, 8, 19, 12, 0, 0, 0, time.Local), "August 19"},
		{time.Date(2025, 12, 31, 12, 0, 0, 0, time.Local), "December 31, 2025"},
	}
	for _, tc := range cases {
		if got := dayLabel(tc.day, now); got != tc.want {
			t.Errorf("dayLabel(%s) = %q, want %q", tc.day.Format(time.RFC3339), got, tc.want)
		}
	}
}
