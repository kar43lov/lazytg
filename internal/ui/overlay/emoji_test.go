package overlay

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/ui/keymap"
)

func openPicker(t *testing.T) EmojiPicker {
	t.Helper()
	p := NewEmojiPicker(keymap.Default()).SetSize(120, 40).Open()
	if !p.Visible() {
		t.Fatal("picker did not open")
	}
	return p
}

func typeInto(p EmojiPicker, s string) EmojiPicker {
	for _, r := range s {
		p, _ = p.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return p
}

func press(p EmojiPicker, code rune) (EmojiPicker, tea.Cmd) {
	return p.Update(tea.KeyPressMsg{Code: code})
}

// Typing is the whole navigation model: in a grid of a thousand characters
// the arrow keys are a way to get lost.
func TestEmojiPicker_TypingFilters(t *testing.T) {
	t.Parallel()

	p := typeInto(openPicker(t), "rocket")
	entries := p.entries()
	if len(entries) == 0 {
		t.Fatal("nothing matched rocket")
	}
	if entries[0].Char != "🚀" {
		t.Fatalf("first match is %q, want 🚀", entries[0].Char)
	}
	if !strings.Contains(p.View(), "rocket") {
		t.Fatal("the view does not show what was typed")
	}
}

func TestEmojiPicker_EnterPicksAndCloses(t *testing.T) {
	t.Parallel()

	p := typeInto(openPicker(t), "rocket")
	p, cmd := press(p, tea.KeyEnter)
	if cmd == nil {
		t.Fatal("Enter produced no message")
	}
	picked, ok := cmd().(EmojiPickedMsg)
	if !ok {
		t.Fatalf("Enter emitted %T, want EmojiPickedMsg", cmd())
	}
	if picked.Char != "🚀" {
		t.Fatalf("picked %q, want 🚀", picked.Char)
	}
	if p.Visible() {
		t.Fatal("the picker stayed open after a pick")
	}
}

func TestEmojiPicker_EscClosesWithoutPicking(t *testing.T) {
	t.Parallel()

	p, cmd := press(openPicker(t), tea.KeyEscape)
	if p.Visible() {
		t.Fatal("Esc left the picker open")
	}
	if cmd == nil {
		t.Fatal("Esc produced no message")
	}
	if _, ok := cmd().(EmojiClosedMsg); !ok {
		t.Fatalf("Esc emitted %T, want EmojiClosedMsg", cmd())
	}
}

// What you picked last is what you are most likely to pick again, and it is
// the one list a picker can build without asking anybody anything.
func TestEmojiPicker_RemembersWhatWasPicked(t *testing.T) {
	t.Parallel()

	p := typeInto(openPicker(t), "rocket")
	p, _ = press(p, tea.KeyEnter)

	p = p.Open()
	cats := p.categories()
	if len(cats) == 0 || cats[0] != recentCategory {
		t.Fatalf("categories start with %v, want Recent first", cats)
	}
	if got := p.entries(); len(got) == 0 || got[0].Char != "🚀" {
		t.Fatalf("Recent holds %v", got)
	}
}

func TestEmojiPicker_RecentKeepsOneCopyNewestFirst(t *testing.T) {
	t.Parallel()

	p := NewEmojiPicker(keymap.Default()).SetSize(120, 40)
	p = p.remember("🚀").remember("🔥").remember("🚀")

	if len(p.recent) != 2 {
		t.Fatalf("recent holds %d entries, want 2: %v", len(p.recent), p.recent)
	}
	if p.recent[0] != "🚀" {
		t.Fatalf("recent starts with %q, want the one just picked", p.recent[0])
	}
}

func TestEmojiPicker_BackspaceWidensTheSearch(t *testing.T) {
	t.Parallel()

	p := typeInto(openPicker(t), "rocket")
	narrow := len(p.entries())
	p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if p.query != "rocke" {
		t.Fatalf("filter reads %q after a backspace, want rocke", p.query)
	}
	if len(p.entries()) < narrow {
		t.Fatalf("backspace narrowed the search instead (%d then %d)", narrow, len(p.entries()))
	}
	for i := 0; i < 5; i++ {
		p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	if p.query != "" {
		t.Fatalf("backspacing to the start left %q", p.query)
	}
	if len(p.entries()) <= narrow {
		t.Fatalf("an empty filter shows %d entries, no more than the search did", len(p.entries()))
	}
}

// Alt+E opens the picker; the "e" must not land in the filter on the way in.
func TestEmojiPicker_IgnoresModifiedKeys(t *testing.T) {
	t.Parallel()

	p := openPicker(t)
	p, _ = p.Update(tea.KeyPressMsg{Code: 'e', Mod: tea.ModAlt})
	if p.query != "" {
		t.Fatalf("a modified key reached the filter: %q", p.query)
	}
}

func TestEmojiPicker_TabWalksTheCategories(t *testing.T) {
	t.Parallel()

	p := openPicker(t)
	first := p.categories()[p.catIndex]
	p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if second := p.categories()[p.catIndex]; second == first {
		t.Fatalf("tab did not move off %q", first)
	}
	// And it wraps, so the strip has no dead end.
	for i := 0; i < len(p.categories()); i++ {
		p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	}
	if got := p.categories()[p.catIndex]; got == "" {
		t.Fatal("the category index left the strip")
	}
}

func TestEmojiPicker_HiddenRendersNothing(t *testing.T) {
	t.Parallel()

	p := NewEmojiPicker(keymap.Default()).SetSize(120, 40)
	if got := p.View(); got != "" {
		t.Fatalf("a hidden picker rendered %q", got)
	}
}

// A search with no hits must say so rather than showing an empty box the user
// has to guess at.
func TestEmojiPicker_SaysWhenNothingMatches(t *testing.T) {
	t.Parallel()

	p := typeInto(openPicker(t), "zzzqqq")
	if !strings.Contains(p.View(), "nothing matches") {
		t.Fatalf("no explanation for an empty result:\n%s", p.View())
	}
}

func TestEmojiPicker_ArrowsStayInsideTheGrid(t *testing.T) {
	t.Parallel()

	p := typeInto(openPicker(t), "rocket")
	for i := 0; i < 20; i++ {
		p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	}
	if p.cursor >= len(p.entries()) {
		t.Fatalf("cursor at %d with %d entries", p.cursor, len(p.entries()))
	}
	for i := 0; i < 20; i++ {
		p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	}
	if p.cursor != 0 {
		t.Fatalf("cursor at %d after walking off the left edge", p.cursor)
	}
}
