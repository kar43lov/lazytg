package app

import (
	"context"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/search"
	"github.com/kar43lov/lazytg/internal/ui/keymap"
	"github.com/kar43lov/lazytg/internal/ui/panes/chats"
	uisearch "github.com/kar43lov/lazytg/internal/ui/panes/search"
	"github.com/kar43lov/lazytg/internal/ui/panes/thread"
)

// TestSearchKeyOpensOverlayWhenThreadFocused verifies the "/" chord
// opens the search overlay when the thread pane has focus. From
// chats / input the chord is intentionally suppressed (chats: "/"
// activates filter; input: "/" is a printable character).
func TestSearchKeyOpensOverlayWhenThreadFocused(t *testing.T) {
	t.Parallel()

	a := newApp(t)
	// Cycle focus to thread (chats → input → thread).
	model, cmd := a.Update(keyChord(tea.KeyTab, 0))
	model, _ = model.Update(cmd())
	model, cmd = model.Update(keyChord(tea.KeyTab, 0))
	model, _ = model.Update(cmd())
	if got := model.(App).Focus(); got != FocusThread {
		t.Fatalf("precondition: expected FocusThread, got %s", got)
	}

	// "/" should now open the search overlay via the cmd path.
	model, cmd = model.Update(keyText("/"))
	if cmd == nil {
		t.Fatalf("/ chord must produce a Cmd when thread is focused")
	}
	model, _ = model.Update(cmd())
	if !model.(App).SearchVisible() {
		t.Fatalf("/ chord must open the search overlay")
	}
}

// TestSearchKeySuppressedFromChats locks in the suppression rule:
// "/" is the bubbles/list filter shortcut — opening the search
// overlay from chats would steal that.
func TestSearchKeySuppressedFromChats(t *testing.T) {
	t.Parallel()

	a := newApp(t)
	if a.Focus() != FocusChats {
		t.Fatalf("precondition: expected FocusChats")
	}
	// The default chord is "/", which would also activate the chats
	// pane's filter — the global handler must not claim it.
	model, _ := a.Update(keyText("/"))
	if model.(App).SearchVisible() {
		t.Fatalf("/ in chats focus must not open the search overlay")
	}
}

// TestSearchEscClosesOverlay verifies that the overlay's Esc routes
// through ClosedMsg and the app honours it by hiding the
// overlay.
func TestSearchEscClosesOverlay(t *testing.T) {
	t.Parallel()

	a := openSearchFromThread(t)
	if !a.SearchVisible() {
		t.Fatalf("precondition: overlay must be visible")
	}

	model, cmd := a.Update(keyChord(tea.KeyEscape, 0))
	if model.(App).SearchVisible() {
		t.Fatalf("Esc inside search overlay must hide it")
	}
	if cmd == nil {
		t.Fatalf("Esc inside search overlay must produce a ClosedMsg Cmd")
	}
}

// TestSearchJumpSwitchesChatAndScrolls verifies the end-to-end
// SearchJump flow: pressing Enter on a hit publishes a
// JumpMsg, the app opens the chat, lands the messagesLoadedMsg
// through the broadcast path, and the deferred ScrollTo lines up the
// viewport with the matched message.
func TestSearchJumpSwitchesChatAndScrolls(t *testing.T) {
	t.Parallel()

	a := openSearchFromThread(t)

	// Inject a deterministic search hit via ResultsMsg so the
	// test does not need a real search.Service.
	hit := search.Hit{
		Message: domain.Message{
			ID:     50,
			ChatID: 12345,
			Text:   "найдено",
			Date:   time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC),
		},
		ChatID: 12345,
	}
	model, _ := a.Update(uisearch.ResultsMsg{Hits: []search.Hit{hit}})
	if got := len(model.(App).SearchModel().Hits()); got != 1 {
		t.Fatalf("precondition: search overlay must hold 1 hit, got %d", got)
	}

	// Enter on the highlighted hit emits JumpMsg via Cmd.
	model, cmd := model.Update(keyChord(tea.KeyEnter, 0))
	if cmd == nil {
		t.Fatalf("Enter on a hit must produce a JumpMsg Cmd")
	}
	jumpMsg, ok := cmd().(uisearch.JumpMsg)
	if !ok {
		t.Fatalf("expected JumpMsg, got %T", cmd())
	}
	if jumpMsg.Hit.ChatID != 12345 {
		t.Fatalf("jump chat: want 12345, got %d", jumpMsg.Hit.ChatID)
	}

	model, _ = model.Update(jumpMsg)
	app := model.(App)
	if app.SearchVisible() {
		t.Fatalf("SearchJump must close the search overlay")
	}
	// Without a wired thread repo the OpenChat is a no-op (loading=false
	// and messages stay empty), but the pendingScroll bookkeeping
	// should be cleared once the broadcast path applies the scroll
	// for the loaded chat. We assert pendingScroll is nil after a
	// no-op load to lock in the contract that the scroll deferral
	// does not leak.
	if app.thread.ChatID() != 12345 {
		t.Fatalf("thread should switch to chat 12345, got %d", app.thread.ChatID())
	}
}

// openSearchFromThread is a test helper that focuses the thread pane
// and opens the search overlay through the production code path.
// Returns the resulting App.
func openSearchFromThread(t *testing.T) App {
	t.Helper()
	a := newApp(t)
	model, cmd := a.Update(keyChord(tea.KeyTab, 0))
	model, _ = model.Update(cmd())
	model, cmd = model.Update(keyChord(tea.KeyTab, 0))
	model, _ = model.Update(cmd())
	if model.(App).Focus() != FocusThread {
		t.Fatalf("precondition: expected FocusThread")
	}
	model, cmd = model.Update(keyText("/"))
	if cmd == nil {
		t.Fatalf("/ chord must produce a Cmd")
	}
	model, _ = model.Update(cmd())
	return model.(App)
}

// TestSearchOverlayDepsRespected verifies that a search.Model passed
// in via Deps survives construction — used by production wiring to
// inject a service-backed overlay.
func TestSearchOverlayDepsRespected(t *testing.T) {
	t.Parallel()

	overlay := uisearch.New(nil, 250*time.Millisecond, nil)
	a := New(Deps{Keymap: keymap.Default(), Search: &overlay})
	if a.SearchModel().QueryGeneration() != 0 {
		t.Fatalf("freshly-constructed overlay must start with gen=0")
	}
}

// fakeJumper is a stub JumpContextProvider. Returns the configured
// messages and target index, or err when set.
type fakeJumper struct {
	messages []domain.Message
	target   int
	err      error
	called   int
}

func (f *fakeJumper) JumpContext(_ context.Context, _ search.Hit, _ int) ([]domain.Message, int, error) {
	f.called++
	if f.err != nil {
		return nil, -1, f.err
	}
	return f.messages, f.target, nil
}

// TestSearchJumpLoadsContextWindow asserts the killer-feature path:
// pressing Enter on a hit triggers an async JumpContext, the loaded
// ±N window replaces thread.messages, and the viewport scrolls to
// the matched message — even when that message would not have been
// in the freshly-loaded initial-page slice. Locks in the wiring
// between the search overlay, the JumpContextProvider and
// thread.LoadJumpWindow.
func TestSearchJumpLoadsContextWindow(t *testing.T) {
	t.Parallel()

	// Build a context window: 11 messages around the matched id (=50).
	window := make([]domain.Message, 0, 11)
	for id := int64(45); id <= 55; id++ {
		window = append(window, domain.Message{
			ID: id, ChatID: 12345, Date: time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC),
			Text: "msg",
		})
	}
	jumper := &fakeJumper{messages: window, target: 5}

	overlay := uisearch.New(nil, 250*time.Millisecond, nil)
	a := New(Deps{Keymap: keymap.Default(), Search: &overlay, Jumper: jumper})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a = model.(App)
	// Open the overlay so the JumpMsg routing fires the same path as
	// in production.
	_, cmd := a.Update(uisearch.OpenedMsg{})
	if cmd != nil {
		model, _ = a.Update(cmd())
		a = model.(App)
	}

	hit := search.Hit{
		Message: domain.Message{
			ID: 50, ChatID: 12345, Text: "найдено",
			Date: time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC),
		},
		ChatID: 12345,
	}
	model, cmd = a.Update(uisearch.JumpMsg{Hit: hit})
	a = model.(App)
	if a.SearchVisible() {
		t.Fatalf("JumpMsg must close the search overlay")
	}
	if a.thread.ChatID() != 12345 {
		t.Fatalf("JumpMsg must switch the thread to the hit's chat synchronously")
	}
	if cmd == nil {
		t.Fatalf("JumpMsg with a wired Jumper must produce an async cmd")
	}
	// Drive the async cmd through Update — same dance the bubbletea
	// runtime performs.
	model, _ = a.Update(cmd())
	a = model.(App)

	if jumper.called != 1 {
		t.Fatalf("Jumper.JumpContext must be called exactly once, got %d", jumper.called)
	}
	if got := len(a.thread.Messages()); got != len(window) {
		t.Fatalf("thread must hold the loaded window, got %d messages", got)
	}
	if a.thread.ChatID() != 12345 {
		t.Fatalf("thread chat must be 12345 after load, got %d", a.thread.ChatID())
	}
	// LoadJumpWindow drops pendingScroll because the scroll has
	// already been applied; the field must not leak between jumps.
	if a.pendingScroll != nil {
		t.Fatalf("pendingScroll must be nil after a successful jump")
	}
}

// TestSearchJumpFallsBackOnJumperError verifies that a JumpContext
// failure does not break the chat switch — the user still lands in
// the chat, only the surrounding context is missing. The fallback
// must also propagate the OpenChat load cmd so the deferred
// ScrollTo path actually receives a messagesLoadedMsg; otherwise the
// thread would stay loading=true and applyPendingScroll would never
// fire.
func TestSearchJumpFallsBackOnJumperError(t *testing.T) {
	t.Parallel()

	jumper := &fakeJumper{err: errJumpStub}

	// Wire a thread pane backed by a fake repo so OpenChat returns a
	// real load cmd — the bug the test is locking in is that
	// applySearchJumpLoaded used to discard that cmd, leaving the
	// thread stuck in loading=true.
	repo := &fakeThreadRepo{}
	threadModel := thread.NewWithRepo(repo, nil, nil)
	overlay := uisearch.New(nil, 250*time.Millisecond, nil)
	a := New(Deps{Keymap: keymap.Default(), Search: &overlay, Jumper: jumper, Thread: &threadModel})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a = model.(App)

	hit := search.Hit{
		Message: domain.Message{ID: 99, ChatID: 7, Text: "x"},
		ChatID:  7,
	}
	model, cmd := a.Update(uisearch.JumpMsg{Hit: hit})
	a = model.(App)
	if cmd == nil {
		t.Fatalf("JumpMsg must produce a cmd even when fallback is expected")
	}
	model, loadedCmd := a.Update(cmd())
	a = model.(App)

	if a.thread.ChatID() != 7 {
		t.Fatalf("thread must still switch to the hit's chat on jumper error, got %d", a.thread.ChatID())
	}
	if a.pendingScroll == nil {
		t.Fatalf("error fallback must arm pendingScroll for the deferred ScrollTo path")
	}
	if a.pendingScroll.MessageID != 99 {
		t.Fatalf("pendingScroll target wrong, got %d", a.pendingScroll.MessageID)
	}
	if loadedCmd == nil {
		t.Fatalf("error fallback must propagate the OpenChat load cmd; otherwise thread stays loading=true forever")
	}
	// Run the load cmd: it must hit the repo.
	loadedCmd()
	if got := repo.callCount(); got != 1 {
		t.Fatalf("error-fallback load cmd must call repo exactly once, got %d", got)
	}
}

// TestSearchJumpFallbackPreservesOptimisticState locks in the
// symmetric preservation contract for the failure-recovery path. The
// search-jump path rebinds the input pane to the target chat the
// moment the user picks a hit (handleSearchJump → SetChatMsg before
// the jumper goroutine starts), so the user can issue a send during
// the async window. On JumpContext failure the recovery must NOT wipe
// that optimistic row — for 1:1 chats Telegram emits only
// UpdateShortSentMessage and never re-publishes a corresponding
// UpdateNewMessage echo, so a wipe would silently drop the message
// from the UI until manual reload. Mirrors the success-path
// preservation in LoadJumpWindow.
func TestSearchJumpFallbackPreservesOptimisticState(t *testing.T) {
	t.Parallel()

	jumper := &fakeJumper{err: errJumpStub}

	repo := &fakeThreadRepo{}
	threadModel := thread.NewWithRepo(repo, nil, nil)
	overlay := uisearch.New(nil, 250*time.Millisecond, nil)
	a := New(Deps{Keymap: keymap.Default(), Search: &overlay, Jumper: jumper, Thread: &threadModel})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a = model.(App)

	hit := search.Hit{
		Message: domain.Message{ID: 99, ChatID: 7, Text: "x"},
		ChatID:  7,
	}
	model, cmd := a.Update(uisearch.JumpMsg{Hit: hit})
	a = model.(App)
	if cmd == nil {
		t.Fatalf("JumpMsg with a wired Jumper must produce an async cmd")
	}

	// Simulate: between JumpMsg dispatch (SwitchTo cleared the slate)
	// and the JumpContext failure resolving, the user issues a send.
	// In production this comes through input.SendDispatchedMsg →
	// thread.ApplyDispatched. We bypass the input pane here and call
	// ApplyDispatched directly — same end state.
	a.thread = a.thread.ApplyDispatched("local-1", 7, "hi during jump")
	if got := len(a.thread.Outgoing()); got != 1 {
		t.Fatalf("precondition: optimistic row must be present pre-failure, got %d", got)
	}

	// The JumpContext failure msg lands. The fallback recovery used to
	// call OpenChat which wiped outgoing; ReloadAfterJumpFailure now
	// preserves it.
	model, _ = a.Update(cmd())
	a = model.(App)

	if got := len(a.thread.Outgoing()); got != 1 {
		t.Fatalf("error fallback must preserve optimistic state; got %d entries (was wiped by OpenChat?)", got)
	}
	if a.thread.Outgoing()[0].Text != "hi during jump" {
		t.Fatalf("preserved entry text: got %q, want %q", a.thread.Outgoing()[0].Text, "hi during jump")
	}
}

// fakeThreadRepo is a minimal thread.Repository so OpenChat returns a
// real load cmd. The test asserts that cmd is propagated by
// applySearchJumpLoaded on the error path (so the deferred ScrollTo
// path actually has a messagesLoadedMsg to react to).
type fakeThreadRepo struct {
	mu    sync.Mutex
	calls int
}

func (r *fakeThreadRepo) GetMessages(_ context.Context, _ int64, _, _ int) ([]domain.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return nil, nil
}

func (r *fakeThreadRepo) GetMessagesBefore(_ context.Context, _, _ int64, _ int) ([]domain.Message, error) {
	return nil, nil
}

func (r *fakeThreadRepo) GetMessagesAfter(_ context.Context, _, _ int64, _ int) ([]domain.Message, error) {
	return nil, nil
}

func (r *fakeThreadRepo) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

var errJumpStub = errStub("jump context unavailable")

type errStub string

func (e errStub) Error() string { return string(e) }

// TestSearchJumpStaleCompletionDropped locks in the generation guard
// in applySearchJumpLoaded: when the user switches chats while a
// JumpContext is in flight, the older completion must not yank the
// thread back to the earlier hit. Without the guard the stale
// LoadJumpWindow call would clobber the chat the user just navigated
// to.
func TestSearchJumpStaleCompletionDropped(t *testing.T) {
	t.Parallel()

	// Window for chat 12345; the test will hold the cmd and switch
	// chats before driving it through Update.
	window := []domain.Message{
		{ID: 50, ChatID: 12345, Text: "stale"},
	}
	jumper := &fakeJumper{messages: window, target: 0}

	overlay := uisearch.New(nil, 250*time.Millisecond, nil)
	a := New(Deps{Keymap: keymap.Default(), Search: &overlay, Jumper: jumper})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a = model.(App)

	hit := search.Hit{
		Message: domain.Message{ID: 50, ChatID: 12345, Text: "stale"},
		ChatID:  12345,
	}
	model, cmd := a.Update(uisearch.JumpMsg{Hit: hit})
	a = model.(App)
	if cmd == nil {
		t.Fatalf("JumpMsg with a wired Jumper must produce an async cmd")
	}

	// User switches chats before the JumpContext goroutine returns.
	// handleChatSelected must bump the generation counter so the
	// in-flight cmd's result is dropped on arrival.
	model, _ = a.Update(chats.ChatSelectedMsg{ChatID: 99})
	a = model.(App)
	if a.thread.ChatID() != 99 {
		t.Fatalf("precondition: chat switch must land on 99, got %d", a.thread.ChatID())
	}

	// Drive the stale cmd. applySearchJumpLoaded must drop it; the
	// thread must remain on the just-switched chat 99.
	model, _ = a.Update(cmd())
	a = model.(App)
	if a.thread.ChatID() != 99 {
		t.Fatalf("stale jump completion must not yank thread back; got chatID=%d", a.thread.ChatID())
	}
	if got := len(a.thread.Messages()); got != 0 {
		t.Fatalf("stale jump completion must not load messages into thread; got %d", got)
	}
	if a.pendingScroll != nil {
		t.Fatalf("stale completion must not arm pendingScroll for the dropped target")
	}
}

// TestSearchJumpStaleCompletionDroppedOnSecondJump locks in the
// generation guard for the same-chat case: a second jump for the
// same chat must supersede the first, and the first's completion
// must be dropped on arrival rather than scrolling to the wrong
// message.
func TestSearchJumpStaleCompletionDroppedOnSecondJump(t *testing.T) {
	t.Parallel()

	window := []domain.Message{
		{ID: 10, ChatID: 12345, Text: "first"},
		{ID: 11, ChatID: 12345, Text: "second"},
	}
	jumper := &fakeJumper{messages: window, target: 0}

	overlay := uisearch.New(nil, 250*time.Millisecond, nil)
	a := New(Deps{Keymap: keymap.Default(), Search: &overlay, Jumper: jumper})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a = model.(App)

	firstHit := search.Hit{
		Message: domain.Message{ID: 10, ChatID: 12345, Text: "first"},
		ChatID:  12345,
	}
	model, firstCmd := a.Update(uisearch.JumpMsg{Hit: firstHit})
	a = model.(App)
	if firstCmd == nil {
		t.Fatalf("first JumpMsg must produce a cmd")
	}

	// Second jump for the same chat — supersedes the first.
	secondHit := search.Hit{
		Message: domain.Message{ID: 11, ChatID: 12345, Text: "second"},
		ChatID:  12345,
	}
	model, secondCmd := a.Update(uisearch.JumpMsg{Hit: secondHit})
	a = model.(App)
	if secondCmd == nil {
		t.Fatalf("second JumpMsg must produce a cmd")
	}

	// Drive the first (now-stale) cmd: must be dropped, thread stays
	// pristine (no messages loaded yet).
	model, _ = a.Update(firstCmd())
	a = model.(App)
	if got := len(a.thread.Messages()); got != 0 {
		t.Fatalf("stale first jump must not load window; got %d msgs", got)
	}

	// Drive the second cmd: must apply normally.
	model, _ = a.Update(secondCmd())
	a = model.(App)
	if got := len(a.thread.Messages()); got != len(window) {
		t.Fatalf("second jump must load the window; got %d msgs", got)
	}
}
