package thread

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
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
	case events.MessagesDeleted:
		return m.applyDeleted(typed)
	case events.OutgoingMessageStateChanged:
		return m.applyOutgoingState(typed)
	case tea.KeyPressMsg:
		return m.handleKey(typed)
	case tea.MouseWheelMsg:
		return m.handleWheel(typed)
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
	// The boundary is worked out here, once, and then left alone:
	// acknowledging the chat clears the count within a second of opening
	// it, and a divider recomputed from that would vanish while the reader
	// was still looking at it.
	m = m.locateUnread()
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
	if ev.Edited {
		// The row is replaced where it stands, the way a local edit is. A
		// message not loaded here is left alone: it will come back rewritten
		// when its page does, and inserting it now would put it in a window
		// it does not belong to.
		return m.ApplyEdit(ev.MessageID, ev.Text, ev.Entities, ev.EditDate), nil
	}
	wasAtBottom := m.viewport.AtBottom()
	if localID, ok := m.pendingServerIDs[ev.MessageID]; ok {
		m.outgoing = removeOutgoing(m.outgoing, localID)
		delete(m.pendingServerIDs, ev.MessageID)
	}
	if i := m.indexOfMessage(ev.MessageID); i >= 0 {
		// Already here — the same message by another path. Replace rather
		// than append: two rows with one id is the one thing worse than
		// a late arrival.
		msgs := make([]domain.Message, len(m.messages))
		copy(msgs, m.messages)
		msgs[i] = messageFromEvent(ev)
		m.messages = msgs
		m.viewport.SetContent(m.renderAll())
		return m, nil
	}
	m.messages = insertByDate(m.messages, messageFromEvent(ev))
	m.recomputeOldestID()
	m.viewport.SetContent(m.renderAll())
	if wasAtBottom {
		m.viewport.GotoBottom()
	}
	return m, nil
}

// applyDeleted removes messages Telegram reported as deleted from the open
// thread, so a deletion made on another device disappears here too instead of
// lingering until the chat is reopened.
//
// A zero ChatID means the update named no chat, which is how Telegram reports
// deletions in private chats and basic groups: the ids identify the messages
// across that entire id space. A channel numbers its own messages from one, so
// the same id exists there and means something else — applying the update to
// an open channel would delete an unrelated message from view. Hence the kind
// check rather than a plain id match.
//
// The kind must be positively known, not merely "not a channel": it is empty
// until SetDirectory runs, and treating unknown as private would let a
// peerless deletion erase a live row from a channel during that window.
func (m Model) applyDeleted(ev events.MessagesDeleted) (Model, tea.Cmd) {
	switch {
	case ev.ChatID != 0 && ev.ChatID != m.chatID:
		return m, nil
	case ev.ChatID == 0 && m.chatKind != domain.ChatTypePrivate && m.chatKind != domain.ChatTypeGroup:
		return m, nil
	case len(ev.MessageIDs) == 0 || len(m.messages) == 0:
		return m, nil
	}

	doomed := make(map[int64]struct{}, len(ev.MessageIDs))
	for _, id := range ev.MessageIDs {
		doomed[id] = struct{}{}
	}
	kept := make([]domain.Message, 0, len(m.messages))
	for _, msg := range m.messages {
		if _, gone := doomed[msg.ID]; gone {
			continue
		}
		kept = append(kept, msg)
	}
	if len(kept) == len(m.messages) {
		return m, nil
	}

	wasAtBottom := m.viewport.AtBottom()
	m.messages = kept
	m.recomputeOldestID()
	m.viewport.SetContent(m.renderAll())
	if wasAtBottom {
		m.viewport.GotoBottom()
	}
	return m, nil
}

// insertByDate places msg into a slice held in ascending (Date, ID) order.
// It scans back from the tail, so the ordinary case — a message newer than
// everything on screen — costs one comparison and an append.
//
// The thread used to append unconditionally, which rendered messages in
// arrival order rather than chronological order. That held while one producer
// existed; since Stage 4 there are two (the live update path and the polling
// fallback), they race, and a reconnect delivers a backlog through whichever
// wins. Observed on 19.08.2026: two messages sent seconds apart during an
// outage appeared swapped once the link came back.
//
// ID breaks ties because Telegram stamps whole seconds: two messages sent in
// the same second are ordered by the server's own sequence, not by ours.
//
// This ordering leans on an invariant worth stating, because the pagination
// cursors depend on it: within one chat Telegram hands out ids in ascending
// order of date, so (Date, ID) and ID alone produce the same sequence, and
// ties resolve by id either way. Editing a message does not change its date
// (edit_date is a separate field) and a forward is a new message with a new
// id and a new date, so neither breaks it. If that ever stops holding, this
// function and the id-based cursors in repo.go go out of step and scroll-up
// can skip a row — change both together.
func insertByDate(msgs []domain.Message, msg domain.Message) []domain.Message {
	i := len(msgs)
	for i > 0 && sortsAfter(msgs[i-1], msg) {
		i--
	}
	msgs = append(msgs, domain.Message{})
	copy(msgs[i+1:], msgs[i:])
	msgs[i] = msg
	return msgs
}

// sortsAfter reports whether a belongs after b in the thread.
func sortsAfter(a, b domain.Message) bool {
	if !a.Date.Equal(b.Date) {
		return a.Date.After(b.Date)
	}
	return a.ID > b.ID
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
		m.finalizedLocalIDs[ev.LocalID] = struct{}{}
		if ev.ServerID > 0 && m.indexOfMessage(ev.ServerID) >= 0 {
			// The echo got here first — it can, since the sender announces
			// the message before the send state is written — so the real
			// row is already on screen and the optimistic one is done.
			m.outgoing = removeOutgoing(m.outgoing, ev.LocalID)
			m.viewport.SetContent(m.renderAll())
			return m, nil
		}
		if ev.ServerID > 0 {
			m.pendingServerIDs[ev.ServerID] = ev.LocalID
		}
		// If the optimistic row is already present (the common ordering
		// — ApplyDispatched ran first), flip its state in place. If it
		// is missing (the inverted race — Sent fired before
		// SendDispatchedMsg), the state machine is finished: the
		// finalizedLocalIDs guard tells ApplyDispatched not to re-create
		// it, and applyIncoming will dedupe the echo via
		// pendingServerIDs.
		if row, ok := findOutgoing(m.outgoing, ev.LocalID); ok && row.Text != "" {
			row.ChatID = ev.ChatID
			row.State = events.OutgoingStateSent
			m.outgoing = upsertOutgoing(m.outgoing, row)
		}
	case events.OutgoingStateFailed:
		m.finalizedLocalIDs[ev.LocalID] = struct{}{}
		// Same caveat as Sent: only patch the row if it already exists.
		// A Failed event before SendDispatchedMsg would otherwise
		// produce a [✗]-glyph row with empty text — uglier than no row
		// at all. The finalizedLocalIDs guard suppresses the late
		// Dispatched insert, so the user sees nothing instead.
		if row, ok := findOutgoing(m.outgoing, ev.LocalID); ok && row.Text != "" {
			row.ChatID = ev.ChatID
			row.State = events.OutgoingStateFailed
			row.Error = ev.Error
			m.outgoing = upsertOutgoing(m.outgoing, row)
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
		SentAt:  time.Now(),
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
func findOutgoing(list []OutgoingMessage, localID string) (OutgoingMessage, bool) {
	for _, m := range list {
		if m.LocalID == localID {
			return m, true
		}
	}
	return OutgoingMessage{}, false
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
// The line keys move the message cursor rather than the viewport. The
// pane's unit is a message, not a row: everything the user does here
// names one — reply to it, save its attachment, open it — so a cursor
// that walked rows would leave them nothing to name. Page keys, the
// configurable scroll chords and the wheel still scroll, because those
// are for covering distance rather than for pointing at something.
func (m Model) handleKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	switch k.String() {
	case "up", "k":
		return m.MoveCursor(-1).paginateAfterScroll(nil, false)
	case "down", "j":
		return m.MoveCursor(1).paginateAfterScroll(nil, true)
	case " ", "space":
		// Space marks the message under the cursor. It is the gesture a
		// file manager uses for the same job, and it is free here: the
		// composer is a separate pane, so nothing in the thread is
		// typing.
		return m.ToggleMark(), nil
	case "esc":
		// One key clears both kinds of "never mind" — a dragged text
		// selection and a set of marks — because to the user they are
		// one state: something is picked out and they no longer want it.
		return m.ClearMarks().ClearSelection(), nil
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(k)
	return m.paginateAfterScroll(cmd, isScrollDownKey(k))
}

// handleWheel is handleKey's mouse twin. The viewport handles MouseWheelMsg
// itself (MouseWheelEnabled is on by default in bubbles), so the only work here
// is the pagination decision — which must be the same one keyboard scrolling
// makes, or the wheel would stop at the loaded window's edge and look like the
// history simply ends.
func (m Model) handleWheel(w tea.MouseWheelMsg) (Model, tea.Cmd) {
	// Only the vertical wheel scrolls this pane. A sideways nudge is not "up":
	// treating it as one asks for another page of older history every time a
	// trackpad drifts horizontally at the top of the thread. The app filters
	// horizontal wheels today, so this guards the pane's own contract rather
	// than a live path.
	if w.Button != tea.MouseWheelUp && w.Button != tea.MouseWheelDown {
		return m, nil
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(w)
	return m.paginateAfterScroll(cmd, w.Button == tea.MouseWheelDown)
}

// paginateAfterScroll decides whether a scroll that just happened should pull
// another page, and in which direction. down disambiguates the small-window
// case where the viewport reports AtTop and AtBottom at once: without it the
// older side always wins and scrolling down through a jump window stalls.
func (m Model) paginateAfterScroll(cmd tea.Cmd, down bool) (Model, tea.Cmd) {
	if down {
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

// indexOfMessage is the position of the message with id in the loaded
// window, or -1.
func (m Model) indexOfMessage(id int64) int {
	for i := range m.messages {
		if m.messages[i].ID == id {
			return i
		}
	}
	return -1
}

// messageFromEvent is the stored shape of a live arrival. One place, so a
// field added to the event is added here and nowhere else forgets it.
func messageFromEvent(ev events.MessageReceived) domain.Message {
	return domain.Message{
		ID:        ev.MessageID,
		ChatID:    ev.ChatID,
		FromID:    ev.FromID,
		Date:      ev.Date,
		Text:      ev.Text,
		Media:     ev.Media,
		ReplyTo:   ev.ReplyTo,
		Reactions: ev.Reactions,
		Entities:  ev.Entities,
		EditDate:  ev.EditDate,
		Outgoing:  ev.Outgoing,
	}
}
