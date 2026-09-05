package graphics

import (
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envFrom(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

func TestDetect_RecognisesTheTerminalsThatImplementIt(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		env  map[string]string
		want Protocol
	}{
		{"ghostty", map[string]string{"TERM": "xterm-ghostty"}, ProtocolKitty},
		{"kitty", map[string]string{"TERM": "xterm-kitty"}, ProtocolKitty},
		{"kitty by window id", map[string]string{"TERM": "xterm-256color", "KITTY_WINDOW_ID": "1"}, ProtocolKitty},
		{"wezterm", map[string]string{"TERM": "xterm-256color", "TERM_PROGRAM": "WezTerm"}, ProtocolKitty},
		{"plain xterm", map[string]string{"TERM": "xterm-256color"}, ProtocolNone},
		{"nothing at all", map[string]string{}, ProtocolNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Detect(envFrom(tc.env)); got != tc.want {
				t.Fatalf("Detect(%v) = %q, want %q", tc.env, got, tc.want)
			}
		})
	}
}

// A multiplexer hides the outer terminal and does not forward the escape
// unless configured to. Reporting the outer terminal's capability would paint
// base64 into the user's pane — the exact failure this whole detection exists
// to avoid.
func TestDetect_RefusesInsideAMultiplexer(t *testing.T) {
	t.Parallel()

	inTmux := map[string]string{"TERM": "xterm-kitty", "TMUX": "/tmp/tmux-501/default"}
	if got := Detect(envFrom(inTmux)); got != ProtocolNone {
		t.Fatalf("inside tmux = %q, want none", got)
	}
	inScreen := map[string]string{"TERM": "screen.xterm-kitty"}
	if got := Detect(envFrom(inScreen)); got != ProtocolNone {
		t.Fatalf("inside screen = %q, want none", got)
	}
	// …unless the user, who knows their passthrough setup, says otherwise.
	inTmux[OverrideEnv] = "kitty"
	if got := Detect(envFrom(inTmux)); got != ProtocolKitty {
		t.Fatalf("override inside tmux = %q, want kitty", got)
	}
}

func TestDetect_OverrideCanTurnItOff(t *testing.T) {
	t.Parallel()

	env := map[string]string{"TERM": "xterm-ghostty", OverrideEnv: "none"}
	if got := Detect(envFrom(env)); got != ProtocolNone {
		t.Fatalf("override none = %q, want none", got)
	}
}

func writePNG(t *testing.T, dir string, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	path := filepath.Join(dir, "test.png")
	f, err := os.Create(path) //nolint:gosec
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _ = f.Close() }()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return path
}

// writeNoisePNG writes an image PNG cannot compress, so the payload is
// certain to exceed one protocol chunk.
func writeNoisePNG(t *testing.T, dir string, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	seed := uint32(12345)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			seed = seed*1664525 + 1013904223
			img.Set(x, y, color.RGBA{
				R: uint8(seed >> 24), G: uint8(seed >> 16), B: uint8(seed >> 8), A: 255,
			})
		}
	}
	path := filepath.Join(dir, "noise.png")
	f, err := os.Create(path) //nolint:gosec
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _ = f.Close() }()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return path
}

func TestEncode_FitsTheImageIntoTheCellBox(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// A wide image: 400×100 pixels. In cells, where one cell is twice as
	// tall as it is wide, that is a 4:1 picture drawn at 8:1 in cells.
	path := writePNG(t, dir, 400, 100)

	img, err := Encode(path, 7, 40, 20, 2)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if img.Cols != 40 {
		t.Fatalf("cols = %d, want the full width", img.Cols)
	}
	if img.Rows != 5 {
		t.Fatalf("rows = %d, want 5 (40 cols ÷ 8)", img.Rows)
	}
	if img.Rows > 20 {
		t.Fatalf("rows = %d, exceeds the box", img.Rows)
	}
}

// A tall image has to be limited by the row budget instead, or it runs off
// the bottom of the pane and takes the conversation with it.
func TestEncode_ClampsATallImageByRows(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writePNG(t, dir, 100, 400)

	img, err := Encode(path, 7, 40, 6, 2)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if img.Rows != 6 {
		t.Fatalf("rows = %d, want the row cap", img.Rows)
	}
	if img.Cols > 40 {
		t.Fatalf("cols = %d, exceeds the box", img.Cols)
	}
}

func TestEncode_ProducesAKittyTransmitSequence(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writePNG(t, dir, 64, 64)

	img, err := Encode(path, 7, 20, 20, 2)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.HasPrefix(img.Escape, "\x1b_G") {
		t.Fatalf("escape does not start with the APC introducer: %.20q", img.Escape)
	}
	if !strings.HasSuffix(img.Escape, "\x1b\\") {
		t.Fatalf("escape is not terminated: %.20q", img.Escape[len(img.Escape)-10:])
	}
	// f=100 is PNG, and it is the one compressed format every
	// implementation accepts.
	if !strings.Contains(img.Escape, "f=100") {
		t.Fatal("escape does not declare a PNG payload")
	}
	// C=1 keeps the cursor still: the caller lays out the rows itself, and
	// a moved cursor would double-count them.
	if !strings.Contains(img.Escape, "C=1") {
		t.Fatal("escape does not suppress cursor movement")
	}
}

// The protocol obliges a terminal to accept only 4096 base64 bytes per
// escape, so anything larger must arrive in chunks with m=1 on all but the
// last.
func TestEncode_ChunksALargePayload(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Noise compresses badly, which is the point: this has to exceed one
	// chunk after PNG encoding. A gradient would not — PNG would squeeze
	// it under 4096 base64 bytes and the test would pass by proving
	// nothing.
	path := writeNoisePNG(t, dir, 400, 400)

	img, err := Encode(path, 7, 80, 40, 2)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	chunks := strings.Count(img.Escape, "\x1b_G")
	if chunks < 2 {
		t.Fatalf("payload arrived in %d chunk(s); the test image is too small to prove chunking", chunks)
	}
	if !strings.Contains(img.Escape, "m=1") {
		t.Fatal("no continuation marker on a chunked payload")
	}
	if strings.Count(img.Escape, "m=0") != 1 {
		t.Fatalf("want exactly one terminating chunk, got %d", strings.Count(img.Escape, "m=0"))
	}
}

func TestEncode_ReadsJPEGToo(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "photo.jpg")
	f, err := os.Create(path) //nolint:gosec
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	if err := jpeg.Encode(f, img, nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	_ = f.Close()

	// Telegram photos arrive as JPEG; a decoder that only read PNG would
	// cover nothing that actually happens.
	if _, err := Encode(path, 7, 20, 20, 2); err != nil {
		t.Fatalf("Encode(jpeg): %v", err)
	}
}

func TestEncode_RefusesWhatItCannotDraw(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "video.mp4")
	if err := os.WriteFile(path, []byte("not an image"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Encode(path, 7, 20, 20, 2); err == nil {
		t.Fatal("a file that is not an image should be refused, not guessed at")
	}
}

func TestEncode_RefusesAnImpossibleBox(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writePNG(t, dir, 32, 32)
	if _, err := Encode(path, 7, 0, 10, 2); err == nil {
		t.Fatal("zero columns should be refused")
	}
}

// A picture is anchored by the terminal outside the text grid, so writing the
// escape again at a new row leaves the previous copy where it was. Every
// escape therefore opens by deleting its own id's placements — otherwise a
// thread that scrolls multiplies the photo down the pane.
func TestEncode_DeletesItsOwnPlacementsFirst(t *testing.T) {
	t.Parallel()

	path := writePNG(t, t.TempDir(), 40, 40)
	img, err := Encode(path, 99, 20, 20, 2)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.HasPrefix(img.Escape, "\x1b_Ga=d,d=i,i=99,q=2\x1b\\") {
		t.Fatalf("escape does not open with a delete for its own id: %.60q", img.Escape)
	}
	if !strings.Contains(img.Escape, "i=99,") {
		t.Fatal("the transmission carries no image id, so the delete has nothing to match")
	}
	if img.ID != 99 {
		t.Fatalf("Image.ID = %d, want 99", img.ID)
	}
}

// Every draw would otherwise be acknowledged on stdin, and the TUI's input
// parser would have to swallow a reply for each one.
func TestEncode_SilencesTheTerminalsReply(t *testing.T) {
	t.Parallel()

	path := writePNG(t, t.TempDir(), 40, 40)
	img, err := Encode(path, 3, 20, 20, 2)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.Contains(img.Escape, "q=2") {
		t.Fatal("no q=2: the terminal will answer every draw")
	}
}

// Zero is not a valid image number, so a terminal handed one has to guess —
// and every picture guessed into the same slot deletes the last.
func TestEncode_RefusesImageIDZero(t *testing.T) {
	t.Parallel()

	path := writePNG(t, t.TempDir(), 10, 10)
	if _, err := Encode(path, 0, 20, 20, 2); err == nil {
		t.Fatal("Encode accepted image id 0")
	}
}

func TestDeleteImage_TargetsOneID(t *testing.T) {
	t.Parallel()

	got := DeleteImage(1234)
	if !strings.Contains(got, "i=1234") {
		t.Fatalf("DeleteImage does not name the image: %q", got)
	}
	if strings.Contains(got, "d=A") {
		t.Fatalf("DeleteImage wipes everything instead of one picture: %q", got)
	}
}
