package files

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// Telegram's filenames are not unique and are not chosen by the sender:
// every video shot on a phone arrives as "video.mp4". Two of them in one
// chat used to resolve to one path, so the second download overwrote the
// first — and the dedup cache then held two file ids pointing at one
// file and served the wrong bytes for the older of them.
//
// Reproduced from real data: a mirror with eight attachments, all named
// video.mp4, all in the same chat.
func TestFileStore_ReserveDoesNotReuseAName(t *testing.T) {
	t.Parallel()

	store, err := NewFileStore(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("file store: %v", err)
	}

	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		path, f, err := store.Reserve("Chat", "video.mp4")
		if err != nil {
			t.Fatalf("reserve %d: %v", i, err)
		}
		if seen[path] {
			t.Fatalf("reserve %d handed out %q again — a second download would overwrite the first", i, path)
		}
		seen[path] = true
		// Stand in for a completed download so the name stays taken.
		if _, err := f.WriteString("bytes"); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		if err := os.Rename(path+".partial", path); err != nil {
			t.Fatalf("rename: %v", err)
		}
	}

	// The first keeps the sender's name; the rest are suffixed, so the
	// download directory stays readable rather than becoming a list of
	// file ids.
	first := filepath.Join(store.Root(), "Chat", "video.mp4")
	if !seen[first] {
		t.Fatalf("the first download did not keep the plain name %q", first)
	}
	for path := range seen {
		if !strings.HasSuffix(path, ".mp4") {
			t.Fatalf("reserved %q lost its extension — the system viewer dispatches on it", path)
		}
	}
}

// Reserve claims the name by creating the .partial exclusively, so two
// downloads starting at the same moment cannot both take it. Checking
// first and creating after would leave exactly that window open.
func TestFileStore_ReserveClaimsTheNameBeforeReturning(t *testing.T) {
	t.Parallel()

	store, err := NewFileStore(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("file store: %v", err)
	}

	first, f1, err := store.Reserve("Chat", "photo.jpg")
	if err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	defer func() { _ = f1.Close() }()

	// Nothing has been renamed into place yet — only the .partial exists.
	if _, err := os.Stat(first); err == nil {
		t.Fatalf("Reserve created the final file; it must only claim the .partial")
	}

	second, f2, err := store.Reserve("Chat", "photo.jpg")
	if err != nil {
		t.Fatalf("second reserve: %v", err)
	}
	defer func() { _ = f2.Close() }()

	if second == first {
		t.Fatalf("both reservations returned %q while the first was still in flight", first)
	}
}

// A file with no extension still gets a distinct name rather than
// growing a suffix in the middle of nothing.
func TestFileStore_ReserveHandlesNamesWithoutAnExtension(t *testing.T) {
	t.Parallel()

	store, err := NewFileStore(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("file store: %v", err)
	}
	first, f1, err := store.Reserve("Chat", "README")
	if err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	_ = f1.Close()
	if err := os.Rename(first+".partial", first); err != nil {
		t.Fatalf("rename: %v", err)
	}
	second, f2, err := store.Reserve("Chat", "README")
	if err != nil {
		t.Fatalf("second reserve: %v", err)
	}
	_ = f2.Close()
	if second == first {
		t.Fatalf("extensionless name was reused: %q", second)
	}
}

// The end-to-end version of the same defect: two downloads of different
// attachments that happen to share a filename must leave two files, each
// holding its own bytes.
func TestDownloadService_TwoAttachmentsWithOneNameKeepTheirOwnBytes(t *testing.T) {
	first := bytes.Repeat([]byte("A"), 64)
	svc, dl, _, _, _ := setupService(t, first)

	pathA, err := svc.Download(t.Context(), 100, "Chat", domain.MediaInfo{
		Kind: domain.MediaKindVideo, FileID: 1, AccessHash: 1,
		Filename: "video.mp4", Size: int64(len(first)),
	})
	if err != nil {
		t.Fatalf("first download: %v", err)
	}

	second := bytes.Repeat([]byte("B"), 64)
	dl.payload = second
	pathB, err := svc.Download(t.Context(), 100, "Chat", domain.MediaInfo{
		Kind: domain.MediaKindVideo, FileID: 2, AccessHash: 2,
		Filename: "video.mp4", Size: int64(len(second)),
	})
	if err != nil {
		t.Fatalf("second download: %v", err)
	}

	if pathA == pathB {
		t.Fatalf("both attachments landed on %q — the second overwrote the first", pathA)
	}
	gotA, err := os.ReadFile(pathA) //nolint:gosec
	if err != nil {
		t.Fatalf("read first: %v", err)
	}
	if !bytes.Equal(gotA, first) {
		t.Fatalf("the first attachment no longer holds its own bytes")
	}
	gotB, err := os.ReadFile(pathB) //nolint:gosec
	if err != nil {
		t.Fatalf("read second: %v", err)
	}
	if !bytes.Equal(gotB, second) {
		t.Fatalf("the second attachment does not hold its own bytes")
	}
}
