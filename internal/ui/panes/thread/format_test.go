package thread

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
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
//
// Deliberately in time.Local, not UTC: the header renders in the user's zone,
// so a UTC fixture would make the golden files pass only on a UTC machine (CI)
// and fail everywhere else. The UTC→local conversion itself is pinned by
// TestFormatHeader_RendersInLocalZone, which fixes the zone explicitly.
func fixedDate() time.Time {
	return time.Date(2026, 5, 2, 15, 42, 0, 0, time.Local)
}

// TestFormatHeader_RendersInLocalZone pins the conversion the first live smoke
// caught: the domain keeps Date in UTC, and formatting it directly printed the
// UTC wall clock — a message sent at 19:32 MSK appeared as [16:32], three hours
// in the past, which reads as a stale thread rather than a formatting bug.
//
// Not parallel: it swaps time.Local for the duration.
func TestFormatHeader_RendersInLocalZone(t *testing.T) {
	orig := time.Local
	time.Local = time.FixedZone("TEST+03", 3*60*60)
	t.Cleanup(func() { time.Local = orig })

	msg := domain.Message{
		ID:     1,
		ChatID: 777000,
		FromID: 0,
		Date:   time.Date(2026, 8, 17, 16, 32, 0, 0, time.UTC),
		Text:   "Login code: 94532",
	}
	got := stripANSI(renderHeader(msg.Date, authorLabel(msg.FromID), ""))
	if !strings.HasPrefix(got, "[19:32]") {
		t.Errorf("header = %q, want it to start with [19:32] (16:32 UTC in a +03:00 zone)", got)
	}
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

func TestFormatMessage_MediaBadge(t *testing.T) {
	t.Parallel()

	doc := domain.Message{
		ID: 1, ChatID: 1, FromID: 100, Date: fixedDate(),
		Text: "see attached",
		Media: &domain.MediaInfo{
			Kind: domain.MediaKindDocument, FileID: 7,
			Filename: "report.pdf", Size: 234567,
		},
	}
	out := stripANSI(FormatMessage(doc, 60, nil))
	if !strings.Contains(out, "📎 report.pdf") {
		t.Fatalf("missing document badge: %q", out)
	}
	if !strings.Contains(out, "229.1 KiB") {
		t.Fatalf("missing size cell: %q", out)
	}
	if !strings.Contains(out, "ctrl+d to save") {
		t.Fatalf("missing hint: %q", out)
	}
	if !strings.Contains(out, "see attached") {
		t.Fatalf("missing body text: %q", out)
	}

	photo := domain.Message{
		ID: 2, ChatID: 1, FromID: 100, Date: fixedDate(),
		Media: &domain.MediaInfo{
			Kind: domain.MediaKindPhoto, FileID: 8,
			Filename: "photo.jpg", Size: 0,
		},
	}
	out = stripANSI(FormatMessage(photo, 60, nil))
	if !strings.Contains(out, "🖼 photo.jpg") {
		t.Fatalf("missing photo badge: %q", out)
	}
	// Zero-size badge should not show "B" / "KiB" cells.
	if strings.Contains(out, " B]") || strings.Contains(out, " KiB]") {
		t.Fatalf("unexpected size for unknown-size media: %q", out)
	}

	plain := domain.Message{
		ID: 3, ChatID: 1, FromID: 100, Date: fixedDate(), Text: "hi",
	}
	out = stripANSI(FormatMessage(plain, 60, nil))
	if strings.Contains(out, "📎") || strings.Contains(out, "🖼") {
		t.Fatalf("plain message gained media badge: %q", out)
	}
}

func TestFormatBytes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{234567, "229.1 KiB"},
		{2 * 1024 * 1024, "2.0 MiB"},
		{int64(1.5 * 1024 * 1024 * 1024), "1.50 GiB"},
	}
	for _, tc := range cases {
		if got := formatBytes(tc.in); got != tc.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The thread used to expose LatestMediaMessage for the Ctrl-D chord.
// MediaTarget replaced it: with an untouched cursor the two agree, and
// with a moved cursor only MediaTarget answers the question the user is
// actually asking. Keeping both would have left two functions deciding
// "which attachment" — the setup where one of them quietly goes wrong.
// Coverage moved to cursor_test.go.

// TestResolveAuthor covers the sender naming the first live session made
// unavoidable: every line, including the reader's own, was labelled
// "user-8385473863".
func TestResolveAuthor(t *testing.T) {
	t.Parallel()

	const peer = 862242381
	names := map[int64]string{peer: "Иван Егошин"}

	cases := []struct {
		name    string
		msg     domain.Message
		chatID  int64
		private bool
		want    string
	}{
		{"service message", domain.Message{FromID: 0}, peer, true, "system"},
		// The 1:1 pair below is what the second live run exposed: Telegram
		// sends no from_id in a private dialog, so both directions arrive
		// with 0 and every line rendered as "system".
		{"incoming private message with no from_id", domain.Message{FromID: peer}, peer, true, "Иван Егошин"},
		{"own private message carries direction, not an id", domain.Message{FromID: 0, Outgoing: true}, peer, true, "you"},
		{"known peer by name", domain.Message{FromID: peer}, peer, true, "Иван Егошин"},
		{"own message in a private chat", domain.Message{FromID: 8385473863}, peer, true, "you"},
		{"unknown sender in a group", domain.Message{FromID: 555}, -100123, false, "user-555"},
		{"named sender in a group", domain.Message{FromID: peer}, -100123, false, "Иван Егошин"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := resolveAuthor(tc.msg, tc.chatID, tc.private, names); got != tc.want {
				t.Errorf("resolveAuthor = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSetDirectory_NamesRenderedMessages is the end-to-end: names supplied by
// the app must reach the rendered thread, not just the resolver.
func TestSetDirectory_NamesRenderedMessages(t *testing.T) {
	t.Parallel()

	const peer = 862242381
	m := sized(New())
	m, _ = m.OpenChat(peer)
	m, _ = m.Update(messagesLoadedMsg{
		chatID: peer,
		gen:    m.loadGen,
		messages: []domain.Message{
			{ID: 1, ChatID: peer, FromID: peer, Date: fixedDate(), Text: "from them"},
			{ID: 2, ChatID: peer, FromID: 8385473863, Date: fixedDate(), Text: "from me"},
		},
	})
	m = m.SetDirectory(map[int64]string{peer: "Иван Егошин"}, domain.ChatTypePrivate)

	view := stripANSI(m.View())
	if !strings.Contains(view, "Иван Егошин") {
		t.Errorf("peer name missing from the rendered thread:\n%s", view)
	}
	if !strings.Contains(view, "you") {
		t.Errorf("own message not labelled \"you\":\n%s", view)
	}
	if strings.Contains(view, "user-862242381") {
		t.Errorf("raw numeric id still rendered:\n%s", view)
	}
}

// The badge names what an attachment is, not how it travels. A video
// note used to render as "[📎 document_5123.bin]" — the icon, the label
// and the duration are the three things that tell a reader whether the
// thing above is worth opening.
func TestFormatMessage_BadgeNamesTheKindAndDuration(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		media domain.MediaInfo
		want  []string
		avoid []string
	}{
		{
			name: "video note",
			media: domain.MediaInfo{
				Kind: domain.MediaKindVideoNote, FileID: 1,
				Filename: "video_note_1.mp4", Size: 1258291, Duration: 7,
			},
			// The invented filename is noise next to "video note, 0:07".
			want:  []string{"⏺ video note", "0:07", "1.2 MiB"},
			avoid: []string{"video_note_1.mp4"},
		},
		{
			name: "voice message",
			media: domain.MediaInfo{
				Kind: domain.MediaKindVoice, FileID: 2,
				Filename: "voice_2.ogg", Size: 30720, Duration: 42,
			},
			want:  []string{"🎤 voice", "0:42"},
			avoid: []string{"voice_2.ogg"},
		},
		{
			name: "long video keeps the sender's filename",
			media: domain.MediaInfo{
				Kind: domain.MediaKindVideo, FileID: 3,
				Filename: "holiday.mp4", Size: 5242880, Duration: 3725,
			},
			want: []string{"🎬 holiday.mp4", "1:02:05"},
		},
		{
			name: "a document has no duration cell",
			media: domain.MediaInfo{
				Kind: domain.MediaKindDocument, FileID: 4,
				Filename: "report.pdf", Size: 1024,
			},
			want:  []string{"📎 report.pdf"},
			avoid: []string{"0:00"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := domain.Message{ID: 1, ChatID: 1, FromID: 100, Date: fixedDate(), Media: &tc.media}
			out := stripANSI(FormatMessage(msg, 60, nil))
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("badge %q is missing %q", out, want)
				}
			}
			for _, avoid := range tc.avoid {
				if strings.Contains(out, avoid) {
					t.Errorf("badge %q should not contain %q", out, avoid)
				}
			}
			if !strings.Contains(out, "o to open") {
				t.Errorf("badge %q does not advertise the open gesture", out)
			}
		})
	}
}

// TestFormatMessage_DoesNotLetTheSenderDriveTheTerminal covers the whole
// rendered block rather than one field, because every string in it comes
// from somebody else: the body, the filename on the badge, and the text
// echoed in the reply hint. lazytg copies to the clipboard with OSC 52,
// which proves the terminal honours OSC — a sequence reaching the screen
// from a message is not a cosmetic problem.
//
// stripANSI removes the escapes lipgloss itself emits, so the assertion
// is made against the raw output: what survives here would reach the
// terminal verbatim.
func TestFormatMessage_DoesNotLetTheSenderDriveTheTerminal(t *testing.T) {
	t.Parallel()

	msg := domain.Message{
		ID:     1,
		ChatID: 7,
		FromID: 42,
		Date:   time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC),
		Text:   "before\x1b]52;c;cHduZWQ=\x07after",
		Media: &domain.MediaInfo{
			Kind:     domain.MediaKindDocument,
			FileID:   9,
			Filename: "photo\x1b[2J\u202egnp.dnammoc",
			Size:     1024,
		},
	}
	parent := domain.Message{ID: 0, Text: "parent\x1b[31mtext"}

	out := FormatMessage(msg, 80, &parent)
	for _, bad := range []string{"\x1b]52", "\x1b[2J", "\u202e", "\x1b[31m"} {
		if strings.Contains(out, bad) {
			t.Fatalf("rendered block still carries %q:\n%s", bad, out)
		}
	}
	// The visible text must survive the cleaning — a filter that ate the
	// message would pass the assertion above and be useless.
	plain := stripANSI(out)
	for _, want := range []string{"before", "after", "photo", "parent"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("cleaning dropped %q from the block:\n%s", want, plain)
		}
	}
}
