package files

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/pgmac/lazytg/internal/core/events"
)

// fakeTGUploader records each Upload call and replays a scripted
// handle / error. Calls is a slice so concurrent tests (none here)
// would still see a stable record; we keep it for symmetry with the
// download fake.
type fakeTGUploader struct {
	handle       any
	err          error
	calls        int
	lastPath     string
	emitProgress bool
}

func (f *fakeTGUploader) Upload(_ context.Context, path string, progress UploadProgressCallback) (any, error) {
	f.calls++
	f.lastPath = path
	if f.emitProgress && progress != nil {
		// Emit a couple of progress callbacks so the throttler is
		// exercised; values picked to cross both the 1 MiB threshold
		// and the 5% threshold so shouldEmit definitely fires twice.
		progress(1, 4)
		progress(4, 4)
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.handle, nil
}

// fakeSender records SendMedia calls and replays a scripted msg id /
// error.
type fakeSender struct {
	messageID   int64
	err         error
	calls       int
	lastChat    int64
	lastHandle  any
	lastCaption string
	lastReplyTo int
}

func (f *fakeSender) SendMedia(_ context.Context, chatID int64, handle any, caption string, replyTo int) (int64, error) {
	f.calls++
	f.lastChat = chatID
	f.lastHandle = handle
	f.lastCaption = caption
	f.lastReplyTo = replyTo
	if f.err != nil {
		return 0, f.err
	}
	return f.messageID, nil
}

// recorderBus collects every published event so the test can assert
// on the exact sequence.
type recorderBus struct {
	events []events.Event
}

func (r *recorderBus) Publish(ev events.Event) {
	r.events = append(r.events, ev)
}

// writeFile creates a regular file at path with size bytes.
func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	f, err := os.Create(path) //nolint:gosec
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _ = f.Close() }()
	if size <= 0 {
		return
	}
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = byte(i % 256)
	}
	if _, err := f.Write(buf); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestUploadService_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.pdf")
	writeFile(t, path, 1024)

	tg := &fakeTGUploader{handle: "fakehandle", emitProgress: true}
	sender := &fakeSender{messageID: 4242}
	bus := &recorderBus{}
	svc, err := NewUploadService(tg, sender, bus, nil)
	if err != nil {
		t.Fatalf("NewUploadService: %v", err)
	}

	uploadID, err := svc.SendFile(t.Context(), 99, path, "look", 0)
	if err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if uploadID == 0 {
		t.Fatalf("uploadID = 0, expected non-zero")
	}
	if tg.calls != 1 {
		t.Fatalf("Upload calls = %d, want 1", tg.calls)
	}
	if sender.calls != 1 {
		t.Fatalf("SendMedia calls = %d, want 1", sender.calls)
	}
	if sender.lastChat != 99 {
		t.Fatalf("SendMedia chatID = %d, want 99", sender.lastChat)
	}
	if sender.lastCaption != "look" {
		t.Fatalf("SendMedia caption = %q", sender.lastCaption)
	}
	if sender.lastHandle != "fakehandle" {
		t.Fatalf("SendMedia handle = %v", sender.lastHandle)
	}

	if len(bus.events) < 2 {
		t.Fatalf("expected Started + Completed, got %d events: %+v", len(bus.events), bus.events)
	}
	started, ok := bus.events[0].(events.FileUploadStarted)
	if !ok {
		t.Fatalf("first event = %T, want FileUploadStarted", bus.events[0])
	}
	if started.UploadID != uploadID || started.ChatID != 99 || started.Filename != "report.pdf" || started.Size != 1024 {
		t.Fatalf("started: %+v", started)
	}

	completed, ok := bus.events[len(bus.events)-1].(events.FileUploadCompleted)
	if !ok {
		t.Fatalf("last event = %T, want FileUploadCompleted", bus.events[len(bus.events)-1])
	}
	if completed.UploadID != uploadID || completed.MessageID != 4242 {
		t.Fatalf("completed: %+v", completed)
	}
}

func TestUploadService_LargeFileEmitsWarning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.bin")
	// Create a sparse file > UploadSoftLimit (50 MiB) without
	// allocating real bytes — Truncate on a seekable file produces a
	// hole on every common filesystem.
	f, err := os.Create(path) //nolint:gosec
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.Truncate(UploadSoftLimit + 1); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	_ = f.Close()

	tg := &fakeTGUploader{handle: "h"}
	sender := &fakeSender{messageID: 1}
	bus := &recorderBus{}
	svc, _ := NewUploadService(tg, sender, bus, nil)
	if _, err := svc.SendFile(t.Context(), 1, path, "", 0); err != nil {
		t.Fatalf("SendFile: %v", err)
	}

	// Expect Started, Warning, Completed in that order.
	if len(bus.events) < 3 {
		t.Fatalf("expected 3 events, got %d: %+v", len(bus.events), bus.events)
	}
	if _, ok := bus.events[0].(events.FileUploadStarted); !ok {
		t.Fatalf("event 0 = %T, want Started", bus.events[0])
	}
	warn, ok := bus.events[1].(events.FileUploadWarning)
	if !ok {
		t.Fatalf("event 1 = %T, want Warning", bus.events[1])
	}
	if warn.Threshold != UploadSoftLimit {
		t.Fatalf("warning threshold = %d, want %d", warn.Threshold, UploadSoftLimit)
	}
}

func TestUploadService_RejectsHardLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tooBig.bin")
	f, err := os.Create(path) //nolint:gosec
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.Truncate(UploadHardLimit + 1); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	_ = f.Close()

	tg := &fakeTGUploader{}
	sender := &fakeSender{}
	bus := &recorderBus{}
	svc, _ := NewUploadService(tg, sender, bus, nil)
	if _, err := svc.SendFile(t.Context(), 1, path, "", 0); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("err = %v, want ErrFileTooLarge", err)
	}
	if tg.calls != 0 || sender.calls != 0 {
		t.Fatalf("uploader/sender should not be called for oversize file: %d/%d", tg.calls, sender.calls)
	}
	if len(bus.events) != 0 {
		t.Fatalf("no events should be published, got: %+v", bus.events)
	}
}

func TestUploadService_PathDoesNotExist(t *testing.T) {
	tg := &fakeTGUploader{}
	sender := &fakeSender{}
	svc, _ := NewUploadService(tg, sender, nil, nil)
	_, err := svc.SendFile(t.Context(), 1, "/no/such/file/here.txt", "", 0)
	if err == nil {
		t.Fatalf("expected error for missing file, got nil")
	}
	if tg.calls != 0 {
		t.Fatalf("Upload should not be called when stat fails")
	}
}

func TestUploadService_RejectsNonRegularFile(t *testing.T) {
	dir := t.TempDir()
	tg := &fakeTGUploader{}
	sender := &fakeSender{}
	svc, _ := NewUploadService(tg, sender, nil, nil)
	if _, err := svc.SendFile(t.Context(), 1, dir, "", 0); err == nil {
		t.Fatalf("expected error when path is a directory, got nil")
	}
}

func TestUploadService_UploadFailureEmitsFailedEvent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	writeFile(t, path, 16)

	tg := &fakeTGUploader{err: errors.New("network down")}
	sender := &fakeSender{}
	bus := &recorderBus{}
	svc, _ := NewUploadService(tg, sender, bus, nil)

	_, err := svc.SendFile(t.Context(), 1, path, "", 0)
	if err == nil {
		t.Fatalf("expected error from underlying tg.Upload")
	}
	if sender.calls != 0 {
		t.Fatalf("SendMedia should not run after upload failure")
	}
	// Events: Started + Failed.
	if len(bus.events) != 2 {
		t.Fatalf("expected 2 events (Started, Failed), got %d: %+v", len(bus.events), bus.events)
	}
	if _, ok := bus.events[1].(events.FileUploadFailed); !ok {
		t.Fatalf("last event = %T, want Failed", bus.events[1])
	}
}

func TestUploadService_SendFailureEmitsFailedEvent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	writeFile(t, path, 16)

	tg := &fakeTGUploader{handle: "h"}
	sender := &fakeSender{err: errors.New("validation: caption too long")}
	bus := &recorderBus{}
	svc, _ := NewUploadService(tg, sender, bus, nil)

	if _, err := svc.SendFile(t.Context(), 1, path, "", 0); err == nil {
		t.Fatalf("expected error from sendMedia")
	}
	if len(bus.events) != 2 {
		t.Fatalf("expected Started + Failed, got %d: %+v", len(bus.events), bus.events)
	}
	if _, ok := bus.events[1].(events.FileUploadFailed); !ok {
		t.Fatalf("last event = %T, want Failed", bus.events[1])
	}
}

func TestUploadService_RejectsZeroChatID(t *testing.T) {
	tg := &fakeTGUploader{}
	sender := &fakeSender{}
	svc, _ := NewUploadService(tg, sender, nil, nil)
	if _, err := svc.SendFile(t.Context(), 0, "anything", "", 0); err == nil {
		t.Fatalf("expected error for chat_id=0")
	}
}

func TestNewUploadService_RejectsNilDeps(t *testing.T) {
	if _, err := NewUploadService(nil, &fakeSender{}, nil, nil); err == nil {
		t.Fatalf("expected error for nil tg uploader")
	}
	if _, err := NewUploadService(&fakeTGUploader{}, nil, nil, nil); err == nil {
		t.Fatalf("expected error for nil sender")
	}
}
