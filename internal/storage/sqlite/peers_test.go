package sqlite_test

import (
	"errors"
	"testing"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/storage/sqlite"
)

func TestPeerRepo_SaveGet_RoundTrip(t *testing.T) {
	repo, ctx := openTestRepo(t)
	peers := sqlite.NewPeerRepo(repo.DB())

	want := domain.Peer{ID: 1234, Type: domain.ChatTypeChannel, AccessHash: 0xCAFE}
	if err := peers.Save(ctx, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := peers.Get(ctx, want.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != want {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, want)
	}
}

func TestPeerRepo_Save_UpsertsOnConflict(t *testing.T) {
	repo, ctx := openTestRepo(t)
	peers := sqlite.NewPeerRepo(repo.DB())

	if err := peers.Save(ctx, domain.Peer{ID: 7, Type: domain.ChatTypePrivate, AccessHash: 1}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := peers.Save(ctx, domain.Peer{ID: 7, Type: domain.ChatTypeSupergroup, AccessHash: 99}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	got, err := peers.Get(ctx, 7)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Type != domain.ChatTypeSupergroup || got.AccessHash != 99 {
		t.Fatalf("upsert did not refresh row: %+v", got)
	}
}

func TestPeerRepo_Get_NotFound(t *testing.T) {
	repo, ctx := openTestRepo(t)
	peers := sqlite.NewPeerRepo(repo.DB())

	_, err := peers.Get(ctx, 99999)
	if !errors.Is(err, sqlite.ErrPeerNotFound) {
		t.Fatalf("Get on missing row: want ErrPeerNotFound, got %v", err)
	}
}

func TestPeerRepo_Save_RejectsZeroID(t *testing.T) {
	repo, ctx := openTestRepo(t)
	peers := sqlite.NewPeerRepo(repo.DB())
	if err := peers.Save(ctx, domain.Peer{Type: domain.ChatTypePrivate, AccessHash: 1}); err == nil {
		t.Fatalf("Save must reject zero id")
	}
}

func TestPeerRepo_Save_RejectsEmptyType(t *testing.T) {
	repo, ctx := openTestRepo(t)
	peers := sqlite.NewPeerRepo(repo.DB())
	if err := peers.Save(ctx, domain.Peer{ID: 1}); err == nil {
		t.Fatalf("Save must reject empty type")
	}
}
