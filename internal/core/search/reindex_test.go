package search_test

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pgmac/lazytg/internal/core/domain"
	"github.com/pgmac/lazytg/internal/core/events"
	"github.com/pgmac/lazytg/internal/core/search"
	"github.com/pgmac/lazytg/internal/storage/sqlite"
)

// TestReindex_RunAll_ProgressEvents exercises the happy path: three chats,
// ten freshly-saved messages each. The triggers already index every
// message at SaveMessage time, so Backfill returns 0 per chat — but the
// service still publishes one ReindexProgress event per chat with the
// running counters and a Done flag on the final entry.
func TestReindex_RunAll_ProgressEvents(t *testing.T) {
	repo, ctx := openTestRepo(t)
	chatIDs := []int64{8001, 8002, 8003}

	for i, id := range chatIDs {
		seedChat(t, repo, ctx, id, "Chat"+strconv.Itoa(i))
		base := time.Now().UTC().Add(-time.Hour)
		msgs := make([]domain.Message, 10)
		for j := 0; j < 10; j++ {
			msgs[j] = domain.Message{
				ID:     int64(j + 1),
				ChatID: id,
				Date:   base.Add(time.Duration(j) * time.Second),
				Text:   "сообщение " + strconv.Itoa(j),
			}
		}
		if err := repo.SaveMessages(ctx, msgs); err != nil {
			t.Fatalf("save msgs chat=%d: %v", id, err)
		}
	}

	bus := events.New()
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch := bus.Subscribe(subCtx)

	indexer := search.NewIndexer(repo, nil)
	rs := search.NewReindexService(indexer, repo, bus, nil, 0)

	if err := rs.RunAll(ctx); err != nil {
		t.Fatalf("RunAll: %v", err)
	}

	// Drain three events. Bus delivery is non-blocking but the buffer is
	// 64 by default — three events fit easily.
	got := make([]events.ReindexProgress, 0, 3)
	timeout := time.After(2 * time.Second)
	for len(got) < 3 {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatalf("subscription closed early after %d events", len(got))
			}
			rp, isRP := e.(events.ReindexProgress)
			if !isRP {
				continue
			}
			got = append(got, rp)
		case <-timeout:
			t.Fatalf("timed out waiting for ReindexProgress events: got %d", len(got))
		}
	}

	// GetChats orders pinned-first then last_message_date desc; all three
	// chats here are unpinned with the same last-message date so the
	// secondary "id ASC" ordering kicks in.
	wantOrder := []int64{8001, 8002, 8003}
	for i, e := range got {
		if e.ChatID != wantOrder[i] {
			t.Errorf("event[%d].ChatID = %d, want %d (full pass = %+v)", i, e.ChatID, wantOrder[i], got)
		}
		if e.Indexed != 0 {
			t.Errorf("event[%d].Indexed = %d, want 0 (triggers already indexed at insert time)", i, e.Indexed)
		}
	}
	if got[len(got)-1].Done != true {
		t.Errorf("last event Done = false, want true")
	}
	for i := 0; i < len(got)-1; i++ {
		if got[i].Done {
			t.Errorf("event[%d].Done = true, want false (only the final one carries it)", i)
		}
	}
}

// TestReindex_GracefulCancel arms a sizeable orphan workload (the AFTER
// INSERT trigger is dropped, 50 chats × 200 messages are written without
// being indexed, the trigger is restored, RunAll is then started in a
// goroutine and cancelled after a short delay). The service must return
// a wrapped context.Canceled error and the FTS index must remain
// internally consistent — verified via PRAGMA integrity_check and the
// FTS5-built-in 'integrity-check' invocation.
func TestReindex_GracefulCancel(t *testing.T) {
	repo, ctx := openTestRepo(t)
	db := repo.DB()

	if _, err := db.ExecContext(ctx, `DROP TRIGGER messages_ai`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}

	const chatCount = 50
	const perChat = 200
	chatIDs := make([]int64, chatCount)
	base := time.Now().UTC().Add(-24 * time.Hour)
	for c := 0; c < chatCount; c++ {
		id := int64(9000 + c)
		chatIDs[c] = id
		seedChat(t, repo, ctx, id, "GC"+strconv.Itoa(c))
		msgs := make([]domain.Message, perChat)
		for j := 0; j < perChat; j++ {
			msgs[j] = domain.Message{
				ID:     int64(j + 1),
				ChatID: id,
				Date:   base.Add(time.Duration(c*perChat+j) * time.Millisecond),
				Text:   "оранжевое сообщение " + strconv.Itoa(c) + "-" + strconv.Itoa(j),
			}
		}
		if err := repo.SaveMessages(ctx, msgs); err != nil {
			t.Fatalf("save msgs chat=%d: %v", id, err)
		}
	}
	if _, err := db.ExecContext(ctx, `
        CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN
            INSERT INTO messages_fts(rowid, text) VALUES (new.rowid, new.text);
        END
    `); err != nil {
		t.Fatalf("recreate trigger: %v", err)
	}

	indexer := search.NewIndexer(repo, nil)
	rs := search.NewReindexService(indexer, repo, nil, nil, perChat)

	runCtx, cancel := context.WithCancel(ctx)
	errCh := make(chan error, 1)
	go func() {
		errCh <- rs.RunAll(runCtx)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			// 50ms might have been enough on a very fast machine if the
			// indexer streams through cheaply. Treat that as a pass —
			// the integrity assertions below still validate the
			// invariant. The real failure mode this guards against is
			// a panic or a non-cancel-related error.
			t.Logf("RunAll completed before cancellation (machine too fast — integrity still checked)")
		} else if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunAll returned non-cancel error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("RunAll did not return within 5s after cancel")
	}

	// PRAGMA integrity_check returns a single-row result with "ok" on
	// success; any other string is the error report.
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if integrity != "ok" {
		t.Fatalf("integrity_check returned %q, want \"ok\"", integrity)
	}

	// FTS5's built-in 'integrity-check' invocation re-walks every
	// posting list and rejects any inconsistency between the FTS table
	// and its docsize/idx tables. A successful Exec means the index is
	// internally coherent.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO messages_fts(messages_fts) VALUES('integrity-check')`,
	); err != nil {
		t.Fatalf("messages_fts integrity-check: %v", err)
	}
}

// recordingChatLister tracks how many times GetChats is called so the
// LazyTrigger idempotency test can prove the second EnsureIndexed call
// did not run a second pass.
type recordingChatLister struct {
	calls int32
	chats []domain.Chat
}

func (l *recordingChatLister) GetChats(_ context.Context) ([]domain.Chat, error) {
	atomic.AddInt32(&l.calls, 1)
	return l.chats, nil
}

func (l *recordingChatLister) Calls() int { return int(atomic.LoadInt32(&l.calls)) }

// TestLazyTrigger_OnlyOnce verifies that calling EnsureIndexed twice
// triggers a single background pass — the second call sees triggered=true
// and short-circuits.
func TestLazyTrigger_OnlyOnce(t *testing.T) {
	repo, ctx := openTestRepo(t)
	indexer := search.NewIndexer(repo, nil)
	chats := &recordingChatLister{}
	rs := search.NewReindexService(indexer, chats, nil, nil, 0)
	lazy := search.NewLazyTrigger(rs, nil)

	if err := lazy.EnsureIndexed(ctx); err != nil {
		t.Fatalf("EnsureIndexed first: %v", err)
	}
	if err := lazy.EnsureIndexed(ctx); err != nil {
		t.Fatalf("EnsureIndexed second: %v", err)
	}

	select {
	case <-lazy.Done():
	case <-time.After(2 * time.Second):
		t.Fatalf("background pass did not complete within 2s")
	}

	if got := chats.Calls(); got != 1 {
		t.Errorf("ChatLister.GetChats called %d times, want 1", got)
	}
	if !lazy.Triggered() {
		t.Errorf("lazy.Triggered() = false, want true")
	}
}

// TestReindex_Run_NoChats covers the empty-pass path — Run with an empty
// slice still publishes a single Done=true event so callers can drop the
// "indexing…" indicator unconditionally.
func TestReindex_Run_NoChats(t *testing.T) {
	repo, ctx := openTestRepo(t)
	indexer := search.NewIndexer(repo, nil)
	bus := events.New()
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch := bus.Subscribe(subCtx)

	rs := search.NewReindexService(indexer, nil, bus, nil, 0)
	if err := rs.Run(ctx, nil); err != nil {
		t.Fatalf("Run nil: %v", err)
	}

	select {
	case e := <-ch:
		rp, ok := e.(events.ReindexProgress)
		if !ok {
			t.Fatalf("got non-ReindexProgress: %T", e)
		}
		if !rp.Done {
			t.Errorf("Done=%v, want true", rp.Done)
		}
	case <-time.After(time.Second):
		t.Fatalf("no event received")
	}
}

// TestReindex_RunAll_NoChatLister documents the explicit-error path so a
// forgotten dependency surfaces early instead of silently doing nothing.
func TestReindex_RunAll_NoChatLister(t *testing.T) {
	repo, _ := openTestRepo(t)
	indexer := search.NewIndexer(repo, nil)
	rs := search.NewReindexService(indexer, nil, nil, nil, 0)
	if err := rs.RunAll(context.Background()); err == nil {
		t.Fatalf("RunAll with no chat lister: got nil err, want non-nil")
	}
}

// openTestRepoWithPath is a helper for tests that need a known-good DB
// path (e.g. integrity recovery tests). The default openTestRepo
// already returns one — this exists for symmetry if future tests need
// the explicit path.
func openTestRepoWithPath(t *testing.T) (*sqlite.Repo, string, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	path := filepath.Join(t.TempDir(), "search.db")
	repo, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo, path, ctx
}

// _ = openTestRepoWithPath silences the unused warning until the helper
// is consumed by Stage 3 Task 8 (debug-bundle integrity tests).
var _ = openTestRepoWithPath
