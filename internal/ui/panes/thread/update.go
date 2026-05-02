package thread

import (
	tea "charm.land/bubbletea/v2"

	"github.com/pgmac/lazytg/internal/core/domain"
	"github.com/pgmac/lazytg/internal/core/events"
)

// Update routes incoming messages. The order matters:
//  1. Domain payloads (messagesLoadedMsg / messagesPaginationLoadedMsg /
//     messagesLoadFailedMsg) run first so a repo refresh is applied
//     before any subsequent key processing or live event.
//  2. Live events (events.MessageReceived, events.OutgoingMessageStateChanged)
//     are filtered by chatID so a ping in another chat does not
//     re-render the open thread.
//  3. Keys (Up/Down/PgUp/PgDn) are forwarded to the viewport. After a
//     scroll lands at the top with hasMore=true, a pagination cmd is
//     dispatched alongside the viewport's own key cmd.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch typed := msg.(type) {
	case messagesLoadedMsg:
		return m.applyLoaded(typed)
	case messagesPaginationLoadedMsg:
		return m.applyPagination(typed)
	case messagesLoadFailedMsg:
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
func (m Model) applyLoaded(msg messagesLoadedMsg) (Model, tea.Cmd) {
	if msg.chatID != m.chatID {
		// Stale result from a previously-open chat. Drop it; the model
		// already moved on.
		return m, nil
	}
	m.messages = msg.messages
	m.hasMore = msg.hasMore
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
func (m Model) applyPagination(msg messagesPaginationLoadedMsg) (Model, tea.Cmd) {
	if msg.chatID != m.chatID {
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

// applyIncoming appends a freshly-received message to the bottom of
// the thread. Sticky-scroll: if the user was already at the bottom,
// keep them there so the new message slides into view; if they had
// scrolled up to read older history, leave their position alone.
func (m Model) applyIncoming(ev events.MessageReceived) (Model, tea.Cmd) {
	if ev.ChatID != m.chatID {
		return m, nil
	}
	wasAtBottom := m.viewport.AtBottom()
	m.messages = append(m.messages, domain.Message{
		ID:     ev.MessageID,
		ChatID: ev.ChatID,
		FromID: ev.FromID,
		Date:   ev.Date,
		Text:   ev.Text,
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
// pending → sent | failed pill in the visible message list. Stage 2
// Task 8 wires the dispatch but the actual optimistic-message tracking
// table is owned by Task 11; for now we filter by ChatID and forward
// to a no-op that the wiring layer can extend.
func (m Model) applyOutgoingState(ev events.OutgoingMessageStateChanged) (Model, tea.Cmd) {
	if ev.ChatID != m.chatID {
		return m, nil
	}
	if m.log != nil {
		m.log.Debug("thread: outgoing state",
			"chat_id", ev.ChatID, "local_id", ev.LocalID, "state", ev.State)
	}
	return m, nil
}

// handleKey forwards the key to the viewport for scrolling, then
// detects "scrolled to top + hasMore" and dispatches pagination. Both
// cmds are returned via tea.Batch so the viewport's own cmd (e.g. high-
// performance scrolling) is not dropped.
func (m Model) handleKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(k)

	if m.shouldPaginate() {
		m.loading = true
		return m, tea.Batch(cmd, paginateCmd(m.repo, m.chatID, len(m.messages), pageSize))
	}
	return m, cmd
}

// shouldPaginate reports whether a scroll-to-top should trigger another
// page load. Guards against re-entrancy (loading == true) and against
// pagination on a placeholder model (repo == nil).
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
	return m.viewport.AtTop()
}

// LoadMore is the explicit-trigger variant of pagination. The wiring
// layer or tests can call it directly when a key-based scroll is too
// indirect to verify (e.g. unit tests with a fake repo where viewport
// height is artificial). Unlike the key path it does not require the
// viewport to be at the top — the caller is presumed to have decided
// it is the right moment. Re-entrancy is still guarded via the loading
// flag so two near-simultaneous LoadMore calls do not double-fetch.
func (m Model) LoadMore() (Model, tea.Cmd) {
	if m.repo == nil || !m.hasMore || m.loading {
		return m, nil
	}
	m.loading = true
	return m, paginateCmd(m.repo, m.chatID, len(m.messages), pageSize)
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
