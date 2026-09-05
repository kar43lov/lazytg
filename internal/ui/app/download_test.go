package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
	"github.com/kar43lov/lazytg/internal/ui/keymap"
	"github.com/kar43lov/lazytg/internal/ui/panes/thread"
)

// fakeDownloader records every call so tests can assert on the chat
// id / title / media info without reaching the on-disk pipeline.
type fakeDownloader struct {
	mu    sync.Mutex
	calls []dlCall
	err   error
}

type dlCall struct {
	chatID    int64
	chatTitle string
	media     domain.MediaInfo
}

func (f *fakeDownloader) Download(_ context.Context, chatID int64, chatTitle string, info domain.MediaInfo) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, dlCall{chatID: chatID, chatTitle: chatTitle, media: info})
	if f.err != nil {
		return "", f.err
	}
	return "/tmp/x", nil
}

func (f *fakeDownloader) snapshot() []dlCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]dlCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// newAppWithDownloader builds an App with a fake downloader wired and
// the thread pane pre-loaded with one media-bearing message — the
// minimum the Ctrl-D path needs to fire.
func newAppWithDownloader(t *testing.T, dl FileDownloader, withMedia bool) App {
	t.Helper()
	threadModel := thread.New()
	if withMedia {
		threadModel = injectMessages(threadModel, []domain.Message{
			{ID: 1, ChatID: 42, Date: time.Now(), Text: "first"},
			{ID: 2, ChatID: 42, Date: time.Now(), Media: &domain.MediaInfo{
				Kind: domain.MediaKindDocument, FileID: 7777,
				Filename: "report.pdf", Size: 1024,
			}},
		})
	}
	a := New(Deps{Keymap: keymap.Default(), Thread: &threadModel, Downloader: dl})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a = model.(App)
	// Cycle focus to the thread pane (chats → input → thread).
	for i := 0; i < 2; i++ {
		m, cmd := a.Update(keyChord(tea.KeyTab, 0))
		if cmd != nil {
			m, _ = m.Update(cmd())
		}
		a = m.(App)
	}
	return a
}

// injectMessages bypasses the bubbletea Update plumbing to seed the
// thread pane with messages directly. We can't push through the real
// applyLoaded path without spinning up a repo; reaching into the
// model via SetSize-then-applyLoaded would also drag in the loadCmd
// goroutine. The unexported helper lives here so the test stays
// hermetic.
func injectMessages(m thread.Model, msgs []domain.Message) thread.Model {
	// thread.Model exposes the messages slice via Messages() which
	// returns a copy; to seed we use the OpenChat → applyLoaded path
	// indirectly by assigning through unexported state via a helper
	// in the thread package. The simplest alternative is to drive
	// through events.MessageReceived which appends to messages — use
	// that here so we stay on the public contract.
	for _, msg := range msgs {
		updated, _ := m.OpenChat(msg.ChatID)
		// OpenChat clears state; we want incremental appends instead.
		// Use applyIncoming via the public Update path with a
		// MessageReceived event.
		m = updated
	}
	for _, msg := range msgs {
		ev := events.MessageReceived{
			ChatID: msg.ChatID, MessageID: msg.ID, FromID: msg.FromID,
			Date: msg.Date, Text: msg.Text, Media: msg.Media,
			// Direction matters to anything that asks "is this mine":
			// editing refuses somebody else's message, and the author
			// label in a 1:1 dialog has nothing else to go on.
			Outgoing: msg.Outgoing,
			// The reply pointer rides along for the same reason as the
			// direction: the pane renders the quoted line from it, and a
			// fixture that drops it hides every defect in that path.
			ReplyTo:   msg.ReplyTo,
			Reactions: msg.Reactions,
			// Formatting and the link behind words live in the entities;
			// a fixture without them hides every path that reads them.
			Entities: msg.Entities,
			EditDate: msg.EditDate,
			Buttons:  msg.Buttons,
		}
		updated, _ := m.Update(ev)
		m = updated
	}
	return m
}

func TestDownloadChord_FiresWhenLatestMediaPresent(t *testing.T) {
	t.Parallel()
	dl := &fakeDownloader{}
	a := newAppWithDownloader(t, dl, true)

	model, cmd := a.Update(keyChord('d', tea.ModCtrl))
	if cmd == nil {
		t.Fatalf("Ctrl-D with media must produce a Cmd")
	}
	// The cmd produces a thread.DownloadRequestedMsg; route it back
	// through Update to trigger the goroutine.
	msg := cmd()
	if _, ok := msg.(thread.DownloadRequestedMsg); !ok {
		t.Fatalf("expected DownloadRequestedMsg, got %T", msg)
	}
	model, _ = model.Update(msg)
	a = model.(App)
	_ = a

	// Wait briefly for the goroutine to call into the fake.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(dl.snapshot()) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	calls := dl.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 download call, got %d", len(calls))
	}
	if calls[0].chatID != 42 || calls[0].media.FileID != 7777 {
		t.Fatalf("wrong call payload: %+v", calls[0])
	}
}

func TestDownloadChord_NoMediaIsConsumedNoop(t *testing.T) {
	t.Parallel()
	dl := &fakeDownloader{}
	a := newAppWithDownloader(t, dl, false)

	_, cmd := a.Update(keyChord('d', tea.ModCtrl))
	// The chord is consumed (the global handler returns handled=true)
	// to prevent an unfocused fall-through; all it produces is a line
	// for the status bar saying there was nothing to save. The fake
	// downloader must not have been called.
	if cmd == nil {
		t.Fatal("Ctrl-D with nothing to save must still say so")
	}
	if n, ok := cmd().(noticeMsg); !ok || !strings.Contains(string(n), "nothing to download") {
		t.Fatalf("expected a notice, got %#v", cmd())
	}
	if len(dl.snapshot()) != 0 {
		t.Fatalf("downloader called without media: %+v", dl.snapshot())
	}
}

func TestDownloadChord_NilDownloaderIgnoresMessage(t *testing.T) {
	t.Parallel()
	a := newAppWithDownloader(t, nil, true)
	a.downloader = nil // Belt + braces: explicitly nil even after Deps.

	// Send the DownloadRequestedMsg directly — handleDownloadRequest
	// must not panic when the downloader is unwired.
	req := thread.DownloadRequestedMsg{
		ChatID: 1, MessageID: 1, ChatTitle: "x",
		Media: domain.MediaInfo{Kind: domain.MediaKindDocument, FileID: 1},
	}
	_, cmd := a.Update(req)
	if cmd != nil {
		t.Fatalf("nil downloader path must not produce a cmd")
	}
}

func TestDownloadEvents_RoutedToStatusbar(t *testing.T) {
	t.Parallel()
	a := newApp(t)

	model, _ := a.Update(events.FileDownloadStarted{
		FileID: 5, ChatID: 1, Path: "/tmp/x", Filename: "doc.pdf", Size: 1000,
	})
	a = model.(App)
	out := a.View().Content
	if !contains(out, "doc.pdf") {
		t.Fatalf("status bar should show download filename: %q", excerpt(out))
	}

	model, _ = a.Update(events.FileDownloadProgress{
		FileID: 5, BytesDownloaded: 500, TotalBytes: 1000,
	})
	a = model.(App)
	out = a.View().Content
	if !contains(out, "50%") {
		t.Fatalf("status bar should show 50%%: %q", excerpt(out))
	}

	model, _ = a.Update(events.FileDownloadCompleted{FileID: 5})
	a = model.(App)
	out = a.View().Content
	if contains(out, "doc.pdf") {
		t.Fatalf("completed download must drop from status bar: %q", excerpt(out))
	}

	// Failed path follows the same teardown as Completed.
	a, _ = applyMsg(t, newApp(t), events.FileDownloadStarted{FileID: 6, Filename: "f.bin"})
	a, _ = applyMsg(t, a, events.FileDownloadFailed{FileID: 6, Err: "boom"})
	out = a.View().Content
	if contains(out, "f.bin") {
		t.Fatalf("failed download must drop from status bar: %q", excerpt(out))
	}
}

func TestDownloadChord_InFlightGuardDropsDuplicate(t *testing.T) {
	t.Parallel()
	// Block the fake downloader on a release channel so the second
	// Ctrl-D press lands while the first goroutine is still running.
	// Without the in-flight guard the second goroutine would race the
	// first over the same .partial path; the guard turns the second
	// chord into a quiet no-op.
	release := make(chan struct{})
	dl := &blockingDownloader{release: release}
	a := newAppWithDownloader(t, dl, true)

	req := thread.DownloadRequestedMsg{
		ChatID: 42, MessageID: 2, ChatTitle: "title",
		Media: domain.MediaInfo{
			Kind: domain.MediaKindDocument, FileID: 7777,
			Filename: "report.pdf", Size: 1024,
		},
	}
	// First request — reserve+spawn.
	model, _ := a.Update(req)
	a = model.(App)
	// Wait until the goroutine actually entered Download (so we know
	// the FileID is reserved).
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if dl.entered() == 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if dl.entered() != 1 {
		t.Fatalf("first download must have started, got entered=%d", dl.entered())
	}

	// Second request — must be dropped by the guard.
	model, _ = a.Update(req)
	a = model.(App)
	// Give a brief window for any (unwanted) goroutine to enter.
	time.Sleep(20 * time.Millisecond)
	if got := dl.entered(); got != 1 {
		t.Fatalf("second chord must be dropped while first in flight, entered=%d", got)
	}

	// Release the first goroutine, wait for it to exit.
	close(release)
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if dl.exited() == 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if dl.exited() != 1 {
		t.Fatalf("first goroutine must have returned, exited=%d", dl.exited())
	}

	// Third request after release — fileID is free again, must spawn.
	model, _ = a.Update(thread.DownloadRequestedMsg{
		ChatID: 42, MessageID: 3, ChatTitle: "title",
		Media: domain.MediaInfo{
			Kind: domain.MediaKindDocument, FileID: 7777,
			Filename: "report.pdf", Size: 1024,
		},
	})
	a = model.(App)
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if dl.entered() == 2 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if dl.entered() != 2 {
		t.Fatalf("third chord (after release) must spawn, entered=%d", dl.entered())
	}
	_ = a
}

// blockingDownloader holds inside Download until release is closed
// so tests can observe the in-flight window without flakiness.
type blockingDownloader struct {
	mu       sync.Mutex
	enterCnt int
	exitCnt  int
	release  chan struct{}
}

func (d *blockingDownloader) Download(ctx context.Context, _ int64, _ string, _ domain.MediaInfo) (string, error) {
	d.mu.Lock()
	d.enterCnt++
	d.mu.Unlock()
	select {
	case <-d.release:
	case <-ctx.Done():
	}
	d.mu.Lock()
	d.exitCnt++
	d.mu.Unlock()
	return "/tmp/x", nil
}

func (d *blockingDownloader) entered() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.enterCnt
}

func (d *blockingDownloader) exited() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.exitCnt
}

func TestDownloadFailureLog_NoOpWithoutLogger(t *testing.T) {
	t.Parallel()
	dl := &fakeDownloader{err: errors.New("boom")}
	a := newAppWithDownloader(t, dl, true)
	model, cmd := a.Update(keyChord('d', tea.ModCtrl))
	model, _ = model.Update(cmd())
	a = model.(App)
	_ = a

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(dl.snapshot()) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(dl.snapshot()) != 1 {
		t.Fatalf("expected 1 (failed) call, got %d", len(dl.snapshot()))
	}
}

// applyMsg pumps msg through Update and returns the resulting App
// alongside the produced Cmd (if any).
func applyMsg(t *testing.T, a App, msg tea.Msg) (App, tea.Cmd) {
	t.Helper()
	model, cmd := a.Update(msg)
	return model.(App), cmd
}

func contains(s, sub string) bool { return indexOf(s, sub) >= 0 }
func excerpt(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
