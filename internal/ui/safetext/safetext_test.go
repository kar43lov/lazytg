package safetext

import "testing"

// The hostile inputs are written as \u escapes rather than as the
// characters themselves. That is the same reason the package exists: a
// literal bidi override in a test file reverses the source for whoever
// reads it, and staticcheck's ST1018 objects to exactly that.
func TestClean_RemovesTheSequencesThatDriveTheTerminal(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			// The one that matters most: lazytg copies with OSC 52, so the
			// terminal it runs in honours OSC. A filename carrying this
			// sequence would rewrite the user's clipboard on render.
			name: "osc 52 clipboard write",
			in:   "photo\x1b]52;c;aGVsbG8=\x07.png",
			want: "photo]52;c;aGVsbG8=.png",
		},
		{
			name: "csi repaint",
			in:   "hi\x1b[2J\x1b[Hgone",
			want: "hi[2J[Hgone",
		},
		{
			name: "bidi override hides the real extension",
			in:   "photo\u202edna mmoc.gnp",
			want: "photodna mmoc.gnp",
		},
		{
			name: "bidi isolates",
			in:   "a\u2066b\u2069c",
			want: "abc",
		},
		{
			name: "c1 eight-bit introducers",
			in:   "a\u009bmc",
			want: "amc",
		},
		{
			name: "bel and del",
			in:   "a\x07b\x7fc",
			want: "abc",
		},
		{
			name: "ordinary text is returned untouched",
			in:   "Привет, мир! 3 < 5 — «да»",
			want: "Привет, мир! 3 < 5 — «да»",
		},
		{
			// Emoji are built with the zero-width joiner. Stripping it
			// would corrupt ordinary messages to defend against nothing.
			name: "zero-width joiner survives",
			in:   "\U0001f468\u200d\U0001f469\u200d\U0001f467",
			want: "\U0001f468\u200d\U0001f469\u200d\U0001f467",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Clean(tc.in); got != tc.want {
				t.Fatalf("Clean(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestClean_KeepsTheLineBreaksTheThreadLaysOut(t *testing.T) {
	t.Parallel()

	in := "first\nsecond\tthird"
	if got := Clean(in); got != in {
		t.Fatalf("Clean(%q) = %q, want the breaks kept", in, got)
	}
	// U+2028 is a line separator too, and one the pane does not lay out —
	// it would break the row arithmetic the mouse hit-test relies on.
	if got := Clean("a\u2028b"); got != "ab" {
		t.Fatalf("Clean line separator = %q, want %q", got, "ab")
	}
}

func TestCleanLine_CollapsesEverythingOntoOneRow(t *testing.T) {
	t.Parallel()

	cases := []struct{ in, want string }{
		{"first\nsecond", "first second"},
		{"  padded \t out  ", "padded out"},
		{"esc\x1b[31min the middle", "esc[31min the middle"},
		{"", ""},
		{"\x1b\x07", ""},
	}
	for _, tc := range cases {
		if got := CleanLine(tc.in); got != tc.want {
			t.Fatalf("CleanLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
