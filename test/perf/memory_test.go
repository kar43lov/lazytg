package perf

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pgmac/lazytg/internal/app"
	"github.com/pgmac/lazytg/internal/core/config"
	"github.com/pgmac/lazytg/internal/core/domain"
	"github.com/pgmac/lazytg/internal/core/events"
)

// Memory budgets policed by the Stage 4 acceptance gate. The numbers
// match docs/PERFORMANCE.md and the v0.1.0 acceptance criteria.
//
// idleBudgetBytes (50 MiB) covers a freshly-booted runtime with the
// non-MTProto background goroutines running but no traffic — the
// expected steady state on a tmux-resident developer machine.
//
// activeBudgetBytes (150 MiB) covers active load: live-update drain
// from the bus → SQLite + concurrent search queries. The headroom over
// the idle budget is mostly the search service's bm25 page cache and
// the in-flight bus buffers.
const (
	idleBudgetBytes   = 50 * 1024 * 1024
	activeBudgetBytes = 150 * 1024 * 1024
)

// buildIsolatedApp wires app.Build against a freshly-created XDG layout
// inside t.TempDir(). The permissions audit is disabled because t.TempDir
// inherits the runner's umask which often produces 0o755 on CI.
//
// The returned bgCtx scopes the RunBackground goroutines and is cancelled
// during t.Cleanup along with the repo Close, so callers don't need to
// own teardown.
func buildIsolatedApp(t *testing.T) (*app.App, context.Context) {
	t.Helper()

	tmp := t.TempDir()
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

	a, err := app.Build(ctx, app.Config{
		Logger:               slog.New(slog.DiscardHandler),
		Paths:                paths,
		SkipPermissionsAudit: true,
	})
	if err != nil {
		cancel()
		t.Fatalf("app.Build: %v", err)
	}

	bgCtx, cancelBG := context.WithCancel(ctx)
	done := a.RunBackground(bgCtx)

	t.Cleanup(func() {
		cancelBG()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Errorf("RunBackground did not exit within 2s after cancel")
		}
		if err := a.Close(); err != nil {
			t.Errorf("app.Close: %v", err)
		}
		cancel()
	})

	return a, bgCtx
}

// heapAllocAfterGC runs runtime.GC twice (the second pass collects
// finalisable objects whose finalisers ran during the first pass) and
// returns the resulting HeapAlloc. Two passes are the canonical way to
// get a stable reading; a single pass undercounts when finalisers
// allocate.
func heapAllocAfterGC() uint64 {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

// formatMiB renders bytes as "X.X MiB" for human-readable test logs.
func formatMiB(bytes uint64) string {
	return fmt.Sprintf("%.1f MiB", float64(bytes)/(1024*1024))
}

// TestMemoryBudget_Idle asserts the idle steady-state heap stays under
// 50 MiB. The runtime is built with the production wiring (bus, repo,
// LiveService, DegradationDetector, DBSizeMonitor, search pipeline) but
// no MTProto client and no traffic — the load profile of a logged-in
// session that the user has stepped away from.
//
// 5 s of warmup gives every "first probe" goroutine (degradation,
// db-size monitor) a chance to allocate its initial state so the
// reading captures a realistic resident heap rather than a cold start.
func TestMemoryBudget_Idle(t *testing.T) {
	if testing.Short() {
		t.Skip("memory budget tests skipped under -short")
	}
	// macOS runners add scheduler noise that flakes the active-load drain
	// window without surfacing a real regression — same caveat as the
	// search SLA gate. CI ci.yml gates on Linux only; skip on others so
	// `go test ./...` on macOS does not flake.
	if runtime.GOOS != "linux" {
		t.Skipf("memory budget gate runs on linux only (got %s)", runtime.GOOS)
	}

	buildIsolatedApp(t)

	// Stabilisation window. The first DegradationDetector probe and the
	// first DBSizeMonitor probe both fire within ~1 s of Start; 5 s is
	// a conservative upper bound that keeps the gate stable on a loaded
	// CI runner without inflating the measured heap with allocator-side
	// growth.
	time.Sleep(5 * time.Second)

	heap := heapAllocAfterGC()
	t.Logf("idle HeapAlloc = %s (budget %s)", formatMiB(heap), formatMiB(idleBudgetBytes))
	if heap > idleBudgetBytes {
		t.Fatalf("idle HeapAlloc %s exceeds budget %s",
			formatMiB(heap), formatMiB(idleBudgetBytes))
	}
}

// TestMemoryBudget_Active drives 1000 MessageReceived events through
// the bus while ten search workers continuously query the FTS index,
// then asserts the post-load HeapAlloc stays under 150 MiB.
//
// The 30 s window mirrors the plan: ~33 msg/sec sustained, which is
// well above the realistic per-account live-update rate (a chatty
// Telegram account peaks around 5 msg/sec). The seed corpus (10 chats
// × 1000 messages) gives the search service something to actually
// match against so the query path is not a no-op.
//
// Heap is sampled after the load finishes and after GC×2 so we measure
// the resident working set rather than transient allocation peaks. The
// budget polices long-term retention, not allocation rate (which the
// SLA bench in BenchmarkSearch100k already covers).
func TestMemoryBudget_Active(t *testing.T) {
	if testing.Short() {
		t.Skip("memory budget tests skipped under -short")
	}
	if runtime.GOOS != "linux" {
		t.Skipf("memory budget gate runs on linux only (got %s)", runtime.GOOS)
	}

	a, bgCtx := buildIsolatedApp(t)

	const (
		chatCount     = 10
		seedPerChat   = 1000
		eventCount    = 1000
		searchWorkers = 10
		// loadDuration paces event emission. 1000 events / 30 s ≈ 33
		// msg/sec, well above the realistic per-account peak.
		loadDuration = 30 * time.Second
		batchSize    = 32
	)

	// Seed: 10 chats × 1000 messages = 10k. Enough for search workers
	// to hit the FTS index without blowing test runtime out of
	// proportion. Seed time is ~1 s on M-class hardware.
	seedCtx, seedCancel := context.WithTimeout(bgCtx, 30*time.Second)
	t.Cleanup(seedCancel)
	if err := seedActiveCorpus(seedCtx, a, chatCount, seedPerChat); err != nil {
		t.Fatalf("seed corpus: %v", err)
	}

	// Search workers. Each issues queries until ctx is cancelled. The
	// service is concurrency-safe (read-only over an FTS5 index).
	searchCtx, cancelSearch := context.WithCancel(bgCtx)
	queries := []string{"hello", "world", "сообщение", "test", "data"}
	var searchWG sync.WaitGroup
	var searchErr atomic.Value
	// Joint cancel+wait cleanup so a t.Fatalf in the drain probe below
	// doesn't race the workers against a.Close() (registered in
	// buildIsolatedApp). LIFO ordering means this runs before Close().
	t.Cleanup(func() {
		cancelSearch()
		searchWG.Wait()
	})
	for w := 0; w < searchWorkers; w++ {
		searchWG.Add(1)
		go func(workerID int) {
			defer searchWG.Done()
			i := 0
			for {
				select {
				case <-searchCtx.Done():
					return
				default:
				}
				q := queries[(workerID+i)%len(queries)]
				if _, err := a.SearchSvc.Search(searchCtx, q, 50); err != nil {
					if searchCtx.Err() != nil {
						return
					}
					searchErr.Store(err)
					return
				}
				i++
				// Yield so the workers don't monopolise the scheduler
				// at the expense of the live-drain goroutine. 5 ms per
				// query × 10 workers = 2000 q/sec ceiling, plenty for
				// surfacing memory regressions without saturating the
				// disk.
				time.Sleep(5 * time.Millisecond)
			}
		}(w)
	}

	// Event producer. Drip-feed in chunks of batchSize so the bus
	// subscriber buffer (64-slot per subscriber) doesn't drop — the
	// LiveService drain is the consumer that matters here, and a
	// dropped event would mask a real regression by reducing the work
	// the runtime had to do.
	pacer := time.NewTicker(loadDuration / time.Duration(eventCount/batchSize+1))
	defer pacer.Stop()

	produced := 0
	now := time.Now().UTC()
producer:
	for produced < eventCount {
		select {
		case <-bgCtx.Done():
			break producer
		case <-pacer.C:
		}
		end := produced + batchSize
		if end > eventCount {
			end = eventCount
		}
		for j := produced; j < end; j++ {
			a.Bus.Publish(events.MessageReceived{
				ChatID:    int64(j%chatCount) + 1,
				MessageID: int64(seedPerChat*chatCount + j + 1),
				Text:      fmt.Sprintf("active load event %d hello world", j),
				FromID:    int64(j) + 100,
				Date:      now.Add(time.Duration(j) * time.Millisecond),
			})
		}
		produced = end
	}

	// Wait until the LiveService has drained every published event so
	// the heap reading reflects the post-load steady state, not an
	// in-flight queue.
	drainDeadline := time.Now().Add(15 * time.Second)
	for {
		// We seeded chatCount*seedPerChat rows + eventCount live events
		// into the same chats. The simplest drain probe is to count
		// rows in chat 1 and confirm the live-drain caught up.
		count, err := countMessages(bgCtx, a)
		if err != nil {
			t.Fatalf("count messages: %v", err)
		}
		expected := chatCount*seedPerChat + eventCount
		if count >= expected {
			break
		}
		if time.Now().After(drainDeadline) {
			t.Fatalf("LiveService did not drain within 15s (got %d, want %d)", count, expected)
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancelSearch()
	searchWG.Wait()
	if v := searchErr.Load(); v != nil {
		t.Fatalf("search worker error: %v", v)
	}

	heap := heapAllocAfterGC()
	t.Logf("active HeapAlloc = %s (budget %s)", formatMiB(heap), formatMiB(activeBudgetBytes))
	if heap > activeBudgetBytes {
		t.Fatalf("active HeapAlloc %s exceeds budget %s",
			formatMiB(heap), formatMiB(activeBudgetBytes))
	}
}

// seedActiveCorpus writes chatCount synthetic chats and perChat
// messages per chat directly through *sqlite.Repo. Inserts go through
// SaveMessages so the per-chat batch lands in a single transaction.
func seedActiveCorpus(ctx context.Context, a *app.App, chatCount, perChat int) error {
	now := time.Now().UTC()
	for c := 1; c <= chatCount; c++ {
		if err := a.Repo.SaveChat(ctx, domain.Chat{
			ID:    int64(c),
			Type:  domain.ChatTypePrivate,
			Title: fmt.Sprintf("MemBench %d", c),
		}); err != nil {
			return fmt.Errorf("save chat %d: %w", c, err)
		}
	}
	vocab := []string{
		"hello", "world", "сообщение", "test", "data",
		"привет", "system", "report", "fast", "slow",
	}
	batch := make([]domain.Message, 0, perChat)
	for c := 1; c <= chatCount; c++ {
		batch = batch[:0]
		for i := 0; i < perChat; i++ {
			text := fmt.Sprintf("%s %s %s seed", vocab[i%len(vocab)], vocab[(i+3)%len(vocab)], vocab[(i+7)%len(vocab)])
			batch = append(batch, domain.Message{
				ID:     int64(i + 1),
				ChatID: int64(c),
				Date:   now.Add(time.Duration(i) * time.Millisecond),
				Text:   text,
			})
		}
		if err := a.Repo.SaveMessages(ctx, batch); err != nil {
			return fmt.Errorf("save messages chat %d: %w", c, err)
		}
	}
	return nil
}

// countMessages totals rows across the messages table. Used to detect
// when the LiveService drain has caught up with the producer.
func countMessages(ctx context.Context, a *app.App) (int, error) {
	row := a.Repo.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM messages")
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
