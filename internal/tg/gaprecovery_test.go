package tg

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
)

// stubUpdatesAPI is the three-method surface updates.Manager needs. It never
// reports a difference, which is all this test requires: the question is
// whether a manager can be run twice, not what it recovers.
type stubUpdatesAPI struct{}

func (stubUpdatesAPI) UpdatesGetState(context.Context) (*tg.UpdatesState, error) {
	return &tg.UpdatesState{Pts: 1, Qts: 1, Date: 1, Seq: 1}, nil
}

func (stubUpdatesAPI) UpdatesGetDifference(context.Context, *tg.UpdatesGetDifferenceRequest) (tg.UpdatesDifferenceClass, error) {
	return &tg.UpdatesDifferenceEmpty{}, nil
}

func (stubUpdatesAPI) UpdatesGetChannelDifference(context.Context, *tg.UpdatesGetChannelDifferenceRequest) (tg.UpdatesChannelDifferenceClass, error) {
	return &tg.UpdatesChannelDifferenceEmpty{}, nil
}

// runOnce starts a manager run and stops it, returning the run's error.
func runOnce(t *testing.T, m *updates.Manager, run func(context.Context) error) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()
	// Give the run a moment to reach its wait loop before pulling the
	// context: cancelling during setup would exercise a different path.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("manager run did not return after cancellation")
		return nil
	}
}

// TestRunManager_SurvivesASessionRestart is the regression for the failure a
// working reconnect introduces. The manager is the client's update handler
// and therefore outlives any single session, but it keeps its in-memory
// state after Run returns and nothing in gotd clears it — so the run that
// follows a reconnect refuses with "already authorized" and gap recovery is
// dead for the rest of the process, visible only as one warn line.
func TestRunManager_SurvivesASessionRestart(t *testing.T) {
	t.Parallel()
	m := NewUpdatesDispatcher(nil, nil).Manager(nil, nil)
	api := stubUpdatesAPI{}

	first := runOnce(t, m, func(ctx context.Context) error { return RunManager(ctx, m, api, 42) })
	if first != nil && !errors.Is(first, context.Canceled) {
		t.Fatalf("first run = %v want nil or context.Canceled", first)
	}

	second := runOnce(t, m, func(ctx context.Context) error { return RunManager(ctx, m, api, 42) })
	if second != nil && !errors.Is(second, context.Canceled) {
		t.Fatalf("second run = %v want nil or context.Canceled", second)
	}
	if second != nil && strings.Contains(second.Error(), "already authorized") {
		t.Fatal("second run refused as already authorized — the reset before Run is gone")
	}
}

// TestRunManager_WithoutAManager pins the nil case: attach falls back to the
// plain dispatcher when no state storage exists, and the supervisor must not
// be handed a recovery closure that panics instead of reporting.
func TestRunManager_WithoutAManager(t *testing.T) {
	t.Parallel()
	if err := RunManager(context.Background(), nil, stubUpdatesAPI{}, 42); err == nil {
		t.Fatal("RunManager returned nil for a nil manager")
	}
}
