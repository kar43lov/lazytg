package search_test

import (
	"context"
	"math/rand/v2"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/search"
	"github.com/kar43lov/lazytg/internal/storage/sqlite"
)

// p95SLA is the Stage 3 SLA gate: BenchmarkSearch100k must keep the 95th
// percentile latency below this threshold across the sampled queries.
// Crossing it fails the bench, surfacing search regressions in CI before
// they reach a user.
const p95SLA = 100 * time.Millisecond

// benchSearchQueries are the queries the bench cycles through. Mix of
// Russian and English plus a 2-word phrase exercises the FTS5 trigram
// pipeline more honestly than a single-word probe.
var benchSearchQueries = []string{
	"привет",
	"hello world",
	"тест",
	"abc def",
}

// benchSeedSize is the corpus the SLA gate is parameterised on. The plan
// pegs it at 100k messages — large enough to surface index-walk costs,
// small enough to keep the bench under a minute on a developer laptop.
const benchSeedSize = 100_000

// BenchmarkSearch100k is the SLA gate: seed 100k synthetic messages,
// drive 100 search iterations and fail if p95 latency exceeds 100ms.
// Setup runs under b.StopTimer so only the search loop counts.
//
// Run with:
//
//	go test -bench=BenchmarkSearch100k -benchtime=1x ./internal/core/search/
//
// The -benchtime=1x cap is intentional: we measure per-call latency, not
// throughput, and a single run delivers the 100-sample window the p95
// math expects. Letting Go choose b.N would re-run the seed and dilute
// the signal.
func BenchmarkSearch100k(b *testing.B) {
	b.StopTimer()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	path := filepath.Join(b.TempDir(), "bench.db")
	repo, err := sqlite.Open(ctx, path)
	if err != nil {
		b.Fatalf("open repo: %v", err)
	}
	defer func() { _ = repo.Close() }()

	seedBenchCorpus(b, repo, ctx)

	svc := search.NewService(repo, nil, nil)

	// Warm up the connection pool / page cache so the first measured
	// search does not pay the JIT-style cost of opening the FTS5 idx
	// segment files.
	for _, q := range benchSearchQueries {
		if _, err := svc.Search(ctx, q, 50); err != nil {
			b.Fatalf("warmup search %q: %v", q, err)
		}
	}

	const samples = 100
	latencies := make([]time.Duration, 0, samples)

	b.StartTimer()
	for i := 0; i < samples; i++ {
		q := benchSearchQueries[i%len(benchSearchQueries)]
		start := time.Now()
		if _, err := svc.Search(ctx, q, 50); err != nil {
			b.Fatalf("search %q: %v", q, err)
		}
		latencies = append(latencies, time.Since(start))
	}
	b.StopTimer()

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p50 := latencies[len(latencies)*50/100]
	p95 := latencies[len(latencies)*95/100]
	p99 := latencies[len(latencies)*99/100]
	worst := latencies[len(latencies)-1]

	b.ReportMetric(float64(p50.Microseconds())/1000.0, "p50_ms")
	b.ReportMetric(float64(p95.Microseconds())/1000.0, "p95_ms")
	b.ReportMetric(float64(p99.Microseconds())/1000.0, "p99_ms")

	if p95 > p95SLA {
		b.Fatalf("search p95 = %v, want < %v (p50=%v, p99=%v, worst=%v)",
			p95, p95SLA, p50, p99, worst)
	}
}

// seedBenchCorpus inserts benchSeedSize synthetic messages distributed
// across 20 chats. Texts mix Russian and English vocabulary so the
// trigram tokenizer fires on shapes the bench queries actually share.
// Inserts go through one big transaction (fewer fsyncs) and the FTS
// triggers fold each row into the index as it lands.
func seedBenchCorpus(b *testing.B, repo *sqlite.Repo, ctx context.Context) {
	b.Helper()

	// 20 chats so per-chat ordering tests downstream stay realistic
	// without making any single chat dwarf the others.
	const chatCount = 20
	for i := 0; i < chatCount; i++ {
		if err := repo.SaveChat(ctx, domain.Chat{
			ID:    int64(i + 1),
			Type:  domain.ChatTypePrivate,
			Title: "Bench" + strings.Repeat(string(rune('A'+i)), 1),
		}); err != nil {
			b.Fatalf("save chat %d: %v", i, err)
		}
	}

	rng := rand.New(rand.NewPCG(42, 4242)) //nolint:gosec // deterministic seed for reproducibility

	vocab := []string{
		"привет", "мир", "тест", "сообщение", "система", "быстрый", "медленный",
		"hello", "world", "example", "data", "system", "fast", "slow",
		"абв", "def", "обед", "вечером", "утром", "report", "queue",
	}

	now := time.Now().UTC()

	const batchSize = 5_000
	batch := make([]domain.Message, 0, batchSize)
	for i := 0; i < benchSeedSize; i++ {
		nWords := 3 + rng.IntN(15)
		var sb strings.Builder
		for w := 0; w < nWords; w++ {
			if w > 0 {
				sb.WriteByte(' ')
			}
			sb.WriteString(vocab[rng.IntN(len(vocab))])
		}
		batch = append(batch, domain.Message{
			ID:     int64(i + 1),
			ChatID: int64(rng.IntN(chatCount) + 1),
			Date:   now.Add(time.Duration(i) * time.Millisecond),
			Text:   sb.String(),
		})
		if len(batch) == batchSize {
			if err := repo.SaveMessages(ctx, batch); err != nil {
				b.Fatalf("seed batch %d: %v", i, err)
			}
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		if err := repo.SaveMessages(ctx, batch); err != nil {
			b.Fatalf("seed final batch: %v", err)
		}
	}
}
