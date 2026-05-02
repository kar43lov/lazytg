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

// TestOutgoingStatePendingThenSentDropsRow exercises the lifecycle:
// pending entry inserted via ApplyDispatched, then the bus event for
// state=sent flips the entry — but because the SendService publishes
// the serverID, the thread drops the entry and remembers the
// localID→serverID mapping for later dedup of the live echo.
func TestOutgoingStatePendingThenSentDropsRow(t *testing.T) {
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

	if got := len(m.Outgoing()); got != 0 {
		t.Fatalf("expected sent → drop, got %d outgoing", got)
	}
	if _, tracking := m.pendingServerIDs[555]; !tracking {
		t.Fatalf("expected pendingServerIDs to track 555 for dedup")
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
