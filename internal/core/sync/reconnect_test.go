package sync

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/events"
)

// fakeReconnectClient is a controllable ReconnectClient. It exposes the
// disconnect channel directly to test code (so we can drive disconnects
// from the test goroutine) and counts Connect / Ping invocations so the
// retry loop can be asserted without timing.
type fakeReconnectClient struct {
	disconnect chan error

	mu           sync.Mutex
	connectErrs  []error // consumed in order on each Connect call
	pingErrs     []error // consumed in order on each Ping call
	connectCalls int
	pingCalls    int
}

func newFakeReconnectClient() *fakeReconnectClient {
	return &fakeReconnectClient{disconnect: make(chan error, 4)}
}

func (f *fakeReconnectClient) Connect(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := f.connectCalls
	f.connectCalls++
	if idx < len(f.connectErrs) {
		return f.connectErrs[idx]
	}
	return nil
}

func (f *fakeReconnectClient) Ping(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := f.pingCalls
	f.pingCalls++
	if idx < len(f.pingErrs) {
		return f.pingErrs[idx]
	}
	return nil
}

func (f *fakeReconnectClient) OnDisconnect() <-chan error { return f.disconnect }

func (f *fakeReconnectClient) connectCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connectCalls
}

// drainConnectionStates collects up to want ConnectionStateChanged events from
// ch within d. Returns whatever it observed at deadline.
func drainConnectionStates(t *testing.T, ch <-chan events.Event, want int, d time.Duration) []events.ConnectionStateChanged {
	t.Helper()
	deadline := time.After(d)
	var out []events.ConnectionStateChanged
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			if e, ok := ev.(events.ConnectionStateChanged); ok {
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

// recordingSleep replaces contextSleep so reconnect tests don't sleep
// real wall-clock seconds. It records every requested duration so the
// exponential-backoff math can be asserted without timing flakes.
type recordingSleep struct {
	mu        sync.Mutex
	durations []time.Duration
}

func (r *recordingSleep) sleep(ctx context.Context, d time.Duration) error {
	r.mu.Lock()
	r.durations = append(r.durations, d)
	r.mu.Unlock()
	return ctx.Err()
}

func (r *recordingSleep) snapshot() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]time.Duration, len(r.durations))
	copy(out, r.durations)
	return out
}

func TestReconnectManager_DisconnectThenOnline(t *testing.T) {
	t.Parallel()
	client := newFakeReconnectClient()
	bus := events.New()
	subCtx, cancelSub := context.WithCancel(context.Background())
	defer cancelSub()
	ch := bus.Subscribe(subCtx)

	rec := &recordingSleep{}
	mgr := NewReconnectManager(client, bus, nil, ReconnectConfig{
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     100 * time.Millisecond,
	})
	mgr.sleep = rec.sleep
	mgr.jitter = func() float64 { return 0.5 }

	runCtx, cancelRun := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Run(runCtx) }()

	// Trigger one disconnect; expect offline → connecting → online.
	client.disconnect <- errors.New("transport closed")

	got := drainConnectionStates(t, ch, 3, 2*time.Second)
	if len(got) < 3 {
		t.Fatalf("got %d events want 3: %+v", len(got), got)
	}
	if got[0].State != ConnectionStateOffline {
		t.Fatalf("first event = %s want offline", got[0].State)
	}
	if got[1].State != ConnectionStateConnecting {
		t.Fatalf("second event = %s want connecting", got[1].State)
	}
	if got[2].State != ConnectionStateOnline {
		t.Fatalf("third event = %s want online", got[2].State)
	}
	if client.connectCount() != 1 {
		t.Fatalf("connect calls = %d want 1", client.connectCount())
	}

	cancelRun()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("run err = %v", err)
	}
}

func TestReconnectManager_ExponentialBackoff(t *testing.T) {
	t.Parallel()
	client := newFakeReconnectClient()
	// First two Connect calls fail, third succeeds.
	connErr := errors.New("dial: refused")
	client.connectErrs = []error{connErr, connErr, nil}

	bus := events.New()
	subCtx, cancelSub := context.WithCancel(context.Background())
	defer cancelSub()
	_ = bus.Subscribe(subCtx) // drain side; we don't assert events here

	rec := &recordingSleep{}
	mgr := NewReconnectManager(client, bus, nil, ReconnectConfig{
		InitialBackoff: 100 * time.Millisecond,
		MaxBackoff:     1 * time.Second,
	})
	mgr.sleep = rec.sleep
	// jitter=0.5 means jitterDuration returns d exactly (no scatter).
	mgr.jitter = func() float64 { return 0.5 }

	runCtx, cancelRun := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Run(runCtx) }()

	client.disconnect <- errors.New("boom")

	deadline := time.After(2 * time.Second)
	for client.connectCount() < 3 {
		select {
		case <-deadline:
			t.Fatalf("connect not called 3 times; got %d", client.connectCount())
		case <-time.After(2 * time.Millisecond):
		}
	}
	cancelRun()
	<-done

	durs := rec.snapshot()
	if len(durs) < 3 {
		t.Fatalf("expected ≥3 sleeps, got %v", durs)
	}
	// 1st backoff = 100ms, 2nd = 200ms, 3rd = 400ms (capped at 1s).
	wants := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond}
	for i, want := range wants {
		if durs[i] != want {
			t.Fatalf("sleep[%d] = %v want %v (full sequence: %v)", i, durs[i], want, durs)
		}
	}
}

func TestReconnectManager_ContextCanceledDisconnectIgnored(t *testing.T) {
	t.Parallel()
	client := newFakeReconnectClient()
	bus := events.New()
	subCtx, cancelSub := context.WithCancel(context.Background())
	defer cancelSub()
	ch := bus.Subscribe(subCtx)

	mgr := NewReconnectManager(client, bus, nil, ReconnectConfig{InitialBackoff: time.Millisecond})
	mgr.sleep = func(ctx context.Context, d time.Duration) error { return nil }
	mgr.jitter = func() float64 { return 0.5 }

	runCtx, cancelRun := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Run(runCtx) }()

	// A context.Canceled disconnect must NOT trigger reconnect.
	client.disconnect <- context.Canceled
	// Give the loop a moment to process it.
	time.Sleep(20 * time.Millisecond)

	got := drainConnectionStates(t, ch, 1, 50*time.Millisecond)
	if len(got) != 0 {
		t.Fatalf("context.Canceled must not emit events, got %+v", got)
	}
	if client.connectCount() != 0 {
		t.Fatalf("Connect must not be called for context.Canceled, got %d", client.connectCount())
	}

	cancelRun()
	<-done
}

func TestReconnectManager_MaxAttemptsRespected(t *testing.T) {
	t.Parallel()
	client := newFakeReconnectClient()
	connErr := errors.New("dial fail")
	client.connectErrs = []error{connErr, connErr, connErr, connErr, connErr}

	bus := events.New()
	subCtx, cancelSub := context.WithCancel(context.Background())
	defer cancelSub()
	_ = bus.Subscribe(subCtx)

	mgr := NewReconnectManager(client, bus, nil, ReconnectConfig{
		InitialBackoff: time.Millisecond,
		MaxBackoff:     time.Millisecond,
		MaxAttempts:    2,
	})
	var slept atomic.Int32
	mgr.sleep = func(ctx context.Context, d time.Duration) error {
		slept.Add(1)
		return nil
	}
	mgr.jitter = func() float64 { return 0.5 }

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	done := make(chan error, 1)
	go func() { done <- mgr.Run(runCtx) }()

	client.disconnect <- errors.New("boom")

	deadline := time.After(2 * time.Second)
	for client.connectCount() < 2 {
		select {
		case <-deadline:
			t.Fatalf("never reached MaxAttempts; got %d", client.connectCount())
		case <-time.After(2 * time.Millisecond):
		}
	}
	// Give the loop a moment to wind down past MaxAttempts so we can verify
	// it didn't try a 3rd time.
	time.Sleep(20 * time.Millisecond)
	if got := client.connectCount(); got != 2 {
		t.Fatalf("connect count = %d want 2", got)
	}
}

// reportingReconnectClient is a fakeReconnectClient that also implements
// ConnectionStateReporter, so the manager's transport-state pump can be
// driven from the test goroutine.
type reportingReconnectClient struct {
	*fakeReconnectClient
	states chan string
}

func newReportingReconnectClient() *reportingReconnectClient {
	return &reportingReconnectClient{
		fakeReconnectClient: newFakeReconnectClient(),
		states:              make(chan string, 8),
	}
}

func (r *reportingReconnectClient) ConnectionStates() <-chan string { return r.states }

// TestReconnectManager_RepublishesTransportStates covers the case the manager
// used to be blind to: the transport reconnecting under an active run loop.
// Nothing calls Connect there — gotd repairs itself and Run never returns —
// so before the client could report its own state the indicator sat on its
// last value for the entire outage.
func TestReconnectManager_RepublishesTransportStates(t *testing.T) {
	t.Parallel()
	client := newReportingReconnectClient()
	bus := events.New()
	subCtx, cancelSub := context.WithCancel(context.Background())
	defer cancelSub()
	ch := bus.Subscribe(subCtx)

	mgr := NewReconnectManager(client, bus, nil, ReconnectConfig{})
	runCtx, cancelRun := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Run(runCtx) }()

	client.states <- ConnectionStateOffline
	client.states <- ConnectionStateConnecting
	client.states <- ConnectionStateOnline

	got := drainConnectionStates(t, ch, 3, 2*time.Second)
	if len(got) < 3 {
		t.Fatalf("got %d events want 3: %+v", len(got), got)
	}
	want := []string{ConnectionStateOffline, ConnectionStateConnecting, ConnectionStateOnline}
	for i, w := range want {
		if got[i].State != w {
			t.Fatalf("event %d = %s want %s (all: %+v)", i, got[i].State, w, got)
		}
	}
	// No disconnect was signalled, so the repair path must not have run: the
	// transport fixed itself and the manager only reported it.
	if client.connectCount() != 0 {
		t.Fatalf("connect calls = %d want 0 — the manager reconnected a live session", client.connectCount())
	}

	cancelRun()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("run err = %v", err)
	}
}

// TestReconnectManager_SuppressesRepeatedTransportStates pins the dedup.
// gotd emits "connecting" once per attempt, and a flapping link produces a
// long run of identical events; republishing each one wakes every bus
// subscriber — the whole UI — to redraw a cell that did not change.
func TestReconnectManager_SuppressesRepeatedTransportStates(t *testing.T) {
	t.Parallel()
	client := newReportingReconnectClient()
	bus := events.New()
	subCtx, cancelSub := context.WithCancel(context.Background())
	defer cancelSub()
	ch := bus.Subscribe(subCtx)

	mgr := NewReconnectManager(client, bus, nil, ReconnectConfig{})
	runCtx, cancelRun := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Run(runCtx) }()

	client.states <- ConnectionStateConnecting
	client.states <- ConnectionStateConnecting
	client.states <- ConnectionStateConnecting
	client.states <- ConnectionStateOnline

	got := drainConnectionStates(t, ch, 4, 500*time.Millisecond)
	if len(got) != 2 {
		t.Fatalf("published %d events want 2 (repeats suppressed): %+v", len(got), got)
	}
	if got[0].State != ConnectionStateConnecting || got[1].State != ConnectionStateOnline {
		t.Fatalf("events = %+v want connecting then online", got)
	}

	cancelRun()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("run err = %v", err)
	}
}
