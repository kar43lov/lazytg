package cmd

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

// TestSessionSupervisor_StopIsIdempotent covers the shutdown path when no
// session was ever started — the branch attachTelegram takes when the client
// could not be built at all.
func TestSessionSupervisor_StopIsIdempotent(t *testing.T) {
	t.Parallel()
	s := newSessionSupervisor(context.Background(), nil, slog.New(slog.DiscardHandler))
	s.Stop()
	s.Stop()
}

// TestSessionSupervisor_RestartAfterStop pins the shutdown race. Restart is
// called by ReconnectManager from its own goroutine, which is still running
// while the user quits: without the stopped flag, a disconnect observed
// during teardown would open a fresh MTProto session moments before the
// process exits — a connection nobody owns, and one more device-slot login on
// an account that unofficial clients already put under observation.
func TestSessionSupervisor_RestartAfterStop(t *testing.T) {
	t.Parallel()
	s := newSessionSupervisor(context.Background(), nil, slog.New(slog.DiscardHandler))
	s.Stop()

	err := s.Restart(context.Background())
	if !errors.Is(err, errSessionStopped) {
		t.Fatalf("Restart after Stop = %v want %v", err, errSessionStopped)
	}
}

// TestSessionSupervisor_AwaitTimesOut pins the deadline branch: a handshake
// that never reports readiness must give up rather than hold the TUI, and the
// message must name the budget so a slow link is distinguishable from a hang.
func TestSessionSupervisor_AwaitTimesOut(t *testing.T) {
	t.Parallel()
	s := newSessionSupervisor(context.Background(), nil, slog.New(slog.DiscardHandler))
	err := s.await(context.Background(), make(chan error), 10*time.Millisecond)
	if err == nil {
		t.Fatal("await returned nil for a session that never reported ready")
	}
}

// TestSessionSupervisor_AwaitPrefersALateReady is the reason await reads the
// channel twice. select picks at random among ready cases, so a session that
// came up in the same instant the deadline fired had a coin-flip chance of
// being torn down for nothing.
func TestSessionSupervisor_AwaitPrefersALateReady(t *testing.T) {
	t.Parallel()
	s := newSessionSupervisor(context.Background(), nil, slog.New(slog.DiscardHandler))
	ready := make(chan error, 1)
	ready <- nil
	if err := s.await(context.Background(), ready, time.Nanosecond); err != nil {
		t.Fatalf("await discarded a ready session that landed on the deadline: %v", err)
	}
}

// TestSessionSupervisor_StopIsAShutdownNotAFailure pins how the reconnect
// manager must read a stopped supervisor. Restart is called from the
// manager's own goroutine, which is still running while the user quits, and
// the manager's context is the TUI's rather than the session's — so without
// context.Canceled in the chain it would treat "stopped" as one more failed
// attempt and keep retrying against something that will never start again.
func TestSessionSupervisor_StopIsAShutdownNotAFailure(t *testing.T) {
	t.Parallel()
	s := newSessionSupervisor(context.Background(), nil, slog.New(slog.DiscardHandler))
	s.Stop()

	err := s.Restart(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Restart after Stop = %v — reconnect will keep retrying through shutdown", err)
	}
}

// TestSessionSupervisor_RefusesToOpenASecondSession is the ban-risk guard.
// A teardown that does not finish inside the grace period used to be a warn
// line and nothing more: Restart went on to open a new session while the old
// Run loop was still holding a connection. Two Run loops on one account are
// two devices logging in, on a client Telegram already watches for being
// unofficial, and a flapping link produces the situation over and over.
func TestSessionSupervisor_RefusesToOpenASecondSession(t *testing.T) {
	t.Parallel()
	s := newSessionSupervisor(context.Background(), nil, slog.New(slog.DiscardHandler))
	s.grace = 10 * time.Millisecond

	// A session whose goroutine never returns: done stays open.
	_, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})

	// The client is nil, so reaching Start would panic — not reaching it is
	// half the assertion, and the error names the reason.
	restartErr := s.Restart(context.Background())
	if !errors.Is(restartErr, errSessionStuck) {
		t.Fatalf("Restart = %v want errSessionStuck", restartErr)
	}
	// Retryable on purpose: the manager has to come back once the old
	// session is really gone, so this must not read as a shutdown the way
	// errSessionStopped deliberately does.
	if errors.Is(restartErr, context.Canceled) {
		t.Fatal("errSessionStuck wraps context.Canceled — the manager would stop retrying instead of waiting the old session out")
	}
}

// TestSessionSupervisor_WaitReportsACleanStop pins the other branch: a
// goroutine that does finish must not be reported as stuck, or every
// reconnect would spend one extra backoff refusing to start.
func TestSessionSupervisor_WaitReportsACleanStop(t *testing.T) {
	t.Parallel()
	s := newSessionSupervisor(context.Background(), nil, slog.New(slog.DiscardHandler))
	s.grace = time.Second

	_, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	close(done)

	if !s.wait(cancel, done) {
		t.Fatal("wait reported a finished goroutine as stuck")
	}
}

// TestSessionSupervisor_RevokedAuthIsTerminal covers the one failure that
// cannot be waited out. A session revoked from another device fails every
// attempt identically, so a retryable error would have the reconnect manager
// re-offer a dead auth key every backoff period for as long as the TUI stays
// open — a client repeatedly presenting credentials Telegram has already
// invalidated, on an account already watched for running an unofficial
// client.
func TestSessionSupervisor_RevokedAuthIsTerminal(t *testing.T) {
	t.Parallel()
	s := newSessionSupervisor(context.Background(), nil, slog.New(slog.DiscardHandler))
	s.grace = 10 * time.Millisecond

	// Stand in for the run goroutine reporting a revoked session.
	ready := make(chan error, 1)
	ready <- errNotAuthorized
	_, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	close(done)

	err := s.await(context.Background(), ready, time.Second)
	if !errors.Is(err, errNotAuthorized) {
		t.Fatalf("await = %v want errNotAuthorized", err)
	}
	// Start is what turns that into a terminal condition; drive the same
	// branch directly since Start needs a client to reach the goroutine.
	s.teardown(cancel, done)
	s.markStopped()

	if restartErr := s.Restart(context.Background()); !errors.Is(restartErr, context.Canceled) {
		t.Fatalf("Restart after a revoked session = %v — the manager would keep retrying a dead auth key", restartErr)
	}
}
