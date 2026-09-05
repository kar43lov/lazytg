package thread

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
)

// A forwarded message says who wrote it above its words; a message
// forwarded from a hidden account says only that it was forwarded.
func TestForwardedLine(t *testing.T) {
	t.Parallel()

	fwd := incoming(1, 0, "their words")
	fwd.Forwarded = &domain.Forward{From: "News \x1b]52;c;x\x07(Editor)", FromID: 500}
	hidden := incoming(2, 1, "more")
	hidden.Forwarded = &domain.Forward{}
	m := loadedThread(t, 0, fwd, hidden, incoming(3, 2, "plain"))
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "↪ forwarded from News ") || !strings.Contains(view, "(Editor)") {
		t.Fatalf("no origin line:\n%s", view)
	}
	if strings.Contains(m.View(), "\x1b]52") {
		t.Fatal("the origin's escape reached the screen")
	}
	if strings.Count(view, "↪ forwarded") != 2 {
		t.Fatalf("origin lines = %d, want 2:\n%s", strings.Count(view, "↪ forwarded"), view)
	}
}

// The newest pinned message in view sits in a bar under the header and
// takes one row from the viewport; the pin update moves the bar and the
// header mark, and unpinning takes both away.
func TestPinnedBar(t *testing.T) {
	t.Parallel()

	first := incoming(1, 0, "rules: be kind")
	first.Pinned = true
	m := loadedThread(t, 0, first, incoming(2, 1, "chatter"), incoming(3, 2, "more"))
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[1], "📌 user-7: rules: be kind") {
		t.Fatalf("no bar under the header:\n%s", ansi.Strip(m.View()))
	}
	if m.PinnedMessageID() != 1 || m.viewport.Height() != 30-2 {
		t.Fatalf("pinned id %d, viewport height %d", m.PinnedMessageID(), m.viewport.Height())
	}
	if !strings.Contains(ansi.Strip(m.View()), "user-7 📌") {
		t.Fatal("the pinned message's header carries no mark")
	}

	m, _ = m.Update(events.MessagesPinned{ChatID: 42, IDs: []int64{3}, Pinned: true})
	if m.PinnedMessageID() != 3 || !strings.Contains(ansi.Strip(m.View()), "📌 user-7: more") {
		t.Fatalf("the bar did not move to the newer pin: %d", m.PinnedMessageID())
	}
	m, _ = m.Update(events.MessagesPinned{ChatID: 99, IDs: []int64{1, 3}, Pinned: false})
	if m.PinnedMessageID() != 3 {
		t.Fatal("another chat's unpin touched this one")
	}
	m, _ = m.Update(events.MessagesPinned{ChatID: 42, IDs: []int64{1, 3}, Pinned: false})
	if m.PinnedMessageID() != 0 || strings.Contains(ansi.Strip(m.View()), "📌") || m.viewport.Height() != 30-1 {
		t.Fatalf("unpin left something behind: id %d height %d\n%s", m.PinnedMessageID(), m.viewport.Height(), ansi.Strip(m.View()))
	}
}
