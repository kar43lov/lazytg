package app

import (
	"context"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/pgmac/lazytg/internal/core/events"
	"github.com/pgmac/lazytg/internal/ui/keymap"
	"github.com/pgmac/lazytg/internal/ui/panes/attach"
	"github.com/pgmac/lazytg/internal/ui/panes/thread"
)

// fakeUploader records every SendFile call so tests can assert on the
// arguments without spinning up a real gotd uploader.
type fakeUploader struct {
	mu       sync.Mutex
	calls    []ulCall
	uploadID int64
	err      error
}

type ulCall struct {
	chatID  int64
	path    string
	caption string
	replyTo int
}

func (f *fakeUploader) SendFile(_ context.Context, chatID int64, path, caption string, replyTo int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, ulCall{chatID: chatID, path: path, caption: caption, replyTo: replyTo})
	if f.err != nil {
		return 0, f.err
	}
	return f.uploadID, nil
}

func (f *fakeUploader) snapshot() []ulCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ulCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// newAppWithUploader builds an App pre-configured with a fake uploader
// and (optionally) the thread pane open on a chat — Ctrl-U needs a
// chat id from the thread to know where to upload to.
func newAppWithUploader(t *testing.T, up *fakeUploader, withChat bool) App {
	t.Helper()
	threadModel := thread.New()
	if withChat {
		threadModel, _ = threadModel.OpenChat(42)
	}
	a := New(Deps{Keymap: keymap.Default(), Thread: &threadModel, Uploader: up})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a = model.(App)
	return a
}

func TestAttachChord_OpensOverlayWhenChatIsOpen(t *testing.T) {
	t.Parallel()
	a := newAppWithUploader(t, &fakeUploader{}, true)

	model, cmd := a.Update(keyChord('u', tea.ModCtrl))
	if cmd == nil {
		t.Fatalf("Ctrl-U with chat open must produce a Cmd")
	}
	msg := cmd()
	openMsg, ok := msg.(attach.OpenedMsg)
	if !ok {
		t.Fatalf("expected attach.OpenedMsg, got %T", msg)
	}
	if openMsg.ChatID != 42 {
		t.Fatalf("OpenedMsg.ChatID = %d, want 42", openMsg.ChatID)
	}
	model, _ = model.Update(msg)
	a = model.(App)
	if !a.AttachVisible() {
		t.Fatalf("overlay should be visible after Ctrl-U")
	}
}

func TestAttachChord_NoChatIsConsumedNoop(t *testing.T) {
	t.Parallel()
	a := newAppWithUploader(t, &fakeUploader{}, false)
	_, cmd := a.Update(keyChord('u', tea.ModCtrl))
	if cmd != nil {
		t.Fatalf("expected no cmd when no chat open, got %T", cmd)
	}
}

func TestAttachSubmit_DispatchesToUploader(t *testing.T) {
	t.Parallel()
	up := &fakeUploader{uploadID: 99}
	a := newAppWithUploader(t, up, true)

	// Open the overlay manually so we don't need a real Cmd round-trip.
	model, _ := a.Update(attach.OpenedMsg{ChatID: 42})
	a = model.(App)

	// Submit a SubmitMsg directly (the overlay would normally produce
	// one via Enter on a regular file).
	model, _ = a.Update(attach.SubmitMsg{ChatID: 42, Path: "/tmp/x.txt", Caption: "hi"})
	a = model.(App)

	if a.AttachVisible() {
		t.Fatalf("submit should close overlay")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(up.snapshot()) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	calls := up.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 upload call, got %d", len(calls))
	}
	if calls[0].chatID != 42 || calls[0].path != "/tmp/x.txt" || calls[0].caption != "hi" {
		t.Fatalf("call = %+v", calls[0])
	}
}

func TestAttachSubmit_NilUploaderIsHarmless(t *testing.T) {
	t.Parallel()
	a := newAppWithUploader(t, nil, true)
	a.uploader = nil // belt + braces
	model, cmd := a.Update(attach.SubmitMsg{ChatID: 42, Path: "/tmp/x", Caption: ""})
	if cmd != nil {
		t.Fatalf("nil uploader path must not produce a cmd")
	}
	a = model.(App)
	if a.AttachVisible() {
		t.Fatalf("submit should still close overlay even when uploader is nil")
	}
}

func TestAttachEsc_ClosesOverlay(t *testing.T) {
	t.Parallel()
	a := newAppWithUploader(t, &fakeUploader{}, true)
	model, _ := a.Update(attach.OpenedMsg{ChatID: 42})
	a = model.(App)
	if !a.AttachVisible() {
		t.Fatalf("overlay should be visible after open")
	}
	model, _ = a.Update(attach.ClosedMsg{})
	a = model.(App)
	if a.AttachVisible() {
		t.Fatalf("ClosedMsg should hide overlay")
	}
}

func TestUploadEvents_RoutedToStatusbar(t *testing.T) {
	t.Parallel()
	a := newApp(t)

	model, _ := a.Update(events.FileUploadStarted{
		UploadID: 11, ChatID: 1, Path: "/tmp/x", Filename: "doc.bin", Size: 2000,
	})
	a = model.(App)
	out := a.View().Content
	if !contains(out, "doc.bin") {
		t.Fatalf("status bar should show upload filename: %q", excerpt(out))
	}
	if !contains(out, "⬆") {
		t.Fatalf("status bar should show upload glyph: %q", excerpt(out))
	}

	model, _ = a.Update(events.FileUploadProgress{
		UploadID: 11, BytesUploaded: 1000, TotalBytes: 2000,
	})
	a = model.(App)
	out = a.View().Content
	if !contains(out, "50%") {
		t.Fatalf("status bar should show 50%% upload: %q", excerpt(out))
	}

	model, _ = a.Update(events.FileUploadCompleted{UploadID: 11})
	a = model.(App)
	out = a.View().Content
	if contains(out, "doc.bin") {
		t.Fatalf("completed upload must drop from status bar: %q", excerpt(out))
	}

	// Failed path follows the same teardown.
	a, _ = applyMsg(t, newApp(t), events.FileUploadStarted{UploadID: 12, Filename: "f.bin"})
	a, _ = applyMsg(t, a, events.FileUploadFailed{UploadID: 12, Err: "boom"})
	out = a.View().Content
	if contains(out, "f.bin") {
		t.Fatalf("failed upload must drop from status bar: %q", excerpt(out))
	}
}

func TestUploadWarning_DoesNotPanic(t *testing.T) {
	t.Parallel()
	a := newApp(t)
	// Just ensure the routing arm exists and the event is consumed.
	_, _ = a.Update(events.FileUploadWarning{UploadID: 1, Size: 100, Threshold: 10})
}
