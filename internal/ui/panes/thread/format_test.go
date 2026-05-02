package thread

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pgmac/lazytg/internal/core/domain"
)

// updateGolden is set by `go test -update` to rewrite the golden files
// from the current FormatMessage output instead of comparing. Used
// during development when the rendering changes intentionally.
var updateGolden = flag.Bool("update", false, "regenerate testdata/thread/*.txt golden files")

// ansiRE matches every ECMA-48 SGR escape sequence so the golden files
// can stay plain-text and reviewable. lipgloss's actual ANSI output is
// too noisy to commit and varies subtly across terminal capability
// detections — comparing on stripped content keeps the assertions
// stable across CI runners and developer machines.
var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

// stripANSI removes every SGR escape from s. Used by both the golden
// equality check and the structural assertions below.
func stripANSI(s string) string {
	return ansiRE.ReplaceAllString(s, "")
}

// fixedDate returns a deterministic timestamp. All format tests use
// the same hour:minute so the "[15:04]" header is stable.
func fixedDate() time.Time {
	return time.Date(2026, 5, 2, 15, 42, 0, 0, time.UTC)
}

func TestFormatMessageGolden(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		msg     domain.Message
		width   int
		replyTo *domain.Message
	}{
		{
			name: "plain",
			msg: domain.Message{
				ID:     1,
				ChatID: 100,
				FromID: 42,
				Date:   fixedDate(),
				Text:   "hello there",
			},
			width: 80,
		},
		{
			name: "with-reply",
			msg: domain.Message{
				ID:     2,
				ChatID: 100,
				FromID: 42,
				Date:   fixedDate(),
				Text:   "got it, will do",
			},
			width: 80,
			replyTo: &domain.Message{
				ID:     1,
				ChatID: 100,
				FromID: 99,
				Date:   fixedDate().Add(-time.Minute),
				Text:   "please review the design doc",
			},
		},
		{
			name: "markdown-bold",
			msg: domain.Message{
				ID:     3,
				ChatID: 100,
				FromID: 42,
				Date:   fixedDate(),
				Text:   "this is **important** stuff",
			},
			width: 80,
		},
		{
			name: "markdown-italic",
			msg: domain.Message{
				ID:     4,
				ChatID: 100,
				FromID: 42,
				Date:   fixedDate(),
				Text:   "kind of *maybe* a problem",
			},
			width: 80,
		},
		{
			name: "markdown-code",
			msg: domain.Message{
				ID:     5,
				ChatID: 100,
				FromID: 42,
				Date:   fixedDate(),
				Text:   "use `make test` first",
			},
			width: 80,
		},
		{
			name: "long-wrap",
			msg: domain.Message{
				ID:     6,
				ChatID: 100,
				FromID: 42,
				Date:   fixedDate(),
				Text:   "this message is intentionally long enough that it has to wrap onto multiple lines when the pane is narrow",
			},
			width: 40,
		},
		{
			name: "system-author",
			msg: domain.Message{
				ID:     7,
				ChatID: 100,
				FromID: 0,
				Date:   fixedDate(),
				Text:   "user joined the chat",
			},
			width: 80,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := stripANSI(FormatMessage(tc.msg, tc.width, tc.replyTo))
			golden := filepath.Join("testdata", tc.name+".txt")

			if *updateGolden {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatalf("mkdir testdata: %v", err)
				}
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}

			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden %s (run with -update to create): %v", golden, err)
			}
			if got != string(want) {
				t.Fatalf("FormatMessage output mismatch for %s\n--- want ---\n%s\n--- got ---\n%s",
					tc.name, want, got)
			}
		})
	}
}

func TestFormatMessageHeaderHasTimestamp(t *testing.T) {
	t.Parallel()

	m := domain.Message{
		ID:     1,
		ChatID: 100,
		FromID: 42,
		Date:   fixedDate(),
		Text:   "x",
	}
	out := stripANSI(FormatMessage(m, 80, nil))
	if !strings.Contains(out, "[15:42]") {
		t.Fatalf("expected '[15:42]' in header, got %q", out)
	}
	if !strings.Contains(out, "user-42") {
		t.Fatalf("expected author label 'user-42' in header, got %q", out)
	}
}

func TestFormatMessageReplyHintTruncates(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("я", 80) // 80 runes — beyond replyPreviewMax cap
	m := domain.Message{
		ID:     1,
		ChatID: 100,
		FromID: 42,
		Date:   fixedDate(),
		Text:   "ok",
	}
	parent := domain.Message{
		ID:     2,
		ChatID: 100,
		FromID: 99,
		Date:   fixedDate(),
		Text:   long,
	}
	out := stripANSI(FormatMessage(m, 80, &parent))
	if !strings.Contains(out, "↳ replying to:") {
		t.Fatalf("expected reply hint in output, got %q", out)
	}
	// The truncated preview ends with the ellipsis.
	if !strings.Contains(out, "…") {
		t.Fatalf("expected ellipsis from truncation, got %q", out)
	}
}

func TestFormatMessageWordWrapHonoursWidth(t *testing.T) {
	t.Parallel()

	body := "word " + strings.Repeat("filler ", 30)
	m := domain.Message{
		ID:     1,
		ChatID: 100,
		FromID: 42,
		Date:   fixedDate(),
		Text:   body,
	}
	out := stripANSI(FormatMessage(m, 30, nil))
	for _, line := range strings.Split(out, "\n") {
		if runeWidth(line) > 30 {
			t.Fatalf("wrapped line exceeds width 30 (%d): %q", runeWidth(line), line)
		}
	}
}

// runeWidth is a minimal stand-in for uniseg/runewidth: every rune
// counts as 1 column. The test inputs are ASCII so this is exact;
// using a real width library here would just add a dependency for
// negligible gain.
func runeWidth(s string) int { return len([]rune(s)) }

func TestFormatMessageMinWidthFloor(t *testing.T) {
	t.Parallel()

	m := domain.Message{
		ID:     1,
		ChatID: 100,
		FromID: 42,
		Date:   fixedDate(),
		Text:   "hi",
	}
	// width=0 should not panic. The floor is enforced by the impl.
	out := stripANSI(FormatMessage(m, 0, nil))
	if out == "" {
		t.Fatalf("expected non-empty output for width=0, got empty")
	}
}

func TestRenderInlineMarkdownStripsMarkers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		out  string
	}{
		{"bold", "x **y** z", "x y z"},
		{"italic", "x *y* z", "x y z"},
		{"code", "x `y` z", "x y z"},
		{"unmatched-bold", "x **y", "x **y"},
		{"unmatched-italic", "x *y", "x *y"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := stripANSI(renderInlineMarkdown(tc.in))
			if got != tc.out {
				t.Fatalf("want %q, got %q", tc.out, got)
			}
		})
	}
}

func TestTruncRunes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in  string
		n   int
		out string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello", 4, "hel…"},
		{strings.Repeat("я", 5), 4, "яяя…"},
		{"x", 0, ""},
	}
	for _, tc := range cases {
		got := truncRunes(tc.in, tc.n)
		if got != tc.out {
			t.Fatalf("truncRunes(%q,%d): want %q, got %q", tc.in, tc.n, tc.out, got)
		}
	}
}
