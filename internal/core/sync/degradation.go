package sync

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/kar43lov/lazytg/internal/core/events"
)

// DegradationStore is the storage surface DegradationDetector relies on.
// The interface is intentionally narrow — only the three operations the
// detector needs — so the concrete *sqlite.Repo and an in-memory test
// fake can both satisfy it without dragging the rest of the repo API.
type DegradationStore interface {
	// ProbeWrite attempts a no-op write against the underlying file.
	// Implementations must bypass any soft read-only flag they maintain
	// internally; otherwise the detector would never recover from a
	// transient permission flip.
	ProbeWrite(ctx context.Context) error
	// IsReadOnly returns the current soft mode.
	IsReadOnly() bool
	// SetReadOnly toggles the soft mode. The detector flips it true on a
	// failed probe and back to false once probes recover.
	SetReadOnly(v bool)
}

// DegradationConfig groups the optional knobs. Zero values pick the
// documented defaults (probe immediately, then every 30 seconds — the
// cadence the plan specifies for surfacing fs-level outages).
type DegradationConfig struct {
	// Interval between probes. Default: 30s.
	Interval time.Duration
}

func (c *DegradationConfig) defaults() {
	if c.Interval <= 0 {
		c.Interval = 30 * time.Second
	}
}

// DegradationDetector polls the storage layer and publishes
// StorageStateChanged transitions on the bus. The detector only emits
// when the mode actually changes, so a steady-state read-only file does
// not flood subscribers every 30s.
//
// The detector is safe for a single Run goroutine; concurrent Run calls
// would emit duplicate events and are unsupported.
type DegradationDetector struct {
	store DegradationStore
	bus   *events.Bus
	log   *slog.Logger
	cfg   DegradationConfig

	// Test seam — replaced in unit tests so we don't sleep real wall
	// clock seconds.
	sleep func(ctx context.Context, d time.Duration) error
}

// NewDegradationDetector wires a detector. log may be nil — a no-op
// logger is used in that case so unit tests stay free of plumbing noise.
func NewDegradationDetector(store DegradationStore, bus *events.Bus, log *slog.Logger, cfg DegradationConfig) *DegradationDetector {
	cfg.defaults()
	if log == nil {
		log = slog.New(noopHandler{})
	}
	return &DegradationDetector{
		store: store,
		bus:   bus,
		log:   log,
		cfg:   cfg,
		sleep: contextSleep,
	}
}

// CheckWriteAccess runs a single probe synchronously. It returns true
// when the underlying store accepts writes and false when the probe
// failed; the second return value carries the probe error (nil when
// writable). The detector also flips the store's soft read-only flag
// and publishes a StorageStateChanged event when the mode changes —
// callers wired into the periodic loop don't need to do that themselves.
//
// Exposed publicly so the wire-up at startup can run a probe before the
// UI loop starts and avoid the "first 30s look writable then flip"
// flicker.
func (d *DegradationDetector) CheckWriteAccess(ctx context.Context) (bool, error) {
	err := d.store.ProbeWrite(ctx)
	writable := err == nil
	d.transition(writable, err)
	return writable, err
}

// Run blocks until ctx is cancelled, probing the store every cfg.Interval
// and emitting StorageStateChanged on transitions. The first probe runs
// immediately so the status bar reflects reality from the moment the
// app starts. Returns ctx.Err on a clean shutdown.
func (d *DegradationDetector) Run(ctx context.Context) error {
	if _, err := d.CheckWriteAccess(ctx); err != nil {
		d.log.Warn("degradation: initial probe failed", "err", err)
	}
	for {
		if err := d.sleep(ctx, d.cfg.Interval); err != nil {
			return err
		}
		if _, err := d.CheckWriteAccess(ctx); err != nil {
			d.log.Debug("degradation: probe failed", "err", err)
		}
	}
}

// transition updates the soft flag and publishes an event when the mode
// changes. The "no event on no-change" rule keeps subscribers quiet
// during steady state and avoids spuriously redrawing the status bar.
func (d *DegradationDetector) transition(writable bool, probeErr error) {
	wasRO := d.store.IsReadOnly()
	switch {
	case writable && wasRO:
		d.store.SetReadOnly(false)
		d.publish(events.StorageStateChanged{Mode: events.StorageModeReadWrite, Reason: ""})
	case !writable && !wasRO:
		d.store.SetReadOnly(true)
		d.publish(events.StorageStateChanged{
			Mode:   events.StorageModeReadOnly,
			Reason: degradationReason(probeErr),
		})
	}
}

// publish is a nil-safe wrapper around Bus.Publish so DegradationDetector
// can be constructed without a bus in the smallest possible test setup.
func (d *DegradationDetector) publish(e events.Event) {
	if d.bus == nil {
		return
	}
	d.bus.Publish(e)
}

// degradationReason produces a short human-readable summary for the
// status bar from a SQLite/filesystem error. We keep the original error
// text so unusual cases still surface, but normalise common substrings
// ("readonly", "permission denied", "disk i/o") into stable copy.
func degradationReason(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "readonly") || strings.Contains(lower, "read-only"):
		return "database is read-only"
	case strings.Contains(lower, "permission denied"):
		return "permission denied"
	case strings.Contains(lower, "disk i/o"):
		return "disk i/o error"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "probe cancelled"
	default:
		return msg
	}
}
