package thread

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/kar43lov/lazytg/internal/core/events"
)

func countTicks(view string) (read, sent int) {
	for _, line := range strings.Split(ansi.Strip(view), "\n") {
		switch {
		case strings.Contains(line, "✓✓"):
			read++
		case strings.Contains(line, "✓"):
			sent++
		}
	}
	return read, sent
}

// One tick on a message you sent, two once the other side has read it,
// none on theirs. The pointer moves forward on the update for this chat
// and ignores another chat's, and a stale one.
func TestTicks_OneWhenSentTwoWhenRead(t *testing.T) {
	t.Parallel()

	m := loadedThread(t, 0, outgoing(1, 0, "first"), outgoing(2, 1, "second"), incoming(3, 2, "theirs"))
	m = m.MarkReadOutbox(1)
	if read, sent := countTicks(m.View()); read != 1 || sent != 1 {
		t.Fatalf("after open: read %d sent %d\n%s", read, sent, ansi.Strip(m.View()))
	}

	m, _ = m.Update(events.ChatReadOutbox{ChatID: 99, MaxID: 2})
	if read, _ := countTicks(m.View()); read != 1 {
		t.Fatalf("another chat's pointer moved this one")
	}
	m, _ = m.Update(events.ChatReadOutbox{ChatID: 42, MaxID: 2})
	if read, sent := countTicks(m.View()); read != 2 || sent != 0 {
		t.Fatalf("after the update: read %d sent %d", read, sent)
	}
	m, _ = m.Update(events.ChatReadOutbox{ChatID: 42, MaxID: 1})
	if read, _ := countTicks(m.View()); read != 2 {
		t.Fatalf("a stale pointer took a tick away")
	}
	if m.ReadOutboxMaxID() != 2 {
		t.Fatalf("pointer = %d", m.ReadOutboxMaxID())
	}
}
