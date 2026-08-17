package sqlite_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/storage/sqlite"
)

// openContentionRepo opens a repo on a real file. These tests are about
// SQLite's write lock, so ":memory:" would not exercise the behaviour.
func openContentionRepo(t *testing.T) (*sqlite.Repo, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	t.Cleanup(cancel)
	repo, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "contention.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo, ctx
}

// TestConcurrentWriters_NoBusyFailures pins the busy_timeout pragma.
//
// SQLite allows one writer at a time. Without a busy timeout, a connection
// that finds the write lock taken fails instantly with SQLITE_BUSY instead of
// waiting — and lazytg writes from several paths at once by design (live
// drain, history backfill, dialog sync, FTS reindex). The failures surface to
// the user as messages that never arrive, with nothing wrong on the disk.
//
// The measurement quoted elsewhere (973 lost of 1200) came from a 4 × 300
// burst; this test runs 4 × 150 to stay quick and asserts zero failures, which
// is enough to fail if the pragma is removed.
func TestConcurrentWriters_NoBusyFailures(t *testing.T) {
	repo, ctx := openContentionRepo(t)

	const (
		writers        = 4
		writesPerChat  = 150
		messageBaseSec = 1700000000
	)

	for c := 1; c <= writers; c++ {
		if err := repo.SaveChat(ctx, domain.Chat{
			ID:    int64(c),
			Type:  domain.ChatTypePrivate,
			Title: fmt.Sprintf("contention %d", c),
		}); err != nil {
			t.Fatalf("save chat %d: %v", c, err)
		}
	}

	var mu sync.Mutex
	failures := make([]error, 0)
	var wg sync.WaitGroup
	base := time.Unix(messageBaseSec, 0).UTC()

	for c := 1; c <= writers; c++ {
		wg.Add(1)
		go func(chatID int64) {
			defer wg.Done()
			// Every write is attempted, including after a failure: the count
			// in the failure message is then the real number of lost writes
			// rather than the number of goroutines that gave up first.
			for i := 0; i < writesPerChat; i++ {
				err := repo.SaveMessage(ctx, domain.Message{
					ID:     int64(i) + 1,
					ChatID: chatID,
					Date:   base.Add(time.Duration(i) * time.Second),
					Text:   fmt.Sprintf("message %d-%d", chatID, i),
				})
				if err != nil {
					mu.Lock()
					failures = append(failures, err)
					mu.Unlock()
				}
			}
		}(int64(c))
	}
	wg.Wait()

	if len(failures) > 0 {
		t.Fatalf("%d of %d concurrent writes failed, first: %v — is busy_timeout still set in pragmaQuery?",
			len(failures), writers*writesPerChat, failures[0])
	}
}

// TestProbeWrite_ToleratesContention pins the probe's contention semantics.
//
// DegradationDetector turns a probe error into soft read-only mode, where
// every write returns ErrReadOnly until a later probe recovers. SQLITE_BUSY
// must therefore not count as failure: another connection holding the write
// lock is proof the storage accepts writes. When this was treated as an
// outage, a probe landing inside a write burst disabled all writes for the
// probe interval and told the user the filesystem was read-only.
func TestProbeWrite_ToleratesContention(t *testing.T) {
	repo, ctx := openContentionRepo(t)

	conn, err := repo.DB().Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Hold the write lock for the duration of the probe.
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("begin immediate: %v", err)
	}

	start := time.Now()
	probeErr := repo.ProbeWrite(ctx)
	waited := time.Since(start)

	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	if probeErr != nil {
		t.Fatalf("ProbeWrite under a held write lock = %v, want nil (contention is not an outage)", probeErr)
	}
	// The probe waits out the pool's busy_timeout before concluding
	// contention, which is deliberate — see ProbeWrite on why it does not set
	// a shorter per-connection timeout. It must still return rather than hang.
	if waited > 30*time.Second {
		t.Errorf("ProbeWrite waited %s under contention — expected it to give up after the busy_timeout", waited)
	}
	t.Logf("ProbeWrite returned nil after %s of contention", waited.Round(100*time.Millisecond))
}

// TestProbeWrite_ReportsNonContentionFailure is the other half of the
// contract: tolerating contention must not swallow every failure, or the
// detector would never enter read-only mode and the change above would have
// quietly disabled the feature.
//
// A closed database is the portable way to produce a non-contention failure.
// It does not cover SQLITE_READONLY — see
// TestProbeWrite_KnownGap_ReadOnlyDatabaseUndetected for why that path cannot
// be asserted here.
func TestProbeWrite_ReportsNonContentionFailure(t *testing.T) {
	repo, ctx := openContentionRepo(t)

	if err := repo.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	err := repo.ProbeWrite(ctx)
	if err == nil {
		t.Fatalf("ProbeWrite on a closed database = nil, want an error")
	}
	if strings.Contains(err.Error(), "SQLITE_BUSY") {
		t.Errorf("closed database reported as contention: %v", err)
	}
}

// TestProbeWrite_KnownGap_ReadOnlyDatabaseUndetected documents a pre-existing
// limitation rather than a desired behaviour, so that the next person to read
// ProbeWrite does not assume coverage that does not exist.
//
// ProbeWrite runs `BEGIN IMMEDIATE; ROLLBACK`, which takes the write lock but
// never writes a page — so SQLite has no reason to raise SQLITE_READONLY, and
// the probe reports a read-only database as healthy. DegradationDetector
// therefore never enters read-only mode for the very condition it exists to
// detect: ordinary writes fail with "attempt to write a readonly database"
// while the probe keeps saying the storage is fine.
//
// This predates the contention change (verified against the previous revision:
// the probe returned nil there too) and is not fixed here, because the fix
// belongs with the detector rather than inside a test. Any statement that
// actually dirties a page inside the probe transaction surfaces the condition
// — `DELETE FROM chats WHERE 0`, for instance, was measured to return
// SQLITE_READONLY (8) while touching no rows.
//
// The assertion is deliberately inverted: it passes while the gap exists and
// fails once the probe learns to detect it, which is the signal to delete this
// test and assert the real behaviour instead.
func TestProbeWrite_KnownGap_ReadOnlyDatabaseUndetected(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	t.Cleanup(cancel)

	path := filepath.Join(t.TempDir(), "readonly.db")
	rw, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("open read-write: %v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close read-write: %v", err)
	}

	ro, err := sqlite.Open(ctx, "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("reopen read-only: %v", err)
	}
	t.Cleanup(func() { _ = ro.Close() })

	writeErr := ro.SaveChat(ctx, domain.Chat{
		ID:    1,
		Type:  domain.ChatTypePrivate,
		Title: "probe gap",
	})
	if writeErr == nil {
		t.Fatalf("SaveChat on a mode=ro database succeeded — the fixture no longer reproduces a read-only database")
	}

	if probeErr := ro.ProbeWrite(ctx); probeErr != nil {
		t.Errorf("ProbeWrite now detects a read-only database (%v) — the known gap is closed; delete this test and assert detection instead", probeErr)
	} else {
		t.Logf("known gap holds: writes fail with %q while ProbeWrite reports the storage writable", writeErr)
	}
}
