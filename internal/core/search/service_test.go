package search_test

import (
	"strings"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/search"
)

// TestService_Search_BasicMatch covers the happy path — the FTS5 trigram
// MATCH returns a hit, the snippet contains the bold markers and the
// underlying domain.Message round-trips through the JOIN.
func TestService_Search_BasicMatch(t *testing.T) {
	repo, ctx := openTestRepo(t)
	const chatID int64 = 12001
	seedChat(t, repo, ctx, chatID, "Service")

	if err := repo.SaveMessage(ctx, domain.Message{
		ID:     1,
		ChatID: chatID,
		FromID: 42,
		Date:   time.Date(2025, 12, 1, 12, 0, 0, 0, time.UTC),
		Text:   "Привет любимый коллега",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := repo.SaveMessage(ctx, domain.Message{
		ID:     2,
		ChatID: chatID,
		FromID: 43,
		Date:   time.Date(2025, 12, 2, 12, 0, 0, 0, time.UTC),
		Text:   "обед в час",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	svc := search.NewService(repo, nil, nil)
	hits, err := svc.Search(ctx, "привет", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1 (got %+v)", len(hits), hits)
	}
	got := hits[0]
	if got.Message.ID != 1 {
		t.Errorf("Message.ID = %d, want 1", got.Message.ID)
	}
	if got.Message.ChatID != chatID {
		t.Errorf("Message.ChatID = %d, want %d", got.Message.ChatID, chatID)
	}
	if got.ChatID != chatID {
		t.Errorf("Hit.ChatID = %d, want %d", got.ChatID, chatID)
	}
	if got.Message.FromID != 42 {
		t.Errorf("Message.FromID = %d, want 42", got.Message.FromID)
	}
	if !strings.Contains(got.Snippet, "<b>") || !strings.Contains(got.Snippet, "</b>") {
		t.Errorf("Snippet = %q, want bold markers", got.Snippet)
	}
}

// TestService_Search_EmptyRejected guards the trim+reject path so we do
// not accidentally hand FTS5 an empty MATCH expression (it would return
// every row, which is both wrong and slow).
func TestService_Search_EmptyRejected(t *testing.T) {
	repo, ctx := openTestRepo(t)
	svc := search.NewService(repo, nil, nil)
	if _, err := svc.Search(ctx, "   ", 10); err == nil {
		t.Errorf("empty query: got nil err, want non-nil")
	}
}

// TestService_Search_DefaultLimit verifies that limit <= 0 falls back to
// DefaultSearchLimit so UI callers can omit the argument during early
// debugging without accidentally getting zero rows.
func TestService_Search_DefaultLimit(t *testing.T) {
	repo, ctx := openTestRepo(t)
	const chatID int64 = 12002
	seedChat(t, repo, ctx, chatID, "Limit")

	for i := 0; i < 60; i++ {
		if err := repo.SaveMessage(ctx, domain.Message{
			ID:     int64(i + 1),
			ChatID: chatID,
			Date:   time.Now().UTC().Add(time.Duration(i) * time.Second),
			Text:   "тестовое сообщение",
		}); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}

	svc := search.NewService(repo, nil, nil)
	hits, err := svc.Search(ctx, "тест", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != search.DefaultSearchLimit {
		t.Errorf("hits = %d, want %d (default limit)", len(hits), search.DefaultSearchLimit)
	}
}

// TestService_Search_HydratesMedia verifies that a hit on a media
// message round-trips MediaInfo through the search SQL — the thread
// pane needs Media populated for the [📎 …] badge to render and for
// Ctrl-D to find a downloadable target.
func TestService_Search_HydratesMedia(t *testing.T) {
	repo, ctx := openTestRepo(t)
	const chatID int64 = 12003
	seedChat(t, repo, ctx, chatID, "MediaSearch")

	if err := repo.SaveMessage(ctx, domain.Message{
		ID:     1,
		ChatID: chatID,
		FromID: 7,
		Date:   time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		Text:   "квартальный отчёт",
		Media: &domain.MediaInfo{
			Kind:     domain.MediaKindDocument,
			FileID:   1234,
			Filename: "Q4-report.pdf",
			Size:     54321,
			MimeType: "application/pdf",
		},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	svc := search.NewService(repo, nil, nil)
	hits, err := svc.Search(ctx, "отчёт", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(hits))
	}
	got := hits[0].Message
	if got.Media == nil {
		t.Fatalf("Media: want non-nil, got nil")
	}
	if got.Media.Filename != "Q4-report.pdf" {
		t.Errorf("Media.Filename = %q, want Q4-report.pdf", got.Media.Filename)
	}
	if got.Media.Size != 54321 {
		t.Errorf("Media.Size = %d, want 54321", got.Media.Size)
	}
	if got.Media.Kind != domain.MediaKindDocument {
		t.Errorf("Media.Kind = %q, want document", got.Media.Kind)
	}
}
