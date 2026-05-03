package obs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/events"
)

// fakeRepo implements RepoSizeSource by returning a fixed path. The
// monitor's stat hook is replaced separately so tests don't need to
// allocate real files of the threshold size.
type fakeRepo struct{ path string }

func (f fakeRepo) DBPath() string { return f.path }

// fakeFileInfo is the minimal os.FileInfo needed to drive the monitor.
// Only Size() is read; the rest return zero values that satisfy the
// interface without doing anything meaningful.
type fakeFileInfo struct {
	size int64
	name string
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return f.size }
func (f fakeFileInfo) Mode() os.FileMode  { return 0 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }

// mainOnlyStat returns a stat hook that reports `size` for the
// configured main-DB path and ErrNotExist for everything else (i.e.
// the `-wal` and `-shm` side-files). Tests that only want to drive
// the main-file branch use this helper to avoid the WAL summation
// double-counting their fixture size.
func mainOnlyStat(path string, size int64) func(string) (os.FileInfo, error) {
	return func(p string) (os.FileInfo, error) {
		if p != path {
			return nil, os.ErrNotExist
		}
		return fakeFileInfo{size: size}, nil
	}
}

// drainWithTimeout pulls an event from sub or fails after timeout.
// Test-helper for TestDBSizeMonitor_* — keeps each test free of
// repeating select-with-timeout boilerplate.
func drainWithTimeout(t *testing.T, sub <-chan events.Event, timeout time.Duration) (events.Event, bool) {
	t.Helper()
	select {
	case e := <-sub:
		return e, true
	case <-time.After(timeout):
		return nil, false
	}
}

func TestDBSizeMonitor_BelowThresholdEmitsNothing(t *testing.T) {
	t.Parallel()

	bus := events.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := bus.Subscribe(ctx)

	repo := fakeRepo{path: "/tmp/lazytg.db"}
	m := NewDBSizeMonitor(repo, bus, nil, DBSizeConfig{Threshold: 1 << 30, Interval: 50 * time.Millisecond})
	m.stat = mainOnlyStat("/tmp/lazytg.db", 100<<20)
	// Synchronous tick instead of running the loop — avoids racing
	// against the deferred cancel.
	var warned bool
	m.tick(ctx, &warned)

	if warned {
		t.Fatalf("warned flag should remain false below threshold")
	}
	if _, ok := drainWithTimeout(t, sub, 20*time.Millisecond); ok {
		t.Fatalf("no event expected when DB is below threshold")
	}
}

func TestDBSizeMonitor_AboveThresholdEmitsWarning(t *testing.T) {
	t.Parallel()

	bus := events.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := bus.Subscribe(ctx)

	repo := fakeRepo{path: "/tmp/lazytg.db"}
	m := NewDBSizeMonitor(repo, bus, nil, DBSizeConfig{Threshold: 1 << 30, Interval: time.Hour})
	m.stat = mainOnlyStat("/tmp/lazytg.db", 1500<<20) // 1500 MiB main, no WAL/SHM

	var warned bool
	m.tick(ctx, &warned)

	if !warned {
		t.Fatalf("warned flag should flip to true once size crosses threshold")
	}
	e, ok := drainWithTimeout(t, sub, 100*time.Millisecond)
	if !ok {
		t.Fatalf("expected StorageStateChanged event")
	}
	got, ok := e.(events.StorageStateChanged)
	if !ok {
		t.Fatalf("expected StorageStateChanged, got %T", e)
	}
	if got.Reason != events.ReasonDBSizeWarning {
		t.Fatalf("Reason: got %q, want %q", got.Reason, events.ReasonDBSizeWarning)
	}
	if got.Mode != events.StorageModeReadWrite {
		t.Fatalf("Mode: got %q, want %q (warning is informational, file remains writable)", got.Mode, events.StorageModeReadWrite)
	}
	if got.DBSizeMB != 1500 {
		t.Fatalf("DBSizeMB: got %d, want 1500", got.DBSizeMB)
	}
}

func TestDBSizeMonitor_OnlyEmitsOnTransition(t *testing.T) {
	t.Parallel()

	bus := events.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := bus.Subscribe(ctx)

	repo := fakeRepo{path: "/tmp/lazytg.db"}
	m := NewDBSizeMonitor(repo, bus, nil, DBSizeConfig{Threshold: 1 << 30, Interval: time.Hour})
	m.stat = mainOnlyStat("/tmp/lazytg.db", 1500<<20)

	var warned bool
	m.tick(ctx, &warned)
	m.tick(ctx, &warned)
	m.tick(ctx, &warned)

	// First tick emits, subsequent ticks are silent because warned
	// stays latched. Drain once and then assert the bus is quiet.
	if _, ok := drainWithTimeout(t, sub, 50*time.Millisecond); !ok {
		t.Fatalf("expected one warning event")
	}
	if _, ok := drainWithTimeout(t, sub, 30*time.Millisecond); ok {
		t.Fatalf("steady-state above threshold should not re-emit")
	}
}

func TestDBSizeMonitor_TransitionBackEmitsClearedEvent(t *testing.T) {
	t.Parallel()

	bus := events.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := bus.Subscribe(ctx)

	repo := fakeRepo{path: "/tmp/lazytg.db"}
	m := NewDBSizeMonitor(repo, bus, nil, DBSizeConfig{Threshold: 1 << 30, Interval: time.Hour})

	var size int64 = 1500 << 20
	m.stat = func(p string) (os.FileInfo, error) {
		if p != "/tmp/lazytg.db" {
			return nil, os.ErrNotExist
		}
		return fakeFileInfo{size: size}, nil
	}

	var warned bool
	m.tick(ctx, &warned)

	// Drain the warning event.
	if e, ok := drainWithTimeout(t, sub, 50*time.Millisecond); !ok {
		t.Fatalf("expected initial warning")
	} else if got := e.(events.StorageStateChanged); got.Reason != events.ReasonDBSizeWarning {
		t.Fatalf("first event: got reason %q, want %q", got.Reason, events.ReasonDBSizeWarning)
	}

	// Simulate a VACUUM that shrinks the file under the threshold.
	size = 100 << 20
	m.tick(ctx, &warned)

	if warned {
		t.Fatalf("warned flag should clear when size drops below threshold")
	}
	e, ok := drainWithTimeout(t, sub, 50*time.Millisecond)
	if !ok {
		t.Fatalf("expected cleared event")
	}
	got := e.(events.StorageStateChanged)
	if got.Reason != events.ReasonDBSizeWarning {
		t.Fatalf("cleared event: Reason should stay set to %q so the status bar routes it to the DB cell, got %q", events.ReasonDBSizeWarning, got.Reason)
	}
	if got.DBSizeMB != 0 {
		t.Fatalf("cleared event: DBSizeMB should be 0 (sentinel for 'warning cleared'), got %d", got.DBSizeMB)
	}
}

func TestDBSizeMonitor_ThresholdConfigurable(t *testing.T) {
	t.Parallel()

	bus := events.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := bus.Subscribe(ctx)

	repo := fakeRepo{path: "/tmp/lazytg.db"}
	m := NewDBSizeMonitor(repo, bus, nil, DBSizeConfig{Threshold: 50 << 20, Interval: time.Hour})
	m.stat = mainOnlyStat("/tmp/lazytg.db", 64<<20)

	var warned bool
	m.tick(ctx, &warned)

	e, ok := drainWithTimeout(t, sub, 50*time.Millisecond)
	if !ok {
		t.Fatalf("expected warning at custom 50 MiB threshold")
	}
	got := e.(events.StorageStateChanged)
	if got.DBSizeMB != 64 {
		t.Fatalf("DBSizeMB: got %d, want 64", got.DBSizeMB)
	}
}

func TestDBSizeMonitor_MissingFileIsTolerated(t *testing.T) {
	t.Parallel()

	repo := fakeRepo{path: "/no/such/file"}
	m := NewDBSizeMonitor(repo, events.New(), nil, DBSizeConfig{})
	m.stat = func(string) (os.FileInfo, error) {
		return nil, os.ErrNotExist
	}

	var warned bool
	m.tick(context.Background(), &warned)
	if warned {
		t.Fatalf("missing-file should leave warned flag untouched")
	}
}

func TestDBSizeMonitor_NilRepoExitsCleanly(t *testing.T) {
	t.Parallel()

	m := NewDBSizeMonitor(nil, events.New(), nil, DBSizeConfig{Interval: 10 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := m.Run(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run with nil repo: got %v, want DeadlineExceeded", err)
	}
}

func TestDBSizeMonitor_RunIntegratesWithRealLoop(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "lazytg.db")
	// Write a small file so os.Stat succeeds and reports a real size.
	if err := os.WriteFile(path, []byte("seed"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	bus := events.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := bus.Subscribe(ctx)

	repo := fakeRepo{path: path}
	m := NewDBSizeMonitor(repo, bus, nil, DBSizeConfig{Threshold: 1, Interval: 5 * time.Millisecond})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = m.Run(ctx)
	}()

	if e, ok := drainWithTimeout(t, sub, 200*time.Millisecond); !ok {
		t.Fatalf("expected warning from Run")
	} else if got := e.(events.StorageStateChanged); got.Reason != events.ReasonDBSizeWarning {
		t.Fatalf("Reason: got %q, want %q", got.Reason, events.ReasonDBSizeWarning)
	}

	cancel()
	wg.Wait()
}

func TestDBSizeMonitor_SumsWALAndSHM(t *testing.T) {
	t.Parallel()

	bus := events.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := bus.Subscribe(ctx)

	repo := fakeRepo{path: "/tmp/lazytg.db"}
	m := NewDBSizeMonitor(repo, bus, nil, DBSizeConfig{Threshold: 1 << 30, Interval: time.Hour})
	// Main file alone (800 MiB) is below the 1 GiB threshold; total
	// with WAL (300 MiB) + SHM (1 MiB) crosses it.
	m.stat = func(p string) (os.FileInfo, error) {
		switch p {
		case "/tmp/lazytg.db":
			return fakeFileInfo{size: 800 << 20}, nil
		case "/tmp/lazytg.db-wal":
			return fakeFileInfo{size: 300 << 20}, nil
		case "/tmp/lazytg.db-shm":
			return fakeFileInfo{size: 1 << 20}, nil
		default:
			return nil, os.ErrNotExist
		}
	}

	var warned bool
	m.tick(ctx, &warned)
	if !warned {
		t.Fatalf("warned should flip true once main + WAL + SHM cross 1 GiB")
	}
	e, ok := drainWithTimeout(t, sub, 100*time.Millisecond)
	if !ok {
		t.Fatalf("expected warning event")
	}
	got := e.(events.StorageStateChanged)
	// 800 + 300 + 1 = 1101 MiB, which exceeds 1024 (1 GiB) by enough
	// that the threshold check fires even though the main file alone
	// (800 MiB) would not.
	if got.DBSizeMB != 1101 {
		t.Fatalf("DBSizeMB: got %d, want 1101 (sum of main + WAL + SHM)", got.DBSizeMB)
	}
}

func TestBytesToMB_RoundsUp(t *testing.T) {
	t.Parallel()

	cases := []struct {
		n    int64
		want int
	}{
		{0, 0},
		{1, 1},         // any non-zero rounds up to at least 1
		{1 << 20, 1},   // exact 1 MiB
		{1<<20 + 1, 2}, // round up
		{1500 << 20, 1500},
	}
	for _, c := range cases {
		if got := bytesToMB(c.n); got != c.want {
			t.Errorf("bytesToMB(%d) = %d, want %d", c.n, got, c.want)
		}
	}
}
