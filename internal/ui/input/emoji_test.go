package input

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/ui/keymap"
)

func composerWith(t *testing.T, text string) Model {
	t.Helper()
	m := New()
	m.textarea.SetValue(text)
	m.textarea.MoveToEnd()
	return m
}

func typeTab(m Model) Model {
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	return updated
}

func TestEmojiPrefix_FindsTheShortcodeUnderTheCursor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		line string
		want string
	}{
		{"ship it :rocket", "rocket"},
		{":rocket", "rocket"},
		{"hello :+1", "+1"},
		{"", ""},
		{"nothing here", ""},
		{"already done :", ""},
		// A colon that does not open a word is punctuation, not a
		// shortcode. A client that eats the time in "10:30" the moment
		// somebody presses Tab is worse than one with no completion.
		{"meeting at 10:30", ""},
		{"http://x", ""},
	}
	for _, c := range cases {
		if got := emojiPrefix(c.line, len([]rune(c.line))); got != c.want {
			t.Errorf("emojiPrefix(%q) = %q, want %q", c.line, got, c.want)
		}
	}
}

func TestTab_ReplacesTheShortcodeWithTheEmoji(t *testing.T) {
	t.Parallel()

	m := typeTab(composerWith(t, "ship it :rocket"))
	if got := m.textarea.Value(); got != "ship it 🚀" {
		t.Fatalf("composer holds %q, want %q", got, "ship it 🚀")
	}
}

// Being wrong has to cost one keystroke, not a deleted word — otherwise the
// ranking has to be perfect, and it never is.
func TestTab_CyclesThroughTheCandidates(t *testing.T) {
	t.Parallel()

	m := typeTab(composerWith(t, "on :fire"))
	first := m.textarea.Value()
	m = typeTab(m)
	second := m.textarea.Value()

	if first == second {
		t.Fatalf("a second tab changed nothing: %q", first)
	}
	if !strings.HasPrefix(first, "on ") || !strings.HasPrefix(second, "on ") {
		t.Fatalf("the cycle ate the text around it: %q then %q", first, second)
	}
	if strings.Contains(second, ":") {
		t.Fatalf("the shortcode came back on the second press: %q", second)
	}
}

func TestTab_LeavesUnknownShortcodesAlone(t *testing.T) {
	t.Parallel()

	m := typeTab(composerWith(t, "see :zzzqqq"))
	if got := m.textarea.Value(); got != "see :zzzqqq" {
		t.Fatalf("composer holds %q, want it untouched", got)
	}
}

// The app hands Tab to the composer only when the composer says it has
// something to finish; the rest of the time Tab cycles focus.
func TestEmojiPrefix_ReportsWhetherTabBelongsHere(t *testing.T) {
	t.Parallel()

	if got := composerWith(t, "just text").EmojiPrefix(); got != "" {
		t.Fatalf("EmojiPrefix on plain text = %q, want empty", got)
	}
	if got := composerWith(t, "go :roc").EmojiPrefix(); got != "roc" {
		t.Fatalf("EmojiPrefix = %q, want roc", got)
	}
}

// Typing after a completion ends the cycle: the emoji in the box is part of
// the message now, and the next Tab must start from what is typed.
func TestTypingEndsTheCycle(t *testing.T) {
	t.Parallel()

	m := typeTab(composerWith(t, "on :fire"))
	if m.EmojiHint() == "" {
		t.Fatal("no hint while cycling")
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: '!', Text: "!"})
	if m.EmojiHint() != "" {
		t.Fatalf("the cycle survived a keystroke: %q", m.EmojiHint())
	}
}

func TestEmojiHint_ShowsWhatTheNextTabWouldDo(t *testing.T) {
	t.Parallel()

	m := typeTab(composerWith(t, ":fire"))
	hint := m.EmojiHint()
	if !strings.Contains(hint, ":fire") {
		t.Fatalf("hint does not say what was typed: %q", hint)
	}
	if !strings.Contains(hint, "[🔥]") {
		t.Fatalf("hint does not mark the current candidate: %q", hint)
	}
}

func TestTab_KeepsTheTextAfterTheCursor(t *testing.T) {
	t.Parallel()

	m := New()
	m.textarea.SetValue("go :rocket now")
	m.textarea.MoveToEnd()
	m.textarea.SetCursorColumn(len("go :rocket"))
	m = typeTab(m)

	if got := m.textarea.Value(); got != "go 🚀 now" {
		t.Fatalf("composer holds %q, want %q", got, "go 🚀 now")
	}
}

func TestInsertTextMsg_PutsThePickedEmojiIn(t *testing.T) {
	t.Parallel()

	m := composerWith(t, "hi ")
	m, _ = m.Update(InsertTextMsg{Text: "👋"})
	if got := m.textarea.Value(); got != "hi 👋" {
		t.Fatalf("composer holds %q", got)
	}
}

// The binding is shared with focus-next on purpose, and the loader has to
// keep letting that through — otherwise every start-up fails on the default
// keymap.
func TestDefaultKeymap_SharesTabDeliberately(t *testing.T) {
	t.Parallel()

	km := keymap.Default()
	if len(km.CompleteEmoji.Keys()) == 0 || km.CompleteEmoji.Keys()[0] != "tab" {
		t.Fatalf("CompleteEmoji is bound to %v, want tab", km.CompleteEmoji.Keys())
	}
	if got := keymap.DetectConflicts(km); len(got) != 0 {
		t.Fatalf("default keymap reports conflicts: %v", got)
	}
}
