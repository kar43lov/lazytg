package app

import (
	"context"
	"errors"
	"testing"
)

// TestReconnectAdapter_ConnectWithoutSupervisor covers the failure this
// adapter shipped with: Connect returned nil unconditionally, which told
// ReconnectManager that a dead session had been repaired. The manager then
// published "online" over a client that could no longer send, and every
// message the user typed failed against a green indicator. Saying so out loud
// is the whole point — an error keeps the state machine in "offline", which
// is at least true.
func TestReconnectAdapter_ConnectWithoutSupervisor(t *testing.T) {
	t.Parallel()
	adapter := reconnectAdapter{}
	if err := adapter.Connect(context.Background()); err == nil {
		t.Fatal("Connect returned nil without a supervisor — a dead session would be reported as repaired")
	}
}

// TestReconnectAdapter_ConnectDelegates pins that the adapter is a pass-through
// to the cmd layer's session supervisor and forwards its context and error.
func TestReconnectAdapter_ConnectDelegates(t *testing.T) {
	t.Parallel()
	type ctxKey struct{}
	want := errors.New("restart failed")
	var gotCtx context.Context
	calls := 0

	adapter := reconnectAdapter{restart: func(ctx context.Context) error {
		calls++
		gotCtx = ctx
		return want
	}}

	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")
	if err := adapter.Connect(ctx); !errors.Is(err, want) {
		t.Fatalf("Connect err = %v want %v", err, want)
	}
	if calls != 1 {
		t.Fatalf("restart called %d times want 1", calls)
	}
	if gotCtx.Value(ctxKey{}) != "marker" {
		t.Fatal("restart got a different context than Connect was given")
	}
}

// TestReconnectAdapter_ConnectionStatesWithoutClient guards the nil case: an
// App whose attach never produced a client must report "cannot describe
// state" rather than hand back a channel that never yields, which would leave
// the manager's pump goroutine parked forever.
func TestReconnectAdapter_ConnectionStatesWithoutClient(t *testing.T) {
	t.Parallel()
	if ch := (reconnectAdapter{}).ConnectionStates(); ch != nil {
		t.Fatal("ConnectionStates returned a channel for a nil client")
	}
}
