package files

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pgmac/lazytg/internal/core/domain"
	"github.com/pgmac/lazytg/internal/core/events"
)

// fakeDownloader implements the Downloader interface with a static byte
// payload so the orchestration logic can be exercised without gotd.
type fakeDownloader struct {
	mu       sync.Mutex
	calls    int
	payload  []byte
	err      error
	progress []int64 // bytesSoFar at each callback we triggered
}

func (f *fakeDownloader) Download(_ context.Context, _ domain.MediaInfo, w io.Writer, cb ProgressCallback) (int64, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	// Stream payload in 256-byte chunks so the progress throttler has
	// something to chew on (the throttler emits every 1 MiB; small files
	// still get the initial 0% + final 100% events).
	const chunk = 256
	for i := 0; i < len(f.payload); i += chunk {
		end := i + chunk
		if end > len(f.payload) {
			end = len(f.payload)
		}
		n, err := w.Write(f.payload[i:end])
		if err != nil {
			return int64(i + n), err
		}
		if cb != nil {
			cb(int64(end), int64(len(f.payload)))
			f.mu.Lock()
			f.progress = append(f.progress, int64(end))
			f.mu.Unlock()
		}
	}
	return int64(len(f.payload)), nil
}

// fakeBus collects every published event so tests can assert on the
// emit order without spinning up the real bus.
type fakeBus struct {
	mu   sync.Mutex
	evts []events.Event
}

func (b *fakeBus) Publish(ev events.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.evts = append(b.evts, ev)
}

func (b *fakeBus) snapshot() []events.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]events.Event, len(b.evts))
	copy(out, b.evts)
	return out
}

// fakeDedupStore is an in-memory DedupStore, sized to the test scope.
type fakeDedupStore struct {
	mu   sync.Mutex
	rows map[int64]DedupRecord
}

func newFakeDedup() *fakeDedupStore { return &fakeDedupStore{rows: map[int64]DedupRecord{}} }

func (s *fakeDedupStore) GetDownloadedPath(_ context.Context, fileID int64) (string, int64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[fileID]
	if !ok {
		return "", 0, false, nil
	}
	return r.Path, r.Size, true, nil
}

func (s *fakeDedupStore) SaveDownloadedFile(_ context.Context, f DedupRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[f.FileID] = f
	return nil
}

func setupService(t *testing.T, payload []byte) (*DownloadService, *fakeDownloader, *fakeBus, *FileStore, *fakeDedupStore) {
	t.Helper()
	root := t.TempDir()
	store, err := NewFileStore(root, nil)
	if err != nil {
		t.Fatalf("file store: %v", err)
	}
	dedupStore := newFakeDedup()
	dedup, err := NewDedupCache(dedupStore)
	if err != nil {
		t.Fatalf("dedup cache: %v", err)
	}
	dl := &fakeDownloader{payload: payload}
	bus := &fakeBus{}
	svc, err := NewDownloadService(dl, store, dedup, bus, nil)
	if err != nil {
		t.Fatalf("svc: %v", err)
	}
	return svc, dl, bus, store, dedupStore
}

func TestDownloadService_HappyPath(t *testing.T) {
	payload := bytes.Repeat([]byte("AB"), 3000) // 6000 bytes
	svc, _, bus, store, dedup := setupService(t, payload)

	info := domain.MediaInfo{
		Kind:       domain.MediaKindDocument,
		FileID:     999,
		AccessHash: 1,
		Filename:   "report.pdf",
		Size:       int64(len(payload)),
	}
	path, err := svc.Download(t.Context(), 100, "Family Group", info)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	wantDir := filepath.Join(store.Root(), "Family Group")
	if filepath.Dir(path) != wantDir {
		t.Fatalf("path %q not under %q", path, wantDir)
	}
	if filepath.Base(path) != "report.pdf" {
		t.Fatalf("filename = %q", filepath.Base(path))
	}

	on, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(on, payload) {
		t.Fatalf("payload mismatch: got %d bytes, want %d", len(on), len(payload))
	}
	// .partial cleaned up after rename.
	if _, err := os.Stat(path + ".partial"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf(".partial leaked: %v", err)
	}
	// Final file is 0600.
	info2, _ := os.Stat(path)
	if info2.Mode().Perm() != filePerm {
		t.Fatalf("final perm %o want %o", info2.Mode().Perm(), filePerm)
	}

	evts := bus.snapshot()
	if len(evts) < 2 {
		t.Fatalf("expected Started + Completed at minimum, got %d", len(evts))
	}
	if _, ok := evts[0].(events.FileDownloadStarted); !ok {
		t.Fatalf("first event = %T, want Started", evts[0])
	}
	if _, ok := evts[len(evts)-1].(events.FileDownloadCompleted); !ok {
		t.Fatalf("last event = %T, want Completed", evts[len(evts)-1])
	}
	// Dedup cache was updated.
	if _, ok := dedup.rows[999]; !ok {
		t.Fatalf("dedup row not persisted")
	}
}

func TestDownloadService_DedupHit(t *testing.T) {
	payload := []byte("hello")
	svc, dl, bus, store, dedupStore := setupService(t, payload)
	// Pre-populate the cache with an existing on-disk file.
	pre := store.Path("Bob", "saved.bin")
	_ = os.MkdirAll(filepath.Dir(pre), dirPerm)
	if err := os.WriteFile(pre, []byte("cached"), filePerm); err != nil {
		t.Fatalf("write cached: %v", err)
	}
	dedupStore.rows[42] = DedupRecord{FileID: 42, Path: pre, Size: 6}

	got, err := svc.Download(t.Context(), 1, "Bob", domain.MediaInfo{
		Kind: domain.MediaKindDocument, FileID: 42, Filename: "saved.bin", Size: 6,
	})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if got != pre {
		t.Fatalf("dedup hit returned different path: %q vs %q", got, pre)
	}
	if dl.calls != 0 {
		t.Fatalf("downloader was called on cache hit (%d calls)", dl.calls)
	}
	// Even on a cache hit we must emit Started + Completed so the UI
	// status bar can render the "cached" outcome consistently.
	evts := bus.snapshot()
	if len(evts) != 2 {
		t.Fatalf("expected 2 events on cache hit, got %d", len(evts))
	}
}

func TestDownloadService_DedupHitButFileMissing(t *testing.T) {
	svc, dl, _, store, dedupStore := setupService(t, []byte("payload"))
	// Cache row points at a missing path → fall back to download.
	dedupStore.rows[7] = DedupRecord{
		FileID: 7,
		Path:   filepath.Join(store.Root(), "ghost", "nope.bin"),
		Size:   7,
	}
	if _, err := svc.Download(t.Context(), 1, "Bob", domain.MediaInfo{
		Kind: domain.MediaKindDocument, FileID: 7, Filename: "real.bin",
	}); err != nil {
		t.Fatalf("download: %v", err)
	}
	if dl.calls != 1 {
		t.Fatalf("expected 1 download call after stale dedup, got %d", dl.calls)
	}
}

func TestDownloadService_Failure(t *testing.T) {
	svc, dl, bus, _, _ := setupService(t, nil)
	dl.err = errors.New("boom")

	_, err := svc.Download(t.Context(), 1, "Bob", domain.MediaInfo{
		Kind: domain.MediaKindDocument, FileID: 9, Filename: "x.bin",
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	evts := bus.snapshot()
	if len(evts) < 2 {
		t.Fatalf("expected Started + Failed, got %d events", len(evts))
	}
	if _, ok := evts[len(evts)-1].(events.FileDownloadFailed); !ok {
		t.Fatalf("last event = %T, want Failed", evts[len(evts)-1])
	}
	// .partial must be cleaned up on failure.
	matches, _ := filepath.Glob(filepath.Join(t.TempDir(), "*.partial"))
	if len(matches) != 0 {
		t.Fatalf(".partial leaked on failure: %v", matches)
	}
}

func TestDownloadService_SanitisesChatTitle(t *testing.T) {
	svc, _, _, store, _ := setupService(t, []byte("ok"))
	if _, err := svc.Download(t.Context(), 1, "weird/title", domain.MediaInfo{
		Kind: domain.MediaKindDocument, FileID: 100, Filename: "f.bin",
	}); err != nil {
		t.Fatalf("download: %v", err)
	}
	want := filepath.Join(store.Root(), "weird_title", "f.bin")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected sanitised dir, stat err: %v", err)
	}
}

func TestDownloadService_ZeroFileIDRejected(t *testing.T) {
	svc, _, _, _, _ := setupService(t, []byte("ok"))
	_, err := svc.Download(t.Context(), 1, "Bob", domain.MediaInfo{
		Kind: domain.MediaKindDocument, FileID: 0, Filename: "x.bin",
	})
	if err == nil || !strings.Contains(err.Error(), "FileID") {
		t.Fatalf("expected FileID error, got %v", err)
	}
}
