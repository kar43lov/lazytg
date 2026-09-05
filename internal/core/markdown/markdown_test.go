package markdown

import (
	"reflect"
	"testing"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

func e(kind domain.EntityKind, off, n int) domain.Entity {
	return domain.Entity{Kind: kind, Offset: off, Length: n}
}

func TestParse_EachMarker(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		text string
		want []domain.Entity
	}{
		{"**bold** x", "bold x", []domain.Entity{e(domain.EntityBold, 0, 4)}},
		{"a __it__", "a it", []domain.Entity{e(domain.EntityItalic, 2, 2)}},
		{"~~gone~~", "gone", []domain.Entity{e(domain.EntityStrike, 0, 4)}},
		{"||shh||", "shh", []domain.Entity{e(domain.EntitySpoiler, 0, 3)}},
		{"run `ls -la` now", "run ls -la now", []domain.Entity{e(domain.EntityCode, 4, 6)}},
		{"```go\nfmt.Println()\n```", "fmt.Println()", []domain.Entity{{Kind: domain.EntityPre, Offset: 0, Length: 13, Language: "go"}}},
		{"```\nplain\n```", "plain", []domain.Entity{e(domain.EntityPre, 0, 5)}},
		{"see [the docs](https://x.y/d) now", "see the docs now", []domain.Entity{{Kind: domain.EntityTextURL, Offset: 4, Length: 8, URL: "https://x.y/d"}}},
	}
	for _, tc := range cases {
		text, got := Parse(tc.in)
		if text != tc.text || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Parse(%q) = %q %+v, want %q %+v", tc.in, text, got, tc.text, tc.want)
		}
	}
}

// Offsets count runes: the emoji before the bold word is one rune, and a
// byte count would put the span three characters to the right.
func TestParse_OffsetsAreRunes(t *testing.T) {
	t.Parallel()

	text, got := Parse("🚀 **go**")
	if text != "🚀 go" || !reflect.DeepEqual(got, []domain.Entity{e(domain.EntityBold, 2, 2)}) {
		t.Fatalf("got %q %+v", text, got)
	}
}

func TestParse_NestedAndOverlapping(t *testing.T) {
	t.Parallel()

	text, got := Parse("**bold __both__** [**b**](https://x.y)")
	if text != "bold both b" {
		t.Fatalf("text = %q", text)
	}
	want := []domain.Entity{
		e(domain.EntityBold, 0, 9),
		e(domain.EntityItalic, 5, 4),
		e(domain.EntityBold, 10, 1),
		{Kind: domain.EntityTextURL, Offset: 10, Length: 1, URL: "https://x.y"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

// What is not markup stays as typed. Eating a character somebody wrote is
// worse than showing a marker.
func TestParse_LeavesNonMarkupAlone(t *testing.T) {
	t.Parallel()

	for _, in := range []string{
		"2 ** 3 = 8",
		"snake_case and a_b_c",
		"a * b * c",
		"lone ` backtick",
		"```never closed",
		"[not a link]",
		"[text](with space)",
		"****",
		"||||",
		"x || y",
		"10:30",
		"~~",
	} {
		text, got := Parse(in)
		if text != in || got != nil {
			t.Errorf("Parse(%q) = %q %+v, want it untouched", in, text, got)
		}
	}
}

// __init__.py is what a Python programmer types most; the double
// underscores there enclose "init", which is markup by the letter of the
// rule, and Telegram Desktop italicises it too. Documented rather than
// special-cased, so the behaviour is the same on both ends.
func TestParse_DunderIsItalicLikeTelegramDesktop(t *testing.T) {
	t.Parallel()

	text, got := Parse("edit __init__.py")
	if text != "edit init.py" || len(got) != 1 || got[0].Kind != domain.EntityItalic {
		t.Fatalf("got %q %+v", text, got)
	}
}

func TestParse_CodeIsNotParsedInside(t *testing.T) {
	t.Parallel()

	text, got := Parse("`**not bold**` and ```\n__x__\n```")
	if text != "**not bold** and __x__" {
		t.Fatalf("text = %q", text)
	}
	for _, en := range got {
		if en.Kind != domain.EntityCode && en.Kind != domain.EntityPre {
			t.Fatalf("markup parsed inside code: %+v", got)
		}
	}
}

func TestRender_RoundTrip(t *testing.T) {
	t.Parallel()

	for _, src := range []string{
		"**bold** and __it__ and ~~st~~ and ||sp||",
		"run `ls` then ```go\nmain()\n```",
		"see [docs](https://x.y/d) now",
		"**bold __both__** tail",
		"🚀 **go** 🚀",
		"plain text with no markup",
	} {
		text, es := Parse(src)
		back := Render(text, es)
		if back != src {
			t.Errorf("Render(Parse(%q)) = %q", src, back)
		}
		text2, es2 := Parse(back)
		if text2 != text || !reflect.DeepEqual(es2, es) {
			t.Errorf("second parse of %q differs: %q %+v vs %q %+v", back, text2, es2, text, es)
		}
	}
}

// Spans without a marker in this dialect are the text they cover.
func TestRender_SkipsKindsWithoutMarkup(t *testing.T) {
	t.Parallel()

	got := Render("@user #tag https://x.y", []domain.Entity{
		e(domain.EntityMention, 0, 5), e(domain.EntityHashtag, 6, 4), e(domain.EntityURL, 11, 11),
	})
	if got != "@user #tag https://x.y" {
		t.Fatalf("got %q", got)
	}
}

func TestRender_SurvivesSpansPastTheText(t *testing.T) {
	t.Parallel()

	got := Render("ab", []domain.Entity{e(domain.EntityBold, 1, 9), e(domain.EntityItalic, 7, 1)})
	if got != "a**b**" {
		t.Fatalf("got %q", got)
	}
}
