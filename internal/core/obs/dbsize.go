package obs

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/kar43lov/lazytg/internal/core/events"
)

// DefaultDBSizeThreshold is the size at which DBSizeMonitor surfaces a
// status-bar warning. The trigram FTS5 index is documented to grow 3-5×
// the underlying message text, so a 1 GB on-disk size corresponds to
// "this user has accumulated material amounts of indexed history" — far
// before the SQLite file becomes a stability concern.
const DefaultDBSizeThreshold int64 = 1 << 30 // 1 GiB

// DefaultDBSizeInterval is the polling cadence. A minute is a good
// compromise: rare enough to be invisible in CPU profiles, frequent
// enough to surface a sudden growth (e.g. the user's first
// `lazytg reindex --all`) without forcing them to restart.
const DefaultDBSizeInterval = time.Minute

// DBSizeMonitor watches the SQLite file's on-disk size and publishes a
// StorageStateChanged{Reason: ReasonDBSizeWarning} event each time the
// file crosses the configured threshold from below. It also fires a
// "warning cleared" event when the file drops back under the threshold
// (e.g. after VACUUM). The event carries the current size so the
// status-bar can render "DB 1.4 GB" without statting the file again.
//
// The monitor only emits on transitions — a steady-state large database
// does not flood the bus every tick.
type DBSizeMonitor struct {
	repo      RepoSizeSource
	bus       *events.Bus
	log       *slog.Logger
	threshold int64
	interval  time.Duration

	// sleep is a test seam: the loop calls sleep(ctx, interval) instead
	// of time.Sleep so tests can drive the schedule deterministically.
	sleep func(ctx context.Context, d time.Duration) error

	// stat is a test seam: the loop calls stat(path) instead of
	// os.Stat directly so tests can simulate large files without
	// allocating a 1 GB sparse file.
	stat func(path string) (os.FileInfo, error)
}

// RepoSizeSource is the narrow contract DBSizeMonitor needs from the
// storage layer. *sqlite.Repo satisfies it via DBPath. The interface
// keeps the obs package free of a hard dependency on storage internals.
type RepoSizeSource interface {
	// DBPath returns the on-disk path of the database file or ""
	// when the underlying database is not file-backed.
	DBPath() string
}

// DBSizeConfig groups the optional knobs. Zero values pick the
// documented defaults (1 GiB threshold, 60 s interval).
type DBSizeConfig struct {
	Threshold int64
	Interval  time.Duration
}

func (c *DBSizeConfig) defaults() {
	if c.Threshold <= 0 {
		c.Threshold = DefaultDBSizeThreshold
	}
	if c.Interval <= 0 {
		c.Interval = DefaultDBSizeInterval
	}
}

// NewDBSizeMonitor wires a monitor. log may be nil — a no-op logger is
// used in that case so unit tests stay free of plumbing noise. cfg may
// be the zero value to pick the documented defaults.
func NewDBSizeMonitor(repo RepoSizeSource, bus *events.Bus, log *slog.Logger, cfg DBSizeConfig) *DBSizeMonitor {
	cfg.defaults()
	if log == nil {
		log = slog.New(discardHandler{})
	}
	return &DBSizeMonitor{
		repo:      repo,
		bus:       bus,
		log:       log,
		threshold: cfg.Threshold,
		interval:  cfg.Interval,
		sleep:     contextSleep,
		stat:      os.Stat,
	}
}

// Run blocks until ctx is cancelled, polling the SQLite file every
// cfg.Interval and emitting StorageStateChanged on transitions. The
// first probe runs immediately so the status bar reflects reality from
// the moment the app starts. Returns ctx.Err on a clean shutdown.
//
// A nil RepoSizeSource or an empty DBPath() (in-memory / file: URI)
// short-circuits to a passive loop that just waits for ctx — the
// monitor stays installed but emits nothing.
func (m *DBSizeMonitor) Run(ctx context.Context) error {
	if m.repo == nil || m.repo.DBPath() == "" {
		<-ctx.Done()
		return ctx.Err()
	}

	var warned bool
	m.tick(ctx, &warned)
	for {
		if err := m.sleep(ctx, m.interval); err != nil {
			return err
		}
		m.tick(ctx, &warned)
	}
}

// tick performs one stat + transition. warned is the latched "are we
// currently above the threshold?" flag; it is updated in-place so the
// caller's loop carries state across iterations without exposing the
// private bool to the public surface.
//
// SQLite WAL mode keeps two side files (`-wal` and `-shm`) next to the
// main DB; their on-disk size legitimately grows to 100+ MiB during
// heavy write bursts before checkpoint. We sum all three so a 1 GiB
// total footprint trips the warning even when the main file alone is
// still well under threshold.
func (m *DBSizeMonitor) tick(_ context.Context, warned *bool) {
	path := m.repo.DBPath()
	if path == "" {
		return
	}
	info, err := m.stat(path)
	if err != nil {
		// Missing-file is not an emergency: the repo may be in the
		// middle of a VACUUM-and-rename, or the user may have removed
		// it for a clean restart. Log at debug so a permanent failure
		// is visible with --debug but does not flood production logs.
		if !errors.Is(err, os.ErrNotExist) {
			m.log.Debug("dbsize: stat failed", "path", path, "err", err)
		}
		return
	}
	size := info.Size()
	for _, suffix := range []string{"-wal", "-shm"} {
		sideInfo, sideErr := m.stat(path + suffix)
		if sideErr != nil {
			// Side files are absent in non-WAL setups and during
			// idle periods after checkpoint — silent skip is correct.
			continue
		}
		size += sideInfo.Size()
	}
	above := size >= m.threshold
	switch {
	case above && !*warned:
		*warned = true
		m.publish(events.StorageStateChanged{
			Mode:     events.StorageModeReadWrite,
			Reason:   events.ReasonDBSizeWarning,
			DBSizeMB: bytesToMB(size),
		})
		m.log.Warn("dbsize: above threshold",
			"size_bytes", size,
			"threshold_bytes", m.threshold,
		)
	case !above && *warned:
		*warned = false
		// Reason kept set to ReasonDBSizeWarning so the status bar can
		// route this event to its DB-size cell instead of the
		// degradation cell — the empty-Reason / Mode=rw shape would
		// be indistinguishable from a degradation rw recovery, which
		// must not clear an unrelated DB-size warning. DBSizeMB=0
		// signals "warning cleared" — the status bar drops the chip
		// on receipt.
		m.publish(events.StorageStateChanged{
			Mode:     events.StorageModeReadWrite,
			Reason:   events.ReasonDBSizeWarning,
			DBSizeMB: 0,
		})
	}
}

// publish is a nil-safe wrapper around Bus.Publish so DBSizeMonitor can
// be constructed without a bus in the smallest possible test setup.
func (m *DBSizeMonitor) publish(e events.Event) {
	if m.bus == nil {
		return
	}
	m.bus.Publish(e)
}

// bytesToMB rounds a byte count to whole megabytes. We round up so the
// status bar never shows "0 MB" for a non-empty file.
func bytesToMB(n int64) int {
	const mb = 1 << 20
	if n <= 0 {
		return 0
	}
	if n%mb == 0 {
		return int(n / mb)
	}
	return int(n/mb) + 1
}

// contextSleep blocks for d or until ctx is cancelled, returning ctx.Err
// in the latter case. A timer (rather than time.After) is used so a
// pre-cancelled ctx returns immediately without leaking the timer.
func contextSleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// discardHandler is a slog.Handler that drops every record. We use it
// instead of slog.New(slog.NewTextHandler(io.Discard, …)) to avoid
// importing the io package for a single sentinel.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (h discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return h }
func (h discardHandler) WithGroup(string) slog.Handler           { return h }
