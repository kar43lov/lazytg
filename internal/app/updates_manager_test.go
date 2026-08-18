package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/storage/sqlite"
)

func openPeerRepo(t *testing.T) *sqlite.PeerRepo {
	t.Helper()
	repo, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "lazytg.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return sqlite.NewPeerRepo(repo.DB())
}

// TestChannelAccessHasher_RoundTrip is the reason the manager gets a hasher
// at all: it loads a channel's stored pts only when it can also find that
// channel's access hash, so an in-memory hasher means every channel is
// skipped on the next start and gap recovery covers private chats alone.
func TestChannelAccessHasher_RoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	hasher := channelAccessHasher{peers: openPeerRepo(t)}

	if _, found, err := hasher.GetChannelAccessHash(ctx, 1, 555); err != nil || found {
		t.Fatalf("unknown channel: found=%v err=%v want false/nil", found, err)
	}
	if err := hasher.SetChannelAccessHash(ctx, 1, 555, 42); err != nil {
		t.Fatalf("set: %v", err)
	}
	hash, found, err := hasher.GetChannelAccessHash(ctx, 1, 555)
	if err != nil || !found || hash != 42 {
		t.Fatalf("get = (%d, %v, %v) want (42, true, nil)", hash, found, err)
	}
}

// TestChannelAccessHasher_KeepsTheRecordedKind pins that refreshing a hash
// does not rewrite what the peer is. Channels and supergroups both address
// as InputPeerChannel, so the damage would not show up in sending — it would
// quietly make the stored type wrong for every other reader.
func TestChannelAccessHasher_KeepsTheRecordedKind(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	peers := openPeerRepo(t)
	if err := peers.Save(ctx, domain.Peer{ID: 555, Type: domain.ChatTypeSupergroup, AccessHash: 1}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := (channelAccessHasher{peers: peers}).SetChannelAccessHash(ctx, 1, 555, 2); err != nil {
		t.Fatalf("set: %v", err)
	}

	peer, err := peers.Get(ctx, 555)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if peer.Type != domain.ChatTypeSupergroup {
		t.Fatalf("peer type = %q want %q", peer.Type, domain.ChatTypeSupergroup)
	}
	if peer.AccessHash != 2 {
		t.Fatalf("access hash = %d want 2", peer.AccessHash)
	}
}

// TestUpdatesManager_NilWithoutStateStorage covers the degraded path the cmd
// layer branches on: no state storage means no gap recovery, and saying so
// with a nil is what lets attach fall back to the plain dispatcher instead
// of handing gotd a manager that cannot persist anything.
func TestUpdatesManager_NilWithoutStateStorage(t *testing.T) {
	t.Parallel()
	if m := (&App{}).UpdatesManager(nil); m != nil {
		t.Fatal("UpdatesManager built a manager with no dispatcher and no state storage")
	}
}
