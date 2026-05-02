package sync

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pgmac/lazytg/internal/core/events"
)

// fakeDegradationStore is a minimal in-memory DegradationStore. The probe
// outcome is driven by an atomic.Pointer[error] so tests can flip the
// state from a different goroutine without locking.
type fakeDegradationStore struct {
	probeErr atomic.Pointer[error]
	readOnly atomic.Bool
	probes   atomic.Int32
}

func (f *fakeDegradationStore) ProbeWrite(_ context.Context) error {
	f.probes.Add(1)
	if e := f.probeErr.Load(); e != nil {
		return *e
	}
	return nil
}

func (f *fakeDegradationStore) IsReadOnly() bool { return f.readOnly.Load() }
func (f *fakeDegradationStore) SetReadOnly(v bool) {
	f.readOnly.Store(v)
}

func (f *fakeDegradationStore) setError(err error) {
	if err == nil {
		f.probeErr.Store(nil)
		return
	}
	f.probeErr.Store(&err)
}

func drainStorageStates(t *testing.T, ch <-chan events.Event, want int, d time.Duration) []events.StorageStateChanged {
	t.Helper()
	deadline := time.After(d)
	var out []events.StorageStateChanged
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			if e, ok := ev.(events.StorageStateChanged); ok {
				out = append(out, e)
				if len(out) >= want {
					return out
				}
			}
		case <-deadline:
			return out
		}
	}
}

func TestDegradationDetector_TransitionsToReadOnlyAndBack(t *testing.T) {
	t.Parallel()
	store := &fakeDegradationStore{}
	bus := events.New()
	subCtx, cancelSub := context.WithCancel(context.Background())
	defer cancelSub()
	ch := bus.Subscribe(subCtx)

	det := NewDegradationDetector(store, bus, nil, DegradationConfig{Interval: time.Hour})

	// 1. Healthy probe: no event, mode unchanged.
	writable, err := det.CheckWriteAccess(context.Background())
	if !writable || err != nil {
		t.Fatalf("initial probe: writable=%v err=%v", writable, err)
	}
	if got := drainStorageStates(t, ch, 1, 50*time.Millisecond); len(got) != 0 {
		t.Fatalf("no event expected when staying writable, got %+v", got)
	}

	// 2. Probe fails → expect read-only event with reason.
	store.setError(errors.New("attempt to write a readonly database"))
	writable, err = det.CheckWriteAccess(context.Background())
	if writable || err == nil {
		t.Fatalf("expected probe failure")
	}
	got := drainStorageStates(t, ch, 1, 500*time.Millisecond)
	if len(got) != 1 {
		t.Fatalf("expected one event, got %+v", got)
	}
	if got[0].Mode != events.StorageModeReadOnly {
		t.Fatalf("mode = %s want read-only", got[0].Mode)
	}
	if got[0].Reason != "database is read-only" {
		t.Fatalf("reason = %q want normalised copy", got[0].Reason)
	}
	if !store.IsReadOnly() {
		t.Fatalf("store soft flag must be set")
	}

	// 3. Same failure mode again — must NOT re-emit.
	if _, err := det.CheckWriteAccess(context.Background()); err == nil {
		t.Fatalf("probe still expected to fail")
	}
	if extra := drainStorageStates(t, ch, 1, 50*time.Millisecond); len(extra) != 0 {
		t.Fatalf("steady read-only must not re-emit, got %+v", extra)
	}

	// 4. Recovery: probe succeeds → expect rw event, soft flag cleared.
	store.setError(nil)
	if _, err := det.CheckWriteAccess(context.Background()); err != nil {
		t.Fatalf("recovery probe: %v", err)
	}
	rec := drainStorageStates(t, ch, 1, 500*time.Millisecond)
	if len(rec) != 1 {
		t.Fatalf("expected recovery event, got %+v", rec)
	}
	if rec[0].Mode != events.StorageModeReadWrite {
		t.Fatalf("recovery mode = %s want rw", rec[0].Mode)
	}
	if store.IsReadOnly() {
		t.Fatalf("soft flag must be cleared after recovery")
	}
}

func TestDegradationDetector_RunPeriodicProbe(t *testing.T) {
	t.Parallel()
	store := &fakeDegradationStore{}
	bus := events.New()
	subCtx, cancelSub := context.WithCancel(context.Background())
	defer cancelSub()
	ch := bus.Subscribe(subCtx)

	det := NewDegradationDetector(store, bus, nil, DegradationConfig{Interval: 10 * time.Millisecond})

	// Inject the failure deterministically: the first sleep flips the
	// store into "permission denied" so the second probe (right after the
	// sleep returns) catches it. After two ticks we cancel via ctx so the
	// loop unwinds cleanly.
	var ticks atomic.Int32
	det.sleep = func(ctx context.Context, _ time.Duration) error {
		n := ticks.Add(1)
		if n == 1 {
			store.setError(errors.New("permission denied"))
			return nil
		}
		return context.Canceled
	}

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	done := make(chan error, 1)
	go func() { done <- det.Run(runCtx) }()

	got := drainStorageStates(t, ch, 1, 1*time.Second)
	if len(got) == 0 {
		t.Fatalf("expected at least one StorageStateChanged event from periodic probe")
	}
	if got[0].Mode != events.StorageModeReadOnly {
		t.Fatalf("expected read-only event, got %+v", got)
	}
	cancelRun()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v", err)
	}
}

func TestDegradationReason_NormalisesCommonErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   error
		want string
	}{
		{errors.New("attempt to write a readonly database"), "database is read-only"},
		{errors.New("open foo: permission denied"), "permission denied"},
		{errors.New("disk i/o error"), "disk i/o error"},
		{nil, ""},
		{context.Canceled, "probe cancelled"},
	}
	for _, c := range cases {
		got := degradationReason(c.in)
		if got != c.want {
			t.Errorf("degradationReason(%v) = %q want %q", c.in, got, c.want)
		}
	}
}

// concurrencyHelper proves SetReadOnly + IsReadOnly are race-free under
// concurrent access from the detector goroutine and the storage write
// path. Run with -race to catch regressions.
func TestDegradationDetector_ConcurrentSetReadOnly(t *testing.T) {
	t.Parallel()
	store := &fakeDegradationStore{}
	store.setError(errors.New("readonly database"))
	det := NewDegradationDetector(store, events.New(), nil, DegradationConfig{Interval: time.Hour})

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = det.CheckWriteAccess(context.Background())
			_ = store.IsReadOnly()
		}()
	}
	wg.Wait()
	if !store.IsReadOnly() {
		t.Fatalf("expected read-only after probes failed")
	}
}
