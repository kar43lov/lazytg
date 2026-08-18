package thread

import (
	"strings"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
)

// threadWith builds a pane holding the given message texts, one message each,
// already loaded and sized.
func threadWith(t *testing.T, texts ...string) Model {
	t.Helper()
	msgs := make([]domain.Message, len(texts))
	for i, text := range texts {
		msgs[i] = domain.Message{
			ID:     int64(i + 1),
			ChatID: 7,
			FromID: 7,
			Date:   time.Date(2026, 8, 17, 19, 39, 0, 0, time.Local),
			Text:   text,
		}
	}
	m, _ := sized(New()).OpenChat(7)
	m, _ = m.Update(messagesLoadedMsg{chatID: 7, gen: m.loadGen, messages: msgs})
	return m
}

// lineOf returns the index of the first content line containing want.
func lineOf(t *testing.T, m Model, want string) int {
	t.Helper()
	content, _ := m.renderContent()
	for i, line := range strings.Split(content, "\n") {
		if strings.Contains(stripANSI(line), want) {
			return i
		}
	}
	t.Fatalf("no rendered line contains %q", want)
	return -1
}

// TestSelection_WithinOneMessageIsCharacterExact covers the everyday case:
// dragging across part of a line copies exactly that part, so a link or a code
// snippet can be lifted out of a longer message.
func TestSelection_WithinOneMessageIsCharacterExact(t *testing.T) {
	t.Parallel()

	m := threadWith(t, "alpha beta gamma")
	line := lineOf(t, m, "alpha beta gamma")
	content, spans := m.renderContent()

	sel := selection{
		anchor: cell{line: line, col: 6},  // start of "beta"
		cursor: cell{line: line, col: 10}, // end of "beta"
	}
	if got := selectionText(content, spans, sel); got != "beta" {
		t.Errorf("selected text = %q, want %q", got, "beta")
	}
}

// TestSelection_AcrossMessagesTakesThemWhole is the Telegram behaviour the user
// asked for: once a drag crosses a message boundary, both messages come along
// entirely — half of one and a third of the next is never what was meant.
func TestSelection_AcrossMessagesTakesThemWhole(t *testing.T) {
	t.Parallel()

	m := threadWith(t, "first message", "second message", "third message")
	content, spans := m.renderContent()
	firstLine := lineOf(t, m, "first message")
	secondLine := lineOf(t, m, "second message")

	// A deliberately partial drag: from the middle of the first message's text
	// to the middle of the second's.
	sel := selection{
		anchor: cell{line: firstLine, col: 6},
		cursor: cell{line: secondLine, col: 4},
	}
	got := selectionText(content, spans, sel)

	if !strings.Contains(got, "first message") {
		t.Errorf("the first message was not taken whole:\n%s", got)
	}
	if !strings.Contains(got, "second message") {
		t.Errorf("the second message was not taken whole:\n%s", got)
	}
	if strings.Contains(got, "third message") {
		t.Errorf("a message the drag never reached was included:\n%s", got)
	}
	if !strings.Contains(got, "[19:39]") {
		t.Errorf("whole-message selection should carry the header line:\n%s", got)
	}
}

// TestSelection_BackwardsDragIsTheSameRange: dragging up must select the same
// text as dragging down over it.
func TestSelection_BackwardsDragIsTheSameRange(t *testing.T) {
	t.Parallel()

	m := threadWith(t, "alpha beta gamma")
	line := lineOf(t, m, "alpha beta gamma")
	content, spans := m.renderContent()

	forward := selectionText(content, spans, selection{
		anchor: cell{line: line, col: 6}, cursor: cell{line: line, col: 10},
	})
	backward := selectionText(content, spans, selection{
		anchor: cell{line: line, col: 10}, cursor: cell{line: line, col: 6},
	})
	if forward != backward {
		t.Errorf("direction changed the selection: %q vs %q", forward, backward)
	}
}

// TestSelection_ClampsOutsideTheContent covers a drag that runs off the pane:
// the pointer keeps reporting positions past the last line and past the end of
// short lines, and neither may panic or select phantom text.
func TestSelection_ClampsOutsideTheContent(t *testing.T) {
	t.Parallel()

	m := threadWith(t, "only message")
	content, spans := m.renderContent()

	got := selectionText(content, spans, selection{
		anchor: cell{line: -5, col: -20},
		cursor: cell{line: 9999, col: 9999},
	})
	if !strings.Contains(got, "only message") {
		t.Errorf("a drag past both ends should cover the whole thread, got:\n%s", got)
	}
}

// TestApplySelection_HighlightsWithoutChangingText pins that the highlight is
// purely visual: the same characters are on screen, only styled.
func TestApplySelection_HighlightsWithoutChangingText(t *testing.T) {
	t.Parallel()

	m := threadWith(t, "alpha beta gamma")
	line := lineOf(t, m, "alpha beta gamma")
	content, spans := m.renderContent()
	sel := selection{anchor: cell{line: line, col: 6}, cursor: cell{line: line, col: 10}}

	highlighted := applySelection(content, spans, sel)
	if stripANSI(highlighted) != stripANSI(content) {
		t.Errorf("selection changed the visible text:\n--- before ---\n%s\n--- after ---\n%s",
			stripANSI(content), stripANSI(highlighted))
	}
	if highlighted == content {
		t.Error("selection produced no styling at all")
	}
}

// TestSelectMessageAt_TakesTheWholeMessage covers the double-click gesture.
func TestSelectMessageAt_TakesTheWholeMessage(t *testing.T) {
	t.Parallel()

	m := threadWith(t, "first message", "second message")
	line := lineOf(t, m, "second message")
	// Pane row = content line - scroll offset + the header row.
	paneY := line - m.viewport.YOffset() + threadHeaderRows

	updated, text, ok := m.SelectMessageAt(2, paneY)
	if !ok {
		t.Fatal("double click on a message row reported no hit")
	}
	if !strings.Contains(text, "second message") {
		t.Errorf("copied text does not contain the message: %q", text)
	}
	if strings.Contains(text, "first message") {
		t.Errorf("copied text spilled into the neighbouring message: %q", text)
	}
	if !updated.HasSelection() {
		t.Error("double click left nothing highlighted")
	}
}

// TestSelectMessageAt_BetweenMessagesIsAMiss keeps the blank separator rows
// inert: a double click there would otherwise copy an arbitrary neighbour.
func TestSelectMessageAt_BetweenMessagesIsAMiss(t *testing.T) {
	t.Parallel()

	m := threadWith(t, "first message", "second message")
	gap := lineOf(t, m, "second message") - 2 // the blank row between blocks
	paneY := gap - m.viewport.YOffset() + threadHeaderRows

	if _, _, ok := m.SelectMessageAt(2, paneY); ok {
		t.Error("the blank row between messages reported a hit")
	}
}

// TestSelection_ClearedByStartingANewOne guards against a stale highlight
// surviving the next click.
func TestSelection_ClearedByStartingANewOne(t *testing.T) {
	t.Parallel()

	m := threadWith(t, "alpha beta gamma")
	m, _ = m.StartSelection(2, threadHeaderRows)
	if !m.HasSelection() {
		t.Fatal("StartSelection did not record a selection")
	}
	if got := m.ClearSelection(); got.HasSelection() {
		t.Error("ClearSelection left the highlight in place")
	}
}

// TestExtendSelection_WithoutStartIsNoop: a drag that began in another pane
// must not start selecting text the moment it crosses into the thread.
func TestExtendSelection_WithoutStartIsNoop(t *testing.T) {
	t.Parallel()

	m := threadWith(t, "alpha")
	if m.ExtendSelection(4, 2).HasSelection() {
		t.Error("ExtendSelection created a selection out of nothing")
	}
}

// TestDragSnapshot_ReleasedContentIsFresh pins the bounded trade-off behind the
// drag snapshot: while the button is down the body is not re-rendered (that
// costs 3.1ms per motion event on a full page), so a message that arrives
// mid-drag becomes visible when the drag ends — and it must actually become
// visible, not stay hidden behind a stale snapshot.
func TestDragSnapshot_ReleasedContentIsFresh(t *testing.T) {
	t.Parallel()

	m := threadWith(t, "first message")
	m, _ = m.StartSelection(2, threadHeaderRows)

	m, _ = m.Update(events.MessageReceived{
		ChatID: 7, MessageID: 99, FromID: 7,
		Date: time.Date(2026, 8, 17, 19, 45, 0, 0, time.Local),
		Text: "arrived mid-drag",
	})

	if strings.Contains(stripANSI(m.View()), "arrived mid-drag") {
		t.Error("precondition changed: the snapshot is supposed to hold the body still during a drag")
	}

	m, _ = m.FinishSelection()
	if !strings.Contains(stripANSI(m.View()), "arrived mid-drag") {
		t.Error("the message that arrived during the drag never appeared after it ended")
	}
}

// TestDragSnapshot_ClearedBetweenDrags guards the obvious way a cache goes
// wrong: a second drag must render the thread as it is now, not as it was when
// the first drag started.
func TestDragSnapshot_ClearedBetweenDrags(t *testing.T) {
	t.Parallel()

	m := threadWith(t, "first message")
	m, _ = m.StartSelection(2, threadHeaderRows)
	m, _ = m.FinishSelection()

	m, _ = m.Update(events.MessageReceived{
		ChatID: 7, MessageID: 99, FromID: 7,
		Date: time.Date(2026, 8, 17, 19, 45, 0, 0, time.Local),
		Text: "between drags",
	})
	m, _ = m.StartSelection(2, threadHeaderRows)

	if !strings.Contains(stripANSI(m.View()), "between drags") {
		t.Error("the second drag rendered a stale snapshot from the first")
	}
}

// TestSelection_WideRunesSurviveTheCut covers the characters a chat is full of:
// emoji and CJK occupy two cells, and mouse columns can land in the middle of
// one. Cutting on cell boundaries with a byte-oriented slice would corrupt the
// text or the escape sequences around it; this asserts neither happens.
func TestSelection_WideRunesSurviveTheCut(t *testing.T) {
	t.Parallel()

	const text = "смотри 🎉 вот 日本語 текст"
	m := threadWith(t, text)
	line := lineOf(t, m, "смотри")
	content, spans := m.renderContent()

	for col := 0; col <= 24; col++ {
		sel := selection{
			anchor: cell{line: line, col: col},
			cursor: cell{line: line, col: col + 4},
		}
		got := selectionText(content, spans, sel)
		if strings.ContainsRune(got, '�') {
			t.Fatalf("col %d produced a replacement character: %q", col, got)
		}
		if strings.Contains(got, "\x1b") {
			t.Fatalf("col %d leaked an escape sequence into the clipboard: %q", col, got)
		}
		highlighted := applySelection(content, spans, sel)
		if stripANSI(highlighted) != stripANSI(content) {
			t.Fatalf("col %d changed the visible text", col)
		}
	}
}

// TestSelection_MediaBadgeLineIsSelectable pins the same for a rendered media
// badge, which is where emoji actually appear in production.
func TestSelection_MediaBadgeLineIsSelectable(t *testing.T) {
	t.Parallel()

	m, _ := sized(New()).OpenChat(7)
	m, _ = m.Update(messagesLoadedMsg{chatID: 7, gen: m.loadGen, messages: []domain.Message{{
		ID: 1, ChatID: 7, FromID: 7,
		Date:  time.Date(2026, 8, 17, 17, 5, 0, 0, time.Local),
		Text:  "смотри видео",
		Media: &domain.MediaInfo{Kind: domain.MediaKindPhoto, Filename: "video.mp4", Size: 7_500_000},
	}}})

	content, spans := m.renderContent()
	line := lineOf(t, m, "video.mp4")
	got := selectionText(content, spans, selection{
		anchor: cell{line: line, col: 0},
		cursor: cell{line: line, col: 40},
	})
	if strings.Contains(got, "\x1b") {
		t.Errorf("escape sequences leaked into the copied badge: %q", got)
	}
	if !strings.Contains(got, "video.mp4") {
		t.Errorf("the badge text was not copied: %q", got)
	}
}

// TestSelection_PaneHeaderRowIsInert pins the one row that is chrome rather
// than content. Mapped through cellAt the title row lands on the line above the
// viewport, which clamps onto the first message — so pressing the word "Thread"
// started a selection there, and a double click on it copied a message the
// pointer was nowhere near. Both engines in review flagged it independently.
func TestSelection_PaneHeaderRowIsInert(t *testing.T) {
	t.Parallel()

	m := threadWith(t, "first message", "second message")

	if _, started := m.StartSelection(4, 0); started {
		t.Error("a press on the pane header started a text selection")
	}
	if _, _, ok := m.SelectMessageAt(4, 0); ok {
		t.Error("a double click on the pane header selected a message")
	}
	// The row directly below it is content and must still work.
	if _, started := m.StartSelection(4, threadHeaderRows); !started {
		t.Error("the first body row refused to start a selection")
	}
}

// TestSelection_DroppedWhenTheChatChanges covers the keyboard paths — Enter,
// ctrl+tab, the palette — which never touched the mouse code that used to be
// the only thing clearing a highlight. A selection is a range of line/column
// cells with no tie to the messages under it, so carrying it into another
// conversation lights up whatever happens to fall inside the range.
func TestSelection_DroppedWhenTheChatChanges(t *testing.T) {
	t.Parallel()

	m := threadWith(t, "first message", "second message")
	m, _ = m.StartSelection(2, threadHeaderRows)
	m = m.ExtendSelection(12, threadHeaderRows+3)
	if !m.HasSelection() {
		t.Fatal("precondition: nothing was selected to begin with")
	}

	m, _ = m.OpenChat(9)
	if m.HasSelection() {
		t.Error("the highlight survived a chat switch and now covers another conversation")
	}
	if m.SelectionText() != "" {
		t.Error("the stale range still yields text after the chat changed")
	}
}
