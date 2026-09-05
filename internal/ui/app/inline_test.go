package app

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/ui/graphics"
	"github.com/kar43lov/lazytg/internal/ui/keymap"
	"github.com/kar43lov/lazytg/internal/ui/panes/chats"
	"github.com/kar43lov/lazytg/internal/ui/panes/thread"
)

// photoDownloader hands back a real PNG on disk, because the inline path
// does not stop at the download: it decodes and re-encodes the file, so a
// fake returning "/tmp/x" would exercise half of it and report an error the
// user would never see.
type photoDownloader struct {
	path  string
	calls int
}

func (d *photoDownloader) Download(_ context.Context, _ int64, _ string, _ domain.MediaInfo) (string, error) {
	d.calls++
	return d.path, nil
}

func writePNG(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 0x40, A: 0xff})
		}
	}
	path := filepath.Join(t.TempDir(), "photo.png")
	f, err := os.Create(path) //nolint:gosec // t.TempDir
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _ = f.Close() }()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return path
}

// newAppWithPhoto seeds the thread with a text message and, newest, a photo —
// newest because an unplaced cursor answers with the last message, which is
// the state the user is in when they open a chat and press the key.
func newAppWithPhoto(t *testing.T, dl FileDownloader, proto graphics.Protocol, kind domain.MediaKind) App {
	t.Helper()
	threadModel := thread.New()
	threadModel = injectMessages(threadModel, []domain.Message{
		{ID: 1, ChatID: 42, Date: time.Now(), Text: "look at this"},
		{ID: 2, ChatID: 42, Date: time.Now(), Media: &domain.MediaInfo{
			Kind: kind, FileID: 4242, Filename: "photo.jpg", Size: 2048,
		}},
	})
	a := New(Deps{Keymap: keymap.Default(), Thread: &threadModel, Downloader: dl})
	a.imageProtocol = proto
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a = model.(App)
	for i := 0; i < 2; i++ {
		m, cmd := a.Update(keyChord(tea.KeyTab, 0))
		if cmd != nil {
			m, _ = m.Update(cmd())
		}
		a = m.(App)
	}
	return a
}

func pressInline(t *testing.T, a App) App {
	t.Helper()
	model, cmd := a.Update(keyChord('i', 0))
	a = model.(App)
	if cmd == nil {
		return a
	}
	msg := cmd()
	if msg == nil {
		return a
	}
	model, _ = a.Update(msg)
	return model.(App)
}

func TestInlineKey_DrawsAPhoto(t *testing.T) {
	t.Parallel()

	dl := &photoDownloader{path: writePNG(t, 64, 48)}
	a := newAppWithPhoto(t, dl, graphics.ProtocolKitty, domain.MediaKindPhoto)

	a = pressInline(t, a)

	if dl.calls != 1 {
		t.Fatalf("downloader called %d times, want 1", dl.calls)
	}
	if !a.thread.HasImage(2) {
		t.Fatal("the photo was fetched but never drawn")
	}
	if !strings.Contains(a.View().Content, "\x1b_G") {
		t.Fatal("no graphics escape reached the screen")
	}
}

// rawEscape digs the terminal sequence out of whatever a command produced,
// walking a batch if that is what came back.
func rawEscape(t *testing.T, msg tea.Msg) string {
	t.Helper()
	switch m := msg.(type) {
	case tea.RawMsg:
		s, _ := m.Msg.(string)
		return s
	case tea.BatchMsg:
		for _, c := range m {
			if c == nil {
				continue
			}
			if got := rawEscape(t, c()); got != "" {
				return got
			}
		}
	}
	return ""
}

// Hiding has to tell the terminal, not just stop rendering the escape: the
// picture is placed outside the text grid and painting spaces over those
// cells leaves it on screen.
func TestInlineKey_HidingWipesThePictureFromTheTerminal(t *testing.T) {
	t.Parallel()

	dl := &photoDownloader{path: writePNG(t, 32, 32)}
	a := newAppWithPhoto(t, dl, graphics.ProtocolKitty, domain.MediaKindPhoto)
	a = pressInline(t, a)

	_, cmd := a.Update(keyChord('i', 0))
	if cmd == nil {
		t.Fatal("hiding produced no command, so the terminal was never told")
	}
	if got := rawEscape(t, cmd()); !strings.Contains(got, "a=d") {
		t.Fatalf("no delete escape reached the terminal: %q", got)
	}
}

// Same reason, for a whole chat: the pictures of the conversation being left
// would sit on top of the one being opened.
func TestOpenChat_WipesDrawnPictures(t *testing.T) {
	t.Parallel()

	dl := &photoDownloader{path: writePNG(t, 32, 32)}
	a := newAppWithPhoto(t, dl, graphics.ProtocolKitty, domain.MediaKindPhoto)
	a = pressInline(t, a)
	if !a.thread.HasImage(2) {
		t.Fatal("setup: no picture drawn")
	}

	_, cmd := a.Update(chats.ChatSelectedMsg{ChatID: 77})
	if cmd == nil {
		t.Fatal("switching chats produced no command")
	}
	if got := rawEscape(t, cmd()); !strings.Contains(got, "a=d") {
		t.Fatalf("last chat's pictures were left on screen: %q", got)
	}
}

func TestInlineKey_TogglesOff(t *testing.T) {
	t.Parallel()

	dl := &photoDownloader{path: writePNG(t, 32, 32)}
	a := newAppWithPhoto(t, dl, graphics.ProtocolKitty, domain.MediaKindPhoto)

	a = pressInline(t, a)
	a = pressInline(t, a)

	if a.thread.HasImage(2) {
		t.Fatal("the second press left the picture on screen")
	}
	// Hiding must not re-fetch: the second press is a hide, and a download
	// on it would spend the user's bandwidth to remove something.
	if dl.calls != 1 {
		t.Fatalf("downloader called %d times across a show and a hide, want 1", dl.calls)
	}
}

// A terminal that cannot draw must not be sent the escape: it prints the
// base64 payload as text, which is worse than the badge it had.
func TestInlineKey_RefusesWhenTheTerminalCannotDraw(t *testing.T) {
	t.Parallel()

	dl := &photoDownloader{path: writePNG(t, 32, 32)}
	a := newAppWithPhoto(t, dl, graphics.ProtocolNone, domain.MediaKindPhoto)

	a = pressInline(t, a)

	if dl.calls != 0 {
		t.Fatalf("downloaded %d files for a terminal that cannot show them", dl.calls)
	}
	if a.thread.HasImage(2) {
		t.Fatal("an image was installed for a terminal that cannot draw")
	}
	if !strings.Contains(a.statusText(), "cannot draw") {
		t.Fatalf("no explanation shown; status = %q", a.statusText())
	}
}

// Video is deliberately out of scope: showing its first frame needs a decoder
// this client does not have, and a wrong picture is worse than none. The user
// gets told to press o instead of getting silence.
func TestInlineKey_RefusesNonPhotoMedia(t *testing.T) {
	t.Parallel()

	dl := &photoDownloader{path: writePNG(t, 32, 32)}
	a := newAppWithPhoto(t, dl, graphics.ProtocolKitty, domain.MediaKindVideo)

	a = pressInline(t, a)

	if dl.calls != 0 {
		t.Fatalf("downloaded %d files for media that cannot be drawn", dl.calls)
	}
	if !strings.Contains(a.statusText(), "photos") {
		t.Fatalf("no explanation shown; status = %q", a.statusText())
	}
}

// The box is what the thread can spare, not what the terminal has: a picture
// filling the pane leaves the user looking at one photo with no idea what was
// said around it.
func TestInlineImageBox_LeavesRoomForTheConversation(t *testing.T) {
	t.Parallel()

	dl := &photoDownloader{path: writePNG(t, 32, 32)}
	a := newAppWithPhoto(t, dl, graphics.ProtocolKitty, domain.MediaKindPhoto)

	cols, rows := a.inlineImageBox()
	l := a.layout()
	if cols >= l.threadW {
		t.Fatalf("box is %d cols wide in a %d-cell pane", cols, l.threadW)
	}
	if rows > l.paneH/2 {
		t.Fatalf("box is %d rows tall, more than half of %d", rows, l.paneH)
	}
	if cols < 1 || rows < 1 {
		t.Fatalf("degenerate box %dx%d", cols, rows)
	}
}

// statusText is the status bar as plain text. The bar is width-aware, so a
// test asking "did it say why" has to give it one.
func (a App) statusText() string {
	return ansi.Strip(a.status.View(a.width))
}
