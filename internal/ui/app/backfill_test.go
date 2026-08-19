package app

import (
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/ui/keymap"
	"github.com/kar43lov/lazytg/internal/ui/palette"
	"github.com/kar43lov/lazytg/internal/ui/panes/chats"
)

// recordingBackfiller captures the chat ids the app asked history for. The
// signal channel exists because the app enqueues from its own goroutine — the
// update loop must not block on a full queue — so assertions have to wait
// rather than read immediately.
type recordingBackfiller struct {
	mu     sync.Mutex
	ids    []int64
	signal chan int64
}

func newRecordingBackfiller() *recordingBackfiller {
	return &recordingBackfiller{signal: make(chan int64, 8)}
}

func (r *recordingBackfiller) Enqueue(chatID int64) {
	r.mu.Lock()
	r.ids = append(r.ids, chatID)
	r.mu.Unlock()
	select {
	case r.signal <- chatID:
	default:
	}
}

func (r *recordingBackfiller) await(t *testing.T) int64 {
	t.Helper()
	select {
	case id := <-r.signal:
		return id
	case <-time.After(2 * time.Second):
		t.Fatalf("no history request arrived")
		return 0
	}
}

func sizedApp(t *testing.T, deps Deps) App {
	t.Helper()
	a := New(deps)
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return model.(App)
}

// Every path that opens a chat has to pull its history: the thread pane reads
// the local mirror only, and a chat that just arrived from the dialog sync
// holds no messages, so it would open empty and stay empty.
func TestOpeningChat_RequestsHistoryOnEveryPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		msg    tea.Msg
		chatID int64
	}{
		{"chat row pick", chats.ChatSelectedMsg{ChatID: 4242}, 4242},
		{"palette pick", palette.SelectedMsg{ChatID: 777}, 777},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			backfiller := newRecordingBackfiller()
			a := sizedApp(t, Deps{Keymap: keymap.Default(), Backfiller: backfiller})

			if _, cmd := a.Update(tc.msg); cmd == nil {
				t.Logf("no cmd returned — fine, the request is out-of-band")
			}
			if got := backfiller.await(t); got != tc.chatID {
				t.Fatalf("history requested for %d, want %d", got, tc.chatID)
			}
		})
	}
}

// Offline (no backfiller wired) the chat must still open — a nil interface has
// to degrade to a no-op rather than panic.
func TestOpeningChat_NoBackfillerIsHarmless(t *testing.T) {
	t.Parallel()

	a := sizedApp(t, Deps{Keymap: keymap.Default()})
	model, _ := a.Update(chats.ChatSelectedMsg{ChatID: 7})
	a = model.(App)

	if a.thread.ChatID() != 7 {
		t.Fatalf("chat must open regardless of backfiller, got %d", a.thread.ChatID())
	}
}

// A zero chat id is not a chat; asking Telegram for it would be a wasted
// round-trip on a peer that cannot resolve.
func TestRequestHistory_IgnoresZeroChatID(t *testing.T) {
	t.Parallel()

	backfiller := newRecordingBackfiller()
	a := sizedApp(t, Deps{Keymap: keymap.Default(), Backfiller: backfiller})
	a.onChatOpened(0)

	select {
	case id := <-backfiller.signal:
		t.Fatalf("chat id 0 must not be requested, got %d", id)
	case <-time.After(200 * time.Millisecond):
	}
}
