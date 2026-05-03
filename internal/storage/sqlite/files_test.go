package sqlite_test

import (
	"errors"
	"testing"
	"time"

	"github.com/pgmac/lazytg/internal/core/domain"
	"github.com/pgmac/lazytg/internal/storage/sqlite"
)

func TestDownloadedFiles_RoundTrip(t *testing.T) {
	repo, ctx := openTestRepo(t)

	// missing → ok=false, err=nil so callers branch cleanly without
	// having to interpret an error sentinel.
	path, size, ok, err := repo.GetDownloadedPath(ctx, 4242)
	if err != nil {
		t.Fatalf("get on empty: %v", err)
	}
	if ok || path != "" || size != 0 {
		t.Fatalf("expected empty result, got path=%q size=%d ok=%v", path, size, ok)
	}

	now := time.Now().UTC().Truncate(time.Second)
	rec := sqlite.DownloadedFile{
		FileID:       4242,
		AccessHash:   1234567890,
		Path:         "/tmp/lazytg/photo.jpg",
		Size:         98765,
		DownloadedAt: now,
	}
	if err := repo.SaveDownloadedFile(ctx, rec); err != nil {
		t.Fatalf("save: %v", err)
	}

	path, size, ok, err = repo.GetDownloadedPath(ctx, 4242)
	if err != nil {
		t.Fatalf("get after save: %v", err)
	}
	if !ok || path != rec.Path || size != rec.Size {
		t.Fatalf("expected (%q,%d,true), got (%q,%d,%v)", rec.Path, rec.Size, path, size, ok)
	}

	// Idempotent UPSERT: same file_id, different path → second wins.
	rec2 := sqlite.DownloadedFile{
		FileID: 4242,
		Path:   "/Users/me/Downloads/lazytg/photo.jpg",
		Size:   98765,
	}
	if err := repo.SaveDownloadedFile(ctx, rec2); err != nil {
		t.Fatalf("save 2: %v", err)
	}
	path, _, ok, err = repo.GetDownloadedPath(ctx, 4242)
	if err != nil || !ok {
		t.Fatalf("get after upsert: ok=%v err=%v", ok, err)
	}
	if path != rec2.Path {
		t.Fatalf("expected upsert to overwrite path: got %q want %q", path, rec2.Path)
	}
}

func TestDownloadedFiles_Validation(t *testing.T) {
	repo, ctx := openTestRepo(t)

	if err := repo.SaveDownloadedFile(ctx, sqlite.DownloadedFile{Path: "/tmp/x"}); err == nil {
		t.Fatalf("expected error on zero file_id, got nil")
	}
	if err := repo.SaveDownloadedFile(ctx, sqlite.DownloadedFile{FileID: 1}); err == nil {
		t.Fatalf("expected error on empty path, got nil")
	}
	if _, _, _, err := repo.GetDownloadedPath(ctx, 0); err == nil {
		t.Fatalf("expected error on zero file_id, got nil")
	}
}

func TestDownloadedFiles_ReadOnly(t *testing.T) {
	repo, ctx := openTestRepo(t)
	repo.SetReadOnly(true)
	err := repo.SaveDownloadedFile(ctx, sqlite.DownloadedFile{
		FileID: 1, Path: "/tmp/x",
	})
	if !errors.Is(err, sqlite.ErrReadOnly) {
		t.Fatalf("expected ErrReadOnly, got %v", err)
	}
}

// TestMessages_MediaRoundTrip verifies that the Media columns added in
// migration 0008 round-trip through SaveMessage / GetMessages cleanly:
// nil-Media on save → nil-Media on load, populated MediaInfo → equal
// MediaInfo. Also covers SaveMessages batch path so both write helpers
// stay in lockstep.
func TestMessages_MediaRoundTrip(t *testing.T) {
	repo, ctx := openTestRepo(t)
	now := time.Now().UTC().Truncate(time.Second)

	// Need a chat to satisfy the messages.chat_id FK.
	if err := repo.SaveChat(ctx, domain.Chat{
		ID:    777,
		Type:  domain.ChatTypePrivate,
		Title: "Bob",
	}); err != nil {
		t.Fatalf("save chat: %v", err)
	}

	plain := domain.Message{
		ID:     1,
		ChatID: 777,
		FromID: 100,
		Date:   now,
		Text:   "hello",
	}
	withDoc := domain.Message{
		ID:     2,
		ChatID: 777,
		FromID: 100,
		Date:   now.Add(time.Second),
		Text:   "see attached",
		Media: &domain.MediaInfo{
			Kind:          domain.MediaKindDocument,
			FileID:        555666777,
			AccessHash:    9876543210,
			FileReference: []byte{0x01, 0x02, 0x03, 0x04},
			DC:            2,
			Filename:      "report.pdf",
			Size:          234567,
			MimeType:      "application/pdf",
		},
	}
	withPhoto := domain.Message{
		ID:     3,
		ChatID: 777,
		FromID: 200,
		Date:   now.Add(2 * time.Second),
		Text:   "",
		Media: &domain.MediaInfo{
			Kind:          domain.MediaKindPhoto,
			FileID:        888,
			AccessHash:    -123,
			FileReference: []byte{0xff, 0xee},
			DC:            5,
			Filename:      "photo_888.jpg",
			Size:          120_345,
			ThumbSize:     "y",
		},
	}

	if err := repo.SaveMessage(ctx, plain); err != nil {
		t.Fatalf("save plain: %v", err)
	}
	if err := repo.SaveMessages(ctx, []domain.Message{withDoc, withPhoto}); err != nil {
		t.Fatalf("save batch: %v", err)
	}

	got, err := repo.GetMessages(ctx, 777, 10, 0)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(got))
	}

	// GetMessages orders newest-first; flatten by ID for assertions.
	byID := make(map[int64]domain.Message, len(got))
	for _, m := range got {
		byID[m.ID] = m
	}
	if byID[1].Media != nil {
		t.Fatalf("plain message must round-trip with nil media, got %+v", byID[1].Media)
	}
	doc := byID[2].Media
	if doc == nil {
		t.Fatalf("doc message lost media on round-trip")
	}
	if doc.Kind != domain.MediaKindDocument || doc.FileID != withDoc.Media.FileID ||
		doc.AccessHash != withDoc.Media.AccessHash || doc.DC != 2 ||
		doc.Filename != "report.pdf" || doc.Size != 234567 || doc.MimeType != "application/pdf" {
		t.Fatalf("doc media mismatch: %+v", doc)
	}
	if string(doc.FileReference) != string(withDoc.Media.FileReference) {
		t.Fatalf("doc file_reference mismatch: %x vs %x",
			doc.FileReference, withDoc.Media.FileReference)
	}
	photo := byID[3].Media
	if photo == nil {
		t.Fatalf("photo message lost media on round-trip")
	}
	if photo.Kind != domain.MediaKindPhoto || photo.FileID != 888 ||
		photo.AccessHash != -123 || photo.DC != 5 || photo.ThumbSize != "y" {
		t.Fatalf("photo media mismatch: %+v", photo)
	}
}
