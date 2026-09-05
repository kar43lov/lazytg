package palette

import (
	"context"
	"errors"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// fakeFrecency records every Top / RecordVisit call and returns
// canned data. Mutex guards the shared state because the loader
// runs the call inside a tea.Cmd goroutine in production.
type fakeFrecency struct {
	mu         sync.Mutex
	topResult  []int64
	topErr     error
	topCalls   int
	visitCalls []int64
	visitErr   error
}

func (f *fakeFrecency) Top(_ context.Context, _ int) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.topCalls++
	return append([]int64(nil), f.topResult...), f.topErr
}

func (f *fakeFrecency) RecordVisit(_ context.Context, chatID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.visitCalls = append(f.visitCalls, chatID)
	return f.visitErr
}

func (f *fakeFrecency) TopCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.topCalls
}

// fakeChats returns canned chat list. Empty list is the
// first-time-user / no-sync scenario covered by one of the tests.
type fakeChats struct {
	chats []domain.Chat
	err   error
}

func (f *fakeChats) GetChats(_ context.Context) ([]domain.Chat, error) {
	return append([]domain.Chat(nil), f.chats...), f.err
}

// runCmd executes a tea.Cmd to completion. Used to drive the
// loader synchronously inside tests.
func runCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	return cmd()
}

// keyText fakes a printable-character key event (matches the
// helper used by panes/search/model_test.go).
func keyText(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Text: s, Code: []rune(s)[0]}
}

// keyChord fakes a non-printable key event.
func keyChord(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code}
}

func TestNew_DefaultsAreSane(t *testing.T) {
	t.Parallel()
	m := New(nil, nil, nil)
	if m.Visible {
		t.Fatalf("New should construct hidden palette")
	}
	if m.Value() != "" {
		t.Fatalf("Value(): want empty, got %q", m.Value())
	}
	if len(m.Items()) != 0 {
		t.Fatalf("Items(): want empty, got %d", len(m.Items()))
	}
}

func TestOpen_LoadsTopChats(t *testing.T) {
	t.Parallel()
	frecency := &fakeFrecency{topResult: []int64{2, 1}}
	chats := &fakeChats{chats: []domain.Chat{
		{ID: 1, Type: domain.ChatTypePrivate, Title: "Алёна"},
		{ID: 2, Type: domain.ChatTypePrivate, Title: "Боб"},
		{ID: 3, Type: domain.ChatTypePrivate, Title: "Зоя"},
	}}
	m := New(frecency, chats, nil)

	m, _ = m.Open()
	if !m.Visible {
		t.Fatalf("Open should flip Visible=true")
	}

	// Open returns tea.Batch(focusCmd, loadCmd); the focusCmd from
	// textinput is a non-LoadedMsg one and we can't unwrap the batch
	// here. Drive the loader directly — the loader is a method on
	// Model so we can call it without going through the batch.
	loaded := runCmd(t, m.loadCandidates())
	lmsg, ok := loaded.(LoadedMsg)
	if !ok {
		t.Fatalf("expected LoadedMsg, got %T", loaded)
	}
	if lmsg.Err != nil {
		t.Fatalf("LoadedMsg.Err: %v", lmsg.Err)
	}

	m, _ = m.Update(lmsg)
	items := m.Items()
	if len(items) != 3 {
		t.Fatalf("want 3 items, got %d (%+v)", len(items), items)
	}
	// frecency order first: 2, 1; then alphabetical of the rest:
	// "Зоя" (3).
	want := []int64{2, 1, 3}
	for i, w := range want {
		if items[i].ChatID != w {
			t.Errorf("item %d: got chat=%d, want %d", i, items[i].ChatID, w)
		}
	}
	if frecency.TopCalls() < 1 {
		t.Errorf("Top should have been called at least once, got %d", frecency.TopCalls())
	}
}

func TestUpdate_FilterPicksUpQuery(t *testing.T) {
	t.Parallel()
	chats := &fakeChats{chats: []domain.Chat{
		{ID: 1, Title: "Алёна"},
		{ID: 2, Title: "Алексей"},
		{ID: 3, Title: "Боб"},
	}}
	m := New(nil, chats, nil)
	m, _ = m.Open()
	loaded := runCmd(t, m.loadCandidates()).(LoadedMsg)
	m, _ = m.Update(loaded)

	// Type "ал" — both "Алёна" and "Алексей" survive (their
	// normalized titles start with "ал"); "Боб" is filtered out.
	m, _ = m.Update(keyText("а"))
	m, _ = m.Update(keyText("л"))
	if m.Value() != "ал" {
		t.Fatalf("Value(): got %q, want %q", m.Value(), "ал")
	}
	filtered := m.Filtered()
	if len(filtered) != 2 {
		t.Fatalf("want 2 matches for 'ал', got %d (%v)", len(filtered), filtered)
	}
	chatIDs := make(map[int64]bool, 2)
	for _, idx := range filtered {
		chatIDs[m.Items()[idx].ChatID] = true
	}
	if !chatIDs[1] || !chatIDs[2] {
		t.Errorf("expected chats 1 and 2 in filtered, got ids %v", chatIDs)
	}
}

// TestUpdate_UnicodeFuzzy is the headline invariant the palette
// exists to deliver: typing "Алена" must find a chat titled "Алёна".
func TestUpdate_UnicodeFuzzy(t *testing.T) {
	t.Parallel()
	chats := &fakeChats{chats: []domain.Chat{
		{ID: 1, Title: "Алёна"},
		{ID: 2, Title: "Боб"},
	}}
	m := New(nil, chats, nil)
	m, _ = m.Open()
	loaded := runCmd(t, m.loadCandidates()).(LoadedMsg)
	m, _ = m.Update(loaded)

	for _, r := range "Алена" {
		m, _ = m.Update(keyText(string(r)))
	}
	filtered := m.Filtered()
	if len(filtered) != 1 || m.Items()[filtered[0]].ChatID != 1 {
		t.Fatalf("expected only chat 1 (Алёна) to match query 'Алена'; got filtered=%v", filtered)
	}
}

func TestUpdate_DownEnterEmitsSelectedMsg(t *testing.T) {
	t.Parallel()
	chats := &fakeChats{chats: []domain.Chat{
		{ID: 11, Title: "first"},
		{ID: 22, Title: "second"},
	}}
	m := New(nil, chats, nil)
	m, _ = m.Open()
	loaded := runCmd(t, m.loadCandidates()).(LoadedMsg)
	m, _ = m.Update(loaded)

	m, _ = m.Update(keyChord(tea.KeyDown))
	if m.Cursor() != 1 {
		t.Fatalf("cursor after Down: want 1, got %d", m.Cursor())
	}

	updated, cmd := m.Update(keyChord(tea.KeyEnter))
	if updated.Visible {
		t.Fatalf("Enter must close the palette")
	}
	msg := runCmd(t, cmd)
	sel, ok := msg.(SelectedMsg)
	if !ok {
		t.Fatalf("expected SelectedMsg, got %T", msg)
	}
	// Items merge keeps frecency-empty mode alphabetical; "first"
	// < "second" so cursor=1 picks "second" → chat 22.
	if sel.ChatID != 22 {
		t.Fatalf("SelectedMsg.ChatID: got %d, want 22", sel.ChatID)
	}
}

func TestUpdate_EscEmitsClosedMsg(t *testing.T) {
	t.Parallel()
	m := New(nil, &fakeChats{}, nil)
	m, _ = m.Open()
	updated, cmd := m.Update(keyChord(tea.KeyEscape))
	if updated.Visible {
		t.Fatalf("Esc must hide palette")
	}
	if msg := runCmd(t, cmd); msg == nil {
		t.Fatalf("Esc must produce a ClosedMsg cmd")
	} else if _, ok := msg.(ClosedMsg); !ok {
		t.Fatalf("Esc cmd: got %T, want ClosedMsg", msg)
	}
}

func TestUpdate_LoadErrorIsSurfaced(t *testing.T) {
	t.Parallel()
	frecency := &fakeFrecency{topErr: errors.New("frecency boom")}
	m := New(frecency, &fakeChats{}, nil)
	m, _ = m.Open()
	loaded := runCmd(t, m.loadCandidates()).(LoadedMsg)
	m, _ = m.Update(loaded)
	if m.LoadErr() == nil {
		t.Fatal("expected LoadErr to be set when Top fails")
	}
}

func TestUpdate_HiddenPaletteDropsKeys(t *testing.T) {
	t.Parallel()
	m := New(nil, &fakeChats{chats: []domain.Chat{{ID: 1, Title: "x"}}}, nil)
	// Without Open, the model is hidden.
	updated, cmd := m.Update(keyText("a"))
	if updated.Visible {
		t.Fatalf("hidden palette must not become visible on key press")
	}
	if cmd != nil {
		t.Fatalf("hidden palette must not produce a Cmd")
	}
}

func TestMatch_EmptyQueryReturnsAllItems(t *testing.T) {
	t.Parallel()
	items := []Item{
		{ChatID: 1, Title: "Alpha", NormalizedTitle: "alpha"},
		{ChatID: 2, Title: "Beta", NormalizedTitle: "beta"},
	}
	got := Match("", items)
	if len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Fatalf("empty query: got %v, want [0 1]", got)
	}
}

func TestUsernameFrom(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"@durov": "durov", " @Durov ": "Durov", "t.me/durov": "durov", "https://t.me/durov/": "durov",
		"http://t.me/some_name": "some_name",
		"durov":                 "", "@du": "", "@1abc": "", "t.me/c/12345/6": "", "t.me/+invite": "", "@has-dash": "", "https://example.com/x": "",
	}
	for in, want := range cases {
		if got := UsernameFrom(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// Enter on a handle that matches nothing asks for the chat that is not
// here; on plain words that match nothing it does nothing, as before.
func TestEnter_OnAHandleWithNoMatchOpensIt(t *testing.T) {
	t.Parallel()

	chats := &fakeChats{chats: []domain.Chat{{ID: 11, Title: "first"}}}
	m := New(nil, chats, nil)
	m, _ = m.Open()
	m, _ = m.Update(runCmd(t, m.loadCandidates()).(LoadedMsg))
	for _, r := range "@durov" {
		m, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if len(m.Filtered()) != 0 {
		t.Fatalf("@durov matched %v", m.Filtered())
	}
	updated, cmd := m.Update(keyChord(tea.KeyEnter))
	if updated.Visible || cmd == nil {
		t.Fatal("Enter on a handle did not close the palette with a command")
	}
	if open, ok := runCmd(t, cmd).(OpenUsernameMsg); !ok || open.Username != "durov" {
		t.Fatalf("got %#v", runCmd(t, cmd))
	}

	m = New(nil, chats, nil)
	m, _ = m.Open()
	m, _ = m.Update(runCmd(t, m.loadCandidates()).(LoadedMsg))
	for _, r := range "zzz" {
		m, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if _, cmd := m.Update(keyChord(tea.KeyEnter)); cmd != nil {
		t.Fatal("Enter on plain words with no match produced a command")
	}
}
