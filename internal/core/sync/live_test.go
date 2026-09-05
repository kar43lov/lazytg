package sync

import (
	"context"
	"errors"
	"fmt"
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
	unread        []int64
	unreadErr     error
	reactions     []reactionWrite
	facts         []string
}

// The list-fact setters record what they were asked, as one line each.
func (s *recordingStore) fact(line string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.facts = append(s.facts, line)
	return nil
}

func (s *recordingStore) factsSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.facts...)
}

func (s *recordingStore) SetUnread(_ context.Context, chatID int64, count int) error {
	return s.fact(fmt.Sprintf("unread %d=%d", chatID, count))
}

func (s *recordingStore) SetPinned(_ context.Context, chatID int64, pinned bool) error {
	return s.fact(fmt.Sprintf("pinned %d=%v", chatID, pinned))
}

func (s *recordingStore) SetMutedUntil(_ context.Context, chatID int64, until time.Time) error {
	return s.fact(fmt.Sprintf("muted %d=%d", chatID, until.Unix()))
}

func (s *recordingStore) SetUnreadMark(_ context.Context, chatID int64, marked bool) error {
	return s.fact(fmt.Sprintf("mark %d=%v", chatID, marked))
}

func (s *recordingStore) SetPresence(_ context.Context, userID int64, online bool, lastSeen time.Time) error {
	return s.fact(fmt.Sprintf("presence %d=%v/%d", userID, online, lastSeen.Unix()))
}

// IncrementUnread satisfies coresync.LiveStore and records which chats had
// their badge raised, which is what the unread tests assert on.
func (s *recordingStore) IncrementUnread(_ context.Context, chatID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unread = append(s.unread, chatID)
	s.order = append(s.order, "unread")
	return s.unreadErr
}

func (s *recordingStore) unreadSnapshot() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]int64, len(s.unread))
	copy(out, s.unread)
	return out
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

func (s *recordingStore) SetReactions(_ context.Context, chatID, messageID int64, rs []domain.Reaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reactions = append(s.reactions, reactionWrite{chatID: chatID, messageID: messageID, rs: append([]domain.Reaction(nil), rs...)})
	s.order = append(s.order, "reactions")
	return nil
}

// reactionsSnapshot returns a copy of the recorded SetReactions calls.
func (s *recordingStore) reactionsSnapshot() []reactionWrite {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]reactionWrite, len(s.reactions))
	copy(out, s.reactions)
	return out
}

type reactionWrite struct {
	chatID    int64
	messageID int64
	rs        []domain.Reaction
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

// TestLiveService_RaisesTheUnreadCounter is the badge half of the second live
// run: a message arriving into a chat the user is not reading showed nothing
// in the list. Dialog sync owned the counter and runs at startup, so the only
// way to learn about a new message was to notice the chat had moved to the
// top — or to restart.
func TestLiveService_RaisesTheUnreadCounter(t *testing.T) {
	t.Parallel()
	bus := events.New()
	store := &recordingStore{}
	svc := NewLiveService(store, bus, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := svc.Start(ctx)

	bus.Publish(events.MessageReceived{ChatID: 42, MessageID: 1, Text: "hi", Date: time.Now()})
	waitFor(t, "the badge to be raised", func() bool {
		return len(store.unreadSnapshot()) == 1
	})
	if got := store.unreadSnapshot()[0]; got != 42 {
		t.Fatalf("raised the badge on chat %d, want 42", got)
	}

	cancel()
	<-done
}

// TestLiveService_LeavesTheOpenChatAlone covers the two cases where a badge
// would be wrong rather than missing: the chat the user is looking at, and
// the user's own messages arriving from another device. Both would show an
// unread count for something already read.
func TestLiveService_LeavesTheOpenChatAlone(t *testing.T) {
	t.Parallel()
	bus := events.New()
	store := &recordingStore{}
	svc := NewLiveService(store, bus, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := svc.Start(ctx)

	bus.Publish(events.ChatOpened{ChatID: 42})
	bus.Publish(events.MessageReceived{ChatID: 42, MessageID: 1, Text: "in the open chat", Date: time.Now()})
	bus.Publish(events.MessageReceived{ChatID: 43, MessageID: 2, Text: "mine", Date: time.Now(), Outgoing: true})
	waitFor(t, "both messages to be stored", func() bool {
		return len(store.snapshot()) == 2
	})
	time.Sleep(20 * time.Millisecond)

	if raised := store.unreadSnapshot(); len(raised) != 0 {
		t.Fatalf("raised the badge on %v, want none", raised)
	}

	cancel()
	<-done
}

// A reaction made on another device reaches the mirror the same way a
// deletion does: through the live path, because nothing else is watching.
func TestLiveService_StoresReactionsFromAnotherDevice(t *testing.T) {
	t.Parallel()
	bus := events.New()
	store := &recordingStore{}
	svc := NewLiveService(store, bus, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := svc.Start(ctx)

	bus.Publish(events.MessageReactionsChanged{
		ChatID: 42, MessageID: 7,
		Reactions: []domain.Reaction{{Emoticon: "👍", Count: 3}},
	})

	deadline := time.After(time.Second)
	for {
		writes := store.reactionsSnapshot()
		if len(writes) == 1 {
			if writes[0].chatID != 42 || writes[0].messageID != 7 || len(writes[0].rs) != 1 {
				t.Fatalf("SetReactions called with %+v", writes[0])
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("SetReactions was never called; reactions from other devices would never appear")
		case <-time.After(5 * time.Millisecond):
		}
	}

	cancel()
	<-done
}

// The event and the stored row are two different sets of fields, and a field
// the persist step does not copy is a field that vanishes on the next page
// load. Formatting is the third field to learn this after from_id and
// reply_to.
func TestLiveService_PersistsEntities(t *testing.T) {
	t.Parallel()
	bus := events.New()
	store := &recordingStore{}
	svc := NewLiveService(store, bus, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := svc.Start(ctx)

	want := []domain.Entity{{Kind: domain.EntityBold, Offset: 0, Length: 5}}
	bus.Publish(events.MessageReceived{
		ChatID: 1, MessageID: 100, Text: "hello", FromID: 7,
		Date: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC), Entities: want,
	})

	deadline := time.After(time.Second)
	for {
		if got := store.snapshot(); len(got) == 1 {
			if len(got[0].Entities) != 1 || got[0].Entities[0] != want[0] {
				t.Fatalf("saved message carries %+v, want %+v", got[0].Entities, want)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("message was never persisted")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

// An edit is the same message again. It is stored — the mirror has to show
// the new text — and it raises no badge.
func TestLiveService_StoresAnEditWithoutRaisingTheBadge(t *testing.T) {
	t.Parallel()
	bus := events.New()
	store := &recordingStore{}
	svc := NewLiveService(store, bus, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := svc.Start(ctx)

	when := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	bus.Publish(events.MessageReceived{ChatID: 42, MessageID: 1, Text: "fixed", Date: when, Edited: true, EditDate: when.Add(time.Minute)})
	waitFor(t, "the edit to be stored", func() bool { return len(store.snapshot()) == 1 })
	if got := store.snapshot()[0]; got.Text != "fixed" || !got.EditDate.Equal(when.Add(time.Minute)) {
		t.Fatalf("stored %+v", got)
	}

	bus.Publish(events.MessageReceived{ChatID: 43, MessageID: 2, Text: "new", Date: when})
	waitFor(t, "the new message to raise its badge", func() bool { return len(store.unreadSnapshot()) == 1 })
	if got := store.unreadSnapshot(); got[0] != 43 {
		t.Fatalf("badges raised for %v, want only 43", got)
	}

	cancel()
	<-done
}

// The list facts arrive one per update and each lands on its own column,
// then the chat list is told to reload.
func TestLiveService_RecordsTheListFacts(t *testing.T) {
	t.Parallel()
	bus := events.New()
	store := &recordingStore{}
	svc := NewLiveService(store, bus, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := svc.Start(ctx)
	watch := bus.Subscribe(ctx)

	until := time.Date(2038, 1, 19, 3, 14, 7, 0, time.UTC)
	bus.Publish(events.ChatReadInbox{ChatID: 42, MaxID: 10, StillUnread: 0})
	bus.Publish(events.ChatPinned{ChatID: 42, Pinned: true})
	bus.Publish(events.ChatMuted{ChatID: 42, Until: until})
	bus.Publish(events.ChatUnreadMark{ChatID: 42, Unread: true})
	bus.Publish(events.PeerPresence{UserID: 42, Online: true})
	waitFor(t, "five facts to be recorded", func() bool { return len(store.factsSnapshot()) == 5 })
	want := []string{"unread 42=0", "pinned 42=true", "muted 42=" + fmt.Sprint(until.Unix()), "mark 42=true", "presence 42=true/-62135596800"}
	if got := store.factsSnapshot(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("recorded %v, want %v", got, want)
	}
	reloads := 0
	deadline := time.After(time.Second)
	for reloads < 5 {
		select {
		case ev := <-watch:
			if _, ok := ev.(events.DialogUpdated); ok {
				reloads++
			}
		case <-deadline:
			t.Fatalf("chat list told to reload %d times, want 5", reloads)
		}
	}
	cancel()
	<-done
}
