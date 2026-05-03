package thread

import (
	"testing"
	"time"

	"github.com/pgmac/lazytg/internal/core/domain"
	"github.com/pgmac/lazytg/internal/core/events"
)

// TestLoadJumpWindow_PreservesLiveAppendsDuringAsync covers the race
// where a live MessageReceived lands between SwitchTo (pre-position)
// and LoadJumpWindow (async result). Without the merge in
// LoadJumpWindow the live row would be silently dropped from the UI
// — see codex review finding #2 (May 2026).
func TestLoadJumpWindow_PreservesLiveAppendsDuringAsync(t *testing.T) {
	t.Parallel()

	const chatID int64 = 7
	m := sized(New())
	m = m.SwitchTo(chatID)

	// Simulate a live event that arrives while JumpContext is still
	// running. applyIncoming appends the row to m.messages.
	live := events.MessageReceived{
		ChatID:    chatID,
		MessageID: 1000,
		FromID:    99,
		Date:      time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC),
		Text:      "live ping during jump",
	}
	m, _ = m.applyIncoming(live)
	if got := len(m.messages); got != 1 {
		t.Fatalf("precondition: live append count = %d, want 1", got)
	}

	// Now JumpContext returns its window. Without the merge the live
	// row would be wiped here.
	window := []domain.Message{
		{ID: 50, ChatID: chatID, Date: time.Date(2026, 1, 1, 0, 0, 50, 0, time.UTC), Text: "ctx-50"},
		{ID: 51, ChatID: chatID, Date: time.Date(2026, 1, 1, 0, 0, 51, 0, time.UTC), Text: "ctx-51"},
		{ID: 52, ChatID: chatID, Date: time.Date(2026, 1, 1, 0, 0, 52, 0, time.UTC), Text: "ctx-52"},
	}
	m = m.LoadJumpWindow(chatID, window, 51, 1)

	got := m.Messages()
	if len(got) != 4 {
		t.Fatalf("merged messages: want 4 (window 3 + live 1), got %d (%v)", len(got), got)
	}
	for i, want := range []int64{50, 51, 52, 1000} {
		if got[i].ID != want {
			t.Errorf("merged[%d].ID = %d, want %d", i, got[i].ID, want)
		}
	}
}

// TestLoadJumpWindow_DropsLiveAlreadyInWindow guards the dedupe path:
// a live row that the JumpContext SQL also picked up (because the
// LiveService persisted it before the SELECT ran) must not render
// twice.
func TestLoadJumpWindow_DropsLiveAlreadyInWindow(t *testing.T) {
	t.Parallel()

	const chatID int64 = 8
	m := sized(New())
	m = m.SwitchTo(chatID)

	live := events.MessageReceived{
		ChatID:    chatID,
		MessageID: 52,
		FromID:    99,
		Date:      time.Date(2026, 1, 1, 0, 0, 52, 0, time.UTC),
		Text:      "raced into the window",
	}
	m, _ = m.applyIncoming(live)

	window := []domain.Message{
		{ID: 50, ChatID: chatID, Date: time.Date(2026, 1, 1, 0, 0, 50, 0, time.UTC), Text: "ctx-50"},
		{ID: 51, ChatID: chatID, Date: time.Date(2026, 1, 1, 0, 0, 51, 0, time.UTC), Text: "ctx-51"},
		{ID: 52, ChatID: chatID, Date: time.Date(2026, 1, 1, 0, 0, 52, 0, time.UTC), Text: "ctx-52"},
	}
	m = m.LoadJumpWindow(chatID, window, 51, 1)

	got := m.Messages()
	if len(got) != 3 {
		t.Fatalf("dedupe: want 3 messages (no duplicate id=52), got %d (%v)", len(got), got)
	}
	for i, want := range []int64{50, 51, 52} {
		if got[i].ID != want {
			t.Errorf("dedup[%d].ID = %d, want %d", i, got[i].ID, want)
		}
	}
}

// TestSwitchTo_SameChatStillResetsForJumpPath locks in the contract
// that SwitchTo always clears the message slice — including the
// same-chat case. Without the reset, a search jump back to an older
// hit in the *currently open* chat would render the loaded ±N window
// alongside the entire prior tail (LoadJumpWindow's "preserve newer
// than maxWindowID" merge cannot tell preexisting tail apart from
// genuine live appends), producing a discontinuous thread with a
// missing gap in the middle.
func TestSwitchTo_SameChatStillResetsForJumpPath(t *testing.T) {
	t.Parallel()

	const chatID int64 = 11
	m := sized(New())
	// Bind the pane to chatID first (real flow: user opens the chat
	// via OpenChat / ChatSelectedMsg), so applyIncoming will accept
	// the seeded live events instead of dropping them as
	// foreign-chat noise.
	m = m.SwitchTo(chatID)
	// Seed a recent tail as if the user has been chatting and
	// scrolled to the bottom — these IDs are all newer than the
	// older hit they will jump to.
	tail := []domain.Message{
		{ID: 70, ChatID: chatID, Date: time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC), Text: "tail-70"},
		{ID: 71, ChatID: chatID, Date: time.Date(2026, 5, 3, 12, 0, 1, 0, time.UTC), Text: "tail-71"},
		{ID: 72, ChatID: chatID, Date: time.Date(2026, 5, 3, 12, 0, 2, 0, time.UTC), Text: "tail-72"},
	}
	for _, msg := range tail {
		m, _ = m.applyIncoming(events.MessageReceived{
			ChatID: msg.ChatID, MessageID: msg.ID, Date: msg.Date, Text: msg.Text,
		})
	}
	if got := len(m.messages); got != 3 {
		t.Fatalf("precondition: seeded tail count = %d, want 3", got)
	}

	// Same-chat jump path: SwitchTo must reset, not no-op.
	m = m.SwitchTo(chatID)
	if got := len(m.messages); got != 0 {
		t.Fatalf("SwitchTo same-chat must clear messages; got %d (would render discontinuous after LoadJumpWindow)", got)
	}

	// JumpContext returns the older window. Without the reset above,
	// LoadJumpWindow would merge [25..27] with the [70..72] tail and
	// render five messages with a gap — the bug this test guards.
	window := []domain.Message{
		{ID: 25, ChatID: chatID, Date: time.Date(2026, 1, 1, 0, 0, 25, 0, time.UTC), Text: "ctx-25"},
		{ID: 26, ChatID: chatID, Date: time.Date(2026, 1, 1, 0, 0, 26, 0, time.UTC), Text: "ctx-26"},
		{ID: 27, ChatID: chatID, Date: time.Date(2026, 1, 1, 0, 0, 27, 0, time.UTC), Text: "ctx-27"},
	}
	m = m.LoadJumpWindow(chatID, window, 26, 1)

	got := m.Messages()
	if len(got) != 3 {
		t.Fatalf("same-chat jump must render only the loaded window; got %d (%v) — preexisting tail leaked through", len(got), got)
	}
	for i, want := range []int64{25, 26, 27} {
		if got[i].ID != want {
			t.Errorf("merged[%d].ID = %d, want %d", i, got[i].ID, want)
		}
	}
}

// TestLoadJumpWindow_PreservesOptimisticStateDuringAsync covers the
// async race where the user issues a send between SwitchTo (the input
// pane is rebound to the target chat the moment the search hit is
// picked) and LoadJumpWindow (the JumpContext result). ApplyDispatched
// inserts the pending row, the SendService bus fires Sent, then the
// jump window lands. Wiping outgoing here would silently drop the row —
// for 1:1 chats Telegram emits only UpdateShortSentMessage and never
// re-publishes a corresponding UpdateNewMessage, so the user would see
// the message disappear until a manual reload.
func TestLoadJumpWindow_PreservesOptimisticStateDuringAsync(t *testing.T) {
	t.Parallel()

	const chatID int64 = 7
	m := sized(New())
	m = m.SwitchTo(chatID)

	// User issues a send while JumpContext is in flight: the input
	// pane reports SendDispatchedMsg and the SendService publishes
	// Sent over the bus before the JumpContext goroutine returns.
	m = m.ApplyDispatched("local-1", chatID, "hi")
	m, _ = m.applyOutgoingState(events.OutgoingMessageStateChanged{
		LocalID:  "local-1",
		ChatID:   chatID,
		ServerID: 5550,
		State:    events.OutgoingStateSent,
	})
	if got := len(m.Outgoing()); got != 1 {
		t.Fatalf("precondition: optimistic row must exist before LoadJumpWindow; got %d", got)
	}

	window := []domain.Message{
		{ID: 50, ChatID: chatID, Date: time.Date(2026, 1, 1, 0, 0, 50, 0, time.UTC), Text: "ctx-50"},
		{ID: 51, ChatID: chatID, Date: time.Date(2026, 1, 1, 0, 0, 51, 0, time.UTC), Text: "ctx-51"},
	}
	m = m.LoadJumpWindow(chatID, window, 51, 0)

	out := m.Outgoing()
	if len(out) != 1 {
		t.Fatalf("LoadJumpWindow must preserve optimistic state added during async window; got %d entries", len(out))
	}
	if out[0].State != events.OutgoingStateSent {
		t.Fatalf("preserved entry state: got %q, want sent", out[0].State)
	}
	if out[0].Text != "hi" {
		t.Fatalf("preserved entry text: got %q, want hi", out[0].Text)
	}

	// The pendingServerIDs / finalizedLocalIDs maps must also survive
	// so a later applyIncoming (when the echo finally arrives, or in
	// group chats) can still dedupe against the optimistic row, and
	// a re-arrived SendDispatchedMsg cannot re-create a Pending entry
	// for the already-finalised localID.
	echo := events.MessageReceived{
		ChatID:    chatID,
		MessageID: 5550,
		Date:      time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC),
		Text:      "hi",
	}
	m, _ = m.applyIncoming(echo)
	if got := len(m.Outgoing()); got != 0 {
		t.Fatalf("echo dedup: pendingServerIDs lost across LoadJumpWindow — outgoing not cleaned up; got %d", got)
	}
}

// TestReloadAfterJumpFailure_PreservesOptimisticState locks in the
// symmetric preservation contract for the failure-recovery path. When
// JumpContext fails the app falls back to a chat-tail load via
// ReloadAfterJumpFailure (NOT OpenChat) so any optimistic-UI rows the
// user added during the async window — input pane was rebound to the
// target chat the moment the search hit was picked — survive into the
// recovered tail. Mirrors LoadJumpWindow's success-path preservation.
func TestReloadAfterJumpFailure_PreservesOptimisticState(t *testing.T) {
	t.Parallel()

	const chatID int64 = 7
	repo := newFakeRepo(50)
	m := sized(NewWithRepo(repo, nil, nil))
	m = m.SwitchTo(chatID)

	// User issues a send during the async JumpContext window: the bus
	// publishes Sent before the JumpContext goroutine returns its error.
	m = m.ApplyDispatched("local-1", chatID, "hi")
	m, _ = m.applyOutgoingState(events.OutgoingMessageStateChanged{
		LocalID:  "local-1",
		ChatID:   chatID,
		ServerID: 5550,
		State:    events.OutgoingStateSent,
	})
	if got := len(m.Outgoing()); got != 1 {
		t.Fatalf("precondition: optimistic row must exist before failure recovery; got %d", got)
	}

	// JumpContext failed — app calls ReloadAfterJumpFailure to load
	// the chat tail without the surrounding context.
	updated, cmd := m.ReloadAfterJumpFailure(chatID)
	if cmd == nil {
		t.Fatalf("ReloadAfterJumpFailure with wired repo must return a load Cmd")
	}
	if !updated.Loading() {
		t.Fatalf("ReloadAfterJumpFailure must set loading=true while the fetch is in flight")
	}
	if got := len(updated.Outgoing()); got != 1 {
		t.Fatalf("ReloadAfterJumpFailure must preserve optimistic state; got %d entries", got)
	}
	if updated.Outgoing()[0].State != events.OutgoingStateSent {
		t.Fatalf("preserved entry state: got %q, want sent", updated.Outgoing()[0].State)
	}

	// pendingServerIDs / finalizedLocalIDs must also survive so the
	// later echo (or the inverted-race re-Dispatched) still dedupes.
	echo := events.MessageReceived{
		ChatID:    chatID,
		MessageID: 5550,
		Date:      time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC),
		Text:      "hi",
	}
	updated, _ = updated.applyIncoming(echo)
	if got := len(updated.Outgoing()); got != 0 {
		t.Fatalf("echo dedup: pendingServerIDs lost across ReloadAfterJumpFailure; got %d", got)
	}
}

// TestReloadAfterJumpFailure_NoRepoIsNoop guards the placeholder-model
// path: with no repo wired the method must clear loading and return a
// nil cmd, matching OpenChat's contract on the same input.
func TestReloadAfterJumpFailure_NoRepoIsNoop(t *testing.T) {
	t.Parallel()

	m := sized(New())
	updated, cmd := m.ReloadAfterJumpFailure(7)
	if cmd != nil {
		t.Fatalf("ReloadAfterJumpFailure on no-repo model: expected nil cmd, got %T", cmd())
	}
	if updated.Loading() {
		t.Fatalf("ReloadAfterJumpFailure on no-repo model: loading must be cleared")
	}
}

// TestLoadJumpWindow_EmptyWindowKeepsTail guards an edge case: when
// JumpContext returns no rows (target missing fallback) but a live
// event already landed, we keep the tail rather than blanking the
// pane.
func TestLoadJumpWindow_EmptyWindowKeepsTail(t *testing.T) {
	t.Parallel()

	const chatID int64 = 9
	m := sized(New())
	m = m.SwitchTo(chatID)

	live := events.MessageReceived{
		ChatID:    chatID,
		MessageID: 200,
		Date:      time.Date(2026, 5, 3, 11, 0, 0, 0, time.UTC),
		Text:      "newer than nothing",
	}
	m, _ = m.applyIncoming(live)

	m = m.LoadJumpWindow(chatID, nil, 0, 0)

	got := m.Messages()
	if len(got) != 1 {
		t.Fatalf("empty-window merge: want 1 (the live row), got %d", len(got))
	}
	if got[0].ID != 200 {
		t.Errorf("preserved id = %d, want 200", got[0].ID)
	}
}
