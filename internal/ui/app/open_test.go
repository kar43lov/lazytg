package app

import (
	"context"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/ui/input"
	"github.com/kar43lov/lazytg/internal/ui/keymap"
	"github.com/kar43lov/lazytg/internal/ui/panes/thread"
)

// fakeOpener records the paths it was asked to show. No window opens, so
// the test is safe on a CI runner with no display.
type fakeOpener struct {
	mu    sync.Mutex
	paths []string
}

func (f *fakeOpener) Open(_ context.Context, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, path)
	return nil
}

func (f *fakeOpener) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

// newAppForOpen builds an App focused on the thread pane, holding two
// attachments so the cursor has somewhere to move.
func newAppForOpen(t *testing.T, dl FileDownloader, opener MediaOpener) App {
	t.Helper()
	threadModel := thread.New()
	now := time.Now()
	threadModel = injectMessages(threadModel, []domain.Message{
		{ID: 1, ChatID: 42, Date: now, Media: &domain.MediaInfo{
			Kind: domain.MediaKindVideoNote, FileID: 111,
			Filename: "video_note_111.mp4", Size: 1024, Duration: 7,
		}},
		{ID: 2, ChatID: 42, Date: now.Add(time.Second), Text: "and a word after it"},
		{ID: 3, ChatID: 42, Date: now.Add(2 * time.Second), Media: &domain.MediaInfo{
			Kind: domain.MediaKindPhoto, FileID: 222,
			Filename: "photo_222.jpg", Size: 2048,
		}},
	})
	a := New(Deps{Keymap: keymap.Default(), Thread: &threadModel, Downloader: dl, Opener: opener})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a = model.(App)
	a = tabTo(t, a, FocusThread)
	return a
}

// The open chord downloads the attachment the cursor points at and hands
// the resulting path to the viewer.
func TestOpenChord_DownloadsThenShows(t *testing.T) {
	t.Parallel()

	dl := &fakeDownloader{}
	opener := &fakeOpener{}
	a := newAppForOpen(t, dl, opener)

	model, cmd := a.Update(keyText("o"))
	if cmd == nil {
		t.Fatalf("the open chord produced no Cmd")
	}
	msg := cmd()
	req, ok := msg.(thread.OpenRequestedMsg)
	if !ok {
		t.Fatalf("expected OpenRequestedMsg, got %T", msg)
	}
	// Untouched cursor: the newest attachment, which is the photo.
	if req.Media.FileID != 222 {
		t.Fatalf("open targeted file %d, want the newest attachment (222)", req.Media.FileID)
	}
	if _, cmd := model.Update(msg); cmd != nil {
		t.Fatalf("handling the open request should not produce a Cmd, got one")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		if paths := opener.snapshot(); len(paths) == 1 {
			if paths[0] != "/tmp/x" {
				t.Fatalf("viewer got %q, want the downloaded path", paths[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("viewer was never called; downloads = %d", len(dl.snapshot()))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if calls := dl.snapshot(); len(calls) != 1 || calls[0].media.FileID != 222 {
		t.Fatalf("download calls = %+v, want one for file 222", calls)
	}
}

// With the cursor moved, both media chords follow it. Before the cursor
// existed every attachment but the newest was unreachable.
func TestOpenChord_FollowsTheCursor(t *testing.T) {
	t.Parallel()

	a := newAppForOpen(t, &fakeDownloader{}, &fakeOpener{})

	// Up three times: newest (3), then 2, then 1 — the video note.
	for i := 0; i < 3; i++ {
		model, _ := a.Update(keyChord(tea.KeyUp, 0))
		a = model.(App)
	}

	_, cmd := a.Update(keyText("o"))
	if cmd == nil {
		t.Fatalf("the open chord produced no Cmd")
	}
	req, ok := cmd().(thread.OpenRequestedMsg)
	if !ok {
		t.Fatalf("expected OpenRequestedMsg, got %T", cmd())
	}
	if req.Media.FileID != 111 {
		t.Fatalf("open targeted file %d, want the video note under the cursor (111)", req.Media.FileID)
	}
	if req.MessageID != 1 {
		t.Fatalf("open named message %d, want 1", req.MessageID)
	}
}

// "o" is a bare letter, so it must not reach the app while the composer
// has focus — a client that swallows a letter is worse than one without
// the shortcut.
func TestOpenChord_IsInertOutsideTheThread(t *testing.T) {
	t.Parallel()

	a := newAppForOpen(t, &fakeDownloader{}, &fakeOpener{})
	a = tabTo(t, a, FocusInput)

	model, _ := a.Update(keyText("o"))
	a = model.(App)
	if got := a.input.Value(); got != "o" {
		t.Fatalf("composer holds %q after typing \"o\"; the chord swallowed the keystroke", got)
	}
}

// Reply arms the message under the cursor. It used to arm the newest one
// unconditionally, so answering the message above quoted the wrong one.
func TestReplyChord_FollowsTheCursor(t *testing.T) {
	t.Parallel()

	a := newAppForOpen(t, &fakeDownloader{}, &fakeOpener{})
	for i := 0; i < 2; i++ {
		model, _ := a.Update(keyChord(tea.KeyUp, 0)) // newest (3), then 2
		a = model.(App)
	}
	// Reply is the composer's chord, so the cursor is set from the thread
	// and the chord is pressed from the input pane — which is exactly the
	// sequence a user performs.
	a = tabTo(t, a, FocusInput)

	model, cmd := a.Update(keyChord('r', tea.ModCtrl))
	a = model.(App)
	for cmd != nil {
		msg := cmd()
		if msg == nil {
			break
		}
		model, cmd = a.Update(msg)
		a = model.(App)
		if _, ok := msg.(input.SetReplyMsg); ok {
			break
		}
	}

	target := a.input.ReplyTo()
	if target == nil {
		t.Fatalf("reply armed nothing")
	}
	if target.ID != 2 {
		t.Fatalf("reply armed message %d, want the one under the cursor (2)", target.ID)
	}
}

// tabTo cycles focus with Tab until the wanted pane has it, draining the
// command each press returns so the focus change actually lands.
func tabTo(t *testing.T, a App, want FocusTarget) App {
	t.Helper()
	for i := 0; i < 4; i++ {
		if a.Focus() == want {
			return a
		}
		m, cmd := a.Update(keyChord(tea.KeyTab, 0))
		if cmd != nil {
			m, _ = m.Update(cmd())
		}
		a = m.(App)
	}
	t.Fatalf("focus never reached %s, stuck at %s", want, a.Focus())
	return a
}
