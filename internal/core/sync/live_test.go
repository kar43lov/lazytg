package sync

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
)

// recordingStore captures every SaveMessage call so tests can assert on
// the exact sequence of writes the LiveService produced.
type recordingStore struct {
	mu            sync.Mutex
	calls         []domain.Message
	ensured       []ensuredChat
	deleted       []deletedBatch
	order         []string
	errOn         int
	errVal        error
	ensureErr     error
	ensureCreated bool
}

// deletedBatch records one DeleteMessages call.
type deletedBatch struct {
	chatID int64
	ids    []int64
}

// ensuredChat records one EnsureChat call so a test can assert the parent
// row was created before the message that needs it.
type ensuredChat struct {
	id   int64
	kind domain.ChatType
	at   time.Time
}

// EnsureChat satisfies coresync.LiveStore and records the call. The fake
// carries no chats table; what matters is that the service asks.
func (s *recordingStore) EnsureChat(_ context.Context, id int64, t domain.ChatType, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensured = append(s.ensured, ensuredChat{id: id, kind: t, at: at})
	s.order = append(s.order, "ensure")
	return s.ensureCreated, s.ensureErr
}

// DeleteMessages satisfies coresync.LiveStore and records the call.
func (s *recordingStore) DeleteMessages(_ context.Context, chatID int64, ids []int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, deletedBatch{chatID: chatID, ids: append([]int64(nil), ids...)})
	s.order = append(s.order, "delete")
	return int64(len(ids)), nil
}

// deletedSnapshot returns a copy of the recorded DeleteMessages calls.
func (s *recordingStore) deletedSnapshot() []deletedBatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]deletedBatch(nil), s.deleted...)
}

// orderSnapshot returns the interleaved sequence of storage calls, so a test
// can assert that the parent row is created before the row that needs it.
func (s *recordingStore) orderSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.order...)
}

// ensuredSnapshot returns a copy of the recorded EnsureChat calls.
func (s *recordingStore) ensuredSnapshot() []ensuredChat {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ensuredChat(nil), s.ensured...)
}

func (s *recordingStore) SaveMessage(_ context.Context, m domain.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, m)
	s.order = append(s.order, "save")
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

// TestLiveService_CreatesTheParentChatBeforeSaving covers the live-observed
// data loss of 19.08.2026: a first message from a contact the mirror had
// never seen was rejected by messages.chat_id's foreign key and dropped, and
// the chat stayed invisible in the list until a restart ran dialog sync.
//
// The service must ask for the parent row before the message, and it must do
// so with the peer kind the event carries — a chats row has a NOT NULL type.
func TestLiveService_CreatesTheParentChatBeforeSaving(t *testing.T) {
	t.Parallel()
	bus := events.New()
	store := &recordingStore{}
	svc := NewLiveService(store, bus, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := svc.Start(ctx)

	bus.Publish(events.MessageReceived{
		ChatID: 275641346, MessageID: 18, Text: "2134",
		Date: time.Date(2026, 8, 19, 11, 14, 28, 0, time.UTC), ChatType: domain.ChatTypePrivate,
	})

	deadline := time.After(time.Second)
	for {
		ensured := store.ensuredSnapshot()
		if len(ensured) == 1 {
			if ensured[0].id != 275641346 || ensured[0].kind != domain.ChatTypePrivate {
				t.Fatalf("EnsureChat called with %+v, want the event's chat and kind", ensured[0])
			}
			// The date orders the row in the chat list: without it the chat
			// that just pinged the user sorts below every chat that ever
			// received a message.
			if !ensured[0].at.Equal(time.Date(2026, 8, 19, 11, 14, 28, 0, time.UTC)) {
				t.Fatalf("EnsureChat date = %v, want the message date", ensured[0].at)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("EnsureChat was never called; a message from an unknown chat would be dropped")
		case <-time.After(5 * time.Millisecond):
		}
	}

	// The name of this test is a claim about order, so it is asserted rather
	// than implied: a swapped persist() would still create the row, and the
	// message it was created for would still be lost. Waiting for both calls
	// first — the loop above returns as soon as EnsureChat lands, which can
	// be before SaveMessage has run at all.
	orderDeadline := time.After(time.Second)
	var order []string
	for {
		order = store.orderSnapshot()
		if len(order) >= 2 {
			break
		}
		select {
		case <-orderDeadline:
			t.Fatalf("storage calls = %v, want both a chat row and a message", order)
		case <-time.After(5 * time.Millisecond):
		}
	}
	if order[0] != "ensure" || order[1] != "save" {
		t.Fatalf("storage call order = %v, want the chat row created before the message", order)
	}

	cancel()
	<-done
}

// TestLiveService_SkipsEnsureChatWithoutAKind pins the other half: an event
// whose producer could not determine the peer kind must not invent one. A
// chats row with an empty type would be worse than no row — it renders as an
// unidentifiable entry and overwrites nothing that dialog sync could fix.
func TestLiveService_SkipsEnsureChatWithoutAKind(t *testing.T) {
	t.Parallel()
	bus := events.New()
	store := &recordingStore{}
	svc := NewLiveService(store, bus, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := svc.Start(ctx)

	bus.Publish(events.MessageReceived{ChatID: 5, MessageID: 1, Text: "x", Date: time.Now()})

	deadline := time.After(time.Second)
	for len(store.snapshot()) != 1 {
		select {
		case <-deadline:
			t.Fatalf("message was never saved")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if ensured := store.ensuredSnapshot(); len(ensured) != 0 {
		t.Fatalf("EnsureChat called %d time(s) for an event with no chat kind: %+v", len(ensured), ensured)
	}

	cancel()
	<-done
}

// TestLiveService_ForgetsDeletedMessages covers the path that makes a
// deletion made on another device stick locally. The mirror is the only copy
// left once the server has dropped the messages, so nothing else can remove
// them: dialog sync upserts and the live path only ever adds.
// TestLiveService_AnnouncesADiscoveredChat covers the other half of creating
// a chat row from an update: the row has an id and a kind and nothing else,
// so somebody has to ask the server what this conversation is called. Dialog
// sync runs at startup only, which is why the placeholder used to survive
// until the next launch.
func TestLiveService_AnnouncesADiscoveredChat(t *testing.T) {
	t.Parallel()
	bus := events.New()
	store := &recordingStore{ensureCreated: true}
	svc := NewLiveService(store, bus, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := svc.Start(ctx)

	watcher := bus.Subscribe(ctx)
	bus.Publish(events.MessageReceived{
		ChatID: 275641346, MessageID: 1, Date: time.Now(), ChatType: domain.ChatTypePrivate,
	})

	deadline := time.After(time.Second)
	for {
		select {
		case ev := <-watcher:
			if discovered, ok := ev.(events.ChatDiscovered); ok {
				if discovered.ChatID != 275641346 {
					t.Fatalf("ChatDiscovered carried chat %d, want 275641346", discovered.ChatID)
				}
				cancel()
				<-done
				return
			}
		case <-deadline:
			t.Fatalf("no ChatDiscovered published; a chat created live would keep its placeholder name")
		}
	}
}

// TestLiveService_StaysQuietForAKnownChat is the counterpart: an ordinary
// message must not ask for a dialog refresh, or every message in the client
// would cost one.
func TestLiveService_StaysQuietForAKnownChat(t *testing.T) {
	t.Parallel()
	bus := events.New()
	store := &recordingStore{ensureCreated: false}
	svc := NewLiveService(store, bus, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := svc.Start(ctx)

	watcher := bus.Subscribe(ctx)
	bus.Publish(events.MessageReceived{
		ChatID: 1, MessageID: 1, Date: time.Now(), ChatType: domain.ChatTypePrivate,
	})

	deadline := time.After(200 * time.Millisecond)
	for {
		select {
		case ev := <-watcher:
			if _, ok := ev.(events.ChatDiscovered); ok {
				t.Fatalf("a message in a known chat announced a discovery")
			}
		case <-deadline:
			cancel()
			<-done
			return
		}
	}
}

func TestLiveService_ForgetsDeletedMessages(t *testing.T) {
	t.Parallel()
	bus := events.New()
	store := &recordingStore{}
	svc := NewLiveService(store, bus, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := svc.Start(ctx)

	bus.Publish(events.MessagesDeleted{ChatID: 0, MessageIDs: []int64{18, 19}})

	deadline := time.After(time.Second)
	for {
		batches := store.deletedSnapshot()
		if len(batches) == 1 {
			if batches[0].chatID != 0 || len(batches[0].ids) != 2 {
				t.Fatalf("DeleteMessages called with %+v, want the event's chat and ids", batches[0])
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("DeleteMessages was never called; deletions would never reach the mirror")
		case <-time.After(5 * time.Millisecond):
		}
	}

	cancel()
	<-done
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
