package thread

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

func span(kind domain.EntityKind, off, n int) domain.Entity {
	return domain.Entity{Kind: kind, Offset: off, Length: n}
}

func TestRenderEntities_BoldIsBold(t *testing.T) {
	t.Parallel()

	out := renderEntities("say hello there", []domain.Entity{span(domain.EntityBold, 4, 5)}, false)
	if !strings.Contains(out, "\x1b[1mhello\x1b[") {
		t.Fatalf("hello is not bold in %q", out)
	}
	if ansi.Strip(out) != "say hello there" {
		t.Fatalf("text changed: %q", ansi.Strip(out))
	}
}

// Telegram sends overlapping spans for a word that is bold and italic at
// once. Cut at every boundary, the overlap is one segment with both.
func TestRenderEntities_OverlapWearsBothStyles(t *testing.T) {
	t.Parallel()

	out := renderEntities("abcdef", []domain.Entity{
		span(domain.EntityBold, 0, 4),
		span(domain.EntityItalic, 2, 4),
	}, false)
	if !strings.Contains(out, "\x1b[1;3mcd\x1b[") && !strings.Contains(out, "\x1b[3;1mcd\x1b[") {
		t.Fatalf("cd is not bold+italic in %q", out)
	}
	if ansi.Strip(out) != "abcdef" {
		t.Fatalf("text changed: %q", ansi.Strip(out))
	}
}

func TestRenderEntities_SpoilerHiddenUntilTheCursorIsOnIt(t *testing.T) {
	t.Parallel()

	es := []domain.Entity{span(domain.EntitySpoiler, 4, 6)}
	hidden := renderEntities("the secret word", es, false)
	if !strings.Contains(hidden, "\x1b[90;100msecret") && !strings.Contains(hidden, "\x1b[100;90msecret") {
		t.Fatalf("spoiler is not drawn grey on grey: %q", hidden)
	}
	revealed := renderEntities("the secret word", es, true)
	if strings.Contains(revealed, "90;100m") || strings.Contains(revealed, "100;90m") {
		t.Fatalf("spoiler still hidden with the cursor on it: %q", revealed)
	}
	if ansi.Strip(revealed) != "the secret word" {
		t.Fatalf("text changed: %q", ansi.Strip(revealed))
	}
}

// "click here" pointing somewhere else is the whole trick, so the host of a
// text_url is shown next to the words, and shown cleaned.
func TestRenderEntities_TextURLShowsTheHost(t *testing.T) {
	t.Parallel()

	out := ansi.Strip(renderEntities("click here now", []domain.Entity{
		{Kind: domain.EntityTextURL, Offset: 6, Length: 4, URL: "https://evil.example/login\x1b]52;c;x\x07"},
	}, false))
	if out != "click here ⟨evil.example⟩ now" {
		t.Fatalf("got %q", out)
	}
}

func TestRenderEntities_BlockquoteBarsEveryLine(t *testing.T) {
	t.Parallel()

	out := ansi.Strip(renderEntities("q1\nq2\nafter", []domain.Entity{span(domain.EntityBlockquote, 0, 5)}, false))
	if out != "▎ q1\n▎ q2\nafter" {
		t.Fatalf("got %q", out)
	}
}

// The cleaner removes characters and the offsets index the text as sent.
// Cleaning before laying out the spans would shift everything after the
// first stripped byte — here a CSI sequence in front of the bold word.
func TestRenderEntities_CleansWithoutShiftingLaterSpans(t *testing.T) {
	t.Parallel()

	text := "\x1b[2Jx bold"
	// "\x1b[2J" is four runes; "bold" starts at rune 6. The cleaner drops
	// the ESC and leaves "[2J" as harmless text, one rune shorter.
	out := renderEntities(text, []domain.Entity{span(domain.EntityBold, 6, 4)}, false)
	if ansi.Strip(out) != "[2Jx bold" {
		t.Fatalf("escape survived or text shifted: %q", ansi.Strip(out))
	}
	if !strings.Contains(out, "\x1b[1mbold\x1b[") {
		t.Fatalf("bold landed on the wrong word: %q", out)
	}
}

func TestRenderEntities_NoSpansKeepsTheMarkdownHeuristic(t *testing.T) {
	t.Parallel()

	out := renderEntities("**old** style", nil, false)
	if !strings.Contains(out, "\x1b[1mold\x1b[") {
		t.Fatalf("the plaintext markdown pass was skipped: %q", out)
	}
}

func TestRenderEntities_UnitsBeyondTheTextDoNotPanic(t *testing.T) {
	t.Parallel()

	out := ansi.Strip(renderEntities("ab", []domain.Entity{span(domain.EntityBold, 1, 40), span(domain.EntityItalic, 9, 2)}, false))
	if out != "ab" {
		t.Fatalf("got %q", out)
	}
}

// The block formatter is where the message's own spans meet the renderer,
// and the cursor is what reveals a spoiler. Both wired here, both checked
// here: the renderer tests above prove nothing about the block.
func TestFormatMessage_UsesTheMessageEntities(t *testing.T) {
	t.Parallel()

	msg := domain.Message{ID: 1, ChatID: 1, Text: "loud secret", Entities: []domain.Entity{
		span(domain.EntityBold, 0, 4),
		span(domain.EntitySpoiler, 5, 6),
	}}
	plain := FormatMessage(msg, 60, nil)
	if !strings.Contains(plain, "\x1b[1mloud\x1b[") {
		t.Fatalf("bold span lost between the message and the renderer: %q", plain)
	}
	if !strings.Contains(plain, "90;100m") && !strings.Contains(plain, "100;90m") {
		t.Fatalf("spoiler shown without the cursor: %q", plain)
	}
	withCursor, _ := formatMessageBlock(msg, 60, nil, "you", true)
	if strings.Contains(withCursor, "90;100m") || strings.Contains(withCursor, "100;90m") {
		t.Fatalf("cursor on the message did not reveal the spoiler: %q", withCursor)
	}
}

func TestMediaBadge_FilelessKinds(t *testing.T) {
	t.Parallel()

	cases := []struct {
		media *domain.MediaInfo
		want  string
	}{
		{&domain.MediaInfo{Kind: domain.MediaKindLocation, Filename: "1.000000,2.000000"}, "[📍 1.000000,2.000000] o to open the map"},
		{&domain.MediaInfo{Kind: domain.MediaKindLocation, Filename: "1.000000,2.000000", MimeType: "Cafe — Main st"}, "[📍 Cafe — Main st, 1.000000,2.000000] o to open the map"},
		{&domain.MediaInfo{Kind: domain.MediaKindContact, Filename: "Ann +1"}, "[👤 Ann +1]"},
		{&domain.MediaInfo{Kind: domain.MediaKindPoll, Filename: "Lunch?"}, "[📊 poll]"},
		{&domain.MediaInfo{Kind: domain.MediaKindDice, Filename: "🎲 4"}, "[🎲 🎲 4]"},
		{&domain.MediaInfo{Kind: domain.MediaKindWebPage, Filename: "Example — A post", MimeType: "https://example.com/post"}, "[🔗 Example — A post] o to open"},
	}
	for _, tc := range cases {
		if got := ansi.Strip(mediaBadge(tc.media)); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.media.Kind, got, tc.want)
		}
	}
	if got := ansi.Strip(mediaBadge(&domain.MediaInfo{Kind: domain.MediaKindPhoto, FileID: 1, Filename: "a.jpg", Size: 10})); !strings.Contains(got, "ctrl+d to save") {
		t.Fatalf("a real file lost its hint: %q", got)
	}
}
