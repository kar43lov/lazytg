package sync

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/kar43lov/lazytg/internal/core/events"
)

// Connection state strings published on the bus via ConnectionStateChanged.
// They mirror the status-bar copy so the UI can render them verbatim
// without an extra mapping layer.
const (
	// ConnectionStateOnline — last reconnect attempt succeeded; live updates
	// flow through the dispatcher.
	ConnectionStateOnline = "online"
	// ConnectionStateConnecting — a reconnect attempt is in flight.
	ConnectionStateConnecting = "connecting"
	// ConnectionStateOffline — disconnect observed, waiting before next
	// reconnect attempt (or retries exhausted).
	ConnectionStateOffline = "offline"
)

// ReconnectClient is the gotd-free contract that ReconnectManager relies
// on. The real *internal/tg.Client satisfies it through its OnDisconnect
// channel (Stage 2 Task 4) and its connect/ping helpers (added alongside
// wire.go in Task 11). Defining the interface here keeps internal/core
// free of gotd imports and makes the reconnect loop trivially testable
// with an in-memory fake.
type ReconnectClient interface {
	// Connect establishes a fresh MTProto session. Implementations should
	// block until the session is fully usable (auth verified, dispatcher
	// re-registered) so a return signals "ready to receive updates".
	Connect(ctx context.Context) error
	// Ping issues a cheap RPC to verify liveness. ReconnectManager calls
	// Ping after every reconnect to confirm the new session before it
	// publishes "online" — a successful Connect that immediately drops
	// would otherwise look identical to a real recovery.
	Ping(ctx context.Context) error
	// OnDisconnect yields the terminal error of the most recent Run
	// invocation. The channel is owned by the implementation and must
	// never be closed; ReconnectManager pairs reads with ctx.Done.
	OnDisconnect() <-chan error
}

// ConnectionStateReporter is the optional second half of ReconnectClient: a
// client that can describe transport-level state on its own. The MTProto
// library reconnects inside an active session without the run loop ever
// returning, so a client that only signals OnDisconnect leaves the status
// indicator frozen through an entire outage and moves it only when the
// session dies outright.
//
// Implementations return a channel of the ConnectionState* values above; the
// channel must never be closed while the client is in use, and it may drop
// intermediate values (only the current state is interesting). Returning nil
// means "this client cannot report state", which is not an error.
type ConnectionStateReporter interface {
	ConnectionStates() <-chan string
}

// ReconnectConfig groups the optional knobs so the constructor signature
// stays stable. Zero values fall back to documented defaults — 1s/60s
// matches what gotd's own samples use, with ±10% jitter to avoid
// thundering-herd retries from many lazytg instances behind the same NAT.
type ReconnectConfig struct {
	// InitialBackoff is the wait before the first reconnect attempt.
	// Default: 1s.
	InitialBackoff time.Duration
	// MaxBackoff caps the per-attempt wait. Default: 60s.
	MaxBackoff time.Duration
	// MaxAttempts caps the number of consecutive reconnect attempts. 0
	// means "unlimited" — the default — because lazytg must keep trying
	// for as long as the user keeps the app open.
	MaxAttempts int
}

func (c *ReconnectConfig) defaults() {
	if c.InitialBackoff <= 0 {
		c.InitialBackoff = 1 * time.Second
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = 60 * time.Second
	}
}

// ReconnectManager owns the offline → connecting → online state machine.
// It reads disconnect signals from the underlying client, publishes a
// matching ConnectionStateChanged event on every transition, and drives
// reconnect attempts with exponential backoff + jitter so the UI status
// bar stays in sync with reality.
//
// The manager does not block other components: Run is meant to live in
// its own goroutine spawned from internal/app/wire.go, with cancellation
// flowing through ctx.
type ReconnectManager struct {
	client ReconnectClient
	bus    *events.Bus
	log    *slog.Logger
	cfg    ReconnectConfig

	// Test-only seams — real callers leave them at the defaults.
	now    func() time.Time
	jitter func() float64 // [0,1), used to scatter exponential backoff
	sleep  func(ctx context.Context, d time.Duration) error
}

// NewReconnectManager wires a manager. log may be nil — a no-op logger is
// used in that case so unit tests stay free of plumbing noise.
func NewReconnectManager(client ReconnectClient, bus *events.Bus, log *slog.Logger, cfg ReconnectConfig) *ReconnectManager {
	cfg.defaults()
	if log == nil {
		log = slog.New(noopHandler{})
	}
	return &ReconnectManager{
		client: client,
		bus:    bus,
		log:    log,
		cfg:    cfg,
		now:    time.Now,
		jitter: rand.Float64,
		sleep:  contextSleep,
	}
}

// Run blocks until ctx is cancelled, listening for disconnect signals and
// driving reconnect attempts. The call returns ctx.Err on a clean shutdown
// so call sites can wire it directly into errgroup-style supervisors.
//
// Lifecycle:
//
//  1. Wait on OnDisconnect (or ctx.Done).
//  2. On any non-nil error: publish "offline", enter retry loop.
//  3. Each retry: publish "connecting", call Connect+Ping, on success
//     publish "online" and resume step 1; on failure back off and retry.
//  4. context.Canceled disconnect errors are treated as user-initiated
//     shutdown — no reconnect attempt is made.
func (m *ReconnectManager) Run(ctx context.Context) error {
	// Transport-level transitions (step 0, effectively) are pumped in
	// parallel: the retry loop below blocks for as long as a backoff lasts,
	// and a state feed that is only read between disconnects would deliver
	// an outage's worth of news minutes late.
	if reporter, ok := m.client.(ConnectionStateReporter); ok {
		if states := reporter.ConnectionStates(); states != nil {
			go m.pumpStates(ctx, states)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-m.client.OnDisconnect():
			if err == nil || errors.Is(err, context.Canceled) {
				// Clean shutdown: do not reconnect.
				return ctx.Err()
			}
			m.log.Warn("reconnect: disconnect observed", "err", err)
			m.publish(ConnectionStateOffline)
			if rerr := m.retry(ctx); rerr != nil {
				return rerr
			}
		}
	}
}

// retry drives the reconnect loop with exponential backoff + jitter. It
// returns ctx.Err when the caller's context is cancelled and nil once a
// reconnect succeeds (so Run can resume listening for the next
// disconnect). MaxAttempts == 0 means "retry forever".
func (m *ReconnectManager) retry(ctx context.Context) error {
	backoff := m.cfg.InitialBackoff
	for attempt := 1; ; attempt++ {
		if err := m.sleep(ctx, jitterDuration(backoff, m.jitter())); err != nil {
			return err
		}
		m.publish(ConnectionStateConnecting)
		m.log.Info("reconnect: attempting", "attempt", attempt, "backoff", backoff)

		if err := m.client.Connect(ctx); err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			m.log.Warn("reconnect: connect failed", "attempt", attempt, "err", err)
			m.publish(ConnectionStateOffline)
			if m.cfg.MaxAttempts > 0 && attempt >= m.cfg.MaxAttempts {
				return nil
			}
			backoff = nextBackoff(backoff, m.cfg.MaxBackoff)
			continue
		}
		m.dropStaleDisconnect()
		if err := m.client.Ping(ctx); err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			m.log.Warn("reconnect: ping failed", "attempt", attempt, "err", err)
			m.publish(ConnectionStateOffline)
			if m.cfg.MaxAttempts > 0 && attempt >= m.cfg.MaxAttempts {
				return nil
			}
			backoff = nextBackoff(backoff, m.cfg.MaxBackoff)
			continue
		}
		m.log.Info("reconnect: online", "attempt", attempt)
		m.publish(ConnectionStateOnline)
		return nil
	}
}

// pumpStates republishes the client's own connection-state feed onto the bus
// until ctx is cancelled. Repeats are swallowed: gotd reports "connecting"
// once per attempt, and a run of identical events would wake every bus
// subscriber (the whole UI included) to redraw a cell that did not change.
//
// The pump and the retry loop below can both publish. That is deliberate and
// not a race for correctness: they describe the same connection, the pump
// while the run loop is alive and the retry loop once it is not, and a status
// indicator only ever shows the most recent value.
func (m *ReconnectManager) pumpStates(ctx context.Context, states <-chan string) {
	last := ""
	for {
		select {
		case <-ctx.Done():
			return
		case state, ok := <-states:
			if !ok {
				return
			}
			if state == "" || state == last {
				continue
			}
			last = state
			m.log.Debug("reconnect: transport state", "state", state)
			m.publish(state)
		}
	}
}

// dropStaleDisconnect discards a disconnect signal left over from the session
// Connect just replaced. Standing a new session up means tearing the old one
// down, and that teardown reports itself on the same channel — so without
// this, the loop below reads its own doing as a fresh failure the moment it
// finishes recovering.
//
// The consequences were not subtle. A cancelled run loop reports
// context.Canceled, which Run treats as user-initiated shutdown, so the
// manager returned and no further disconnect was ever handled: reconnection
// worked exactly once per process. A failed attempt instead leaves a real
// error behind, and that one sends the manager into a cycle of tearing down
// the session it had just brought back — an account logging in over and over
// on a client Telegram already watches for being unofficial.
//
// The trigger is narrower than "any reconnect", which is why it is worth
// naming: a session that died on its own has already reported, and the
// manager has already consumed that report, so replacing it adds nothing to
// the channel. The leftover appears when the session being replaced was
// still alive — the previous attempt connected and then failed its Ping, or
// its handshake failed after a live session had been torn down for it. That
// is the flapping case, not an exotic one.
//
// Called between Connect and Ping on purpose. Anything in the channel at that
// point predates the new session by construction, and Ping is what confirms
// the new session is alive, so a genuine failure in the gap between them is
// caught rather than swallowed.
func (m *ReconnectManager) dropStaleDisconnect() {
	select {
	case err := <-m.client.OnDisconnect():
		m.log.Debug("reconnect: dropped a disconnect from the replaced session", "err", err)
	default:
	}
}

// publish is a nil-safe wrapper around Bus.Publish so ReconnectManager can
// be constructed without a bus in the smallest possible test setup.
func (m *ReconnectManager) publish(state string) {
	if m.bus == nil {
		return
	}
	m.bus.Publish(events.ConnectionStateChanged{State: state})
}

// contextSleep is the production sleep impl used by ReconnectManager and
// DegradationDetector. It returns ctx.Err on cancellation and nil on a
// clean wake-up so callers can chain it directly into select-style
// retry loops.
func contextSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
