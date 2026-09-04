package search_test

import (
	"errors"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/search"
)

// TestService_JumpContext_CentredWindow verifies the ±N window is
// returned in id-asc order with the target index pointing at the
// matched row. 100 messages × around=5 produces 11 rows centred on
// id=50.
func TestService_JumpContext_CentredWindow(t *testing.T) {
	repo, ctx := openTestRepo(t)
	const chatID int64 = 30001
	seedChat(t, repo, ctx, chatID, "Jump")

	for i := 1; i <= 100; i++ {
		if err := repo.SaveMessage(ctx, domain.Message{
			ID:     int64(i),
			ChatID: chatID,
			FromID: 7,
			Date:   time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC),
			Text:   "msg",
		}); err != nil {
			t.Fatalf("save: %v", err)
		}
	}

	svc := search.NewService(repo, nil, nil)
	hit := search.Hit{
		Message: domain.Message{ID: 50, ChatID: chatID},
		ChatID:  chatID,
	}
	msgs, target, err := svc.JumpContext(ctx, hit, 5)
	if err != nil {
		t.Fatalf("JumpContext: %v", err)
	}
	if len(msgs) != 11 {
		t.Fatalf("expected 11 messages (5 + target + 5), got %d", len(msgs))
	}
	if target != 5 {
		t.Fatalf("target index: want 5, got %d", target)
	}
	if msgs[target].ID != 50 {
		t.Fatalf("target message id: want 50, got %d", msgs[target].ID)
	}
	// Window must span ids 45..55 ascending.
	for i, m := range msgs {
		want := int64(45 + i)
		if m.ID != want {
			t.Fatalf("msgs[%d].ID = %d, want %d", i, m.ID, want)
		}
	}
}

// TestService_JumpContext_BoundaryStart verifies the slice is shorter
// at the start of a chat (no messages with id < 1).
func TestService_JumpContext_BoundaryStart(t *testing.T) {
	repo, ctx := openTestRepo(t)
	const chatID int64 = 30002
	seedChat(t, repo, ctx, chatID, "Start")

	for i := 1; i <= 10; i++ {
		if err := repo.SaveMessage(ctx, domain.Message{
			ID:     int64(i),
			ChatID: chatID,
			Date:   time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC),
			Text:   "msg",
		}); err != nil {
			t.Fatalf("save: %v", err)
		}
	}

	svc := search.NewService(repo, nil, nil)
	hit := search.Hit{Message: domain.Message{ID: 2, ChatID: chatID}, ChatID: chatID}
	msgs, target, err := svc.JumpContext(ctx, hit, 5)
	if err != nil {
		t.Fatalf("JumpContext: %v", err)
	}
	// Before window: id=1 (only one row < 2). After window: ids 2..7
	// (the +1 includes the target itself). Total = 1 + 6 = 7.
	if len(msgs) != 7 {
		t.Fatalf("expected 7 messages at start boundary, got %d", len(msgs))
	}
	if msgs[target].ID != 2 {
		t.Fatalf("target message id: want 2, got %d", msgs[target].ID)
	}
	if msgs[0].ID != 1 {
		t.Fatalf("first message id: want 1, got %d", msgs[0].ID)
	}
}

// TestService_JumpContext_DefaultAround uses around <= 0 to confirm
// the DefaultJumpContext fallback is applied.
func TestService_JumpContext_DefaultAround(t *testing.T) {
	repo, ctx := openTestRepo(t)
	const chatID int64 = 30003
	seedChat(t, repo, ctx, chatID, "Default")

	for i := 1; i <= 50; i++ {
		if err := repo.SaveMessage(ctx, domain.Message{
			ID:     int64(i),
			ChatID: chatID,
			Date:   time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC),
			Text:   "msg",
		}); err != nil {
			t.Fatalf("save: %v", err)
		}
	}

	svc := search.NewService(repo, nil, nil)
	hit := search.Hit{Message: domain.Message{ID: 25, ChatID: chatID}, ChatID: chatID}
	msgs, _, err := svc.JumpContext(ctx, hit, 0)
	if err != nil {
		t.Fatalf("JumpContext: %v", err)
	}
	expected := 2*search.DefaultJumpContext + 1
	if len(msgs) != expected {
		t.Fatalf("default around: want %d msgs, got %d", expected, len(msgs))
	}
}

// TestService_JumpContext_Missing returns ErrJumpTargetMissing when
// the hit's message id is not in the repo.
func TestService_JumpContext_Missing(t *testing.T) {
	repo, ctx := openTestRepo(t)
	const chatID int64 = 30004
	seedChat(t, repo, ctx, chatID, "Missing")

	svc := search.NewService(repo, nil, nil)
	hit := search.Hit{Message: domain.Message{ID: 999, ChatID: chatID}, ChatID: chatID}
	_, _, err := svc.JumpContext(ctx, hit, 3)
	if !errors.Is(err, search.ErrJumpTargetMissing) {
		t.Fatalf("expected ErrJumpTargetMissing, got %v", err)
	}
}

// TestService_JumpContext_HydratesMedia verifies the loaded window
// carries MediaInfo for messages that have an attachment, so the
// thread pane can keep rendering the [📎 …] badge after a search jump
// (and Ctrl-D download still finds a target). Without media columns
// the jumped-to attachment would regress to plain text vs. the
// regular GetMessages path.
func TestService_JumpContext_HydratesMedia(t *testing.T) {
	repo, ctx := openTestRepo(t)
	const chatID int64 = 30005
	seedChat(t, repo, ctx, chatID, "Media")

	for i := 1; i <= 5; i++ {
		msg := domain.Message{
			ID:     int64(i),
			ChatID: chatID,
			FromID: 9,
			Date:   time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC),
			Text:   "around",
		}
		if i == 3 {
			msg.Text = "report"
			msg.Media = &domain.MediaInfo{
				Kind:       domain.MediaKindDocument,
				FileID:     7777,
				AccessHash: 8888,
				DC:         2,
				Filename:   "report.pdf",
				Size:       12345,
				MimeType:   "application/pdf",
			}
		}
		if err := repo.SaveMessage(ctx, msg); err != nil {
			t.Fatalf("save: %v", err)
		}
	}

	svc := search.NewService(repo, nil, nil)
	hit := search.Hit{Message: domain.Message{ID: 3, ChatID: chatID}, ChatID: chatID}
	msgs, target, err := svc.JumpContext(ctx, hit, 2)
	if err != nil {
		t.Fatalf("JumpContext: %v", err)
	}
	if msgs[target].Media == nil {
		t.Fatalf("target media: want non-nil, got nil")
	}
	if got := msgs[target].Media.Filename; got != "report.pdf" {
		t.Errorf("filename: want report.pdf, got %q", got)
	}
	if got := msgs[target].Media.Kind; got != domain.MediaKindDocument {
		t.Errorf("kind: want document, got %q", got)
	}
	if got := msgs[target].Media.Size; got != 12345 {
		t.Errorf("size: want 12345, got %d", got)
	}
	for i, m := range msgs {
		if i == target {
			continue
		}
		if m.Media != nil {
			t.Errorf("non-target msg id=%d has Media set", m.ID)
		}
	}
}

// The jump window hand-lists its columns, which is how the media fields once
// went missing from a jumped-to message. Reactions sit in the same position:
// a message reached by a search jump has to carry them, or a jump silently
// strips what the ordinary read shows.
func TestService_JumpContext_CarriesReactions(t *testing.T) {
	repo, ctx := openTestRepo(t)
	const chatID int64 = 30009
	seedChat(t, repo, ctx, chatID, "Reactions")

	for i := 1; i <= 5; i++ {
		if err := repo.SaveMessage(ctx, domain.Message{
			ID:     int64(i),
			ChatID: chatID,
			FromID: 7,
			Date:   time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC),
			Text:   "msg",
		}); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	if err := repo.SetReactions(ctx, chatID, 3, []domain.Reaction{{Emoticon: "\U0001F525", Count: 2, Chosen: true}}); err != nil {
		t.Fatalf("SetReactions: %v", err)
	}

	svc := search.NewService(repo, nil, nil)
	msgs, target, err := svc.JumpContext(ctx, search.Hit{
		Message: domain.Message{ID: 3, ChatID: chatID},
		ChatID:  chatID,
	}, 2)
	if err != nil {
		t.Fatalf("JumpContext: %v", err)
	}
	got := msgs[target].Reactions
	if len(got) != 1 || got[0].Count != 2 || !got[0].Chosen {
		t.Fatalf("jumped-to message carries %v", got)
	}
}
