package thread

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/pgmac/lazytg/internal/core/domain"
	"github.com/pgmac/lazytg/internal/core/events"
)

// TestLoadJumpWindow_ArmsForwardPagination locks in the contract that
// LoadJumpWindow flips hasNewer / forwardCursorID so the user is no
// longer stranded in the ±N slice — covers the codex finding that a
// jump to an old hit makes messages newer than the window unreachable.
func TestLoadJumpWindow_ArmsForwardPagination(t *testing.T) {
	t.Parallel()

	const chatID int64 = 7
	m := sized(New())
	m = m.SwitchTo(chatID)

	window := []domain.Message{
		{ID: 50, ChatID: chatID, Date: time.Date(2026, 1, 1, 0, 0, 50, 0, time.UTC), Text: "ctx-50"},
		{ID: 51, ChatID: chatID, Date: time.Date(2026, 1, 1, 0, 0, 51, 0, time.UTC), Text: "ctx-51"},
		{ID: 52, ChatID: chatID, Date: time.Date(2026, 1, 1, 0, 0, 52, 0, time.UTC), Text: "ctx-52"},
	}
	m = m.LoadJumpWindow(chatID, window, 51, 1)

	if !m.HasNewer() {
		t.Fatalf("LoadJumpWindow with non-empty window must arm hasNewer")
	}
	if got, want := m.ForwardCursorID(), int64(52); got != want {
		t.Fatalf("forwardCursorID after jump: got %d, want %d", got, want)
	}
}

// TestLoadJumpWindow_EmptyWindowKeepsForwardDisabled guards the edge
// case: an empty window (target missing) leaves nothing to paginate
// forward from, so hasNewer must stay false to prevent a useless
// "id > 0" forward fetch loop.
func TestLoadJumpWindow_EmptyWindowKeepsForwardDisabled(t *testing.T) {
	t.Parallel()

	const chatID int64 = 9
	m := sized(New())
	m = m.SwitchTo(chatID)
	m = m.LoadJumpWindow(chatID, nil, 0, 0)

	if m.HasNewer() {
		t.Fatalf("empty-window jump must not arm hasNewer (no cursor anchor)")
	}
	if m.ForwardCursorID() != 0 {
		t.Fatalf("empty-window jump must leave forwardCursorID at 0")
	}
}

// TestLoadMoreNewer_LoadsForwardPageAndAppends drives the symmetric
// scroll-down pagination path explicitly. After LoadJumpWindow the
// thread holds [45..55]; LoadMoreNewer fires GetMessagesAfter(after=55)
// and applyPaginationNewer appends ids 56..255 (page size 200), then
// forwardCursorID advances to 255.
func TestLoadMoreNewer_LoadsForwardPageAndAppends(t *testing.T) {
	t.Parallel()

	const chatID int64 = 1
	repo := newFakeRepo(500)
	m := sized(NewWithRepo(repo, nil, nil))
	m = m.SwitchTo(chatID)

	// Build the ±5 window around id=50.
	window := make([]domain.Message, 0, 11)
	for id := int64(45); id <= 55; id++ {
		window = append(window, domain.Message{
			ID: id, ChatID: chatID,
			Date: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC).Add(time.Duration(id) * time.Minute),
			Text: "msg",
		})
	}
	m = m.LoadJumpWindow(chatID, window, 50, 5)
	if !m.HasNewer() {
		t.Fatalf("precondition: hasNewer must be true after LoadJumpWindow")
	}

	m, cmd := m.LoadMoreNewer()
	if cmd == nil {
		t.Fatalf("LoadMoreNewer must return a Cmd when hasNewer=true")
	}
	if !m.Loading() {
		t.Fatalf("LoadMoreNewer must set loading=true while fetch is in flight")
	}

	pageMsg, ok := runCmd(t, cmd).(messagesPaginationNewerLoadedMsg)
	if !ok {
		t.Fatalf("expected messagesPaginationNewerLoadedMsg, got %T", runCmd(t, cmd))
	}
	if got, want := len(pageMsg.messages), pageSize; got != want {
		t.Fatalf("forward page size: got %d, want %d", got, want)
	}

	updated, _ := m.Update(pageMsg)
	got := updated.Messages()
	// 11-row window + 200 forward = 211 total.
	if len(got) != len(window)+pageSize {
		t.Fatalf("after forward page: want %d msgs, got %d", len(window)+pageSize, len(got))
	}
	if got[0].ID != 45 {
		t.Fatalf("oldest after forward page: want 45, got %d", got[0].ID)
	}
	if got[len(got)-1].ID != 255 {
		t.Fatalf("newest after forward page: want 255, got %d", got[len(got)-1].ID)
	}
	if updated.ForwardCursorID() != 255 {
		t.Fatalf("forwardCursorID after page: want 255, got %d", updated.ForwardCursorID())
	}
	if !updated.HasNewer() {
		t.Fatalf("forward page returned full batch — hasNewer must stay true")
	}

	// Repo call shape: GetMessagesAfter(after=55, limit=pageSize+1).
	if len(repo.forwardCursorCalls) != 1 {
		t.Fatalf("expected 1 GetMessagesAfter call, got %d", len(repo.forwardCursorCalls))
	}
	if repo.forwardCursorCalls[0] != [2]int64{55, int64(pageSize + 1)} {
		t.Fatalf("forward cursor call: got %v, want {55, %d}", repo.forwardCursorCalls[0], pageSize+1)
	}
}

// TestForwardPagination_StopsWhenChatTailReached covers the clear path:
// when the forward page returns fewer than limit rows, hasNewer flips
// to false so AtBottom + scroll no longer schedules useless fetches.
func TestForwardPagination_StopsWhenChatTailReached(t *testing.T) {
	t.Parallel()

	const chatID int64 = 1
	repo := newFakeRepo(60) // chat has 60 messages
	m := sized(NewWithRepo(repo, nil, nil))
	m = m.SwitchTo(chatID)

	// Window around id=50 — only ids 45..55 exist on this side; the
	// chat tops out at 60 so the next forward page returns 56..60 (5
	// rows) — strictly fewer than pageSize, so hasNewer must clear.
	window := make([]domain.Message, 0, 11)
	for id := int64(45); id <= 55; id++ {
		window = append(window, domain.Message{
			ID: id, ChatID: chatID,
			Date: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC).Add(time.Duration(id) * time.Minute),
			Text: "msg",
		})
	}
	m = m.LoadJumpWindow(chatID, window, 50, 5)

	m, cmd := m.LoadMoreNewer()
	pageMsg := runCmd(t, cmd).(messagesPaginationNewerLoadedMsg)
	updated, _ := m.Update(pageMsg)

	if updated.HasNewer() {
		t.Fatalf("short forward page must clear hasNewer; got hasNewer=true")
	}
	got := updated.Messages()
	if got[len(got)-1].ID != 60 {
		t.Fatalf("after final forward page, last id should be 60, got %d", got[len(got)-1].ID)
	}

	// A subsequent LoadMoreNewer must be a no-op.
	_, cmd2 := updated.LoadMoreNewer()
	if cmd2 != nil {
		t.Fatalf("LoadMoreNewer with hasNewer=false must return nil cmd")
	}
}

// TestForwardPagination_AtBottomKeyTrigger drives the scroll-down trigger
// through the same code path the live UI uses (handleKey + AtBottom).
// Locks in that scroll-to-bottom after a jump will fetch the next
// forward page.
func TestForwardPagination_AtBottomKeyTrigger(t *testing.T) {
	t.Parallel()

	const chatID int64 = 1
	repo := newFakeRepo(500)
	m := sized(NewWithRepo(repo, nil, nil))
	m = m.SwitchTo(chatID)

	window := []domain.Message{
		{ID: 50, ChatID: chatID, Date: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), Text: "msg"},
	}
	m = m.LoadJumpWindow(chatID, window, 50, 0)
	// A 1-row window is shorter than the viewport so the scroll-to-end
	// trick puts the viewport AtBottom right away; no need to drive
	// scroll keys from a height-aware setup.
	m.viewport.GotoBottom()

	// Synthetic key press — the value does not matter, handleKey just
	// forwards it to the viewport and then checks shouldPaginateNewer.
	_, cmd := m.handleKey(tea.KeyPressMsg{})
	if cmd == nil {
		t.Fatalf("AtBottom + hasNewer must produce a forward-pagination Cmd")
	}
}

// TestPaginateNewerIfAtBottom_FiresWhenAtBottom drives the explicit
// post-scroll trigger that applyScrollKey uses. After a jump installs
// a small window (fits in the viewport so AtBottom is true), calling
// PaginateNewerIfAtBottom must produce a forward-page Cmd. Locks in
// the contract that the configurable ScrollDown chord (intercepted
// before reaching handleKey) reaches forward pagination via this
// helper.
func TestPaginateNewerIfAtBottom_FiresWhenAtBottom(t *testing.T) {
	t.Parallel()

	const chatID int64 = 1
	repo := newFakeRepo(500)
	m := sized(NewWithRepo(repo, nil, nil))
	m = m.SwitchTo(chatID)

	window := []domain.Message{
		{ID: 50, ChatID: chatID, Date: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), Text: "msg"},
	}
	m = m.LoadJumpWindow(chatID, window, 50, 0)
	m.viewport.GotoBottom()

	updated, cmd := m.PaginateNewerIfAtBottom()
	if cmd == nil {
		t.Fatalf("PaginateNewerIfAtBottom must fire when AtBottom + hasNewer; got nil cmd")
	}
	if !updated.Loading() {
		t.Fatalf("PaginateNewerIfAtBottom must set loading=true while fetch is in flight")
	}
}

// TestPaginateOlderIfAtTop_FiresWhenAtTop is the symmetric backward
// trigger. After OpenChat, the viewport is at the top — scrolling up
// further must drive backward pagination via this helper.
func TestPaginateOlderIfAtTop_FiresWhenAtTop(t *testing.T) {
	t.Parallel()

	const chatID int64 = 1
	repo := newFakeRepo(500)
	m := sized(NewWithRepo(repo, nil, nil))

	m, cmd := m.OpenChat(chatID)
	loaded := runCmd(t, cmd).(messagesLoadedMsg)
	m, _ = m.Update(loaded)
	m.viewport.GotoTop()

	updated, cmd := m.PaginateOlderIfAtTop()
	if cmd == nil {
		t.Fatalf("PaginateOlderIfAtTop must fire when AtTop + hasMore; got nil cmd")
	}
	if !updated.Loading() {
		t.Fatalf("PaginateOlderIfAtTop must set loading=true while fetch is in flight")
	}
}

// TestPaginateNewerIfAtBottom_NoOpWhenNoNewer locks in the contract
// applyScrollKey relies on: when the small-window AtTop+AtBottom case
// holds with hasNewer=false but hasMore=true, PaginateNewerIfAtBottom
// must return a nil cmd so the chord path can fall through to
// PaginateOlderIfAtTop. Without this signal the ScrollDown chord (default
// pgdown / ctrl+f) on a chat that fits entirely in the viewport would
// strand the user — handleKey's direction-aware fallback would have
// paged older, but the chord interception bypasses handleKey entirely.
func TestPaginateNewerIfAtBottom_NoOpWhenNoNewer(t *testing.T) {
	t.Parallel()

	const chatID int64 = 1
	repo := newFakeRepo(500)
	m := sized(NewWithRepo(repo, nil, nil))

	m, cmd := m.OpenChat(chatID)
	loaded := runCmd(t, cmd).(messagesLoadedMsg)
	m, _ = m.Update(loaded)
	if m.HasNewer() {
		t.Fatalf("precondition: applyLoaded must clear hasNewer for normal OpenChat")
	}
	if !m.HasMore() {
		t.Fatalf("precondition: 500-row repo with 200-row page must leave hasMore=true")
	}

	// PaginateNewerIfAtBottom is a no-op (hasNewer=false signals "no
	// forward target"), so the chord-path fallback in applyScrollKey
	// proceeds to PaginateOlderIfAtTop, which fires backward pagination.
	_, newerCmd := m.PaginateNewerIfAtBottom()
	if newerCmd != nil {
		t.Fatalf("PaginateNewerIfAtBottom must be nil when hasNewer=false; chord fallback relies on this")
	}

	m.viewport.GotoTop()
	_, olderCmd := m.PaginateOlderIfAtTop()
	if olderCmd == nil {
		t.Fatalf("PaginateOlderIfAtTop must fire as the chord-path fallback when hasMore=true and AtTop")
	}
}

// TestHandleKey_DownKeyPrefersForwardOnSmallWindow guards the
// direction-aware ordering in handleKey. When a search-jump window
// fits entirely in the viewport, AtTop and AtBottom are both true.
// Pressing a down-direction key (j / down / ctrl+d / etc.) must drive
// forward pagination, not the older-history pagination that the
// shouldPaginate-first ordering would otherwise pick.
func TestHandleKey_DownKeyPrefersForwardOnSmallWindow(t *testing.T) {
	t.Parallel()

	const chatID int64 = 1
	repo := newFakeRepo(500)
	m := sized(NewWithRepo(repo, nil, nil))
	m = m.SwitchTo(chatID)

	// 1-row window — guaranteed AtTop and AtBottom both true.
	window := []domain.Message{
		{ID: 50, ChatID: chatID, Date: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), Text: "msg"},
	}
	m = m.LoadJumpWindow(chatID, window, 50, 0)
	if !m.viewport.AtTop() || !m.viewport.AtBottom() {
		t.Fatalf("precondition: small window must put viewport at both edges (AtTop=%v AtBottom=%v)", m.viewport.AtTop(), m.viewport.AtBottom())
	}

	_, cmd := m.handleKey(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if cmd == nil {
		t.Fatalf("down-direction key on small jump window must produce a Cmd")
	}

	// The cmd must be the forward-pagination Cmd, not the backward one.
	// Drive it and verify the resulting msg type.
	msg := runCmd(t, cmd)
	switch msg.(type) {
	case messagesPaginationNewerLoadedMsg:
		// expected
	case messagesPaginationLoadedMsg:
		t.Fatalf("down-direction key fired backward pagination instead of forward (small-window order bug)")
	default:
		// tea.Batch may return a sequence; check both possible cmds
		t.Fatalf("unexpected msg type from down-direction key: %T", msg)
	}
}

// TestForwardPagination_DedupsLiveAppend covers the race where a live
// MessageReceived landed during JumpContext (so it sits at the tail
// above forwardCursorID) and is also picked up by the forward page
// SQL. Without dedup the row would render twice.
func TestForwardPagination_DedupsLiveAppend(t *testing.T) {
	t.Parallel()

	const chatID int64 = 1
	repo := newFakeRepo(500)
	m := sized(NewWithRepo(repo, nil, nil))
	m = m.SwitchTo(chatID)

	window := []domain.Message{
		{ID: 50, ChatID: chatID, Date: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), Text: "ctx-50"},
	}
	m = m.LoadJumpWindow(chatID, window, 50, 0)

	// Live append at id=200, lands above forwardCursorID (=50) but
	// below where the forward page (50..250) ends — so the page also
	// includes id=200 and dedup must drop one of them.
	m, _ = m.applyIncoming(events.MessageReceived{
		ChatID:    chatID,
		MessageID: 200,
		Date:      time.Date(2026, 1, 1, 14, 20, 0, 0, time.UTC),
		Text:      "live-200",
	})

	_, cmd := m.LoadMoreNewer()
	pageMsg := runCmd(t, cmd).(messagesPaginationNewerLoadedMsg)
	updated, _ := m.Update(pageMsg)

	got := updated.Messages()
	seen := make(map[int64]int)
	for _, msg := range got {
		seen[msg.ID]++
	}
	for id, n := range seen {
		if n > 1 {
			t.Fatalf("forward-pagination duplicated id %d (%d copies)", id, n)
		}
	}
}

// TestApplyLoaded_DropsStaleSameChatLoad locks in the loadGen guard:
// a slow OpenChat fetch landing AFTER LoadJumpWindow installed the
// window for the same chat must not clobber it. Covers the
// codex-reported race where chatID-only filtering let the stale load
// through.
func TestApplyLoaded_DropsStaleSameChatLoad(t *testing.T) {
	t.Parallel()

	const chatID int64 = 1
	repo := newFakeRepo(500)
	m := sized(NewWithRepo(repo, nil, nil))

	// Step 1: OpenChat fires loadCmd (in-flight). loadGen=1.
	m, cmd := m.OpenChat(chatID)
	loaded := runCmd(t, cmd).(messagesLoadedMsg)

	// Step 2: search jump for the same chat. SwitchTo bumps gen to 2,
	// LoadJumpWindow bumps to 3. Window holds id=50.
	m = m.SwitchTo(chatID)
	window := []domain.Message{
		{ID: 50, ChatID: chatID, Date: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), Text: "jump-50"},
	}
	m = m.LoadJumpWindow(chatID, window, 50, 0)
	if got := len(m.Messages()); got != 1 {
		t.Fatalf("precondition: jump window must hold 1 msg, got %d", got)
	}

	// Step 3: the stale loadCmd from step 1 returns. chatID matches
	// (same chat) but gen does not — applyLoaded must drop it.
	updated, _ := m.Update(loaded)
	if got := len(updated.Messages()); got != 1 {
		t.Fatalf("stale same-chat load must NOT clobber jump window; got %d msgs", got)
	}
	if updated.Messages()[0].ID != 50 {
		t.Fatalf("jump window must remain intact after stale-load drop; got id=%d", updated.Messages()[0].ID)
	}
}

// TestApplyPagination_DropsStaleSameChatPage is the symmetric guard for
// the older-page pagination: a scroll-up that started before the jump
// must not prepend stray rows into the freshly-installed window.
func TestApplyPagination_DropsStaleSameChatPage(t *testing.T) {
	t.Parallel()

	const chatID int64 = 1
	repo := newFakeRepo(500)
	m := sized(NewWithRepo(repo, nil, nil))

	// Initial load + apply so LoadMore has an oldestID anchor.
	m, cmd := m.OpenChat(chatID)
	loaded := runCmd(t, cmd).(messagesLoadedMsg)
	m, _ = m.Update(loaded)

	// User scrolls up — pagination cmd in flight. gen captured = 1
	// (after OpenChat).
	_, paginate := m.LoadMore()
	pageMsg, ok := runCmd(t, paginate).(messagesPaginationLoadedMsg)
	if !ok {
		t.Fatalf("expected pagination msg, got %T", runCmd(t, paginate))
	}

	// Search jump arrives first — bumps gen to 3 (SwitchTo + LoadJumpWindow).
	m = m.SwitchTo(chatID)
	window := []domain.Message{
		{ID: 50, ChatID: chatID, Date: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), Text: "jump"},
	}
	m = m.LoadJumpWindow(chatID, window, 50, 0)

	// Stale older-page lands. Drop expected.
	updated, _ := m.Update(pageMsg)
	if got := len(updated.Messages()); got != 1 {
		t.Fatalf("stale older-page must NOT prepend into jump window; got %d msgs", got)
	}
}

// TestForwardPagination_StaleGenDropped guards against the same-chat
// jump-then-rejump race for the forward path: a forward fetch
// scheduled before a second jump must not append into the new
// jump's window.
func TestForwardPagination_StaleGenDropped(t *testing.T) {
	t.Parallel()

	const chatID int64 = 1
	repo := newFakeRepo(500)
	m := sized(NewWithRepo(repo, nil, nil))

	m = m.SwitchTo(chatID)
	first := []domain.Message{
		{ID: 50, ChatID: chatID, Date: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), Text: "first"},
	}
	m = m.LoadJumpWindow(chatID, first, 50, 0)

	// Forward cmd in-flight at gen=2 (SwitchTo=1, LoadJumpWindow=2).
	_, cmd := m.LoadMoreNewer()
	pageMsg := runCmd(t, cmd).(messagesPaginationNewerLoadedMsg)

	// User triggers a second jump for the same chat. gen advances.
	m = m.SwitchTo(chatID)
	second := []domain.Message{
		{ID: 100, ChatID: chatID, Date: time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC), Text: "second"},
	}
	m = m.LoadJumpWindow(chatID, second, 100, 0)

	// Stale forward page from the first jump arrives — must drop.
	updated, _ := m.Update(pageMsg)
	if got := len(updated.Messages()); got != 1 {
		t.Fatalf("stale forward page must drop; got %d msgs", got)
	}
	if updated.Messages()[0].ID != 100 {
		t.Fatalf("second jump window must remain intact; got id=%d", updated.Messages()[0].ID)
	}
}

// TestLoadFailed_StaleGenDropped guards the gen check on the failure
// path. A repo error from a load that the model has already moved on
// from (chat switch / search-jump bumped loadGen) must not flip
// m.loading to false. The search-jump fallback path uses a deferred
// applyPendingScroll keyed off Loading()=false; an early idle signal
// from a stale failure would let the scroll fire before the real load
// completes — ScrollTo then no-ops on the empty slice and pendingScroll
// is cleared, so the user lands at the chat tail instead of the jump
// target.
func TestLoadFailed_StaleGenDropped(t *testing.T) {
	t.Parallel()

	const chatID int64 = 1
	repo := newFakeRepo(500)
	m := sized(NewWithRepo(repo, nil, nil))

	// Step 1: OpenChat fires loadCmd — gen=1, loading=true.
	m, _ = m.OpenChat(chatID)
	if !m.Loading() {
		t.Fatalf("precondition: OpenChat must set loading=true")
	}

	// Step 2: search-jump SwitchTo bumps gen to 2 and clears loading.
	m = m.SwitchTo(chatID)
	if m.Loading() {
		t.Fatalf("precondition: SwitchTo must clear loading")
	}

	// Step 3: simulate the gen=1 load failing late. Without the gen
	// guard, m.loading would flip to false redundantly here — but the
	// real bug bites in the next step.
	stale := messagesLoadFailedMsg{chatID: chatID, gen: 1, err: errBoomTest}
	m, _ = m.Update(stale)
	if m.Loading() {
		t.Fatalf("after stale failure with no in-flight load: loading must stay false")
	}

	// Step 4: search-jump fallback OpenChat fires a new load — gen=3,
	// loading=true.
	m, _ = m.OpenChat(chatID)
	if !m.Loading() {
		t.Fatalf("precondition: fallback OpenChat must set loading=true again")
	}

	// Step 5: another stale failure (also gen=1) lands AFTER the
	// fallback OpenChat. Without the gen guard, m.loading would flip
	// to false even though gen=3 is still in flight — that is the
	// bug: applyPendingScroll would fire prematurely. With the guard
	// the failure must be dropped silently.
	m, _ = m.Update(stale)
	if !m.Loading() {
		t.Fatalf("stale failure must NOT clear loading while a newer load is in flight")
	}
}

// errBoomTest is a sentinel used by stale-failure tests so the message
// matches what loadCmd would emit on a real repo error.
var errBoomTest = errors.New("stale boom")

// repoStub satisfies thread.Repository as a compile-time guard. The
// real *sqlite.Repo and the test fakes both implement the trio.
var _ Repository = (*compileGuardRepo)(nil)

type compileGuardRepo struct{}

func (compileGuardRepo) GetMessages(_ context.Context, _ int64, _, _ int) ([]domain.Message, error) {
	return nil, nil
}

func (compileGuardRepo) GetMessagesBefore(_ context.Context, _, _ int64, _ int) ([]domain.Message, error) {
	return nil, nil
}

func (compileGuardRepo) GetMessagesAfter(_ context.Context, _, _ int64, _ int) ([]domain.Message, error) {
	return nil, nil
}
