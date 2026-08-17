package search

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/search"
)

// fakeService is a Service implementation that records
// every call and returns a canned slice of hits or an error. The mutex
// guards against the (theoretical) concurrent call from a tea.Cmd
// goroutine in production — in tests we run Cmds synchronously, but
// the assertion path is cleaner with a mutex than without.
type fakeService struct {
	mu    sync.Mutex
	calls []fakeServiceCall
	hits  []search.Hit
	err   error
}

type fakeServiceCall struct {
	query string
	limit int
}

func (f *fakeService) Search(_ context.Context, raw string, limit int) ([]search.Hit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeServiceCall{query: raw, limit: limit})
	return append([]search.Hit(nil), f.hits...), f.err
}

func (f *fakeService) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeService) lastQuery() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return ""
	}
	return f.calls[len(f.calls)-1].query
}

// runCmd executes a tea.Cmd to completion and returns the resulting
// tea.Msg. nil cmd → nil msg.
func runCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	return cmd()
}

// keyText fakes a printable-character key event (matches the same
// helper used by app/update_test.go).
func keyText(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Text: s, Code: []rune(s)[0]}
}

// keyChord fakes a non-printable / modifier-bearing key event.
func keyChord(code rune, mod tea.KeyMod) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Mod: mod}
}

func sampleHit(id int64, text string) search.Hit {
	return search.Hit{
		Message: domain.Message{ID: id, ChatID: 7, Text: text, Date: time.Unix(1700000000, 0).UTC()},
		Snippet: "<b>" + text + "</b>",
		ChatID:  7,
		Score:   -1,
	}
}

func TestNew_DefaultsAreSane(t *testing.T) {
	t.Parallel()

	svc := &fakeService{}
	m := New(svc, 0, nil)
	if m.Visible {
		t.Fatalf("New should construct hidden overlay")
	}
	if m.debounce != DefaultDebounce {
		t.Fatalf("debounce: want %s, got %s", DefaultDebounce, m.debounce)
	}
	if got := m.Value(); got != "" {
		t.Fatalf("Value(): want empty, got %q", got)
	}
	if got := len(m.Hits()); got != 0 {
		t.Fatalf("Hits(): want 0, got %d", got)
	}
}

func TestOpen_FocusesAndShowsOverlay(t *testing.T) {
	t.Parallel()

	m := New(&fakeService{}, 10*time.Millisecond, nil)
	m, _ = m.Open()
	if !m.Visible {
		t.Fatalf("Open should flip Visible=true")
	}
}

func TestUpdate_KeyTriggersDebouncedSearch(t *testing.T) {
	t.Parallel()

	svc := &fakeService{hits: []search.Hit{sampleHit(1, "hi")}}
	m := New(svc, time.Hour, nil) // long debounce — we inject the tick manually
	m, _ = m.Open()

	// Type "h" → textinput updates → debounce gen bumps to 1.
	updated, cmd := m.Update(keyText("h"))
	if cmd == nil {
		t.Fatalf("typing must arm a debounce Cmd")
	}
	if updated.Value() != "h" {
		t.Fatalf("Value() after key: got %q, want %q", updated.Value(), "h")
	}
	if updated.QueryGeneration() != 1 {
		t.Fatalf("QueryGeneration after first key: want 1, got %d", updated.QueryGeneration())
	}

	// Type "i" — generation bumps to 2 so a debounceTickMsg with gen=1
	// is now stale.
	updated, cmd2 := updated.Update(keyText("i"))
	if cmd2 == nil {
		t.Fatalf("second key must arm a fresh debounce Cmd")
	}
	if updated.QueryGeneration() != 2 {
		t.Fatalf("QueryGeneration after second key: want 2, got %d", updated.QueryGeneration())
	}
	if updated.Value() != "hi" {
		t.Fatalf("Value() after two keys: got %q, want %q", updated.Value(), "hi")
	}

	// Stale tick — the service should NOT be called.
	updated, staleCmd := updated.Update(debounceTickMsg{generation: 1})
	if staleCmd != nil {
		t.Fatalf("stale tick must not produce a Cmd, got %T", staleCmd())
	}
	if svc.callCount() != 0 {
		t.Fatalf("stale tick must not call service; got %d calls", svc.callCount())
	}

	// Fresh tick — service gets called with "hi".
	updated, searchCmd := updated.Update(debounceTickMsg{generation: 2})
	if searchCmd == nil {
		t.Fatalf("fresh tick must produce a service.Search Cmd")
	}
	if !updated.Loading() {
		t.Fatalf("Loading should be true while service.Search is in flight")
	}

	results := runCmd(t, searchCmd)
	rmsg, ok := results.(ResultsMsg)
	if !ok {
		t.Fatalf("expected ResultsMsg, got %T", results)
	}
	if svc.lastQuery() != "hi" {
		t.Fatalf("service called with %q, want %q", svc.lastQuery(), "hi")
	}
	if len(rmsg.Hits) != 1 {
		t.Fatalf("hits: want 1, got %d", len(rmsg.Hits))
	}

	updated, _ = updated.Update(rmsg)
	if updated.Loading() {
		t.Fatalf("Loading should clear after results land")
	}
	if got := len(updated.Hits()); got != 1 {
		t.Fatalf("model hits: want 1, got %d", got)
	}
}

func TestUpdate_EnterEmitsSearchJump(t *testing.T) {
	t.Parallel()

	svc := &fakeService{hits: []search.Hit{
		sampleHit(11, "first"),
		sampleHit(22, "second"),
	}}
	m := New(svc, time.Millisecond, nil)
	m, _ = m.Open()
	m, _ = m.Update(ResultsMsg{Hits: svc.hits})
	if got := len(m.Hits()); got != 2 {
		t.Fatalf("precondition: want 2 hits, got %d", got)
	}

	// Move cursor down to the second hit.
	m, _ = m.Update(keyChord(tea.KeyDown, 0))
	if m.Cursor() != 1 {
		t.Fatalf("cursor after Down: want 1, got %d", m.Cursor())
	}

	// Enter should emit JumpMsg with the second hit and close
	// the overlay.
	updated, cmd := m.Update(keyChord(tea.KeyEnter, 0))
	if cmd == nil {
		t.Fatalf("Enter must produce a JumpMsg Cmd")
	}
	if updated.Visible {
		t.Fatalf("Enter must close the overlay")
	}
	jumpMsg, ok := runCmd(t, cmd).(JumpMsg)
	if !ok {
		t.Fatalf("expected JumpMsg, got %T", runCmd(t, cmd))
	}
	if jumpMsg.Hit.Message.ID != 22 {
		t.Fatalf("jump target: want id 22, got %d", jumpMsg.Hit.Message.ID)
	}
}

func TestUpdate_EscClosesOverlay(t *testing.T) {
	t.Parallel()

	m := New(&fakeService{}, time.Millisecond, nil)
	m, _ = m.Open()
	updated, cmd := m.Update(keyChord(tea.KeyEscape, 0))
	if updated.Visible {
		t.Fatalf("Esc must close the overlay")
	}
	if cmd == nil {
		t.Fatalf("Esc must produce ClosedMsg Cmd")
	}
	if _, ok := runCmd(t, cmd).(ClosedMsg); !ok {
		t.Fatalf("expected ClosedMsg, got %T", runCmd(t, cmd))
	}
}

func TestUpdate_CursorClampsAtBoundaries(t *testing.T) {
	t.Parallel()

	svc := &fakeService{}
	m := New(svc, time.Millisecond, nil)
	m, _ = m.Open()
	m, _ = m.Update(ResultsMsg{Hits: []search.Hit{
		sampleHit(1, "a"),
		sampleHit(2, "b"),
	}})
	// Up at top → cursor stays 0.
	m, _ = m.Update(keyChord(tea.KeyUp, 0))
	if m.Cursor() != 0 {
		t.Fatalf("cursor at top: want 0, got %d", m.Cursor())
	}
	// Down twice → bottom (cursor=1, since 2 hits).
	m, _ = m.Update(keyChord(tea.KeyDown, 0))
	m, _ = m.Update(keyChord(tea.KeyDown, 0))
	if m.Cursor() != 1 {
		t.Fatalf("cursor at bottom: want 1, got %d", m.Cursor())
	}
}

func TestUpdate_EmptyQueryClearsResults(t *testing.T) {
	t.Parallel()

	svc := &fakeService{}
	m := New(svc, time.Millisecond, nil)
	m, _ = m.Open()
	m, _ = m.Update(ResultsMsg{Hits: []search.Hit{sampleHit(1, "x")}})
	if got := len(m.Hits()); got != 1 {
		t.Fatalf("precondition: want 1 hit, got %d", got)
	}

	// Manually fire a debounce tick when the value is empty — the
	// model should clear hits without calling the service.
	m.queryGeneration = 1
	updated, cmd := m.Update(debounceTickMsg{generation: 1})
	if cmd != nil {
		t.Fatalf("empty query must not produce a service.Search Cmd, got %T", cmd())
	}
	if got := len(updated.Hits()); got != 0 {
		t.Fatalf("hits after empty-query tick: want 0, got %d", got)
	}
}

func TestUpdate_ServiceErrorSurfacedToView(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("boom")
	svc := &fakeService{err: wantErr}
	m := New(svc, time.Millisecond, nil)
	m, _ = m.Open()
	m, _ = m.Update(ResultsMsg{Err: wantErr})
	if !errors.Is(m.Err(), wantErr) {
		t.Fatalf("Err(): want %v, got %v", wantErr, m.Err())
	}
	if m.Loading() {
		t.Fatalf("Loading should clear after error result")
	}
}

func TestView_HiddenReturnsEmpty(t *testing.T) {
	t.Parallel()

	m := New(&fakeService{}, time.Millisecond, nil)
	if got := m.View(80, 24); got != "" {
		t.Fatalf("hidden View should return empty, got %q", got)
	}
}

func TestView_VisibleRendersInputAndHint(t *testing.T) {
	t.Parallel()

	m := New(&fakeService{}, time.Millisecond, nil)
	m, _ = m.Open()
	if got := m.View(120, 40); got == "" {
		t.Fatalf("visible View should render content")
	}
}
