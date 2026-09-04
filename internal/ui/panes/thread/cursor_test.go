package thread

import (
	"strings"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
)

// threadWithMedia builds a pane holding messages, attaching media to the
// ids listed in withMedia so tests can place attachments precisely.
func threadWithMedia(t *testing.T, count int, withMedia ...int64) Model {
	t.Helper()
	carries := make(map[int64]bool, len(withMedia))
	for _, id := range withMedia {
		carries[id] = true
	}
	msgs := make([]domain.Message, count)
	for i := range msgs {
		id := int64(i + 1)
		msgs[i] = domain.Message{
			ID:     id,
			ChatID: 7,
			FromID: 7,
			Date:   time.Date(2026, 9, 4, 12, 0, i, 0, time.Local),
			Text:   "message " + string(rune('a'+i)),
		}
		if carries[id] {
			msgs[i].Media = &domain.MediaInfo{
				Kind:     domain.MediaKindVideoNote,
				FileID:   1000 + id,
				Filename: "video_note.mp4",
				Size:     1 << 20,
				Duration: 7,
			}
		}
	}
	m, _ := sized(New()).OpenChat(7)
	m, _ = m.Update(messagesLoadedMsg{chatID: 7, gen: m.loadGen, messages: msgs})
	return m
}

// An unplaced cursor means the newest message, so every chord that acts
// on "the message" keeps doing what it did before the cursor existed.
func TestCursor_UnplacedResolvesToTheNewestMessage(t *testing.T) {
	t.Parallel()

	m := threadWithMedia(t, 4)
	if got := m.Cursor(); got != 0 {
		t.Fatalf("a fresh pane must have no cursor placed, got %d", got)
	}
	msg, ok := m.CursorMessage()
	if !ok || msg.ID != 4 {
		t.Fatalf("CursorMessage() = %d/%v, want the newest message (4)", msg.ID, ok)
	}
}

// The first "up" selects the newest message rather than skipping past
// it: a user who presses up once expects to be on the last message, not
// on the one before it.
func TestCursor_FirstStepUpLandsOnTheNewestMessage(t *testing.T) {
	t.Parallel()

	m := threadWithMedia(t, 4).MoveCursor(-1)
	if got := m.Cursor(); got != 4 {
		t.Fatalf("first up = message %d, want 4", got)
	}
	if got := m.MoveCursor(-1).Cursor(); got != 3 {
		t.Fatalf("second up = message %d, want 3", got)
	}
}

// Clamping, not wrapping: running off either end stays put.
func TestCursor_ClampsAtBothEnds(t *testing.T) {
	t.Parallel()

	m := threadWithMedia(t, 3)
	for i := 0; i < 10; i++ {
		m = m.MoveCursor(-1)
	}
	if got := m.Cursor(); got != 1 {
		t.Fatalf("cursor after running off the top = %d, want the oldest (1)", got)
	}
	for i := 0; i < 10; i++ {
		m = m.MoveCursor(1)
	}
	if got := m.Cursor(); got != 3 {
		t.Fatalf("cursor after running off the bottom = %d, want the newest (3)", got)
	}
}

// The cursor is a message id, not a row. A message arriving while the
// user is reading further up must not drag the cursor onto a different
// message — the same defect the chat list had, where an index-based
// highlight came to point at whatever had been promoted under it.
func TestCursor_StaysOnItsMessageWhenNewOnesArrive(t *testing.T) {
	t.Parallel()

	m := threadWithMedia(t, 3)
	m = m.MoveCursor(-1).MoveCursor(-1) // newest, then one above: message 2
	if got := m.Cursor(); got != 2 {
		t.Fatalf("precondition: cursor = %d, want 2", got)
	}

	m, _ = m.Update(events.MessageReceived{
		ChatID:    7,
		MessageID: 4,
		FromID:    7,
		Date:      time.Date(2026, 9, 4, 12, 5, 0, 0, time.Local),
		Text:      "arrived while reading",
	})

	if got := m.Cursor(); got != 2 {
		t.Fatalf("cursor moved to %d when a message arrived; it must stay on 2", got)
	}
	msg, ok := m.CursorMessage()
	if !ok || msg.Text != "message b" {
		t.Fatalf("cursor points at %q, want the message it was on", msg.Text)
	}
}

// Opening another chat resets the cursor. Message ids are unique per
// chat, not across chats, so carrying one over would silently point at a
// different message rather than at nothing.
func TestCursor_ResetsWhenAnotherChatIsOpened(t *testing.T) {
	t.Parallel()

	m := threadWithMedia(t, 3).MoveCursor(-1).MoveCursor(-1)
	if got := m.Cursor(); got != 2 {
		t.Fatalf("precondition: cursor = %d, want 2", got)
	}
	m, _ = m.OpenChat(9)
	if got := m.Cursor(); got != 0 {
		t.Fatalf("cursor survived a chat switch as %d, want it cleared", got)
	}
}

// The media chords act on the attachment at or above the cursor. With
// the cursor untouched that is the newest attachment — what Ctrl-D
// always did — and once moved it is the one the user is looking at.
func TestCursor_MediaTargetIsTheAttachmentAtOrAboveIt(t *testing.T) {
	t.Parallel()

	// Attachments on messages 2 and 4, of five.
	m := threadWithMedia(t, 5, 2, 4)

	target, ok := m.MediaTarget()
	if !ok || target.ID != 4 {
		t.Fatalf("untouched cursor targets message %d/%v, want the newest attachment (4)", target.ID, ok)
	}

	// Cursor onto message 3: the attachment above it is 2, not 4.
	m = m.SetCursor(3)
	target, ok = m.MediaTarget()
	if !ok || target.ID != 2 {
		t.Fatalf("cursor on 3 targets message %d/%v, want the attachment above it (2)", target.ID, ok)
	}

	// Cursor onto message 1, which has nothing above it.
	m = m.SetCursor(1)
	if target, ok := m.MediaTarget(); ok {
		t.Fatalf("cursor on the oldest message found attachment %d, want none", target.ID)
	}
}

// A thread with no attachments at all reports none rather than the
// newest message, so the chord stays a quiet no-op.
func TestCursor_MediaTargetIsAbsentWithoutAttachments(t *testing.T) {
	t.Parallel()

	if target, ok := threadWithMedia(t, 3).MediaTarget(); ok {
		t.Fatalf("found attachment %d in a thread with none", target.ID)
	}
}

// SetCursor ignores an id the pane does not hold, so a stale click or a
// jump to a since-deleted message cannot strand the cursor out of sight.
func TestCursor_SetIgnoresAnIDTheThreadDoesNotHold(t *testing.T) {
	t.Parallel()

	m := threadWithMedia(t, 3).SetCursor(2)
	m = m.SetCursor(999)
	if got := m.Cursor(); got != 2 {
		t.Fatalf("cursor = %d after setting an unknown id, want it left on 2", got)
	}
}

// The marker is drawn on the message the cursor points at, and on
// exactly one message.
func TestCursor_MarkerIsDrawnOnceOnTheCursorMessage(t *testing.T) {
	t.Parallel()

	m := threadWithMedia(t, 3).SetCursor(2)
	content, spans := m.renderContent()
	lines := strings.Split(content, "\n")

	var marked []int
	for i, line := range lines {
		if strings.Contains(stripANSI(line), cursorMark) {
			marked = append(marked, i)
		}
	}
	if len(marked) != 1 {
		t.Fatalf("cursor marker appears on %d lines, want exactly 1", len(marked))
	}
	idx := spanIndexAt(spans, marked[0])
	if idx < 0 || spans[idx].id != 2 {
		t.Fatalf("marker sits on message %d, want 2", spans[idx].id)
	}
}

// A click on the media badge line opens that attachment; a click on any
// other line of the same message does not, so a message with a photo can
// still be pointed at and selected like any other.
func TestCursor_MediaClickOnlyCountsOnTheBadgeLine(t *testing.T) {
	t.Parallel()

	m := threadWithMedia(t, 3, 2)
	_, spans := m.renderContent()

	var span blockSpan
	for _, s := range spans {
		if s.id == 2 {
			span = s
		}
	}
	if span.mediaLine < 0 {
		t.Fatalf("message 2 carries media but the renderer reported no badge line")
	}

	// Pane-local y for a content line: the inverse of cellAt, with the
	// viewport at the top.
	yFor := func(line int) int { return line - m.viewport.YOffset() + threadHeaderRows }

	if _, ok := m.MediaClickAt(0, yFor(span.mediaLine)); !ok {
		t.Fatalf("a click on the badge line did not resolve to the attachment")
	}
	if _, ok := m.MediaClickAt(0, yFor(span.start)); ok {
		t.Fatalf("a click on the header line opened the attachment; only the badge line should")
	}
}

// A plain click puts the cursor on the message under the pointer, so the
// mouse and the arrow keys address the same message.
func TestCursor_ClickPlacesTheCursor(t *testing.T) {
	t.Parallel()

	m := threadWithMedia(t, 3)
	_, spans := m.renderContent()
	var span blockSpan
	for _, s := range spans {
		if s.id == 1 {
			span = s
		}
	}

	m, ok := m.CursorAt(0, span.start-m.viewport.YOffset()+threadHeaderRows)
	if !ok {
		t.Fatalf("click inside message 1 did not land on a message")
	}
	if got := m.Cursor(); got != 1 {
		t.Fatalf("cursor = %d after clicking message 1", got)
	}
}
