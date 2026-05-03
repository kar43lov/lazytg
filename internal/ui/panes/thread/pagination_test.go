package thread

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/pgmac/lazytg/internal/core/domain"
	"github.com/pgmac/lazytg/internal/core/events"
)

// fakeRepo is an in-memory Repository used to drive the thread pane in
// tests. Messages are sorted DESC by date / id (matching SQL contract)
// at construction time so paging by offset returns deterministic
// slices regardless of insertion order.
type fakeRepo struct {
	messages []domain.Message
	// callOffsets records every (limit, offset) pair GetMessages was
	// invoked with so tests can assert pagination cursors moved.
	callOffsets [][2]int
	// cursorCalls records every (beforeID, limit) pair GetMessagesBefore
	// was invoked with. Cursor-based pagination is the production path
	// for scroll-up; offset-based is only used for the initial load.
	cursorCalls [][2]int64
	// forwardCursorCalls records every (afterID, limit) pair
	// GetMessagesAfter was invoked with. Tests assert that scroll-down
	// after a search jump triggers the symmetric forward pagination.
	forwardCursorCalls [][2]int64
	err                error
}

func newFakeRepo(n int) *fakeRepo {
	r := &fakeRepo{messages: make([]domain.Message, 0, n)}
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= n; i++ {
		r.messages = append(r.messages, domain.Message{
			ID:     int64(i),
			ChatID: 1,
			FromID: 42,
			Date:   base.Add(time.Duration(i) * time.Minute),
			Text:   "msg " + itoa(int64(i)),
		})
	}
	// Sort DESC for the SQL contract (newest first).
	sort.SliceStable(r.messages, func(i, j int) bool {
		return r.messages[i].ID > r.messages[j].ID
	})
	return r
}

func itoa(n int64) string {
	// Tiny stand-in so we don't pull strconv into the test.
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func (r *fakeRepo) GetMessages(_ context.Context, _ int64, limit, offset int) ([]domain.Message, error) {
	r.callOffsets = append(r.callOffsets, [2]int{limit, offset})
	if r.err != nil {
		return nil, r.err
	}
	if offset >= len(r.messages) {
		return nil, nil
	}
	end := offset + limit
	if end > len(r.messages) {
		end = len(r.messages)
	}
	out := make([]domain.Message, end-offset)
	copy(out, r.messages[offset:end])
	return out, nil
}

// GetMessagesBefore returns up to limit messages with id strictly less
// than beforeID, ordered by id desc. r.messages is already sorted DESC
// (newest first) at construction, so we scan to find the first entry
// with ID < beforeID and slice from there.
func (r *fakeRepo) GetMessagesBefore(_ context.Context, _ int64, beforeID int64, limit int) ([]domain.Message, error) {
	r.cursorCalls = append(r.cursorCalls, [2]int64{beforeID, int64(limit)})
	if r.err != nil {
		return nil, r.err
	}
	start := -1
	for i, m := range r.messages {
		if m.ID < beforeID {
			start = i
			break
		}
	}
	if start == -1 {
		return nil, nil
	}
	end := start + limit
	if end > len(r.messages) {
		end = len(r.messages)
	}
	out := make([]domain.Message, end-start)
	copy(out, r.messages[start:end])
	return out, nil
}

// GetMessagesAfter returns up to limit messages with id strictly
// greater than afterID, ordered by id asc. r.messages is sorted DESC,
// so we walk from the end (oldest) toward the start collecting matches
// — that gives ASC order naturally because the smallest ID > afterID
// appears first.
func (r *fakeRepo) GetMessagesAfter(_ context.Context, _ int64, afterID int64, limit int) ([]domain.Message, error) {
	r.forwardCursorCalls = append(r.forwardCursorCalls, [2]int64{afterID, int64(limit)})
	if r.err != nil {
		return nil, r.err
	}
	if limit <= 0 || afterID <= 0 {
		return nil, nil
	}
	out := make([]domain.Message, 0, limit)
	for i := len(r.messages) - 1; i >= 0 && len(out) < limit; i-- {
		if r.messages[i].ID > afterID {
			out = append(out, r.messages[i])
		}
	}
	return out, nil
}

// runCmd executes a tea.Cmd to completion and returns the resulting
// Msg. Returns nil when cmd is nil. Mirrors the chats pane test
// helper.
func runCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	return cmd()
}

// sized returns m sized to a roomy 80x30 so the viewport has visible
// lines and AtTop()/AtBottom() behave the way the production app
// would.
func sized(m Model) Model { return m.SetSize(80, 30) }

func TestNewWithoutRepo_OpenChatNoOp(t *testing.T) {
	t.Parallel()

	m := sized(New())
	got, cmd := m.OpenChat(1)
	if cmd != nil {
		t.Fatalf("OpenChat on no-repo model should return nil cmd, got %T", cmd())
	}
	if got.Loading() {
		t.Fatalf("no-repo OpenChat must not leave loading=true (UX would be stuck)")
	}
	if got.ChatID() != 1 {
		t.Fatalf("OpenChat should still record chat id, got %d", got.ChatID())
	}
}

func TestOpenChatLoadsInitialPage(t *testing.T) {
	t.Parallel()

	repo := newFakeRepo(500)
	m := sized(NewWithRepo(repo, nil, nil))
	m, cmd := m.OpenChat(1)
	if !m.Loading() {
		t.Fatalf("OpenChat should set loading=true while fetch is in flight")
	}

	loaded, ok := runCmd(t, cmd).(messagesLoadedMsg)
	if !ok {
		t.Fatalf("expected messagesLoadedMsg, got %T", runCmd(t, cmd))
	}
	if len(loaded.messages) != initialPageSize {
		t.Fatalf("expected %d messages on first page, got %d", initialPageSize, len(loaded.messages))
	}
	if !loaded.hasMore {
		t.Fatalf("hasMore should be true with 500 rows beyond a 200-row page")
	}

	updated, _ := m.Update(loaded)
	if got := len(updated.Messages()); got != initialPageSize {
		t.Fatalf("Messages() length: want %d, got %d", initialPageSize, got)
	}
	if updated.Loading() {
		t.Fatalf("loading flag should clear after applyLoaded")
	}
	if !updated.HasMore() {
		t.Fatalf("model should advertise hasMore after first page")
	}
	// Newest 200 messages — i.e. ids 301..500. After reverse-to-ASC the
	// oldest id is 301 and the newest is 500.
	msgs := updated.Messages()
	if msgs[0].ID != 301 {
		t.Fatalf("expected oldest visible id 301, got %d", msgs[0].ID)
	}
	if msgs[len(msgs)-1].ID != 500 {
		t.Fatalf("expected newest visible id 500, got %d", msgs[len(msgs)-1].ID)
	}
	if updated.OldestID() != 301 {
		t.Fatalf("OldestID(): want 301, got %d", updated.OldestID())
	}
}

func TestPaginationLoadsOlderPage(t *testing.T) {
	t.Parallel()

	repo := newFakeRepo(500)
	m := sized(NewWithRepo(repo, nil, nil))
	m, cmd := m.OpenChat(1)
	loaded := runCmd(t, cmd).(messagesLoadedMsg)
	m, _ = m.Update(loaded)

	// Trigger pagination explicitly (the LoadMore method is the test-
	// friendly entry point — KeyPressMsg → AtTop is hard to drive from
	// a unit test because the viewport is empty here).
	m, paginate := m.LoadMore()
	if paginate == nil {
		t.Fatalf("LoadMore should return a Cmd when hasMore=true")
	}
	if !m.Loading() {
		t.Fatalf("LoadMore should flip loading=true")
	}

	pageMsg, ok := runCmd(t, paginate).(messagesPaginationLoadedMsg)
	if !ok {
		t.Fatalf("expected messagesPaginationLoadedMsg, got %T", runCmd(t, paginate))
	}
	if len(pageMsg.messages) != pageSize {
		t.Fatalf("expected %d messages in second page, got %d", pageSize, len(pageMsg.messages))
	}

	updated, _ := m.Update(pageMsg)
	got := updated.Messages()
	if len(got) != initialPageSize+pageSize {
		t.Fatalf("expected %d total after pagination, got %d", initialPageSize+pageSize, len(got))
	}
	// After prepending, oldest visible id should be 101 (ids 101..300
	// from page 2 prepended in front of 301..500).
	if got[0].ID != 101 {
		t.Fatalf("oldest after pagination: want 101, got %d", got[0].ID)
	}
	if got[len(got)-1].ID != 500 {
		t.Fatalf("newest after pagination: want 500, got %d", got[len(got)-1].ID)
	}
	if updated.OldestID() != 101 {
		t.Fatalf("OldestID() should track the prepended page; want 101, got %d", updated.OldestID())
	}
	if !updated.HasMore() {
		t.Fatalf("with 500 rows total and 400 loaded, hasMore should still be true")
	}

	// Initial load uses GetMessages(limit+1, 0); scroll-up uses cursor-
	// based GetMessagesBefore(beforeID=oldestLoaded, limit+1).
	if len(repo.callOffsets) < 1 {
		t.Fatalf("expected at least 1 GetMessages call, got %v", repo.callOffsets)
	}
	if repo.callOffsets[0] != [2]int{initialPageSize + 1, 0} {
		t.Fatalf("first call offsets: want {201,0}, got %v", repo.callOffsets[0])
	}
	if len(repo.cursorCalls) < 1 {
		t.Fatalf("expected at least 1 GetMessagesBefore call after pagination, got %v", repo.cursorCalls)
	}
	// After initial load, oldest visible ID is 301 (newest 200 of 500).
	// Cursor request must page strictly older than 301 to avoid races
	// with applyIncoming.
	if repo.cursorCalls[0][0] != 301 {
		t.Fatalf("cursor beforeID: want 301, got %d", repo.cursorCalls[0][0])
	}
	if repo.cursorCalls[0][1] != int64(pageSize+1) {
		t.Fatalf("cursor limit: want %d, got %d", pageSize+1, repo.cursorCalls[0][1])
	}
}

func TestPaginationStopsWhenNoMoreData(t *testing.T) {
	t.Parallel()

	// Exactly 200 messages — first page exhausts the repo.
	repo := newFakeRepo(initialPageSize)
	m := sized(NewWithRepo(repo, nil, nil))
	m, cmd := m.OpenChat(1)
	loaded := runCmd(t, cmd).(messagesLoadedMsg)
	if loaded.hasMore {
		t.Fatalf("hasMore should be false when row count == page size")
	}
	m, _ = m.Update(loaded)
	if m.HasMore() {
		t.Fatalf("model should not advertise hasMore after exhausting repo")
	}

	_, paginate := m.LoadMore()
	if paginate != nil {
		t.Fatalf("LoadMore must return nil when hasMore=false")
	}
}

func TestRepoErrorDoesNotPanic(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{err: errors.New("boom")}
	m := sized(NewWithRepo(repo, nil, nil))
	m, cmd := m.OpenChat(1)
	failed, ok := runCmd(t, cmd).(messagesLoadFailedMsg)
	if !ok {
		t.Fatalf("expected messagesLoadFailedMsg on repo error, got %T", runCmd(t, cmd))
	}
	if failed.err == nil {
		t.Fatalf("expected error to be propagated, got nil")
	}
	updated, cmd2 := m.Update(failed)
	if cmd2 != nil {
		t.Fatalf("failed-load handler should be silent, got cmd")
	}
	if updated.Loading() {
		t.Fatalf("loading should clear after a failed-load handler")
	}
	if got := updated.View(); !strings.Contains(got, "Thread") {
		t.Fatalf("view should still render header after failure, got %q", got)
	}
}

func TestStaleLoadedDroppedAfterChatSwitch(t *testing.T) {
	t.Parallel()

	repo := newFakeRepo(50)
	m := sized(NewWithRepo(repo, nil, nil))

	m, cmd1 := m.OpenChat(1)
	loaded := runCmd(t, cmd1).(messagesLoadedMsg)
	// User picked chat 2 before chat 1's load completed.
	m, _ = m.OpenChat(2)
	if m.ChatID() != 2 {
		t.Fatalf("OpenChat should switch chat id, got %d", m.ChatID())
	}

	updated, _ := m.Update(loaded)
	if len(updated.Messages()) != 0 {
		t.Fatalf("stale messagesLoadedMsg should be dropped; got %d messages", len(updated.Messages()))
	}
}

func TestIncomingMessageAppendsForCurrentChat(t *testing.T) {
	t.Parallel()

	repo := newFakeRepo(10)
	m := sized(NewWithRepo(repo, nil, nil))
	m, cmd := m.OpenChat(1)
	loaded := runCmd(t, cmd).(messagesLoadedMsg)
	m, _ = m.Update(loaded)

	before := len(m.Messages())
	ev := events.MessageReceived{
		ChatID:    1,
		MessageID: 999,
		FromID:    7,
		Date:      time.Now().UTC(),
		Text:      "live ping",
	}
	updated, _ := m.Update(ev)
	if got := len(updated.Messages()); got != before+1 {
		t.Fatalf("incoming should append; want %d, got %d", before+1, got)
	}
	last := updated.Messages()[len(updated.Messages())-1]
	if last.Text != "live ping" || last.ID != 999 {
		t.Fatalf("incoming payload not preserved: %+v", last)
	}
}

func TestIncomingMessageIgnoredForOtherChat(t *testing.T) {
	t.Parallel()

	repo := newFakeRepo(10)
	m := sized(NewWithRepo(repo, nil, nil))
	m, cmd := m.OpenChat(1)
	loaded := runCmd(t, cmd).(messagesLoadedMsg)
	m, _ = m.Update(loaded)

	before := len(m.Messages())
	updated, _ := m.Update(events.MessageReceived{ChatID: 999, MessageID: 1, Date: time.Now()})
	if len(updated.Messages()) != before {
		t.Fatalf("event for other chat must not append; want %d, got %d", before, len(updated.Messages()))
	}
}

func TestSetFocusUpdatesHeader(t *testing.T) {
	t.Parallel()

	m := sized(New())
	if got := m.View(); strings.Contains(got, "(focused)") {
		t.Fatalf("default view should not advertise focus, got %q", got)
	}
	got := m.SetFocus(true)
	if v := got.View(); !strings.Contains(v, "Thread (focused)") {
		t.Fatalf("focused view should advertise focus, got %q", v)
	}
}

func TestLoadingViewWhileFetching(t *testing.T) {
	t.Parallel()

	repo := newFakeRepo(50)
	m := sized(NewWithRepo(repo, nil, nil))
	m, _ = m.OpenChat(1)
	if !strings.Contains(m.View(), "Loading...") {
		t.Fatalf("expected Loading... while fetch is in flight, got %q", m.View())
	}
}

func TestLoadMoreReentrancyGuard(t *testing.T) {
	t.Parallel()

	repo := newFakeRepo(500)
	m := sized(NewWithRepo(repo, nil, nil))
	m, cmd := m.OpenChat(1)
	loaded := runCmd(t, cmd).(messagesLoadedMsg)
	m, _ = m.Update(loaded)

	// First LoadMore — accepted.
	m, c1 := m.LoadMore()
	if c1 == nil {
		t.Fatalf("first LoadMore should return a Cmd")
	}
	// Second LoadMore while still loading — must not produce another
	// query (otherwise pagination races itself).
	_, c2 := m.LoadMore()
	if c2 != nil {
		t.Fatalf("re-entrant LoadMore should be a no-op while loading=true")
	}
}
