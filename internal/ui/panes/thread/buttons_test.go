package thread

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
)

func keyboard() [][]domain.Button {
	return [][]domain.Button{
		{{Text: "Yes", Kind: domain.ButtonCallback, Data: []byte("y")}, {Text: "No", Kind: domain.ButtonCallback, Data: []byte("n")}},
		{{Text: "Docs", Kind: domain.ButtonURL, URL: "https://example.com"}},
	}
}

// The keyboard is drawn under the message in the bot's rows; on the message
// under the cursor one key is the chosen one, ← and → walk them in reading
// order and wrap, and moving the cursor away resets the choice.
func TestKeyboard_DrawnAndWalked(t *testing.T) {
	t.Parallel()

	withKeys := incoming(1, 0, "pick one")
	withKeys.Buttons = keyboard()
	m := loadedThread(t, 0, withKeys, incoming(2, 1, "later"))

	view := ansi.Strip(m.View())
	for _, label := range []string{"[ Yes ]", "[ No ]", "[ Docs ]"} {
		if !strings.Contains(view, label) {
			t.Fatalf("key %s not drawn:\n%s", label, view)
		}
	}
	if strings.Contains(view, "Enter to press") {
		t.Fatal("the hint shows while the cursor is elsewhere")
	}
	if _, _, ok := m.ChosenButton(); ok {
		t.Fatal("a message without keys offered one")
	}

	// The first move places the cursor on the newest message; the second
	// reaches the one with the keys.
	m = m.MoveCursor(-1).MoveCursor(-1)
	if _, btn, ok := m.ChosenButton(); !ok || btn.Text != "Yes" {
		t.Fatalf("first key = %+v ok=%v", btn, ok)
	}
	if !strings.Contains(ansi.Strip(m.View()), "Enter to press") {
		t.Fatal("no hint on the message under the cursor")
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if _, btn, _ := m.ChosenButton(); btn.Text != "Docs" {
		t.Fatalf("two rights = %q, want Docs", btn.Text)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if _, btn, _ := m.ChosenButton(); btn.Text != "Yes" {
		t.Fatalf("→ past the end = %q, want Yes (wrap)", btn.Text)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if _, btn, _ := m.ChosenButton(); btn.Text != "Docs" {
		t.Fatalf("← from the first = %q, want Docs (wrap)", btn.Text)
	}
	// Any cursor move resets the choice — even one that cannot move,
	// like up at the top of the history.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if _, btn, _ := m.ChosenButton(); btn.Text != "Yes" {
		t.Fatalf("the choice survived up at the top: %q", btn.Text)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if _, btn, _ := m.ChosenButton(); btn.Text != "Yes" {
		t.Fatalf("the choice survived a cursor move: %q", btn.Text)
	}
}

// A bot answers a press by editing the message, usually with a new
// keyboard or none; the edit replaces the keys under the row.
func TestKeyboard_FollowsTheEdit(t *testing.T) {
	t.Parallel()

	withKeys := incoming(1, 0, "pick one")
	withKeys.Buttons = keyboard()
	m := loadedThread(t, 0, withKeys)
	m = m.MoveCursor(-1)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})

	m, _ = m.Update(events.MessageReceived{ChatID: 42, MessageID: 1, Text: "you picked No", Date: withKeys.Date, Edited: true,
		Buttons: [][]domain.Button{{{Text: "Undo", Kind: domain.ButtonCallback}}}})
	view := ansi.Strip(m.View())
	if strings.Contains(view, "[ Yes ]") || !strings.Contains(view, "[ Undo ]") {
		t.Fatalf("the edit did not replace the keyboard:\n%s", view)
	}
	if _, btn, ok := m.ChosenButton(); !ok || btn.Text != "Undo" {
		t.Fatalf("chosen after a shorter keyboard = %+v ok=%v", btn, ok)
	}
	m, _ = m.Update(events.MessageReceived{ChatID: 42, MessageID: 1, Text: "done", Date: withKeys.Date, Edited: true})
	if strings.Contains(ansi.Strip(m.View()), "[ Undo ]") {
		t.Fatal("an edit that took the keyboard away left it on screen")
	}
	if _, _, ok := m.ChosenButton(); ok {
		t.Fatal("a key is still offered after the keyboard went")
	}
}

// Down at the bottom of the history moves nothing and still resets the
// choice, the same as up at the top.
func TestKeyboard_DownAtTheBottomResetsTheChoice(t *testing.T) {
	t.Parallel()

	withKeys := incoming(2, 1, "pick one")
	withKeys.Buttons = keyboard()
	m := loadedThread(t, 0, incoming(1, 0, "earlier"), withKeys)
	m = m.MoveCursor(-1)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if _, btn, _ := m.ChosenButton(); btn.Text != "No" {
		t.Fatalf("right = %q", btn.Text)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if _, btn, _ := m.ChosenButton(); btn.Text != "Yes" {
		t.Fatalf("the choice survived down at the bottom: %q", btn.Text)
	}
}
