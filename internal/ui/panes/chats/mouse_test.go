package chats

import (
	"regexp"
	"strings"
	"testing"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// sgrRE matches SGR escapes so a rendered line can be searched for plain text.
// The delegate colours titles, so a naive strings.Contains would miss.
var sgrRE = regexp.MustCompile("\x1b\\[[0-9;:]*m")

func stripSGR(s string) string { return sgrRE.ReplaceAllString(s, "") }

// loaded builds a pane sized for a roomy terminal with n chats in it, named
// "Chat-0"… so a rendered line can be traced back to its index.
func loaded(t *testing.T, n int) Model {
	t.Helper()
	items := make([]ChatItem, n)
	for i := range items {
		items[i] = NewChatItem(
			domain.Chat{ID: int64(i + 1), Title: chatName(i), Type: domain.ChatTypePrivate},
			"preview",
		)
	}
	m, _ := newModel(nil, nil).SetSize(30, 20).applyLoaded(items)
	return m
}

func chatName(i int) string {
	return "Chat-" + string(rune('A'+i))
}

// TestItemIndexAt_MatchesRenderedRows is the test that keeps click selection
// honest: instead of restating the offsets from mouse.go, it finds where each
// chat actually appears in the rendered pane and requires ItemIndexAt to agree.
//
// The offsets depend on bubbles' internals (a title/filter line is drawn even
// with SetShowTitle(false), and the default delegate spans two rows plus one of
// spacing). A bubbles upgrade that changes either would otherwise silently make
// every click select the wrong chat.
func TestItemIndexAt_MatchesRenderedRows(t *testing.T) {
	t.Parallel()

	m := loaded(t, 3)
	lines := strings.Split(m.View(), "\n")

	for i := 0; i < 3; i++ {
		titleRow := -1
		for row, line := range lines {
			if strings.Contains(stripSGR(line), chatName(i)) {
				titleRow = row
				break
			}
		}
		if titleRow < 0 {
			t.Fatalf("chat %d (%s) not found in the rendered pane", i, chatName(i))
		}
		if got := m.ItemIndexAt(titleRow); got != i {
			t.Errorf("row %d renders %s but ItemIndexAt says index %d, want %d",
				titleRow, chatName(i), got, i)
		}
		// The preview row and the blank spacing row below it read as the same
		// item to the user, so a click there must select it too.
		if got := m.ItemIndexAt(titleRow + 1); got != i {
			t.Errorf("preview row %d: got index %d, want %d", titleRow+1, got, i)
		}
	}
}

// TestItemIndexAt_RejectsNonItemRows covers the rows that carry no chat: the
// pane header, the list's chrome line, and the empty space under the last chat.
// Returning an index for those would open a chat on a click into the void.
func TestItemIndexAt_RejectsNonItemRows(t *testing.T) {
	t.Parallel()

	m := loaded(t, 2)
	for _, row := range []int{-1, 0, 1} {
		if got := m.ItemIndexAt(row); got != -1 {
			t.Errorf("row %d: got index %d, want -1 (header/chrome)", row, got)
		}
	}
	// Two items occupy rows 2..7; anything below is empty space.
	for _, row := range []int{8, 12, 19} {
		if got := m.ItemIndexAt(row); got != -1 {
			t.Errorf("row %d: got index %d, want -1 (below the last chat)", row, got)
		}
	}
}

// TestItemIndexAt_EmptyList covers the pane before the first repo load: no
// items, so no row can be hit.
func TestItemIndexAt_EmptyList(t *testing.T) {
	t.Parallel()

	m := newModel(nil, nil).SetSize(30, 20)
	for row := 0; row < 10; row++ {
		if got := m.ItemIndexAt(row); got != -1 {
			t.Fatalf("empty pane, row %d: got %d, want -1", row, got)
		}
	}
}

// TestSelectAt_SelectsAndEmitsSameMsgAsEnter pins that a click and Enter go
// through one path: the pane must both move its highlight and publish
// ChatSelectedMsg for the row that was clicked, not for the previous cursor.
func TestSelectAt_SelectsAndEmitsSameMsgAsEnter(t *testing.T) {
	t.Parallel()

	m := loaded(t, 3)
	// Row 5 is the second item's title row (items start at 2, 3 rows each).
	updated, cmd, hit := m.SelectAt(5)
	if !hit {
		t.Fatal("SelectAt(5) reported no hit on the second item's row")
	}
	if cmd == nil {
		t.Fatal("SelectAt returned no Cmd — nothing would open the chat")
	}
	sel, ok := updated.SelectedItem()
	if !ok {
		t.Fatal("no item selected after SelectAt")
	}
	if sel.id != 2 {
		t.Errorf("selected id = %d, want 2 (the clicked row)", sel.id)
	}
	msg, ok := cmd().(ChatSelectedMsg)
	if !ok {
		t.Fatalf("Cmd produced %T, want ChatSelectedMsg", cmd())
	}
	if msg.ChatID != 2 {
		t.Errorf("ChatSelectedMsg.ChatID = %d, want 2", msg.ChatID)
	}
}

// TestSelectAt_MissReportsNoHit ensures a click on empty space leaves the
// selection alone instead of jumping to the nearest chat.
func TestSelectAt_MissReportsNoHit(t *testing.T) {
	t.Parallel()

	m := loaded(t, 2)
	before, _ := m.SelectedItem()
	updated, cmd, hit := m.SelectAt(15)
	if hit {
		t.Error("SelectAt(15) claimed a hit below the last chat")
	}
	if cmd != nil {
		t.Error("a miss produced a Cmd")
	}
	after, _ := updated.SelectedItem()
	if after.id != before.id {
		t.Errorf("selection moved on a miss: %d → %d", before.id, after.id)
	}
}

// TestScrollBy_MovesSelectionAndClamps covers the wheel: one notch moves one
// chat, and neither end wraps around — a wheel that wrapped from the first chat
// to the last would look like a jump to a random conversation.
func TestScrollBy_MovesSelectionAndClamps(t *testing.T) {
	t.Parallel()

	m := loaded(t, 3)
	m = m.ScrollBy(1)
	if sel, _ := m.SelectedItem(); sel.id != 2 {
		t.Errorf("after one notch down: id %d, want 2", sel.id)
	}
	m = m.ScrollBy(-5)
	if sel, _ := m.SelectedItem(); sel.id != 1 {
		t.Errorf("scrolling past the top: id %d, want 1 (clamped)", sel.id)
	}
	m = m.ScrollBy(50)
	if sel, _ := m.SelectedItem(); sel.id != 3 {
		t.Errorf("scrolling past the bottom: id %d, want 3 (clamped)", sel.id)
	}
}

// TestScrollBy_EmptyListIsNoop guards the pre-load pane: bubbles' Select on an
// empty list would otherwise be asked for index -1.
func TestScrollBy_EmptyListIsNoop(t *testing.T) {
	t.Parallel()

	m := newModel(nil, nil).SetSize(30, 20)
	if _, ok := m.ScrollBy(3).SelectedItem(); ok {
		t.Error("scrolling an empty list produced a selection")
	}
}
