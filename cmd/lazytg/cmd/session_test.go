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
