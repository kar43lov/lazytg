package sync

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pgmac/lazytg/internal/core/domain"
	"github.com/pgmac/lazytg/internal/core/events"
)

// recordingStore captures every SaveMessage call so tests can assert on
// the exact sequence of writes the LiveService produced.
type recordingStore struct {
	mu     sync.Mutex
	calls  []domain.Message
	errOn  int
	errVal error
}

func (s *recordingStore) SaveMessage(_ context.Context, m domain.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, m)
	if s.errOn > 0 && len(s.calls) == s.errOn {
		return s.errVal
	}
	return nil
}

func (s *recordingStore) snapshot() []domain.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Message, len(s.calls))
	copy(out, s.calls)
	return out
}

func TestLiveService_PersistsMessageReceived(t *testing.T) {
	t.Parallel()
	bus := events.New()
	store := &recordingStore{}
	svc := NewLiveService(store, bus, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := svc.Start(ctx)

	when := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	bus.Publish(events.MessageReceived{
		ChatID: 1, MessageID: 100, Text: "hello", FromID: 7, Date: when,
	})

	deadline := time.After(time.Second)
	for {
		if got := store.snapshot(); len(got) == 1 {
			if got[0].ID != 100 || got[0].ChatID != 1 || got[0].Text != "hello" {
				t.Fatalf("saved message mismatch: %+v", got[0])
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for persist; calls=%d", len(store.snapshot()))
		case <-time.After(2 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Start drain did not exit after cancel")
	}
}

func TestLiveService_IgnoresUnrelatedEvents(t *testing.T) {
	t.Parallel()
	bus := events.New()
	store := &recordingStore{}
	svc := NewLiveService(store, bus, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = svc.Start(ctx)

	bus.Publish(events.DialogUpdated{ChatID: 1})
	bus.Publish(events.ConnectionStateChanged{State: "online"})
	time.Sleep(20 * time.Millisecond)
	if got := store.snapshot(); len(got) != 0 {
		t.Fatalf("expected no calls, got %+v", got)
	}
}

func TestLiveService_SwallowsStorageError(t *testing.T) {
	t.Parallel()
	bus := events.New()
	store := &recordingStore{errOn: 1, errVal: errors.New("io")}
	svc := NewLiveService(store, bus, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = svc.Start(ctx)

	when := time.Now().UTC()
	bus.Publish(events.MessageReceived{ChatID: 1, MessageID: 1, Text: "x", Date: when})
	bus.Publish(events.MessageReceived{ChatID: 1, MessageID: 2, Text: "y", Date: when})

	deadline := time.After(time.Second)
	for {
		if got := store.snapshot(); len(got) >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out; calls=%d", len(store.snapshot()))
		case <-time.After(2 * time.Millisecond):
		}
	}
}

func TestLiveService_DoesNotRepublishDialogUpdated(t *testing.T) {
	t.Parallel()

	// Earlier revisions republished events.DialogUpdated from persist to
	// nudge the chats pane reload. That self-publish landed in
	// LiveService's own subscription buffer (Bus.Publish fans out to all
	// subscribers including the producer) which halved effective capacity
	// under burst. The chats pane now reacts directly to MessageReceived,
	// so persist must NOT emit DialogUpdated.
	bus := events.New()
	store := &recordingStore{}
	svc := NewLiveService(store, bus, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Subscribe a probe BEFORE Start so any DialogUpdated from persist would
	// reach this channel. The probe's drain runs on the test goroutine so we
	// can assert no DialogUpdated arrived.
	probe := bus.Subscribe(ctx)

	_ = svc.Start(ctx)

	bus.Publish(events.MessageReceived{ChatID: 7, MessageID: 1, Text: "x", Date: time.Now().UTC()})

	// Wait for the persist call to complete.
	deadline := time.After(time.Second)
	for {
		if got := store.snapshot(); len(got) >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("persist did not run; calls=%d", len(store.snapshot()))
		case <-time.After(2 * time.Millisecond):
		}
	}

	// Drain the probe channel briefly. It should see only the original
	// MessageReceived (the bus delivers to every subscriber including this
	// probe), and never a DialogUpdated.
	timeout := time.After(50 * time.Millisecond)
	sawMessage := false
loop:
	for {
		select {
		case ev := <-probe:
			switch ev.(type) {
			case events.MessageReceived:
				sawMessage = true
			case events.DialogUpdated:
				t.Fatalf("LiveService.persist must not republish DialogUpdated")
			}
		case <-timeout:
			break loop
		}
	}
	if !sawMessage {
		t.Fatalf("probe should have observed the original MessageReceived")
	}
}

func TestLiveService_RecordsLastIngestLatency(t *testing.T) {
	t.Parallel()
	bus := events.New()
	store := &recordingStore{}
	svc := NewLiveService(store, bus, nil)

	// Override the now() clock to advance by 5ms per call so the
	// measured latency is deterministic.
	tickCount := atomic.Int64{}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc.now = func() time.Time {
		n := tickCount.Add(1)
		return base.Add(time.Duration(n) * 5 * time.Millisecond)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = svc.Start(ctx)

	bus.Publish(events.MessageReceived{ChatID: 1, MessageID: 1, Date: base, Text: "x"})

	deadline := time.After(time.Second)
	for {
		if l := svc.LastIngestLatency(); l > 0 {
			if l != 5*time.Millisecond {
				t.Fatalf("latency = %v, want 5ms", l)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("LastIngestLatency stayed zero")
		case <-time.After(2 * time.Millisecond):
		}
	}
}
