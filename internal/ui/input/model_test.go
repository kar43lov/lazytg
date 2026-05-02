package input

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/pgmac/lazytg/internal/core/domain"
	"github.com/pgmac/lazytg/internal/core/events"
	"github.com/pgmac/lazytg/internal/ui/keymap"
)

// fakeSender records SendText calls and lets tests pre-arm the result.
// All access is mutex-guarded because the input pane runs SendText in a
// tea.Cmd which goroutine-hops out of the test goroutine.
type fakeSender struct {
	mu      sync.Mutex
	calls   []sendCall
	localID string
	err     error
}

type sendCall struct {
	chatID  int64
	text    string
	replyTo int
}

func (f *fakeSender) SendText(_ context.Context, chatID int64, text string, replyTo int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, sendCall{chatID: chatID, text: text, replyTo: replyTo})
	return f.localID, f.err
}

func (f *fakeSender) Calls() []sendCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]sendCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func newInput(t *testing.T, send SendServiceInterface) Model {
	t.Helper()
	m := NewWithDeps(send, keymap.Default(), nil)
	m = m.SetWidth(60)
	m = m.SetFocus(true)
	updated, _ := m.Update(SetChatMsg{ChatID: 42})
	return updated
}

func TestNewWithDepsBootsBlurred(t *testing.T) {
	t.Parallel()

	m := NewWithDeps(nil, keymap.Default(), nil)
	if m.Focused {
		t.Fatalf("default Model should not be focused")
	}
	if m.History() == nil {
		t.Fatalf("history ring should be allocated")
	}
}

func TestSendOnEmptyTextareaIsNoop(t *testing.T) {
	t.Parallel()

	send := &fakeSender{localID: "L1"}
	m := newInput(t, send)
	got, cmd := m.Update(keyChord(tea.KeyEnter, 0))
	if cmd != nil {
		t.Fatalf("Enter on empty composer should not produce a Cmd")
	}
	if len(send.Calls()) != 0 {
		t.Fatalf("Enter on empty composer should not call SendText")
	}
	if got.Value() != "" {
		t.Fatalf("composer should remain empty, got %q", got.Value())
	}
}

func TestSendDispatchesAndClearsTextarea(t *testing.T) {
	t.Parallel()

	send := &fakeSender{localID: "L1"}
	m := newInput(t, send)
	m, _ = m.Update(keyText("h"))
	m, _ = m.Update(keyText("i"))
	if m.Value() != "hi" {
		t.Fatalf("seed: composer should hold 'hi', got %q", m.Value())
	}

	got, cmd := m.Update(keyChord(tea.KeyEnter, 0))
	if cmd == nil {
		t.Fatalf("Enter on non-empty composer must produce a Cmd")
	}
	if got.Value() != "" {
		t.Fatalf("composer should be cleared after Send, got %q", got.Value())
	}

	msg := cmd()
	dispatched, ok := msg.(SendDispatchedMsg)
	if !ok {
		t.Fatalf("expected SendDispatchedMsg, got %T (%v)", msg, msg)
	}
	if dispatched.LocalID != "L1" || dispatched.Text != "hi" || dispatched.ChatID != 42 {
		t.Fatalf("unexpected dispatch payload: %+v", dispatched)
	}

	calls := send.Calls()
	if len(calls) != 1 || calls[0].text != "hi" || calls[0].chatID != 42 {
		t.Fatalf("SendText call list mismatch: %+v", calls)
	}

	if got.History().Len() != 1 {
		t.Fatalf("history should record the sent message, got %d entries", got.History().Len())
	}
}

func TestSendIncludesReplyToAndClearsAfterDispatch(t *testing.T) {
	t.Parallel()

	send := &fakeSender{localID: "L9"}
	m := newInput(t, send)
	reply := &domain.Message{ID: 7777, ChatID: 42, Text: "old message"}
	m, _ = m.Update(SetReplyMsg{Msg: reply})
	if m.ReplyTo() == nil {
		t.Fatalf("SetReplyMsg should arm reply pointer")
	}
	m, _ = m.Update(keyText("y"))
	got, cmd := m.Update(keyChord(tea.KeyEnter, 0))
	if cmd == nil {
		t.Fatalf("Enter must produce a Cmd")
	}
	dispatched := cmd().(SendDispatchedMsg)
	if dispatched.ReplyTo != 7777 {
		t.Fatalf("reply id should propagate to dispatch, got %d", dispatched.ReplyTo)
	}
	if got.ReplyTo() != nil {
		t.Fatalf("reply pointer must be cleared after Send")
	}
	if calls := send.Calls(); calls[0].replyTo != 7777 {
		t.Fatalf("SendText replyTo should be 7777, got %d", calls[0].replyTo)
	}
}

func TestSendFailureRestoresDraft(t *testing.T) {
	t.Parallel()

	send := &fakeSender{err: errors.New("boom")}
	m := newInput(t, send)
	m, _ = m.Update(keyText("z"))
	got, cmd := m.Update(keyChord(tea.KeyEnter, 0))
	if cmd == nil {
		t.Fatalf("Send must always produce a Cmd to await result")
	}
	msg := cmd()
	failed, ok := msg.(SendFailedMsg)
	if !ok {
		t.Fatalf("expected SendFailedMsg, got %T", msg)
	}
	got, _ = got.Update(failed)
	if got.Value() != "z" {
		t.Fatalf("draft should be restored after failure, got %q", got.Value())
	}
}

func TestSendFailureRestoresReplyPointer(t *testing.T) {
	t.Parallel()

	send := &fakeSender{err: errors.New("boom")}
	m := newInput(t, send)
	reply := &domain.Message{ID: 4242, ChatID: 42, Text: "earlier message"}
	m, _ = m.Update(SetReplyMsg{Msg: reply})
	if m.ReplyTo() == nil {
		t.Fatalf("seed: reply should be armed")
	}
	m, _ = m.Update(keyText("z"))
	got, cmd := m.Update(keyChord(tea.KeyEnter, 0))
	if cmd == nil {
		t.Fatalf("Send must always produce a Cmd to await result")
	}
	failed, ok := cmd().(SendFailedMsg)
	if !ok {
		t.Fatalf("expected SendFailedMsg, got %T", cmd())
	}
	if failed.ReplyToMsg == nil || failed.ReplyToMsg.ID != 4242 {
		t.Fatalf("SendFailedMsg should carry original reply target, got %+v", failed.ReplyToMsg)
	}
	got, _ = got.Update(failed)
	if got.ReplyTo() == nil || got.ReplyTo().ID != 4242 {
		t.Fatalf("reply pointer should be rearmed after failure, got %+v", got.ReplyTo())
	}
}

func TestSendFailureKeepsFresherDraft(t *testing.T) {
	t.Parallel()

	// A fresher draft typed while the failed send was in flight must not be
	// clobbered when SendFailedMsg lands. Silently overwriting half-typed
	// text would be a small but trust-eroding data-loss bug.
	m := newInput(t, &fakeSender{})
	failed := SendFailedMsg{Text: "stale"}
	m, _ = m.Update(keyText("f"))
	m, _ = m.Update(keyText("r"))
	m, _ = m.Update(failed)
	if m.Value() != "fr" {
		t.Fatalf("fresher draft must survive SendFailedMsg, got %q", m.Value())
	}
}

func TestSendFailureKeepsRetargetedReply(t *testing.T) {
	t.Parallel()

	// If the user has armed a new reply pointer while the failed send was
	// in flight, the failure must not retarget it back to the original.
	m := newInput(t, &fakeSender{})
	original := &domain.Message{ID: 1, ChatID: 42}
	retargeted := &domain.Message{ID: 2, ChatID: 42}
	m, _ = m.Update(SetReplyMsg{Msg: retargeted})
	m, _ = m.Update(SendFailedMsg{Text: "", ReplyToMsg: original})
	if got := m.ReplyTo(); got == nil || got.ID != 2 {
		t.Fatalf("retargeted reply must survive SendFailedMsg, got %+v", got)
	}
}

func TestNewlineInsertsLiteralNewline(t *testing.T) {
	t.Parallel()

	send := &fakeSender{}
	m := newInput(t, send)
	m, _ = m.Update(keyText("a"))
	m, _ = m.Update(keyChord(tea.KeyEnter, tea.ModAlt))
	m, _ = m.Update(keyText("b"))
	if m.Value() != "a\nb" {
		t.Fatalf("Alt+Enter should insert literal newline, got %q", m.Value())
	}
	if len(send.Calls()) != 0 {
		t.Fatalf("Alt+Enter must not trigger Send")
	}
}

func TestReplyKeyEmitsRequestReplyMsg(t *testing.T) {
	t.Parallel()

	m := newInput(t, &fakeSender{})
	_, cmd := m.Update(keyChord('r', tea.ModCtrl))
	if cmd == nil {
		t.Fatalf("Ctrl+R should emit a Cmd")
	}
	if _, ok := cmd().(RequestReplyMsg); !ok {
		t.Fatalf("Ctrl+R should publish RequestReplyMsg, got %T", cmd())
	}
}

func TestOpenEditorEmitsOpenEditorMsg(t *testing.T) {
	t.Parallel()

	m := newInput(t, &fakeSender{})
	m, _ = m.Update(keyText("d"))
	m, _ = m.Update(keyText("o"))
	_, cmd := m.Update(keyChord('e', tea.ModCtrl))
	if cmd == nil {
		t.Fatalf("Ctrl+E should emit a Cmd")
	}
	msg, ok := cmd().(OpenEditorMsg)
	if !ok {
		t.Fatalf("Ctrl+E should publish OpenEditorMsg, got %T", cmd())
	}
	if msg.CurrentText != "do" {
		t.Fatalf("OpenEditorMsg should carry current text, got %q", msg.CurrentText)
	}
}

func TestEditorClosedMsgWritesBack(t *testing.T) {
	t.Parallel()

	m := newInput(t, &fakeSender{})
	m, _ = m.Update(EditorClosedMsg{Text: "from editor"})
	if m.Value() != "from editor" {
		t.Fatalf("editor result should populate composer, got %q", m.Value())
	}
}

func TestEditorClosedMsgErrorKeepsDraft(t *testing.T) {
	t.Parallel()

	m := newInput(t, &fakeSender{})
	m, _ = m.Update(keyText("k"))
	m, _ = m.Update(EditorClosedMsg{Err: errors.New("editor crashed")})
	if m.Value() != "k" {
		t.Fatalf("editor failure should preserve draft, got %q", m.Value())
	}
}

func TestHistoryPrevReplacesTextareaContent(t *testing.T) {
	t.Parallel()

	send := &fakeSender{localID: "L1"}
	m := newInput(t, send)
	m, _ = m.Update(keyText("a"))
	m, cmd := m.Update(keyChord(tea.KeyEnter, 0))
	_ = cmd() // drain
	m, _ = m.Update(keyText("b"))
	m, cmd = m.Update(keyChord(tea.KeyEnter, 0))
	_ = cmd()
	if m.History().Len() != 2 {
		t.Fatalf("history should have 2 entries, got %d", m.History().Len())
	}

	m, _ = m.Update(keyChord('p', tea.ModCtrl))
	if m.Value() != "b" {
		t.Fatalf("Ctrl+P #1 should restore newest entry, got %q", m.Value())
	}
	m, _ = m.Update(keyChord('p', tea.ModCtrl))
	if m.Value() != "a" {
		t.Fatalf("Ctrl+P #2 should walk older, got %q", m.Value())
	}
	m, _ = m.Update(keyChord('n', tea.ModCtrl))
	if m.Value() != "b" {
		t.Fatalf("Ctrl+N should walk newer, got %q", m.Value())
	}
	m, _ = m.Update(keyChord('n', tea.ModCtrl))
	if m.Value() != "" {
		t.Fatalf("Ctrl+N past newest should clear (draft slot), got %q", m.Value())
	}
}

func TestSetChatResetsReplyPointer(t *testing.T) {
	t.Parallel()

	m := newInput(t, &fakeSender{})
	m, _ = m.Update(SetReplyMsg{Msg: &domain.Message{ID: 1}})
	if m.ReplyTo() == nil {
		t.Fatalf("seed: reply should be armed")
	}
	m, _ = m.Update(SetChatMsg{ChatID: 99})
	if m.ReplyTo() != nil {
		t.Fatalf("SetChatMsg should clear reply pointer")
	}
	if m.ChatID() != 99 {
		t.Fatalf("ChatID should update to 99, got %d", m.ChatID())
	}
}

func TestSendWithoutChatIDIsNoop(t *testing.T) {
	t.Parallel()

	send := &fakeSender{}
	m := NewWithDeps(send, keymap.Default(), nil)
	m = m.SetWidth(40).SetFocus(true)
	m, _ = m.Update(keyText("x"))
	_, cmd := m.Update(keyChord(tea.KeyEnter, 0))
	if cmd != nil {
		t.Fatalf("Enter without chat id should not Send")
	}
	if len(send.Calls()) != 0 {
		t.Fatalf("SendText must not be called without chat id")
	}
}

func TestPrintableKeysAppendToTextarea(t *testing.T) {
	t.Parallel()

	m := newInput(t, &fakeSender{})
	for _, ch := range "hello" {
		m, _ = m.Update(keyText(string(ch)))
	}
	if m.Value() != "hello" {
		t.Fatalf("printable keys should accumulate, got %q", m.Value())
	}
}

func TestViewShowsHintWhenEmpty(t *testing.T) {
	t.Parallel()

	m := newInput(t, &fakeSender{})
	view := m.View()
	if !strings.Contains(view, "Enter to send") {
		t.Fatalf("empty composer view should show hint, got %q", view)
	}
}

func TestViewShowsReplyHintWhenArmed(t *testing.T) {
	t.Parallel()

	m := newInput(t, &fakeSender{})
	m, _ = m.Update(SetReplyMsg{Msg: &domain.Message{ID: 1, Text: "previous"}})
	view := m.View()
	if !strings.Contains(view, "Reply to") || !strings.Contains(view, "previous") {
		t.Fatalf("View should show reply hint with preview, got %q", view)
	}
}

// TestAsyncFailedRestoresDraftAfterDispatch locks the MTProto-failure
// path: the synchronous SendFailedMsg only fires when SendText itself
// returns an error before the optimistic record lands. Real-world
// failures (FloodWait, validation, network retries exhausted) reach
// the input pane via OutgoingMessageStateChanged{Failed} on the bus.
// Without the inFlight tracker the textarea would stay empty after
// such a failure and the user would have to retype the message — a
// promise the CHANGELOG explicitly makes ("Failed sends restore the
// draft and the reply pointer") that previously held only for the
// synchronous surface.
func TestAsyncFailedRestoresDraftAfterDispatch(t *testing.T) {
	t.Parallel()

	parent := &domain.Message{ID: 42, Text: "parent"}
	m := newInput(t, &fakeSender{})
	m, _ = m.Update(SetChatMsg{ChatID: 7})
	m, _ = m.Update(SetReplyMsg{Msg: parent})

	// Simulate the dispatch handshake the app would have performed
	// after the user pressed Enter.
	m, _ = m.Update(SendDispatchedMsg{
		LocalID:    "L1",
		ChatID:     7,
		Text:       "hello",
		ReplyTo:    int(parent.ID),
		ReplyToMsg: parent,
	})
	if m.Value() != "" {
		t.Fatalf("dispatch handshake must not repopulate textarea, got %q", m.Value())
	}

	// Async failure on the bus.
	m, _ = m.Update(events.OutgoingMessageStateChanged{
		LocalID: "L1",
		ChatID:  7,
		State:   events.OutgoingStateFailed,
		Error:   "FLOOD_WAIT_30",
	})

	if m.Value() != "hello" {
		t.Fatalf("async Failed must restore textarea body, got %q", m.Value())
	}
	if m.ReplyTo() == nil || m.ReplyTo().ID != parent.ID {
		t.Fatalf("async Failed must rearm reply pointer, got %+v", m.ReplyTo())
	}
}

// TestAsyncFailedDoesNotClobberFresherDraft is the symmetric guard:
// the user has already started typing a new message by the time the
// failure arrives. We must not silently overwrite their work — the
// failure body is dropped and only logged.
func TestAsyncFailedDoesNotClobberFresherDraft(t *testing.T) {
	t.Parallel()

	m := newInput(t, &fakeSender{})
	m, _ = m.Update(SetChatMsg{ChatID: 7})
	m, _ = m.Update(SendDispatchedMsg{LocalID: "L1", ChatID: 7, Text: "old"})

	// User starts typing a new draft.
	m, _ = m.Update(keyText("n"))
	m, _ = m.Update(keyText("e"))
	m, _ = m.Update(keyText("w"))

	m, _ = m.Update(events.OutgoingMessageStateChanged{
		LocalID: "L1",
		ChatID:  7,
		State:   events.OutgoingStateFailed,
		Error:   "boom",
	})

	if m.Value() != "new" {
		t.Fatalf("fresher draft must be preserved, got %q", m.Value())
	}
}

// TestAsyncSentClearsInFlight verifies that a successful send drops
// the localID from the tracker so a later spurious Failed for the
// same id (e.g. retry of an event already observed) cannot retroactively
// restore stale text on top of whatever the user has typed since.
func TestAsyncSentClearsInFlight(t *testing.T) {
	t.Parallel()

	m := newInput(t, &fakeSender{})
	m, _ = m.Update(SetChatMsg{ChatID: 7})
	m, _ = m.Update(SendDispatchedMsg{LocalID: "L1", ChatID: 7, Text: "old"})
	m, _ = m.Update(events.OutgoingMessageStateChanged{
		LocalID: "L1",
		ChatID:  7,
		State:   events.OutgoingStateSent,
	})
	// Now a stray Failed arrives.
	m, _ = m.Update(events.OutgoingMessageStateChanged{
		LocalID: "L1",
		ChatID:  7,
		State:   events.OutgoingStateFailed,
		Error:   "should be ignored",
	})
	if m.Value() != "" {
		t.Fatalf("Sent must clear inFlight so a stray Failed is a no-op, got %q", m.Value())
	}
}

// TestSetChatClearsInFlight protects against cross-chat state bleed:
// switching chats discards any pending-failure recovery from the
// previous conversation so a late Failed event cannot resurrect text
// into the wrong composer.
func TestSetChatClearsInFlight(t *testing.T) {
	t.Parallel()

	m := newInput(t, &fakeSender{})
	m, _ = m.Update(SetChatMsg{ChatID: 7})
	m, _ = m.Update(SendDispatchedMsg{LocalID: "L1", ChatID: 7, Text: "old"})

	m, _ = m.Update(SetChatMsg{ChatID: 8})

	m, _ = m.Update(events.OutgoingMessageStateChanged{
		LocalID: "L1",
		ChatID:  7,
		State:   events.OutgoingStateFailed,
		Error:   "boom",
	})
	if m.Value() != "" {
		t.Fatalf("chat switch must drop in-flight tracker so stale Failed is a no-op, got %q", m.Value())
	}
}

// TestReplyHintTruncatesAtRuneBoundaryForCyrillic locks the rune-aware
// truncation: a Cyrillic reply target longer than replyPreviewLimit
// runes must be cut on a codepoint boundary, never on a UTF-8 byte
// boundary. Byte slicing would emit invalid UTF-8 to the terminal and
// render as replacement characters — the primary audience for the
// project is Russian-speaking, so this is a critical correctness bug
// regression test.
func TestReplyHintTruncatesAtRuneBoundaryForCyrillic(t *testing.T) {
	t.Parallel()

	// 60 Cyrillic letters — well past the 50-rune cap and 120 bytes in
	// UTF-8, so a byte slice at offset 50 lands mid-codepoint.
	long := strings.Repeat("Й", 60)
	m := newInput(t, &fakeSender{})
	m = m.SetWidth(200)
	m, _ = m.Update(SetReplyMsg{Msg: &domain.Message{ID: 1, Text: long}})
	view := m.View()

	if !utf8.ValidString(view) {
		t.Fatalf("rendered View must be valid UTF-8 even after truncation, got %q", view)
	}
	// Expect the prefix plus 49 Й + ellipsis (truncRunes uses n-1).
	want := "↳ Reply to: " + strings.Repeat("Й", 49) + "…"
	if !strings.Contains(view, want) {
		t.Fatalf("View must contain rune-aware truncation %q, got %q", want, view)
	}
}

func TestSetFocusToggleSwapsPrompt(t *testing.T) {
	t.Parallel()

	m := NewWithDeps(nil, keymap.Default(), nil)
	m = m.SetWidth(40)
	m = m.SetFocus(true)
	if !strings.Contains(m.View(), focusedPrompt) {
		t.Fatalf("focused View should contain %q, got %q", focusedPrompt, m.View())
	}
	m = m.SetFocus(false)
	if strings.Contains(m.View(), focusedPrompt) {
		t.Fatalf("blurred View should not contain focused prompt, got %q", m.View())
	}
}

func TestSendDispatchedMsgArrivesQuickly(t *testing.T) {
	t.Parallel()

	send := &fakeSender{localID: "Lq"}
	m := newInput(t, send)
	m, _ = m.Update(keyText("q"))
	_, cmd := m.Update(keyChord(tea.KeyEnter, 0))

	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("send Cmd should resolve quickly with fake sender")
	}
}
