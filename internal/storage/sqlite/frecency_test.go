package sqlite_test

import (
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// TestFrecency_EmptyRepo confirms TopFrecency on an untouched table
// returns an empty slice (not nil panic). The hot palette path assumes
// it can iterate the result without nil-checking each call.
func TestFrecency_EmptyRepo(t *testing.T) {
	repo, ctx := openTestRepo(t)

	got, err := repo.TopFrecency(ctx, 10)
	if err != nil {
		t.Fatalf("top: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty slice, got %v", got)
	}
}

// TestFrecency_RecencyWins verifies that two chats with one visit each
// are ranked by the recency of their last visit when the visit counts
// tie. Chat 2 visited later → ranks first.
func TestFrecency_RecencyWins(t *testing.T) {
	repo, ctx := openTestRepo(t)

	if err := repo.SaveChat(ctx, domain.Chat{ID: 1, Type: domain.ChatTypePrivate, Title: "A"}); err != nil {
		t.Fatalf("save chat 1: %v", err)
	}
	if err := repo.SaveChat(ctx, domain.Chat{ID: 2, Type: domain.ChatTypePrivate, Title: "B"}); err != nil {
		t.Fatalf("save chat 2: %v", err)
	}

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	if err := repo.RecordVisit(ctx, 1, base); err != nil {
		t.Fatalf("visit 1: %v", err)
	}
	if err := repo.RecordVisit(ctx, 2, base.Add(time.Hour)); err != nil {
		t.Fatalf("visit 2: %v", err)
	}

	got, err := repo.TopFrecency(ctx, 2)
	if err != nil {
		t.Fatalf("top: %v", err)
	}
	want := []int64{2, 1}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// Both chats have score 1.0 (one visit, decay = 1 because prior == now).
	// The query's ORDER BY tie-breaker is last_visit DESC, which puts
	// chat 2 (visited later) ahead.
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d: got %d, want %d", i, got[i], want[i])
		}
	}
}

// TestFrecency_FrequencyWins shows that frequency dominates when both
// chats are visited recently — chat 1 with three visits beats chat 2
// with one. The decay is small over a few seconds so visit_count is
// the deciding factor.
func TestFrecency_FrequencyWins(t *testing.T) {
	repo, ctx := openTestRepo(t)

	if err := repo.SaveChat(ctx, domain.Chat{ID: 1, Type: domain.ChatTypePrivate, Title: "A"}); err != nil {
		t.Fatalf("save chat 1: %v", err)
	}
	if err := repo.SaveChat(ctx, domain.Chat{ID: 2, Type: domain.ChatTypePrivate, Title: "B"}); err != nil {
		t.Fatalf("save chat 2: %v", err)
	}

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := repo.RecordVisit(ctx, 1, base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("visit 1 #%d: %v", i, err)
		}
	}
	if err := repo.RecordVisit(ctx, 2, base.Add(10*time.Second)); err != nil {
		t.Fatalf("visit 2: %v", err)
	}

	got, err := repo.TopFrecency(ctx, 2)
	if err != nil {
		t.Fatalf("top: %v", err)
	}
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("expected [1, 2] (frequency wins), got %v", got)
	}
}

// TestFrecency_DecayShrinksScoreAfterGap shows the exp(-age/30d) decay
// at work: chat 1 with two visits but a 60-day gap between them ends up
// scoring lower than chat 2 with a single fresh visit, because the
// second visit's score is `2 * exp(-2) ≈ 0.27`. The score is only
// recomputed at RecordVisit time (per plan), so this captures the only
// scenario where decay actually shifts ordering.
func TestFrecency_DecayShrinksScoreAfterGap(t *testing.T) {
	repo, ctx := openTestRepo(t)

	if err := repo.SaveChat(ctx, domain.Chat{ID: 1, Type: domain.ChatTypePrivate, Title: "stale"}); err != nil {
		t.Fatalf("save chat 1: %v", err)
	}
	if err := repo.SaveChat(ctx, domain.Chat{ID: 2, Type: domain.ChatTypePrivate, Title: "fresh"}); err != nil {
		t.Fatalf("save chat 2: %v", err)
	}

	old := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	if err := repo.RecordVisit(ctx, 1, old); err != nil {
		t.Fatalf("visit 1 first: %v", err)
	}
	if err := repo.RecordVisit(ctx, 1, old.Add(60*24*time.Hour)); err != nil {
		t.Fatalf("visit 1 second: %v", err)
	}
	if err := repo.RecordVisit(ctx, 2, old.Add(60*24*time.Hour)); err != nil {
		t.Fatalf("visit 2: %v", err)
	}

	got, err := repo.TopFrecency(ctx, 2)
	if err != nil {
		t.Fatalf("top: %v", err)
	}
	if len(got) != 2 || got[0] != 2 || got[1] != 1 {
		t.Fatalf("expected fresh chat 2 first after decay, got %v", got)
	}
}

// TestFrecency_LimitClampsToOne guards against a future caller passing
// limit ≤ 0 and accidentally fetching every row in the table.
func TestFrecency_LimitClampsToOne(t *testing.T) {
	repo, ctx := openTestRepo(t)
	for id := int64(1); id <= 3; id++ {
		if err := repo.SaveChat(ctx, domain.Chat{ID: id, Type: domain.ChatTypePrivate, Title: "x"}); err != nil {
			t.Fatalf("save chat %d: %v", id, err)
		}
		if err := repo.RecordVisit(ctx, id, time.Now()); err != nil {
			t.Fatalf("visit %d: %v", id, err)
		}
	}

	for _, lim := range []int{-1, 0} {
		got, err := repo.TopFrecency(ctx, lim)
		if err != nil {
			t.Fatalf("top(%d): %v", lim, err)
		}
		if len(got) != 1 {
			t.Errorf("limit %d: expected 1 row, got %d", lim, len(got))
		}
	}
}

// TestFrecency_RecordVisit_Validation rejects chat_id == 0 with a clear
// error so a future bug that drops the chat reference does not silently
// poison the table with bogus rows.
func TestFrecency_RecordVisit_Validation(t *testing.T) {
	repo, ctx := openTestRepo(t)
	if err := repo.RecordVisit(ctx, 0, time.Now()); err == nil {
		t.Fatal("expected error for chat_id == 0, got nil")
	}
}
