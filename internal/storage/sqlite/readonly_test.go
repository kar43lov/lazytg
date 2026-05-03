package sqlite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/storage/sqlite"
)

// TestRepo_ReadOnlyFlagGatesWrites verifies that toggling SetReadOnly(true)
// makes every write method return ErrReadOnly while reads keep working.
// The flag is what DegradationDetector flips when ProbeWrite fails so the
// UI degrades cleanly without dropping data on the floor.
func TestRepo_ReadOnlyFlagGatesWrites(t *testing.T) {
	repo, ctx := openTestRepo(t)

	chat := domain.Chat{ID: 1, Type: domain.ChatTypePrivate, Title: "before"}
	if err := repo.SaveChat(ctx, chat); err != nil {
		t.Fatalf("save chat (rw): %v", err)
	}
	msg := domain.Message{ID: 1, ChatID: 1, FromID: 99, Date: time.Now().UTC(), Text: "hi"}
	if err := repo.SaveMessage(ctx, msg); err != nil {
		t.Fatalf("save message (rw): %v", err)
	}

	if repo.IsReadOnly() {
		t.Fatalf("repo must start read-write")
	}
	repo.SetReadOnly(true)
	if !repo.IsReadOnly() {
		t.Fatalf("SetReadOnly(true) must flip the flag")
	}

	// Every write method must reject with ErrReadOnly.
	checks := map[string]error{
		"SaveAccount":         repo.SaveAccount(ctx, domain.Account{Phone: "+1"}),
		"DeleteAccount":       repo.DeleteAccount(ctx, "+1"),
		"SaveChat":            repo.SaveChat(ctx, chat),
		"SaveMessage":         repo.SaveMessage(ctx, msg),
		"SaveMessages":        repo.SaveMessages(ctx, []domain.Message{msg}),
		"SaveOutgoing":        repo.SaveOutgoing(ctx, sqlite.OutgoingMessage{LocalID: "u1", ChatID: 1, State: "pending"}),
		"UpdateOutgoingState": repo.UpdateOutgoingState(ctx, "u1", "sent", 5, ""),
	}
	for name, err := range checks {
		if !errors.Is(err, sqlite.ErrReadOnly) {
			t.Errorf("%s: err = %v, want ErrReadOnly", name, err)
		}
	}

	// Reads must keep working.
	gotChats, err := repo.GetChats(ctx)
	if err != nil {
		t.Fatalf("get chats in read-only: %v", err)
	}
	if len(gotChats) != 1 {
		t.Fatalf("expected 1 chat, got %d", len(gotChats))
	}
	gotMsgs, err := repo.GetMessages(ctx, 1, 10, 0)
	if err != nil {
		t.Fatalf("get messages in read-only: %v", err)
	}
	if len(gotMsgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(gotMsgs))
	}

	// Restore rw.
	repo.SetReadOnly(false)
	if err := repo.SaveChat(ctx, chat); err != nil {
		t.Fatalf("save chat after restoring rw: %v", err)
	}
}

// TestRepo_ProbeWrite_HappyPath confirms the probe round-trips cleanly on
// a writable database. The negative case is exercised by core/sync's
// degradation tests via the fake store; the real-fs failure mode (chmod
// 0444 on the DB file) is platform-dependent and would flake on macOS
// where in-flight FDs retain write access regardless of mode bits.
func TestRepo_ProbeWrite_HappyPath(t *testing.T) {
	repo, ctx := openTestRepo(t)
	if err := repo.ProbeWrite(ctx); err != nil {
		t.Fatalf("probe on writable db: %v", err)
	}
	// Probe must not pollute any user-visible table.
	chats, err := repo.GetChats(ctx)
	if err != nil {
		t.Fatalf("get chats after probe: %v", err)
	}
	if len(chats) != 0 {
		t.Fatalf("probe leaked rows: %+v", chats)
	}
}

// TestRepo_ProbeWrite_BypassesSoftFlag asserts that ProbeWrite ignores the
// soft read-only flag. Otherwise DegradationDetector would never recover
// rw mode once the flag is set.
func TestRepo_ProbeWrite_BypassesSoftFlag(t *testing.T) {
	repo, ctx := openTestRepo(t)
	repo.SetReadOnly(true)
	if err := repo.ProbeWrite(ctx); err != nil {
		t.Fatalf("probe must bypass soft flag, got %v", err)
	}
}

// silence linter on the unused ctx parameter in the contextual sleep below.
var _ = context.Background
