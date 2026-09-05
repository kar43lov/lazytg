package chats

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
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

func TestMessageReceivedSchedulesDebouncedReload(t *testing.T) {
	t.Parallel()

	// LiveService persists incoming messages but no longer republishes a
	// DialogUpdated (avoids self-feedback into its own bus subscription).
	// The chats pane reorders by reacting to MessageReceived directly.
	repo := &fakeRepo{chats: []domain.Chat{makeChat(1, "Only", false, 0, 0)}}
	m := sized(NewWithRepo(repo, nil))
	loaded := runCmd(t, m.Init()).(chatsLoadedMsg)
	got, _ := m.Update(loaded)
	if repo.callCount != 1 {
		t.Fatalf("expected 1 repo call after init, got %d", repo.callCount)
	}

	got, cmd := got.Update(events.MessageReceived{ChatID: 1, MessageID: 42, Text: "hi"})
	if cmd == nil {
		t.Fatalf("MessageReceived should arm a debounced tick cmd")
	}
	out := cmd()
	if _, ok := out.(reloadDebouncedMsg); !ok {
		t.Fatalf("expected reloadDebouncedMsg from tick, got %T", out)
	}
	_, reloadCmd := got.Update(out)
	if reloadCmd == nil {
		t.Fatalf("latest debounce tick should produce a load cmd")
	}
	if _, ok := reloadCmd().(chatsLoadedMsg); !ok {
		t.Fatalf("expected chatsLoadedMsg from reload, got %T", reloadCmd())
	}
	if repo.callCount != 2 {
		t.Fatalf("expected exactly 2 repo calls, got %d", repo.callCount)
	}
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

	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.Local)
	cases := []struct {
		name        string
		chat        domain.Chat
		title, desc string
	}{
		{
			name:  "plain",
			chat:  domain.Chat{ID: 1, Title: "Bob"},
			title: "Bob",
		},
		{
			name:  "unread badge sits on the second row",
			chat:  domain.Chat{ID: 2, Title: "Alice", UnreadCount: 7},
			title: "Alice",
			desc:  "  (7)",
		},
		{
			name:  "pinned",
			chat:  domain.Chat{ID: 3, Title: "News", Pinned: true},
			title: "📌 News",
		},
		{
			name:  "pinned, unread and muted",
			chat:  domain.Chat{ID: 4, Title: "Team", Pinned: true, UnreadCount: 3, MutedUntil: now.Add(time.Hour)},
			title: "📌 Team",
			desc:  "  (3) 🔕",
		},
		{
			name:  "the by-hand dot",
			chat:  domain.Chat{ID: 5, Title: "Later", UnreadMark: true},
			title: "Later",
			desc:  "  ●",
		},
		{
			name:  "time of the last message",
			chat:  domain.Chat{ID: 6, Title: "Bob", LastMessageDate: now.Add(-time.Hour)},
			title: "Bob  11:00",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			it := NewChatItem(tc.chat, "")
			it.now = now
			if got := it.Title(); got != tc.title {
				t.Fatalf("Title(): want %q, got %q", tc.title, got)
			}
			if got := it.Description(); got != tc.desc {
				t.Fatalf("Description(): want %q, got %q", tc.desc, got)
			}
			if got := it.FilterValue(); got != tc.chat.Title {
				t.Fatalf("FilterValue(): want raw title %q, got %q", tc.chat.Title, got)
			}
		})
	}
}

// With a width the row is laid out in two columns: name left, time right,
// the name giving way when both do not fit.
func TestChatItem_ColumnsAtWidth(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.Local)
	it := NewChatItem(domain.Chat{ID: 1, Title: "Иван Егошин", LastMessageDate: now.Add(-time.Minute), UnreadCount: 2}, "а тебе в одно место").withWidth(24)
	it.now = now
	if got := it.Title(); got != "Иван Егошин        11:59" || lipgloss.Width(got) != 24 {
		t.Fatalf("Title() = %q (%d cells)", got, lipgloss.Width(got))
	}
	if got := it.Description(); got != "а тебе в одно место  (2)" || lipgloss.Width(got) != 24 {
		t.Fatalf("Description() = %q (%d cells)", got, lipgloss.Width(got))
	}
	long := NewChatItem(domain.Chat{ID: 2, Title: "Мадина турагентство Ташкент SKY FLY", LastMessageDate: now}, "").withWidth(24)
	long.now = now
	if got := long.Title(); lipgloss.Width(got) != 24 || !strings.HasSuffix(got, " 12:00") || !strings.Contains(got, "…") {
		t.Fatalf("a long name did not give way to the time: %q", got)
	}
}

func TestChatTimeLabel(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.Local) // a Saturday
	cases := map[string]string{
		"today":     chatTimeLabel(now.Add(-3*time.Hour), now),
		"yesterday": chatTimeLabel(now.Add(-24*time.Hour), now),
		"weekday":   chatTimeLabel(now.Add(-3*24*time.Hour), now),
		"this year": chatTimeLabel(now.Add(-30*24*time.Hour), now),
		"older":     chatTimeLabel(now.Add(-400*24*time.Hour), now),
		"unknown":   chatTimeLabel(time.Time{}, now),
	}
	want := map[string]string{"today": "09:00", "yesterday": "Yesterday", "weekday": "Wed", "this year": "06.08", "older": "01.08.25", "unknown": ""}
	for k, v := range want {
		if cases[k] != v {
			t.Errorf("%s: got %q, want %q", k, cases[k], v)
		}
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

// TestSelectionFollowsTheChatAcrossAReorder is the list half of the second
// live run. The list sorts by last-message date, so a new message promotes
// its chat to the top and pushes everything below it down one row. The cursor
// used to be kept by index, so it stayed on row 0 and silently came to point
// at the promoted chat — a chat the user never selected, while the thread pane
// still showed the old one. Observed live on 19.08.2026 as "the chat is open
// but the thread is empty".
func TestSelectionFollowsTheChatAcrossAReorder(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{chats: []domain.Chat{
		makeChat(10, "First", false, 0, 0),
		makeChat(20, "Second", false, -1*time.Hour, 0),
	}}
	m := sized(NewWithRepo(repo, nil))
	m, _ = m.Update(runCmd(t, m.Init()).(chatsLoadedMsg))

	m, _ = m.Update(keyChord(tea.KeyDown, 0))
	if sel, _ := m.SelectedItem(); sel.ID() != 20 {
		t.Fatalf("setup: selection id=%d, want 20", sel.ID())
	}

	// A message arrives in chat 30, which the mirror has not shown before: it
	// sorts to the top and both existing rows shift down.
	repo.chats = []domain.Chat{
		makeChat(30, "Newcomer", false, time.Hour, 1),
		makeChat(10, "First", false, 0, 0),
		makeChat(20, "Second", false, -1*time.Hour, 0),
	}
	m, _ = m.Update(runCmd(t, m.Init()).(chatsLoadedMsg))

	sel, ok := m.SelectedItem()
	if !ok {
		t.Fatalf("nothing selected after the reorder")
	}
	if sel.ID() != 20 {
		t.Fatalf("selection moved to id=%d (%q) after the reorder, want the chat the user picked, id=20",
			sel.ID(), sel.Name())
	}
}

// TestSelectionFallsBackWhenItsChatIsGone covers the other direction: the
// selected chat was deleted from another device, so there is no id to follow.
// The cursor must land on the row that took its place rather than vanish.
func TestSelectionFallsBackWhenItsChatIsGone(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{chats: []domain.Chat{
		makeChat(10, "First", false, 0, 0),
		makeChat(20, "Second", false, -1*time.Hour, 0),
		makeChat(30, "Third", false, -2*time.Hour, 0),
	}}
	m := sized(NewWithRepo(repo, nil))
	m, _ = m.Update(runCmd(t, m.Init()).(chatsLoadedMsg))
	m, _ = m.Update(keyChord(tea.KeyDown, 0))

	repo.chats = []domain.Chat{
		makeChat(10, "First", false, 0, 0),
		makeChat(30, "Third", false, -2*time.Hour, 0),
	}
	m, _ = m.Update(runCmd(t, m.Init()).(chatsLoadedMsg))

	sel, ok := m.SelectedItem()
	if !ok {
		t.Fatalf("nothing selected after the deletion")
	}
	if sel.ID() != 30 {
		t.Fatalf("selection = id=%d after its chat was deleted, want the row that took its place, id=30", sel.ID())
	}
}

// TestSelectionIsLeftAloneWhileFiltering guards the id-following logic against
// the list's two index spaces: Index and Select address the *filtered* list,
// while applyLoaded only ever sees the full one. Restoring a position across a
// reload while a filter is active would therefore aim at the wrong set — and
// at a set SetItems has not rebuilt yet, since it re-filters asynchronously.
func TestSelectionIsLeftAloneWhileFiltering(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{chats: []domain.Chat{
		makeChat(10, "Alpha", false, 0, 0),
		makeChat(20, "Beta", false, -1*time.Hour, 0),
		makeChat(30, "Gamma", false, -2*time.Hour, 0),
	}}
	m := sized(NewWithRepo(repo, nil))
	m, _ = m.Update(runCmd(t, m.Init()).(chatsLoadedMsg))

	// Narrow the list to one row. SetFilterText/SetFilterState is the library's
	// own way in — driving it through keystrokes would test bubbles' text input
	// rather than this pane.
	m.list.SetFilterText("Beta")
	m.list.SetFilterState(list.FilterApplied)

	if got := m.list.FilterState(); got == list.Unfiltered {
		t.Fatalf("setup: filter is not active (state=%v)", got)
	}
	if sel, ok := m.SelectedItem(); !ok || sel.ID() != 20 {
		t.Fatalf("setup: filtered selection id=%d ok=%v, want 20", sel.ID(), ok)
	}

	// A reload arrives while the filter is up. SetItems re-runs the filter
	// through the command it returns, so the match set only exists once that
	// command has been delivered — which is why applyLoaded cannot compute an
	// index into it.
	m, cmd := m.Update(runCmd(t, m.Init()).(chatsLoadedMsg))
	if matches := runCmd(t, cmd); matches != nil {
		m, _ = m.Update(matches)
	}

	if sel, ok := m.SelectedItem(); !ok || sel.ID() != 20 {
		t.Fatalf("selection = id=%d ok=%v after a reload under a filter, want 20", sel.ID(), ok)
	}
}
