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

// TestProbeWrite_DetectsAReadOnlyDatabase covers the condition
// DegradationDetector exists for: a database file the process can read but not
// write. For a long time it did not — the probe ran `BEGIN IMMEDIATE;
// ROLLBACK`, which takes the write lock without dirtying a page, so SQLite
// never raised SQLITE_READONLY and the detector kept the repo in rw mode while
// every actual write failed with "attempt to write a readonly database".
//
// The fixture is the honest one: a real file reopened with mode=ro, checked
// first to still reject an ordinary write, so a driver change that stops
// reproducing read-only databases fails loudly here instead of quietly
// turning this into a test of nothing.
func TestProbeWrite_DetectsAReadOnlyDatabase(t *testing.T) {
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

	probeErr := ro.ProbeWrite(ctx)
	if probeErr == nil {
		t.Fatalf("ProbeWrite reported a read-only database as writable; ordinary writes fail with %v", writeErr)
	}
	// Asserting the condition, not merely that something failed: a probe that
	// errored for an unrelated reason (a connection it could not open, say)
	// would otherwise pass this test while the fix it covers was gone.
	if !strings.Contains(probeErr.Error(), "readonly") {
		t.Fatalf("ProbeWrite failed with %v, want the read-only condition", probeErr)
	}
	if strings.Contains(probeErr.Error(), "SQLITE_BUSY") {
		t.Errorf("read-only database misreported as contention: %v", probeErr)
	}
	t.Logf("ProbeWrite rejected the read-only database with %v", probeErr)
}

// TestProbeWrite_RepeatsCleanlyOnAReadOnlyDatabase covers the connection the
// probe borrows rather than the verdict it returns. The DELETE fails, the
// transaction still has to be closed, and modernc's ResetSession hands a
// connection back to the pool without closing one — so a probe that returns
// with its transaction still open parks the write lock on a pooled connection
// and every later writer waits out the busy_timeout and fails. From outside
// that shows up as the second probe reporting "cannot start a transaction
// within a transaction" instead of the read-only condition, which is exactly
// what this test asserts against.
func TestProbeWrite_RepeatsCleanlyOnAReadOnlyDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	t.Cleanup(cancel)

	path := filepath.Join(t.TempDir(), "readonly-repeat.db")
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

	for i := 0; i < 3; i++ {
		err := ro.ProbeWrite(ctx)
		if err == nil {
			t.Fatalf("probe %d on a read-only database = nil, want an error", i+1)
		}
		if !strings.Contains(err.Error(), "readonly") {
			t.Fatalf("probe %d = %v, want the read-only condition rather than a wedged connection", i+1, err)
		}
	}
}

// TestProbeWrite_LeavesNoRowsBehind pins the other half of that fix: the probe
// now runs a DELETE, and a DELETE inside a probe must never be able to remove
// data. What it pins is `WHERE 0` — nothing else in the suite fails if the
// predicate is dropped. It does not pin the ROLLBACK: with `WHERE 0` in place
// no row goes anywhere regardless of how the transaction ends, so a COMMIT
// here would pass. The rollback is covered by
// TestProbeWrite_RepeatsCleanlyOnAReadOnlyDatabase instead.
func TestProbeWrite_LeavesNoRowsBehind(t *testing.T) {
	repo, ctx := openTestRepo(t)

	for id := int64(1); id <= 3; id++ {
		if err := repo.SaveChat(ctx, domain.Chat{
			ID:    id,
			Type:  domain.ChatTypePrivate,
			Title: "survivor",
		}); err != nil {
			t.Fatalf("seed chat %d: %v", id, err)
		}
	}

	for i := 0; i < 3; i++ {
		if err := repo.ProbeWrite(ctx); err != nil {
			t.Fatalf("ProbeWrite on a healthy database = %v, want nil", err)
		}
	}

	var count int
	if err := repo.DB().QueryRowContext(ctx, "SELECT count(*) FROM chats").Scan(&count); err != nil {
		t.Fatalf("count chats: %v", err)
	}
	if count != 3 {
		t.Fatalf("chats after three probes = %d, want 3 — the probe deleted rows", count)
	}
}
