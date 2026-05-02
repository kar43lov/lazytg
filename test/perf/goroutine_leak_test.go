package perf

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/pgmac/lazytg/internal/app"
	"github.com/pgmac/lazytg/internal/core/config"
)

// TestApp_NoGoroutineLeaks runs the wire.Build → RunBackground →
// cancel cycle and asserts that no goroutines outlive the shutdown.
//
// The test deliberately exercises the production wiring (real SQLite,
// real bus, real LiveService and DegradationDetector) so a regression
// in any of those goroutines surfaces here. The MTProto-aware services
// (Updates, Reconnect, Polling) are *not* wired because they need a
// live gotd session — those have their own unit tests covering
// goroutine cleanup.
//
// goleak's IgnoreCurrent baseline is captured before Build so any
// goroutine the test runner itself created (testing.tRunner, the
// race-detector watchers, etc) is not flagged. The post-cancel
// settling window (100ms) is conservative — every goroutine in the
// non-MTProto path returns inside a single tick of contextSleep.
//
// Note on defer ordering: goleak.VerifyNone runs as the *last* defer
// (LIFO of defer statements), but t.Cleanup callbacks run after every
// defer. We therefore close the repo + cancel the contexts inline
// inside the test body so the verify pass observes a fully-quiesced
// goroutine set. database/sql's connectionOpener exits as part of
// (*DB).Close, so the explicit Close is what makes the verify clean.
func TestApp_NoGoroutineLeaks(t *testing.T) {
	defer goleak.VerifyNone(t,
		goleak.IgnoreCurrent(),
	)

	tmp := t.TempDir()
	logger := slog.New(slog.DiscardHandler)

	// Build expects the XDG paths to exist on disk (sqlite.Open does
	// not mkdir the parent). config.Resolve usually creates them; here
	// we use explicit paths and mkdir them up front.
	paths := config.Paths{
		Config: filepath.Join(tmp, "config"),
		Data:   filepath.Join(tmp, "data"),
		State:  filepath.Join(tmp, "state"),
		Cache:  filepath.Join(tmp, "cache"),
	}
	for _, p := range []string{paths.Config, paths.Data, paths.State, paths.Cache} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())

	runtime, err := app.Build(ctx, app.Config{
		Logger: logger,
		Paths:  paths,
	})
	if err != nil {
		cancel()
		t.Fatalf("app.Build: %v", err)
	}

	bgCtx, cancelBG := context.WithCancel(ctx)
	done := runtime.RunBackground(bgCtx)

	// Let the services run briefly so any deferred init
	// (e.g. the first degradation probe) has time to start its work.
	// 100ms is enough on a loaded CI runner without making the test
	// flaky on faster local boxes.
	time.Sleep(100 * time.Millisecond)

	cancelBG()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatalf("RunBackground did not exit within 2s after cancel")
	}

	// Close the repo explicitly so database/sql's connectionOpener
	// goroutine exits before VerifyNone runs.
	if err := runtime.Close(); err != nil {
		t.Errorf("runtime.Close: %v", err)
	}
	cancel()

	// Brief settle window for the closed-DB cleanup. database/sql
	// signals connectionOpener via channel close which is observed on
	// the next select tick; 50ms is a safe margin without slowing the
	// test.
	time.Sleep(50 * time.Millisecond)
}
