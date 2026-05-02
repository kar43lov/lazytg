package thread

import (
	"strings"
	"testing"

	"github.com/pgmac/lazytg/internal/core/events"
)

// TestApplyDispatchedInsertsPendingRow verifies that the optimistic-UI
// shortcut path used by app/update.go (SendDispatchedMsg → thread)
// inserts a pending entry visible in both the model's Outgoing()
// snapshot and the rendered viewport content.
func TestApplyDispatchedInsertsPendingRow(t *testing.T) {
	t.Parallel()

	m := sized(NewWithRepo(newFakeRepo(0), nil, nil))
	m, _ = m.OpenChat(1)
	m = m.ApplyDispatched("local-1", 1, "hello")

	out := m.Outgoing()
	if len(out) != 1 {
		t.Fatalf("expected 1 outgoing entry, got %d", len(out))
	}
	if out[0].State != events.OutgoingStatePending {
		t.Fatalf("expected pending state, got %q", out[0].State)
	}
	if out[0].Text != "hello" {
		t.Fatalf("expected text=hello, got %q", out[0].Text)
	}

	// The viewport must reflect the pending row so the user sees the
	// "[⏳] hello" line as soon as Enter is pressed. The glyph itself
	// can vary by lipgloss styling; we only check the text body.
	if !strings.Contains(m.View(), "hello") {
		t.Fatalf("rendered view must contain pending text; got\n%s", m.View())
	}
}

// TestApplyDispatchedIgnoresOtherChat verifies that an optimistic
// insert intended for a different chat does not pollute the currently-
// open thread. The chats pane is bound to a single chat at a time but
// the dispatch routing in app/update.go is centralised — relying on
// the thread to drop mismatches keeps the routing logic simple.
func TestApplyDispatchedIgnoresOtherChat(t *testing.T) {
	t.Parallel()

	m := sized(NewWithRepo(newFakeRepo(0), nil, nil))
	m, _ = m.OpenChat(1)
	m = m.ApplyDispatched("local-1", 99, "wrong chat")

	if got := len(m.Outgoing()); got != 0 {
		t.Fatalf("expected 0 outgoing for other chat, got %d", got)
	}
}

// TestOutgoingStatePendingThenSentKeepsRow exercises the lifecycle:
// pending entry inserted via ApplyDispatched, then the bus event for
// state=sent flips the entry to Sent (no glyph). The row is held —
// applyIncoming will remove it when the live echo arrives — so the
// user never sees a visible gap between "[⏳] hi" and the real message.
// For private 1:1 chats Telegram emits UpdateShortSentMessage with no
// follow-up UpdateNewMessage, so dropping on Sent here would empty the
// thread until a manual reload.
func TestOutgoingStatePendingThenSentKeepsRow(t *testing.T) {
	t.Parallel()

	m := sized(NewWithRepo(newFakeRepo(0), nil, nil))
	m, _ = m.OpenChat(1)
	m = m.ApplyDispatched("local-1", 1, "hi")

	m, _ = m.Update(events.OutgoingMessageStateChanged{
		LocalID:  "local-1",
		ChatID:   1,
		ServerID: 555,
		State:    events.OutgoingStateSent,
	})

	out := m.Outgoing()
	if len(out) != 1 {
		t.Fatalf("expected sent state to keep row for visual continuity, got %d outgoing", len(out))
	}
	if out[0].State != events.OutgoingStateSent {
		t.Fatalf("expected state=sent, got %q", out[0].State)
	}
	if out[0].Text != "hi" {
		t.Fatalf("expected text preserved, got %q", out[0].Text)
	}
	if _, tracking := m.pendingServerIDs[555]; !tracking {
		t.Fatalf("expected pendingServerIDs to track 555 for dedup")
	}
	if _, finalized := m.finalizedLocalIDs["local-1"]; !finalized {
		t.Fatalf("expected localID to be marked finalized so a late SendDispatchedMsg cannot re-create it")
	}
}

// TestApplyDispatchedAfterSentSkipsInsert exercises the inverted race
// where the bus event for Sent (or Failed) reaches the thread before
// the synchronous SendDispatchedMsg from input. Without the
// finalizedLocalIDs guard the late ApplyDispatched would re-create a
// Pending row no future event could resolve.
func TestApplyDispatchedAfterSentSkipsInsert(t *testing.T) {
	t.Parallel()

	m := sized(NewWithRepo(newFakeRepo(0), nil, nil))
	m, _ = m.OpenChat(1)

	// Sent arrives first, before any optimistic row exists. The state
	// machine records the localID as finalized but cannot patch a row
	// (no text on hand).
	m, _ = m.Update(events.OutgoingMessageStateChanged{
		LocalID:  "local-1",
		ChatID:   1,
		ServerID: 555,
		State:    events.OutgoingStateSent,
	})
	if got := len(m.Outgoing()); got != 0 {
		t.Fatalf("Sent before Dispatched should leave outgoing empty (no text on hand), got %d", got)
	}

	// Late Dispatched must not resurrect the row.
	m = m.ApplyDispatched("local-1", 1, "hi")
	if got := len(m.Outgoing()); got != 0 {
		t.Fatalf("late ApplyDispatched after Sent must be a no-op, got %d outgoing", got)
	}
}

// TestApplyDispatchedAfterFailedSkipsInsert is the symmetric guard for
// Failed: a late SendDispatchedMsg after a terminal Failed event must
// not produce a phantom [⏳] row.
func TestApplyDispatchedAfterFailedSkipsInsert(t *testing.T) {
	t.Parallel()

	m := sized(NewWithRepo(newFakeRepo(0), nil, nil))
	m, _ = m.OpenChat(1)

	m, _ = m.Update(events.OutgoingMessageStateChanged{
		LocalID: "local-1",
		ChatID:  1,
		State:   events.OutgoingStateFailed,
		Error:   "boom",
	})
	m = m.ApplyDispatched("local-1", 1, "hi")
	if got := len(m.Outgoing()); got != 0 {
		t.Fatalf("late ApplyDispatched after Failed must be a no-op, got %d outgoing", got)
	}
}

// TestOutgoingStateFailedKeepsRowWithError verifies that a failed
// send leaves the row visible with the error reason so the user can
// decide whether to retry.
func TestOutgoingStateFailedKeepsRowWithError(t *testing.T) {
	t.Parallel()

	m := sized(NewWithRepo(newFakeRepo(0), nil, nil))
	m, _ = m.OpenChat(1)
	m = m.ApplyDispatched("local-1", 1, "hi")

	m, _ = m.Update(events.OutgoingMessageStateChanged{
		LocalID: "local-1",
		ChatID:  1,
		State:   events.OutgoingStateFailed,
		Error:   "MESSAGE_TOO_LONG",
	})

	out := m.Outgoing()
	if len(out) != 1 {
		t.Fatalf("expected failed entry kept, got %d", len(out))
	}
	if out[0].State != events.OutgoingStateFailed {
		t.Fatalf("state = %q, want failed", out[0].State)
	}
	if out[0].Error != "MESSAGE_TOO_LONG" {
		t.Fatalf("error = %q, want MESSAGE_TOO_LONG", out[0].Error)
	}
	if !strings.Contains(m.View(), "MESSAGE_TOO_LONG") {
		t.Fatalf("rendered view must surface failure reason")
	}
}

// TestLiveEchoDedupesOptimisticRow exercises the fix for the
// duplicate-render bug: when the SendService reports state=sent with
// serverID=555, and later a MessageReceived with MessageID=555
// arrives via the bus, the optimistic row must not be re-rendered as
// a regular history entry on top of the already-applied dedup.
func TestLiveEchoDedupesOptimisticRow(t *testing.T) {
	t.Parallel()

	m := sized(NewWithRepo(newFakeRepo(0), nil, nil))
	m, _ = m.OpenChat(1)
	m = m.ApplyDispatched("local-1", 1, "hi")
	m, _ = m.Update(events.OutgoingMessageStateChanged{
		LocalID:  "local-1",
		ChatID:   1,
		ServerID: 555,
		State:    events.OutgoingStateSent,
	})

	// The mapping was recorded; now feed the live echo. The thread
	// must end with exactly one rendered "hi" — the regular history
	// row — and zero outgoing entries.
	m, _ = m.Update(events.MessageReceived{
		ChatID:    1,
		MessageID: 555,
		Text:      "hi",
	})

	if got := strings.Count(m.View(), "hi"); got != 1 {
		t.Fatalf("expected exactly one rendered \"hi\", got %d in:\n%s", got, m.View())
	}
	if _, tracking := m.pendingServerIDs[555]; tracking {
		t.Fatalf("dedup mapping for 555 must be cleared after live echo arrived")
	}
}

// TestPendingBusEventBeforeDispatchedIsIgnored protects against the
// race where the SendService publishes OutgoingStateChanged{Pending}
// before the input pane's SendDispatchedMsg lands in the program loop.
// If applyOutgoingState{Pending} created an entry under those
// conditions it would render an empty "[⏳]" row that flickers until
// ApplyDispatched catches up. Treating Pending as a no-op eliminates
// the flicker — Sent | Failed transitions still arrive after
// ApplyDispatched has populated the entry, so the lifecycle remains
// correct.
func TestPendingBusEventBeforeDispatchedIsIgnored(t *testing.T) {
	t.Parallel()

	m := sized(NewWithRepo(newFakeRepo(0), nil, nil))
	m, _ = m.OpenChat(1)

	// Bus event arrives without ApplyDispatched having run yet.
	m, _ = m.Update(events.OutgoingMessageStateChanged{
		LocalID: "local-1",
		ChatID:  1,
		State:   events.OutgoingStatePending,
	})

	if got := len(m.Outgoing()); got != 0 {
		t.Fatalf("Pending bus event before dispatch should not insert an empty row; got %d outgoing", got)
	}
}

// TestOpenChatResetsOptimistic protects against optimistic state
// leaking between chats — switching to chat 2 must wipe pendings that
// were added while chat 1 was open.
func TestOpenChatResetsOptimistic(t *testing.T) {
	t.Parallel()

	m := sized(NewWithRepo(newFakeRepo(0), nil, nil))
	m, _ = m.OpenChat(1)
	m = m.ApplyDispatched("local-1", 1, "first chat")

	m, _ = m.OpenChat(2)
	if got := len(m.Outgoing()); got != 0 {
		t.Fatalf("OpenChat must reset outgoing; got %d", got)
	}
}
