package thread

import (
	tea "charm.land/bubbletea/v2"

	"github.com/pgmac/lazytg/internal/core/domain"
	"github.com/pgmac/lazytg/internal/core/events"
)

// Update routes incoming messages. The order matters:
//  1. Domain payloads (messagesLoadedMsg / messagesPaginationLoadedMsg /
//     messagesPaginationNewerLoadedMsg / messagesLoadFailedMsg) run
//     first so a repo refresh is applied before any subsequent key
//     processing or live event.
//  2. Live events (events.MessageReceived, events.OutgoingMessageStateChanged)
//     are filtered by chatID so a ping in another chat does not
//     re-render the open thread.
//  3. Keys (Up/Down/PgUp/PgDn) are forwarded to the viewport. After a
//     scroll lands at the top with hasMore=true, a pagination cmd is
//     dispatched alongside the viewport's own key cmd; symmetrically
//     for AtBottom + hasNewer (forward pagination after a jump).
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch typed := msg.(type) {
	case messagesLoadedMsg:
		return m.applyLoaded(typed)
	case messagesPaginationLoadedMsg:
		return m.applyPagination(typed)
	case messagesPaginationNewerLoadedMsg:
		return m.applyPaginationNewer(typed)
	case messagesLoadFailedMsg:
		if typed.chatID != m.chatID || typed.gen != m.loadGen {
			// Stale failure from a load that the model has already moved
			// on from (chat switch, search-jump bump). Dropping silently
			// preserves m.loading — clearing it here would let
			// applyPendingScroll (the search-jump fallback's deferred
			// scroll) fire before the real load completes, ScrollTo on
			// an empty slice no-ops, and pendingScroll is cleared, so
			// when the real load finally lands the user gets the chat
			// tail instead of the jump target.
			if m.log != nil {
				m.log.Debug("thread: dropping stale load failure",
					"chat_id", typed.chatID, "msg_gen", typed.gen,
					"current_gen", m.loadGen, "err", typed.err)
			}
			return m, nil
		}
		if m.log != nil {
			m.log.Warn("thread: repo load failed", "chat_id", typed.chatID, "err", typed.err)
		}
		m.loading = false
		return m, nil
	case events.MessageReceived:
		return m.applyIncoming(typed)
	case events.OutgoingMessageStateChanged:
		return m.applyOutgoingState(typed)
	case tea.KeyPressMsg:
		return m.handleKey(typed)
	}
	return m, nil
}

// applyLoaded replaces m.messages and re-renders. The viewport jumps
// to the bottom so the user sees the most recent message first — the
// natural place to start reading after picking a chat.
//
// The chatID + gen guards drop loads that no longer match the current
// model state. chatID alone catches cross-chat staleness; gen also
// catches same-chat staleness — e.g. a slow OpenChat fetch landing
// after a search-jump's LoadJumpWindow installed a new window for
// the same chat.
func (m Model) applyLoaded(msg messagesLoadedMsg) (Model, tea.Cmd) {
	if msg.chatID != m.chatID || msg.gen != m.loadGen {
		// Stale result from a prior chat or a superseded load for the
		// current chat (search-jump bumped gen). Drop it.
		return m, nil
	}
	m.messages = msg.messages
	m.hasMore = msg.hasMore
	// Initial load lands at the chat's tail by definition (offset=0
	// orders by date desc), so forward pagination is meaningless.
	m.hasNewer = false
	m.forwardCursorID = 0
	m.loading = false
	m.recomputeOldestID()
	m.viewport.SetContent(m.renderAll())
	m.viewport.GotoBottom()
	return m, nil
}

// applyPagination prepends older messages to the existing slice. The
// viewport YOffset is bumped by the height of the prepended content so
// the user's reading position stays anchored — without the bump,
// SetContent would shift everything down and the user would suddenly be
// looking at a different region of history.
//
// gen guard symmetric with applyLoaded — a still-in-flight scroll-up
// from before a search-jump must not prepend stray rows into the
// freshly-installed jump window.
func (m Model) applyPagination(msg messagesPaginationLoadedMsg) (Model, tea.Cmd) {
	if msg.chatID != m.chatID || msg.gen != m.loadGen {
		return m, nil
	}
	if len(msg.messages) == 0 {
		m.hasMore = msg.hasMore
		m.loading = false
		return m, nil
	}
	m.messages = append(append([]domain.Message{}, msg.messages...), m.messages...)
	m.hasMore = msg.hasMore
	m.loading = false
	m.recomputeOldestID()

	prevOffset := m.viewport.YOffset()
	m.viewport.SetContent(m.renderAll())
	// Re-anchor: count the lines we just prepended and shift the
	// viewport down by that many so the previously-visible top line
	// stays in view.
	added := countRenderedLines(msg.messages, m.viewport.Width())
	m.viewport.SetYOffset(prevOffset + added)
	return m, nil
}

// applyPaginationNewer appends the next forward page (rows with id >
// forwardCursorID) and updates the cursor. Called after the user
// scrolls to the bottom of a search-jump-loaded window — without
// forward pagination they would be stranded in the ±N slice unable
// to reach messages newer than the loaded window.
//
// Dedup is required because a live MessageReceived may have appended
// a row whose id is also picked up by the forward-page SQL (the page
// ranges past the live id). Without dedup the same row would render
// twice. Insertion preserves ASC order: any preexisting tail rows
// whose id falls inside [oldNewest..newPageMax] are dropped first
// (the page is authoritative), then the page is appended in place,
// then any leftover tail rows strictly above newPageMax are restored
// to keep the live event visible.
//
// hasNewer flips to false when the page returned fewer rows than
// limit (cursor caught up to the chat's tail) so AtBottom no longer
// schedules another forward fetch.
func (m Model) applyPaginationNewer(msg messagesPaginationNewerLoadedMsg) (Model, tea.Cmd) {
	if msg.chatID != m.chatID || msg.gen != m.loadGen {
		return m, nil
	}
	m.loading = false
	if len(msg.messages) == 0 {
		// Cursor caught up to the present — no more forward pagination
		// to do. Subsequent live appends update the visible tail
		// directly via applyIncoming.
		m.hasNewer = false
		return m, nil
	}

	pageIDs := make(map[int64]struct{}, len(msg.messages))
	var pageMax int64
	for _, msgRow := range msg.messages {
		pageIDs[msgRow.ID] = struct{}{}
		if msgRow.ID > pageMax {
			pageMax = msgRow.ID
		}
	}

	// Split existing slice at forwardCursorID: anything <= cursor stays
	// in place; rows above are the live tail that arrived during the
	// async window load (or earlier forward pages that overlap with
	// this one). Tail rows whose id is also in the new page are
	// dropped to avoid duplicates; tail rows strictly above pageMax
	// are kept so live events do not regress out of view.
	var head, tail []domain.Message
	for _, prev := range m.messages {
		if prev.ID <= m.forwardCursorID {
			head = append(head, prev)
			continue
		}
		if _, dup := pageIDs[prev.ID]; dup {
			continue
		}
		if prev.ID > pageMax {
			tail = append(tail, prev)
		}
		// rows with forwardCursorID < id <= pageMax that aren't in
		// the page would indicate an inconsistency; drop them silently
		// (pageIDs is the authoritative view of that range).
	}

	merged := append([]domain.Message(nil), head...)
	merged = append(merged, msg.messages...)
	merged = append(merged, tail...)
	m.messages = merged

	m.forwardCursorID = pageMax
	m.hasNewer = msg.hasMore
	m.recomputeOldestID()
	m.viewport.SetContent(m.renderAll())
	return m, nil
}

// applyIncoming appends a freshly-received message to the bottom of
// the thread. Sticky-scroll: if the user was already at the bottom,
// keep them there so the new message slides into view; if they had
// scrolled up to read older history, leave their position alone.
//
// If the incoming MessageID matches a serverID we are holding for an
// optimistic-UI entry (a sent message that the live pipeline is now
// echoing back), the optimistic row is dropped instead of double-
// rendering the same content.
func (m Model) applyIncoming(ev events.MessageReceived) (Model, tea.Cmd) {
	if ev.ChatID != m.chatID {
		return m, nil
	}
	wasAtBottom := m.viewport.AtBottom()
	if localID, ok := m.pendingServerIDs[ev.MessageID]; ok {
		m.outgoing = removeOutgoing(m.outgoing, localID)
		delete(m.pendingServerIDs, ev.MessageID)
	}
	m.messages = append(m.messages, domain.Message{
		ID:     ev.MessageID,
		ChatID: ev.ChatID,
		FromID: ev.FromID,
		Date:   ev.Date,
		Text:   ev.Text,
		Media:  ev.Media,
	})
	m.recomputeOldestID()
	m.viewport.SetContent(m.renderAll())
	if wasAtBottom {
		m.viewport.GotoBottom()
	}
	return m, nil
}

// applyOutgoingState routes optimistic-state transitions to the matching
// in-memory entry. The thread pane is the consumer that flips the
// pending → sent | failed pill in the visible message list.
//
// State machine:
//   - pending: no-op. The optimistic entry is inserted synchronously by
//     ApplyDispatched (driven by SendDispatchedMsg) which carries the
//     message body. The bus event for "pending" arrives via a separate
//     async path (forwardBusEvents → program.Send) and races with
//     SendDispatchedMsg; if it wins the race, an upsert here would
//     create an entry with empty text that flickers until ApplyDispatched
//     catches up. Treating Pending as a no-op eliminates the flicker —
//     by the time the next state transition (Sent | Failed) arrives,
//     ApplyDispatched has populated the entry.
//   - sent: flip the entry's State to Sent (RenderOptimistic strips the
//     [⏳] glyph in this state so the row reads as a normal sent
//     message), and stash a localID → serverID mapping so applyIncoming
//     can dedupe the server-echoed MessageReceived. The row is held
//     until that echo lands rather than being dropped immediately,
//     because for private 1:1 chats Telegram emits UpdateShortSentMessage
//     and never re-publishes a corresponding UpdateNewMessage —
//     dropping on Sent leaves the user staring at an empty thread until
//     a manual reload. Keeping the row preserves visual continuity in
//     both branches; applyIncoming still removes it before appending
//     when the echo eventually arrives, so no duplicate is ever shown.
//   - failed: keep the entry with the [✗] glyph so the user can see
//     why and decide whether to retype.
//
// Both terminal states record the localID in finalizedLocalIDs so a
// late SendDispatchedMsg (the inverted race against the bus event)
// cannot re-create a Pending row that no future event could resolve.
func (m Model) applyOutgoingState(ev events.OutgoingMessageStateChanged) (Model, tea.Cmd) {
	if ev.ChatID != m.chatID {
		return m, nil
	}
	switch ev.State {
	case events.OutgoingStatePending:
		return m, nil
	case events.OutgoingStateSent:
		if ev.ServerID > 0 {
			m.pendingServerIDs[ev.ServerID] = ev.LocalID
		}
		m.finalizedLocalIDs[ev.LocalID] = struct{}{}
		// If the optimistic row is already present (the common ordering
		// — ApplyDispatched ran first), flip its state in place. If it
		// is missing (the inverted race — Sent fired before
		// SendDispatchedMsg), the state machine is finished: the
		// finalizedLocalIDs guard tells ApplyDispatched not to re-create
		// it, and applyIncoming will dedupe the echo via
		// pendingServerIDs.
		if text := findOutgoingText(m.outgoing, ev.LocalID); text != "" {
			m.outgoing = upsertOutgoing(m.outgoing, OutgoingMessage{
				LocalID: ev.LocalID,
				ChatID:  ev.ChatID,
				Text:    text,
				State:   events.OutgoingStateSent,
			})
		}
	case events.OutgoingStateFailed:
		m.finalizedLocalIDs[ev.LocalID] = struct{}{}
		// Same caveat as Sent: only patch the row if it already exists.
		// A Failed event before SendDispatchedMsg would otherwise
		// produce a [✗]-glyph row with empty text — uglier than no row
		// at all. The finalizedLocalIDs guard suppresses the late
		// Dispatched insert, so the user sees nothing instead.
		if text := findOutgoingText(m.outgoing, ev.LocalID); text != "" {
			m.outgoing = upsertOutgoing(m.outgoing, OutgoingMessage{
				LocalID: ev.LocalID,
				ChatID:  ev.ChatID,
				Text:    text,
				State:   events.OutgoingStateFailed,
				Error:   ev.Error,
			})
		}
	}
	m.viewport.SetContent(m.renderAll())
	return m, nil
}

// ApplyDispatched inserts a fresh optimistic entry the moment the
// input pane reports the send was queued (SendDispatchedMsg). The
// SendService publishes its own pending event over the bus, but that
// races with the UI update — going through the dispatched message
// gives us a synchronous insert keyed by Text so applyOutgoingState
// can flip the state without a separate Text lookup.
//
// If the localID has already reached a terminal state on the bus
// (finalizedLocalIDs holds it) — the inverted race where Sent or
// Failed arrived before this method — the insert is skipped. Without
// the guard the row would be re-created in Pending state and stay
// stuck there because no future event would ever resolve it.
//
// Exported (capital A) because the app-level Update routes
// SendDispatchedMsg directly into this method instead of a generic
// broadcast — keeping the call site explicit makes the optimistic-UI
// flow easy to follow when reading app/update.go.
func (m Model) ApplyDispatched(localID string, chatID int64, text string) Model {
	if chatID != m.chatID {
		return m
	}
	if _, ok := m.finalizedLocalIDs[localID]; ok {
		return m
	}
	m.outgoing = upsertOutgoing(m.outgoing, OutgoingMessage{
		LocalID: localID,
		ChatID:  chatID,
		Text:    text,
		State:   events.OutgoingStatePending,
	})
	wasAtBottom := m.viewport.AtBottom()
	m.viewport.SetContent(m.renderAll())
	if wasAtBottom {
		m.viewport.GotoBottom()
	}
	return m
}

// upsertOutgoing replaces the entry whose LocalID matches msg.LocalID,
// or appends msg if no match exists. Order is preserved so the user
// sees pending rows in send-order.
func upsertOutgoing(list []OutgoingMessage, msg OutgoingMessage) []OutgoingMessage {
	for i, m := range list {
		if m.LocalID == msg.LocalID {
			list[i] = msg
			return list
		}
	}
	return append(list, msg)
}

// removeOutgoing drops the entry whose LocalID matches localID. Idempotent.
func removeOutgoing(list []OutgoingMessage, localID string) []OutgoingMessage {
	for i, m := range list {
		if m.LocalID == localID {
			return append(list[:i], list[i+1:]...)
		}
	}
	return list
}

// findOutgoingText returns the Text of the entry with localID, or
// empty if no match. Used by applyOutgoingState because the bus event
// only carries State + ServerID + Error — not the original message
// body. The body comes from the entry already inserted via
// applyDispatched.
func findOutgoingText(list []OutgoingMessage, localID string) string {
	for _, m := range list {
		if m.LocalID == localID {
			return m.Text
		}
	}
	return ""
}

// handleKey forwards the key to the viewport for scrolling, then
// detects "scrolled to top + hasMore" and dispatches older-page
// pagination, OR "scrolled to bottom + hasNewer" and dispatches the
// symmetric forward pagination. Both cmds are returned via tea.Batch
// so the viewport's own cmd (e.g. high-performance scrolling) is not
// dropped.
//
// Direction-aware ordering: when a search-jump window fits entirely in
// the viewport, AtTop and AtBottom are both true and the order of the
// shouldPaginate / shouldPaginateNewer checks decides which side wins.
// Without direction we'd always pull older history even when the user
// pressed a scroll-down key to walk toward the present — checking the
// down-direction predicate first preserves the "scroll down to reach
// the live tail" UX.
//
// Pagination uses cursor IDs (oldestID / forwardCursorID) instead of
// row offsets so concurrent applyIncoming (live messages landing
// between initial load and a scroll) cannot shift the "skip N rows"
// boundary and produce gaps in displayed history.
func (m Model) handleKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(k)

	if isScrollDownKey(k) {
		if m.shouldPaginateNewer() {
			m.loading = true
			return m, tea.Batch(cmd, paginateNewerCmd(m.repo, m.chatID, m.forwardCursorID, pageSize, m.loadGen))
		}
		if m.shouldPaginate() {
			m.loading = true
			return m, tea.Batch(cmd, paginateCmd(m.repo, m.chatID, m.oldestID, pageSize, m.loadGen))
		}
		return m, cmd
	}

	if m.shouldPaginate() {
		m.loading = true
		return m, tea.Batch(cmd, paginateCmd(m.repo, m.chatID, m.oldestID, pageSize, m.loadGen))
	}
	if m.shouldPaginateNewer() {
		m.loading = true
		return m, tea.Batch(cmd, paginateNewerCmd(m.repo, m.chatID, m.forwardCursorID, pageSize, m.loadGen))
	}
	return m, cmd
}

// isScrollDownKey reports whether k matches one of the viewport's
// down-direction bindings (down, j, pgdown, ctrl+d, d, space, f).
// Used by handleKey to disambiguate the small-window AtTop+AtBottom
// case where direction information is needed to pick the right
// pagination side.
func isScrollDownKey(k tea.KeyPressMsg) bool {
	switch k.String() {
	case "down", "j", "pgdown", "ctrl+d", "d", "space", "f":
		return true
	}
	return false
}

// shouldPaginate reports whether a scroll-to-top should trigger another
// page load. Guards against re-entrancy (loading == true), pagination on
// a placeholder model (repo == nil), and pagination from an empty
// thread (oldestID == 0 — no cursor anchor).
func (m Model) shouldPaginate() bool {
	if m.repo == nil {
		return false
	}
	if m.loading {
		return false
	}
	if !m.hasMore {
		return false
	}
	if m.oldestID == 0 {
		return false
	}
	return m.viewport.AtTop()
}

// shouldPaginateNewer is the AtBottom + hasNewer companion. Same
// re-entrancy / placeholder / cursor guards as shouldPaginate. Only
// fires after a search-jump has set forwardCursorID — outside of jump
// mode hasNewer stays false and this is a permanent no-op.
func (m Model) shouldPaginateNewer() bool {
	if m.repo == nil {
		return false
	}
	if m.loading {
		return false
	}
	if !m.hasNewer {
		return false
	}
	if m.forwardCursorID == 0 {
		return false
	}
	return m.viewport.AtBottom()
}

// LoadMore is the explicit-trigger variant of pagination. The wiring
// layer or tests can call it directly when a key-based scroll is too
// indirect to verify (e.g. unit tests with a fake repo where viewport
// height is artificial). Unlike the key path it does not require the
// viewport to be at the top — the caller is presumed to have decided
// it is the right moment. Re-entrancy is still guarded via the loading
// flag so two near-simultaneous LoadMore calls do not double-fetch.
func (m Model) LoadMore() (Model, tea.Cmd) {
	if m.repo == nil || !m.hasMore || m.loading || m.oldestID == 0 {
		return m, nil
	}
	m.loading = true
	return m, paginateCmd(m.repo, m.chatID, m.oldestID, pageSize, m.loadGen)
}

// LoadMoreNewer is the symmetric explicit-trigger forward pagination
// helper. Same contract as LoadMore — re-entrancy is guarded via the
// loading flag; placeholder / cursor / hasNewer guards make a no-op
// outside of post-jump state.
func (m Model) LoadMoreNewer() (Model, tea.Cmd) {
	if m.repo == nil || !m.hasNewer || m.loading || m.forwardCursorID == 0 {
		return m, nil
	}
	m.loading = true
	return m, paginateNewerCmd(m.repo, m.chatID, m.forwardCursorID, pageSize, m.loadGen)
}

// PaginateOlderIfAtTop fires backward pagination when the viewport is
// at the top edge AND backward pagination is reachable. No-op
// otherwise. The app's scroll-key handler calls this after the
// configurable ScrollUp chord (ctrl+b/pgup) — which intercepts the key
// before it reaches handleKey, so the in-Update pagination check would
// otherwise never run for that chord.
func (m Model) PaginateOlderIfAtTop() (Model, tea.Cmd) {
	if !m.shouldPaginate() {
		return m, nil
	}
	m.loading = true
	return m, paginateCmd(m.repo, m.chatID, m.oldestID, pageSize, m.loadGen)
}

// PaginateNewerIfAtBottom is the symmetric forward variant of
// PaginateOlderIfAtTop. Driven by the configurable ScrollDown chord
// (ctrl+f/pgdn) — same intercept story, same need for an explicit
// post-scroll trigger.
func (m Model) PaginateNewerIfAtBottom() (Model, tea.Cmd) {
	if !m.shouldPaginateNewer() {
		return m, nil
	}
	m.loading = true
	return m, paginateNewerCmd(m.repo, m.chatID, m.forwardCursorID, pageSize, m.loadGen)
}

// countRenderedLines is a worst-case estimate of how many terminal
// lines a slice of messages occupies once rendered. Used by
// applyPagination to bump YOffset by the right amount. The estimate is
// conservative: it counts the explicit '\n' separator plus a single
// header line plus a wrap of the body — close enough to keep the
// scroll position visually stable without actually re-measuring the
// rendered viewport content (which would be expensive).
func countRenderedLines(msgs []domain.Message, width int) int {
	if width <= 0 {
		width = 1
	}
	total := 0
	for i, msg := range msgs {
		// Header line + body wrap.
		total++
		bodyLines := 1
		if width > 2 {
			bodyLines = (lengthRunes(msg.Text)+width-3)/(width-2) + 1
		}
		if bodyLines < 1 {
			bodyLines = 1
		}
		total += bodyLines
		if i < len(msgs)-1 {
			total++ // blank-line separator
		}
	}
	return total
}

// lengthRunes returns the rune count of s. Used by countRenderedLines
// so the line estimate is correct for Cyrillic / CJK content.
func lengthRunes(s string) int {
	return len([]rune(s))
}
