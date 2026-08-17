package tg

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/gotd/td/tgerr"

	coresync "github.com/kar43lov/lazytg/internal/core/sync"
)

func TestFloodWaiter_PassesThroughSuccess(t *testing.T) {
	t.Parallel()
	w := NewFloodWaiter(nil)
	called := 0
	err := w.Do(context.Background(), "rpc", func(context.Context) error {
		called++
		return nil
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if called != 1 {
		t.Fatalf("called = %d, want 1", called)
	}
}

func TestFloodWaiter_PropagatesNonFloodError(t *testing.T) {
	t.Parallel()
	w := NewFloodWaiter(nil)
	want := errors.New("nope")
	got := w.Do(context.Background(), "rpc", func(context.Context) error { return want })
	if !errors.Is(got, want) {
		t.Fatalf("Do = %v, want %v", got, want)
	}
}

func TestFloodWaiter_RetriesUntilSuccess(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	w := NewFloodWaiter(log)
	w.MaxRetries = 5
	calls := 0
	floodErr := tgerr.New(420, "FLOOD_WAIT_0") // 0-second wait keeps the test fast.
	start := time.Now()
	err := w.Do(context.Background(), "messages.sendMessage", func(context.Context) error {
		calls++
		if calls < 3 {
			return floodErr
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("retries took too long: %v", elapsed)
	}
	if !bytes.Contains(buf.Bytes(), []byte("flood wait: sleeping")) {
		t.Fatalf("expected flood-wait warning in log, got %q", buf.String())
	}
}

func TestFloodWaiter_GivesUpAfterMaxRetries(t *testing.T) {
	t.Parallel()
	w := NewFloodWaiter(nil)
	w.MaxRetries = 1
	floodErr := tgerr.New(420, "FLOOD_WAIT_0")
	calls := 0
	err := w.Do(context.Background(), "rpc", func(context.Context) error {
		calls++
		return floodErr
	})
	if err == nil {
		t.Fatalf("Do should fail after MaxRetries")
	}
	var fw *coresync.FloodWaitError
	if !errors.As(err, &fw) {
		t.Fatalf("error should be *coresync.FloodWaitError, got %T: %v", err, err)
	}
	if calls != w.MaxRetries+1 {
		t.Fatalf("calls = %d, want %d", calls, w.MaxRetries+1)
	}
}

func TestFloodWaiter_RespectsContextCancel(t *testing.T) {
	t.Parallel()
	w := NewFloodWaiter(nil)
	w.MaxRetries = 5
	ctx, cancel := context.WithCancel(context.Background())
	floodErr := tgerr.New(420, "FLOOD_WAIT_60")
	cancel()
	err := w.Do(ctx, "rpc", func(context.Context) error { return floodErr })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do err = %v, want context.Canceled", err)
	}
}

func TestAsFloodWaitDuration_HandlesBothShapes(t *testing.T) {
	t.Parallel()
	if _, ok := AsFloodWaitDuration(nil); ok {
		t.Fatalf("nil error must not be a flood wait")
	}
	if _, ok := AsFloodWaitDuration(errors.New("plain")); ok {
		t.Fatalf("plain error must not be a flood wait")
	}
	if d, ok := AsFloodWaitDuration(tgerr.New(420, "FLOOD_WAIT_4")); !ok || d != 4*time.Second {
		t.Fatalf("gotd flood err: ok=%v d=%v", ok, d)
	}
	if d, ok := AsFloodWaitDuration(&coresync.FloodWaitError{RetryAfter: 7 * time.Second}); !ok || d != 7*time.Second {
		t.Fatalf("core flood err: ok=%v d=%v", ok, d)
	}
}
