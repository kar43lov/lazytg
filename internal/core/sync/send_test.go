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

// inMemoryOutgoing is a thread-safe in-memory OutgoingStore. It records
// the order of state transitions so tests can assert the optimistic flow
// pending → sent | failed without inspecting an SQLite file.
type inMemoryOutgoing struct {
	mu      sync.Mutex
	records map[string]*OutgoingRecord
	updates []outgoingUpdate
	saveErr error
}

type outgoingUpdate struct {
	LocalID  string
	State    string
	ServerID int64
	Error    string
}

func newInMemoryOutgoing() *inMemoryOutgoing {
	return &inMemoryOutgoing{records: map[string]*OutgoingRecord{}}
}

func (s *inMemoryOutgoing) SaveOutgoing(_ context.Context, m OutgoingRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	cp := m
	s.records[m.LocalID] = &cp
	return nil
}

func (s *inMemoryOutgoing) UpdateOutgoingState(_ context.Context, localID, state string, serverID int64, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[localID]
	if !ok {
		return errors.New("not found")
	}
	rec.State = state
	s.updates = append(s.updates, outgoingUpdate{LocalID: localID, State: state, ServerID: serverID, Error: errMsg})
	return nil
}

func (s *inMemoryOutgoing) snapshot() []outgoingUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]outgoingUpdate, len(s.updates))
	copy(out, s.updates)
	return out
}

func (s *inMemoryOutgoing) record(localID string) *OutgoingRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.records[localID]; ok {
		cp := *r
		return &cp
	}
	return nil
}

// scriptedSender replays a programmed sequence of (id, err) results so
// the retry / classification policy can be exercised deterministically.
type scriptedSender struct {
	mu    sync.Mutex
	steps []sendStep
	calls int
}

type sendStep struct {
	id  int64
	err error
}

func (s *scriptedSender) SendText(ctx context.Context, _ int64, _ string, _ int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	idx := s.calls
	s.calls++
	if idx >= len(s.steps) {
		return 0, errors.New("scripted: no more steps")
	}
	st := s.steps[idx]
	return st.id, st.err
}

func (s *scriptedSender) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// collectEvents drains bus events for d so tests can assert the exact
// sequence the SendService emitted. It returns any subset of
// OutgoingMessageStateChanged events seen during the window.
func collectEvents(t *testing.T, bus *events.Bus, want int, d time.Duration) []events.OutgoingMessageStateChanged {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := bus.Subscribe(ctx)
	deadline := time.After(d)
	var out []events.OutgoingMessageStateChanged
	for {
		select {
		case ev := <-ch:
			if e, ok := ev.(events.OutgoingMessageStateChanged); ok {
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

func TestSendService_HappyPath_OptimisticToSent(t *testing.T) {
	t.Parallel()
	store := newInMemoryOutgoing()
	bus := events.New()

	// Subscribe before SendText so the pending event is guaranteed to be
	// observed by the test goroutine.
	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := bus.Subscribe(subCtx)

	sender := &scriptedSender{steps: []sendStep{{id: 999}}}
	idCounter := atomic.Int64{}
	svc := NewSendService(sender, store, bus, nil, SendConfig{})
	svc.newID = func() string {
		idCounter.Add(1)
		return "uuid-1"
	}

	localID, err := svc.SendText(context.Background(), 42, "hello", 0)
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if localID != "uuid-1" {
		t.Fatalf("localID = %s want uuid-1", localID)
	}
	rec := store.record("uuid-1")
	if rec == nil || rec.State != events.OutgoingStatePending || rec.Text != "hello" || rec.ChatID != 42 {
		t.Fatalf("optimistic record missing or wrong: %+v", rec)
	}

	got := drainEvents(t, ch, 2, 2*time.Second)
	if len(got) < 2 {
		t.Fatalf("got %d events, want 2: %+v", len(got), got)
	}
	if got[0].State != events.OutgoingStatePending || got[0].LocalID != "uuid-1" {
		t.Fatalf("first event: %+v", got[0])
	}
	if got[1].State != events.OutgoingStateSent || got[1].ServerID != 999 {
		t.Fatalf("second event: %+v", got[1])
	}

	// Wait for the storage update to settle before snapshotting.
	deadline := time.After(time.Second)
	for {
		if u := store.snapshot(); len(u) == 1 && u[0].State == events.OutgoingStateSent {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("storage state never reached sent: %+v", store.snapshot())
		case <-time.After(2 * time.Millisecond):
		}
	}
}

func TestSendService_FloodWait_RetriesAfterDuration(t *testing.T) {
	t.Parallel()
	store := newInMemoryOutgoing()
	bus := events.New()
	sender := &scriptedSender{steps: []sendStep{
		{err: &FloodWaitError{RetryAfter: 5 * time.Millisecond}},
		{id: 7},
	}}
	svc := NewSendService(sender, store, bus, nil, SendConfig{})

	if _, err := svc.SendText(context.Background(), 1, "x", 0); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	deadline := time.After(time.Second)
	for {
		if u := store.snapshot(); len(u) == 1 && u[0].State == events.OutgoingStateSent {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("never sent; updates=%+v calls=%d", store.snapshot(), sender.callCount())
		case <-time.After(2 * time.Millisecond):
		}
	}
	if sender.callCount() != 2 {
		t.Fatalf("call count = %d, want 2 (1 floodwait + 1 success)", sender.callCount())
	}
}

func TestSendService_ValidationError_NoRetry(t *testing.T) {
	t.Parallel()
	store := newInMemoryOutgoing()
	bus := events.New()
	sender := &scriptedSender{steps: []sendStep{
		{err: &ValidationError{Reason: "MESSAGE_TOO_LONG"}},
	}}
	svc := NewSendService(sender, store, bus, nil, SendConfig{})

	if _, err := svc.SendText(context.Background(), 1, "x", 0); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	deadline := time.After(time.Second)
	for {
		if u := store.snapshot(); len(u) == 1 && u[0].State == events.OutgoingStateFailed {
			if u[0].Error != "MESSAGE_TOO_LONG" {
				t.Fatalf("error string lost: %+v", u[0])
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("never failed; updates=%+v", store.snapshot())
		case <-time.After(2 * time.Millisecond):
		}
	}
	if sender.callCount() != 1 {
		t.Fatalf("validation must not retry; call count = %d", sender.callCount())
	}
}

func TestSendService_NetworkError_ExponentialBackoff(t *testing.T) {
	t.Parallel()
	store := newInMemoryOutgoing()
	bus := events.New()
	netErr := errors.New("conn reset")
	sender := &scriptedSender{steps: []sendStep{
		{err: netErr},
		{err: netErr},
		{id: 5},
	}}
	cfg := SendConfig{
		MaxNetworkRetries: 3,
		InitialBackoff:    1 * time.Millisecond,
		BackoffCap:        10 * time.Millisecond,
	}
	svc := NewSendService(sender, store, bus, nil, cfg)
	svc.jitter = func() float64 { return 0.5 } // deterministic

	if _, err := svc.SendText(context.Background(), 1, "x", 0); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	deadline := time.After(time.Second)
	for {
		if u := store.snapshot(); len(u) == 1 && u[0].State == events.OutgoingStateSent {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("never sent; updates=%+v calls=%d", store.snapshot(), sender.callCount())
		case <-time.After(2 * time.Millisecond):
		}
	}
	if sender.callCount() != 3 {
		t.Fatalf("call count = %d, want 3", sender.callCount())
	}
}

func TestSendService_NetworkError_RetriesExhausted(t *testing.T) {
	t.Parallel()
	store := newInMemoryOutgoing()
	bus := events.New()
	netErr := errors.New("io")
	sender := &scriptedSender{steps: []sendStep{
		{err: netErr}, {err: netErr}, {err: netErr}, {err: netErr},
	}}
	cfg := SendConfig{MaxNetworkRetries: 3, InitialBackoff: time.Millisecond, BackoffCap: 2 * time.Millisecond}
	svc := NewSendService(sender, store, bus, nil, cfg)
	svc.jitter = func() float64 { return 0 }

	if _, err := svc.SendText(context.Background(), 1, "x", 0); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	deadline := time.After(time.Second)
	for {
		if u := store.snapshot(); len(u) == 1 && u[0].State == events.OutgoingStateFailed {
			if u[0].Error == "" {
				t.Fatalf("failed without error message: %+v", u[0])
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("never failed; calls=%d updates=%+v", sender.callCount(), store.snapshot())
		case <-time.After(2 * time.Millisecond):
		}
	}
	if sender.callCount() != 4 {
		t.Fatalf("call count = %d, want 4 (initial + 3 retries)", sender.callCount())
	}
}

func TestSendService_RejectsEmptyText(t *testing.T) {
	t.Parallel()
	svc := NewSendService(&scriptedSender{}, newInMemoryOutgoing(), events.New(), nil, SendConfig{})
	if _, err := svc.SendText(context.Background(), 1, "", 0); err == nil {
		t.Fatalf("SendText must reject empty text")
	}
}

func TestSendService_RejectsZeroChatID(t *testing.T) {
	t.Parallel()
	svc := NewSendService(&scriptedSender{}, newInMemoryOutgoing(), events.New(), nil, SendConfig{})
	if _, err := svc.SendText(context.Background(), 0, "x", 0); err == nil {
		t.Fatalf("SendText must reject chat_id=0")
	}
}

func TestSendService_StoreSaveError_ReturnedSync(t *testing.T) {
	t.Parallel()
	store := newInMemoryOutgoing()
	store.saveErr = errors.New("disk full")
	svc := NewSendService(&scriptedSender{}, store, events.New(), nil, SendConfig{})

	_, err := svc.SendText(context.Background(), 1, "x", 0)
	if err == nil || !errors.Is(err, store.saveErr) {
		t.Fatalf("expected wrapped saveErr, got %v", err)
	}
}

func TestSendService_ServiceShutdownAbortsRetry(t *testing.T) {
	t.Parallel()
	store := newInMemoryOutgoing()
	bus := events.New()
	sender := &scriptedSender{steps: []sendStep{
		{err: errors.New("net")},
		{err: errors.New("net")},
	}}
	cfg := SendConfig{MaxNetworkRetries: 5, InitialBackoff: 50 * time.Millisecond, BackoffCap: time.Second}
	svc := NewSendService(sender, store, bus, nil, cfg)
	svc.jitter = func() float64 { return 0 }
	// Service-level ctx scopes the deliver goroutine. Cancelling it
	// (the wiring layer does this on TUI shutdown) aborts any in-flight
	// retry loop and lands the record in Failed. The caller's per-send
	// ctx, in contrast, only scopes the synchronous SaveOutgoing — it
	// has no effect on deliver because the input-pane closure cancels
	// its ctx the moment SendText returns.
	bgCtx, bgCancel := context.WithCancel(context.Background())
	svc.WithBackgroundContext(bgCtx)

	if _, err := svc.SendText(context.Background(), 1, "x", 0); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	// Cancel the service ctx before the first backoff completes.
	time.Sleep(5 * time.Millisecond)
	bgCancel()

	deadline := time.After(time.Second)
	for {
		if u := store.snapshot(); len(u) == 1 && u[0].State == events.OutgoingStateFailed {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("never failed after cancel; calls=%d updates=%+v", sender.callCount(), store.snapshot())
		case <-time.After(2 * time.Millisecond):
		}
	}
}

func TestSendService_CallerCtxCancelDoesNotAbortDeliver(t *testing.T) {
	t.Parallel()
	// Regression: the input pane's tea.Cmd closure cancels its ctx
	// the moment SendText returns. SendService must not treat that as
	// a shutdown signal — otherwise every send fails before the first
	// sender RPC completes.
	store := newInMemoryOutgoing()
	bus := events.New()
	sender := &scriptedSender{steps: []sendStep{{id: 42}}}
	svc := NewSendService(sender, store, bus, nil, SendConfig{})

	callerCtx, cancelCaller := context.WithCancel(context.Background())
	if _, err := svc.SendText(callerCtx, 1, "x", 0); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	cancelCaller()

	deadline := time.After(time.Second)
	for {
		if u := store.snapshot(); len(u) == 1 && u[0].State == events.OutgoingStateSent {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("deliver aborted after caller cancel; updates=%+v calls=%d",
				store.snapshot(), sender.callCount())
		case <-time.After(2 * time.Millisecond):
		}
	}
}

func TestNextBackoff_DoublesUpToCap(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, maxWait, want time.Duration
	}{
		{500 * time.Millisecond, 8 * time.Second, time.Second},
		{4 * time.Second, 8 * time.Second, 8 * time.Second},
		{8 * time.Second, 8 * time.Second, 8 * time.Second},
	}
	for _, c := range cases {
		got := nextBackoff(c.in, c.maxWait)
		if got != c.want {
			t.Fatalf("nextBackoff(%v, %v) = %v, want %v", c.in, c.maxWait, got, c.want)
		}
	}
}

func TestJitterDuration_StaysWithin10Percent(t *testing.T) {
	t.Parallel()
	base := time.Second
	for _, j := range []float64{0, 0.25, 0.5, 0.75, 0.999} {
		got := jitterDuration(base, j)
		min := time.Duration(float64(base) * 0.9)
		max := time.Duration(float64(base) * 1.1)
		if got < min || got > max {
			t.Fatalf("jitter(%v, %v) = %v out of [%v, %v]", base, j, got, min, max)
		}
	}
}

// drainEvents collects up to want events from ch within d. Mirrors
// collectEvents but reuses an existing subscription so the caller can
// guarantee subscribe-before-publish ordering.
func drainEvents(t *testing.T, ch <-chan events.Event, want int, d time.Duration) []events.OutgoingMessageStateChanged {
	t.Helper()
	deadline := time.After(d)
	var out []events.OutgoingMessageStateChanged
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			if e, ok := ev.(events.OutgoingMessageStateChanged); ok {
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

// silence unused warning if the symbol gets removed by future refactors
var _ = collectEvents

// stubLimiter records every Wait invocation and can be programmed to
// return an error (e.g. ctx cancellation) on the first or Nth call so
// the integration with deliver can be exercised without a real
// TokenBucket clock.
type stubLimiter struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (s *stubLimiter) Wait(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.err
}

func (s *stubLimiter) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// TestSendService_RateLimiter_ConsultedBeforeEverySend exercises the
// happy-path wiring: the deliver loop must call limiter.Wait before
// each MTProto attempt (initial + every retry) so a sustained burst of
// transient failures cannot cumulatively exceed the configured ceiling.
func TestSendService_RateLimiter_ConsultedBeforeEverySend(t *testing.T) {
	t.Parallel()
	store := newInMemoryOutgoing()
	bus := events.New()
	netErr := errors.New("conn reset")
	sender := &scriptedSender{steps: []sendStep{
		{err: netErr},
		{err: netErr},
		{id: 7},
	}}
	cfg := SendConfig{MaxNetworkRetries: 3, InitialBackoff: time.Millisecond, BackoffCap: 2 * time.Millisecond}
	limiter := &stubLimiter{}
	svc := NewSendService(sender, store, bus, nil, cfg).WithRateLimiter(limiter)
	svc.jitter = func() float64 { return 0 }

	if _, err := svc.SendText(context.Background(), 1, "x", 0); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	deadline := time.After(time.Second)
	for {
		if u := store.snapshot(); len(u) == 1 && u[0].State == events.OutgoingStateSent {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("never sent; calls=%d limiter=%d updates=%+v",
				sender.callCount(), limiter.callCount(), store.snapshot())
		case <-time.After(2 * time.Millisecond):
		}
	}
	if got, want := limiter.callCount(), sender.callCount(); got != want {
		t.Fatalf("limiter.Wait calls = %d, want %d (one per attempt)", got, want)
	}
}

// TestSendService_RateLimiter_CancellationFailsSend covers the cancel
// path: if the limiter returns ctx.Err() (e.g. service shutdown while
// the deliver goroutine is parked waiting for a token) the send must
// land in Failed without ever calling the underlying sender.
func TestSendService_RateLimiter_CancellationFailsSend(t *testing.T) {
	t.Parallel()
	store := newInMemoryOutgoing()
	bus := events.New()
	sender := &scriptedSender{steps: []sendStep{{id: 1}}}
	limiter := &stubLimiter{err: context.Canceled}
	svc := NewSendService(sender, store, bus, nil, SendConfig{}).WithRateLimiter(limiter)

	if _, err := svc.SendText(context.Background(), 1, "x", 0); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	deadline := time.After(time.Second)
	for {
		if u := store.snapshot(); len(u) == 1 && u[0].State == events.OutgoingStateFailed {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("never failed after limiter cancel; updates=%+v calls=%d limiter=%d",
				store.snapshot(), sender.callCount(), limiter.callCount())
		case <-time.After(2 * time.Millisecond):
		}
	}
	if sender.callCount() != 0 {
		t.Fatalf("sender must not be called after limiter cancellation, calls=%d", sender.callCount())
	}
}

// TestSendService_NilRateLimiter_NoChange asserts the limiter remains
// optional: a SendService constructed without WithRateLimiter behaves
// exactly like the pre-Stage-3 baseline.
func TestSendService_NilRateLimiter_NoChange(t *testing.T) {
	t.Parallel()
	store := newInMemoryOutgoing()
	bus := events.New()
	sender := &scriptedSender{steps: []sendStep{{id: 99}}}
	svc := NewSendService(sender, store, bus, nil, SendConfig{})

	if _, err := svc.SendText(context.Background(), 1, "x", 0); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	deadline := time.After(time.Second)
	for {
		if u := store.snapshot(); len(u) == 1 && u[0].State == events.OutgoingStateSent {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("nil-limiter happy path broken: updates=%+v", store.snapshot())
		case <-time.After(2 * time.Millisecond):
		}
	}
}
