package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	tgclient "github.com/kar43lov/lazytg/internal/tg"
)

// errSessionStopped is returned by Start when the supervisor has already been
// torn down, so a reconnect attempt that lands during shutdown cannot quietly
// resurrect a session the user closed.
//
// It wraps context.Canceled deliberately. ReconnectManager treats a cancelled
// context as user-initiated shutdown and stops; without the wrapping it would
// read "supervisor stopped" as one more failed attempt and keep retrying with
// backoff against a supervisor that will never start anything again — quiet,
// bounded, and pointless work for as long as the process takes to exit.
var errSessionStopped = fmt.Errorf("session supervisor is stopped: %w", context.Canceled)

// errSessionStuck is returned by Restart when the session it is replacing did
// not finish unwinding in time. It is deliberately retryable — unlike
// errSessionStopped it does not wrap context.Canceled — so the reconnect
// manager backs off and tries again once the old session is really gone.
var errSessionStuck = errors.New("previous session did not stop in time; not opening a second one")

// sessionSupervisor owns the lifetime of the MTProto run loop.
//
// gotd's Client.Run holds exactly one connection. It reconnects internally
// while it is running — v0.161 reports those transitions through
// OnConnectionState — but the moment Run returns, the session is over and
// every service the UI holds is bound to a client that can no longer send or
// receive. Nothing used to restart it: reconnectAdapter.Connect returned nil
// without doing anything, and ReconnectManager.Run was not started at all, so
// a dropped session stayed dropped until the user quit lazytg. The only hint
// was one warn line saying sends would fail "until restart".
//
// Restart is what ReconnectManager calls, through that adapter, once it sees
// a disconnect. Start and Restart are serialised with Stop by a mutex so a
// reconnect racing a shutdown ends with the session down rather than with a
// fresh connection nobody owns.
//
// The parent context is held as a field on purpose: it is the lifetime the
// sessions belong to (the TUI's background context), and a reconnect must
// inherit it rather than whatever context the caller happened to pass to
// Connect — that one may be scoped to a single attempt.
type sessionSupervisor struct {
	parent context.Context //nolint:containedctx // owns session lifetime, see doc comment
	client *tgclient.Client
	log    *slog.Logger

	// gapRecovery, when set, is started once a session reports authorised
	// and runs for that session's lifetime. It carries updates.Manager,
	// which needs a live connection and the account's own id, so it cannot
	// be started before the handshake or reused across sessions.
	gapRecovery func(context.Context) error

	// grace is how long a teardown may take before it is treated as stuck.
	// A field rather than the constant directly so the timeout branch —
	// which decides whether a second session may open — is testable in
	// milliseconds instead of seconds.
	grace time.Duration

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	stopped bool
}

// newSessionSupervisor wires a supervisor around an already-built client.
// parent bounds every session it will ever start.
func newSessionSupervisor(parent context.Context, client *tgclient.Client, log *slog.Logger) *sessionSupervisor {
	return &sessionSupervisor{parent: parent, client: client, log: log, grace: shutdownGrace}
}

// Start brings a session up and blocks until it is authorised and holding the
// connection open, the attempt fails, timeout elapses, or the parent context
// is cancelled. A non-nil return means no session is running: the goroutine
// has been cancelled and waited for, so the caller can fall back to the
// cached-only view without leaving a connection attempt behind it.
func (s *sessionSupervisor) Start(ctx context.Context, timeout time.Duration) error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return errSessionStopped
	}
	sessionCtx, cancel := context.WithCancel(s.parent)
	// ready carries the outcome of this attempt. Buffered so the goroutine
	// never blocks on a send after we have stopped waiting.
	ready := make(chan error, 1)
	// done closes when the goroutine is fully gone, so shutdown can wait for
	// it. Without that wait, cancelling the context returns immediately and
	// the caller proceeds to close the SQLite handle while the goroutine may
	// still be persisting an update.
	done := make(chan struct{})
	s.cancel, s.done = cancel, done
	s.mu.Unlock()

	go s.run(sessionCtx, ready, done)

	if err := s.await(ctx, ready, timeout); err != nil {
		s.teardown(cancel, done)
		if errors.Is(err, errNotAuthorized) {
			// A revoked authorisation does not heal by waiting. Left
			// retryable, the reconnect manager would re-offer a dead auth key
			// every backoff period for as long as the TUI stays open — a
			// client repeatedly presenting credentials Telegram has already
			// invalidated, which is exactly the behavioural trace this
			// project tries not to leave. Marking the supervisor stopped
			// makes the next Restart terminal; wrapping context.Canceled
			// stops the manager on this attempt rather than the next one.
			s.markStopped()
			return fmt.Errorf("%w: %w", err, context.Canceled)
		}
		return err
	}
	return nil
}

// markStopped closes the supervisor to further sessions without touching a
// running one — there is none at the call site, Start having just failed.
func (s *sessionSupervisor) markStopped() {
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
}

// Restart tears the current session down and starts a fresh one. This is the
// body of reconnectAdapter.Connect: ReconnectManager's contract is that a nil
// return means "ready to receive updates", which is exactly what Start
// guarantees.
//
// The old session is stopped first rather than left to expire. Two live Run
// loops on one account are two device slots as far as Telegram is concerned,
// and unofficial clients are already under observation — see the ban-risk
// note in CLAUDE.md.
func (s *sessionSupervisor) Restart(ctx context.Context) error {
	if !s.stopCurrent() {
		// Starting anyway would put two Run loops on one account, which
		// Telegram sees as two devices logging in — on a client already
		// under observation for being unofficial, and repeatedly, since a
		// flapping link retries. Staying offline one more backoff is the
		// cheaper failure by a wide margin.
		return errSessionStuck
	}
	return s.Start(ctx, attachTimeout)
}

// Stop cancels the running session and waits briefly for its goroutine. It is
// idempotent, and after it returns no further session can be started — a
// reconnect attempt in flight during shutdown gets errSessionStopped.
func (s *sessionSupervisor) Stop() {
	s.markStopped()
	s.stopCurrent()
}

// run drives one Client.Run and reports the outcome of the handshake on
// ready. It is the goroutine body; everything it needs is passed in so a
// session started after this one cannot have its channels swapped underneath.
func (s *sessionSupervisor) run(sessionCtx context.Context, ready chan<- error, done chan struct{}) {
	// The gap-recovery goroutine has to be joined before done closes, and
	// not merely cancelled. done is what Restart waits on before starting
	// the next session, and the update manager is shared across sessions:
	// the next session resets it, so a previous run still unwinding would
	// have its state torn out from under it while its own goroutines are
	// still writing pts to the same rows.
	var gap sync.WaitGroup
	defer func() {
		gap.Wait()
		close(done)
	}()
	runErr := s.client.Run(sessionCtx, func(runCtx context.Context) error {
		authorized, err := s.client.IsAuthorized(runCtx)
		if err != nil {
			ready <- fmt.Errorf("auth status: %w", err)
			return err
		}
		if !authorized {
			ready <- errNotAuthorized
			return errNotAuthorized
		}

		s.startGapRecovery(runCtx, &gap)

		// Report readiness and let the caller do the attaching. Calling
		// AttachClient from here would race the timeout: cancelling a context
		// does not interrupt code already running, so a handshake finishing
		// just after the deadline would wire services onto the App while the
		// caller was already building the UI from the offline values. The
		// caller attaches only on the success branch, which closes that
		// window by construction rather than by a well-placed check.
		ready <- nil

		// Hold the connection open. Returning here would tear down the
		// session the services are about to bind to.
		<-runCtx.Done()
		return runCtx.Err()
	})
	// Run can fail before the callback ever executes (DNS, handshake,
	// migration). Report that instead of letting the wait time out.
	select {
	case ready <- runErr:
	default:
		// Nobody is waiting any more, so this is the session ending
		// mid-flight rather than a failed handshake. Client.Run has already
		// signalled OnDisconnect, which is what ReconnectManager acts on;
		// this line is the human-readable half of the same event.
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			s.log.Warn("tui: telegram session ended, reconnecting", "err", runErr)
		}
	}
}

// startGapRecovery launches the update manager for this session, if one was
// supplied. Its failure is reported and then tolerated: a manager whose Run
// never succeeded still forwards updates to the handler as they arrive,
// which is exactly what lazytg did before the manager was wired. Tearing the
// session down over it would trade "no gap recovery" for "no updates".
func (s *sessionSupervisor) startGapRecovery(runCtx context.Context, gap *sync.WaitGroup) {
	if s.gapRecovery == nil {
		return
	}
	gap.Add(1)
	go func() {
		defer gap.Done()
		if err := s.gapRecovery(runCtx); err != nil && !errors.Is(err, context.Canceled) {
			s.log.Warn("tui: update gap recovery stopped — live updates continue without it", "err", err)
		}
	}()
}

// await blocks for the handshake outcome. The double read on the timeout
// branch is not redundant: select picks randomly among ready cases, so a
// session that connected in the same instant the deadline fired could
// otherwise be torn down for no reason.
func (s *sessionSupervisor) await(ctx context.Context, ready <-chan error, timeout time.Duration) error {
	select {
	case err := <-ready:
		return err
	case <-time.After(timeout):
		select {
		case err := <-ready:
			if err == nil {
				s.log.Info("tui: telegram session came up just before the deadline")
			}
			return err
		default:
			return fmt.Errorf("telegram did not connect within %s", timeout)
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

// stopCurrent cancels whichever session is running now, if any, and waits for
// its goroutine to unwind. It reports whether the session is actually gone:
// callers that go on to start another one must not do so on a false.
func (s *sessionSupervisor) stopCurrent() bool {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel, s.done = nil, nil
	s.mu.Unlock()
	if cancel == nil {
		return true
	}
	return s.wait(cancel, done)
}

// teardown is stopCurrent for a session whose handles the caller still holds:
// it clears the supervisor's own references only if they still point at this
// session, so a Start that failed cannot erase a Restart that has already
// succeeded.
func (s *sessionSupervisor) teardown(cancel context.CancelFunc, done chan struct{}) {
	s.mu.Lock()
	if s.done == done {
		s.cancel, s.done = nil, nil
	}
	s.mu.Unlock()
	s.wait(cancel, done)
}

// wait cancels and gives the goroutine its grace period to finish, reporting
// whether it did. The wait matters twice over: the caller closes the SQLite
// handle right after, and a goroutine still persisting an update would hit a
// closed database — and a session that has not returned yet still holds a
// connection to Telegram.
func (s *sessionSupervisor) wait(cancel context.CancelFunc, done <-chan struct{}) bool {
	cancel()
	select {
	case <-done:
		return true
	case <-time.After(s.grace):
		s.log.Warn("tui: telegram session did not stop in time", "waited", s.grace)
		return false
	}
}
