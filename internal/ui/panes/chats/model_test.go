package chats

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/pgmac/lazytg/internal/core/domain"
	"github.com/pgmac/lazytg/internal/core/events"
)

// fakeRepo is an in-memory Repository used to drive the chats pane in tests.
// It returns whatever GetChats finds in chats; if err is non-nil, that error
// is propagated instead. callCount lets tests assert that DialogUpdated
// debouncing actually limits how often the repo is read.
type fakeRepo struct {
	chats     []domain.Chat
	err       error
	callCount int
}

func (r *fakeRepo) GetChats(_ context.Context) ([]domain.Chat, error) {
	r.callCount++
	if r.err != nil {
		return nil, r.err
	}
	out := make([]domain.Chat, len(r.chats))
	copy(out, r.chats)
	return out, nil
}

// runCmd executes a tea.Cmd to completion and returns the resulting Msg.
// Returns nil when cmd is nil. Mirrors what the bubbletea runtime does
// for synchronous commands so tests can assert on emitted messages.
func runCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	return cmd()
}

// keyChord builds a tea.KeyPressMsg with the given Code/Mod. Matches the
// helper used in the app package tests so call sites stay familiar.
func keyChord(code rune, mod tea.KeyMod) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Mod: mod}
}

func sized(m Model) Model { return m.SetSize(40, 20) }

// makeChat returns a fully-populated domain.Chat with sensible defaults.
// Tests override only what they care about.
func makeChat(id int64, title string, pinned bool, dateOffset time.Duration, unread int) domain.Chat {
	return domain.Chat{
		ID:              id,
		Type:            domain.ChatTypePrivate,
		Title:           title,
		LastMessageDate: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC).Add(dateOffset),
		UnreadCount:     unread,
		Pinned:          pinned,
	}
}

func TestNewWithoutRepo_InitNoOp(t *testing.T) {
	t.Parallel()

	m := sized(New())
	if cmd := m.Init(); cmd != nil {
		t.Fatalf("Init on no-repo model should return nil cmd, got %T", cmd())
	}
}

func TestEmptyListRenderHeaderOnly(t *testing.T) {
	t.Parallel()

	m := sized(New())
	out := m.View()
	if !strings.Contains(out, "Chats") {
		t.Fatalf("empty view should still render header; got %q", out)
	}
	// No items → no list rows; the bubbles list paints empty space, but no
	// chat titles should appear because we never loaded any.
	if strings.Contains(out, "[📌]") {
		t.Fatalf("empty view should not contain pin glyph; got %q", out)
	}
}

func TestFiveChatsAppearInView(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{chats: []domain.Chat{
		makeChat(1, "Alpha", false, -4*time.Hour, 0),
		makeChat(2, "Bravo", false, -3*time.Hour, 2),
		makeChat(3, "Charlie", false, -2*time.Hour, 0),
		makeChat(4, "Delta", false, -1*time.Hour, 0),
		makeChat(5, "Echo", false, 0, 0),
	}}
	m := sized(NewWithRepo(repo, nil))
	msg := runCmd(t, m.Init())
	loaded, ok := msg.(chatsLoadedMsg)
	if !ok {
		t.Fatalf("expected chatsLoadedMsg, got %T (%v)", msg, msg)
	}

	updated, _ := m.Update(loaded)
	out := updated.View()
	for _, name := range []string{"Alpha", "Bravo", "Charlie", "Delta", "Echo"} {
		if !strings.Contains(out, name) {
			t.Fatalf("view should contain chat %q, got %q", name, out)
		}
	}
}

func TestKeyDownAdvancesSelection(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{chats: []domain.Chat{
		makeChat(10, "First", false, 0, 0),
		makeChat(20, "Second", false, -1*time.Hour, 0),
		makeChat(30, "Third", false, -2*time.Hour, 0),
	}}
	m := sized(NewWithRepo(repo, nil))
	loaded := runCmd(t, m.Init()).(chatsLoadedMsg)
	updated, _ := m.Update(loaded)
	got := updated

	first, ok := got.SelectedItem()
	if !ok || first.ID() != 10 {
		t.Fatalf("expected initial selection id=10, got id=%d ok=%v", first.ID(), ok)
	}

	got, _ = got.Update(keyChord(tea.KeyDown, 0))
	second, _ := got.SelectedItem()
	if second.ID() != 20 {
		t.Fatalf("expected selection id=20 after Down, got id=%d", second.ID())
	}
}

func TestEnterEmitsChatSelectedMsg(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{chats: []domain.Chat{
		makeChat(42, "Picked", false, 0, 0),
	}}
	m := sized(NewWithRepo(repo, nil))
	loaded := runCmd(t, m.Init()).(chatsLoadedMsg)
	got, _ := m.Update(loaded)

	got, cmd := got.Update(keyChord(tea.KeyEnter, 0))
	if cmd == nil {
		t.Fatalf("Enter should produce a Cmd")
	}
	msg := cmd()
	sel, ok := msg.(ChatSelectedMsg)
	if !ok {
		t.Fatalf("expected ChatSelectedMsg, got %T", msg)
	}
	if sel.ChatID != 42 {
		t.Fatalf("expected ChatID=42, got %d", sel.ChatID)
	}
	_ = got
}

func TestEnterOnEmptyListIsNoOp(t *testing.T) {
	t.Parallel()

	m := sized(NewWithRepo(&fakeRepo{}, nil))
	loaded := runCmd(t, m.Init()).(chatsLoadedMsg)
	got, _ := m.Update(loaded)

	_, cmd := got.Update(keyChord(tea.KeyEnter, 0))
	if cmd != nil {
		t.Fatalf("Enter on empty list must not produce a Cmd, got %T", cmd())
	}
}

func TestSortingPinnedFirstThenByDate(t *testing.T) {
	t.Parallel()

	// Repo returns out-of-order to prove the model re-sorts defensively.
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	repo := &fakeRepo{chats: []domain.Chat{
		{ID: 1, Title: "OldUnpinned", LastMessageDate: now.Add(-5 * time.Hour)},
		{ID: 2, Title: "RecentPinned", Pinned: true, LastMessageDate: now.Add(-1 * time.Hour)},
		{ID: 3, Title: "OldPinned", Pinned: true, LastMessageDate: now.Add(-4 * time.Hour)},
		{ID: 4, Title: "RecentUnpinned", LastMessageDate: now.Add(-30 * time.Minute)},
	}}
	m := sized(NewWithRepo(repo, nil))
	loaded := runCmd(t, m.Init()).(chatsLoadedMsg)

	got := loaded.items
	want := []int64{2, 3, 4, 1}
	if len(got) != len(want) {
		t.Fatalf("expected %d items, got %d", len(want), len(got))
	}
	for i, id := range want {
		if got[i].ID() != id {
			t.Fatalf("item %d: expected ID %d (%s), got ID %d (%s)",
				i, id, idTitle(repo.chats, id), got[i].ID(), got[i].Name())
		}
	}
}

func idTitle(chats []domain.Chat, id int64) string {
	for _, c := range chats {
		if c.ID == id {
			return c.Title
		}
	}
	return ""
}

func TestDialogUpdatedSchedulesDebouncedReload(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{chats: []domain.Chat{makeChat(1, "Only", false, 0, 0)}}
	m := sized(NewWithRepo(repo, nil))
	// Drain initial load.
	loaded := runCmd(t, m.Init()).(chatsLoadedMsg)
	got, _ := m.Update(loaded)
	if repo.callCount != 1 {
		t.Fatalf("expected 1 repo call after init, got %d", repo.callCount)
	}

	// Three rapid DialogUpdated events arrive.
	got, c1 := got.Update(events.DialogUpdated{ChatID: 1})
	got, c2 := got.Update(events.DialogUpdated{ChatID: 1})
	got, c3 := got.Update(events.DialogUpdated{ChatID: 1})

	// Each one returns a tick command. The first two carry stale generations
	// and should be dropped by applyDebouncedReload; only the third triggers
	// a repo read.
	for _, cmd := range []tea.Cmd{c1, c2} {
		if cmd == nil {
			t.Fatalf("each DialogUpdated should arm a tick cmd")
		}
		got, _ = got.Update(cmd())
	}
	if c3 == nil {
		t.Fatalf("third DialogUpdated should arm a tick cmd")
	}
	got, reloadCmd := got.Update(c3())
	if reloadCmd == nil {
		t.Fatalf("debounced tick (latest gen) should produce a load cmd")
	}
	// Run the reload cmd — it must hit the repo exactly once more.
	out := reloadCmd()
	if _, ok := out.(chatsLoadedMsg); !ok {
		t.Fatalf("expected chatsLoadedMsg from debounced reload, got %T", out)
	}
	if repo.callCount != 2 {
		t.Fatalf("expected exactly 2 repo calls (init + 1 debounced), got %d", repo.callCount)
	}
	_ = got
}

func TestRepoErrorDoesNotPanic(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{err: errors.New("boom")}
	m := sized(NewWithRepo(repo, nil))
	msg := runCmd(t, m.Init())
	failed, ok := msg.(chatsLoadFailedMsg)
	if !ok {
		t.Fatalf("expected chatsLoadFailedMsg on repo error, got %T", msg)
	}
	if failed.err == nil {
		t.Fatalf("expected error to be propagated, got nil")
	}
	updated, cmd := m.Update(failed)
	if cmd != nil {
		t.Fatalf("failed-load handler should be silent, got cmd")
	}
	if got := updated.View(); !strings.Contains(got, "Chats") {
		t.Fatalf("view should still render header after failure, got %q", got)
	}
}

func TestSetFocusUpdatesHeader(t *testing.T) {
	t.Parallel()

	m := sized(New())
	out := m.View()
	if strings.Contains(out, "(focused)") {
		t.Fatalf("default view should not advertise focus, got %q", out)
	}
	got := m.SetFocus(true)
	out = got.View()
	if !strings.Contains(out, "Chats (focused)") {
		t.Fatalf("focused view should advertise focus, got %q", out)
	}
}

func TestChatItemFormat(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		chat   domain.Chat
		expect string
	}{
		{
			name:   "plain",
			chat:   domain.Chat{ID: 1, Title: "Bob"},
			expect: "Bob",
		},
		{
			name:   "unread",
			chat:   domain.Chat{ID: 2, Title: "Alice", UnreadCount: 7},
			expect: "Alice (7)",
		},
		{
			name:   "pinned",
			chat:   domain.Chat{ID: 3, Title: "News", Pinned: true},
			expect: "[📌] News",
		},
		{
			name:   "pinned+unread",
			chat:   domain.Chat{ID: 4, Title: "Team", Pinned: true, UnreadCount: 3},
			expect: "[📌] Team (3)",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			it := NewChatItem(tc.chat, "")
			if got := it.Title(); got != tc.expect {
				t.Fatalf("Title(): want %q, got %q", tc.expect, got)
			}
			if got := it.FilterValue(); got != tc.chat.Title {
				t.Fatalf("FilterValue(): want raw title %q, got %q", tc.chat.Title, got)
			}
		})
	}
}

func TestDescriptionTruncation(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", 200)
	it := NewChatItem(domain.Chat{ID: 1, Title: "X"}, long)
	desc := it.Description()
	// Counted in runes (one ellipsis at the end).
	got := []rune(desc)
	if len(got) != previewMaxRunes {
		t.Fatalf("Description should be truncated to %d runes, got %d", previewMaxRunes, len(got))
	}
	if !strings.HasSuffix(desc, "…") {
		t.Fatalf("truncated Description should end with ellipsis, got %q", desc)
	}
}

func TestDescriptionUnicodeAware(t *testing.T) {
	t.Parallel()

	// 70 Cyrillic letters — > 60-rune cap, but counted as runes not bytes.
	preview := strings.Repeat("я", 70)
	it := NewChatItem(domain.Chat{ID: 1, Title: "X"}, preview)
	desc := it.Description()
	if len([]rune(desc)) != previewMaxRunes {
		t.Fatalf("expected %d-rune output, got %d", previewMaxRunes, len([]rune(desc)))
	}
}

func TestSetSizeClampsTinyDimensions(t *testing.T) {
	t.Parallel()

	m := New().SetSize(1, 1)
	if m.Width != 1 || m.Height != 1 {
		t.Fatalf("SetSize should preserve raw dimensions for the box layer; got %dx%d", m.Width, m.Height)
	}
	// View must not panic on tiny size.
	if got := m.View(); got == "" {
		t.Fatalf("View on tiny size should still produce header, got empty")
	}
}
