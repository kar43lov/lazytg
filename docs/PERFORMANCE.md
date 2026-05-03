# Performance — бюджеты, SLA и измерения

Документ описывает performance-контракты lazytg v0.1.0: что мы гарантируем, как это измеряется, и как реагировать на регрессии.

Все цифры основаны на синтетических нагрузках на M-class developer hardware (Apple Silicon M4, SSD). На более слабом железе (старые Intel-ноуты, спинальные диски) запас уменьшается, но cliff-edge не пробивается до экстремальных нагрузок.

---

## Сводка: что мы обещаем

| Метрика                         | Бюджет                | Где гарантируется                              | Запас     |
|---------------------------------|-----------------------|------------------------------------------------|-----------|
| Memory idle (logged-in, no traffic) | < 50 MiB HeapAlloc | `TestMemoryBudget_Idle` (`test/perf/`)        | ~25×      |
| Memory active (load + search)   | < 150 MiB HeapAlloc   | `TestMemoryBudget_Active` (`test/perf/`)       | ~65×      |
| Search p95 (100k сообщений)     | < 100 ms              | `BenchmarkSearch100k` (`internal/core/search/`) | ~2×       |
| Live-update p95 (bus → drain)   | < 500 ms              | `TestLiveUpdateLatencySLA` (`test/perf/`)      | ~100×     |
| Goroutine leaks после shutdown  | 0                     | `TestApp_NoGoroutineLeaks` (`test/perf/`)      | strict    |

Все четыре gate'а запускаются в CI (`make test` + `make bench`) и фейлят билд при регрессии.

---

## Memory budgets

### Бюджеты и обоснование

**Idle: < 50 MiB HeapAlloc**

Целевая аудитория lazytg — разработчики, держащие клиент открытым в `tmux` рядом с десятком других long-running процессов (nvim, lsp servers, dev servers, ssh-сессии). Каждые 50 MiB резидентных за TUI-клиент — это minus 50 MiB для productivity-процессов. 50 MiB — порог, при котором клиент остаётся "невидимым" в общем footprint vimer'а.

**Active: < 150 MiB HeapAlloc**

Активная сессия (приходящие сообщения + параллельный поиск) допускает до 3× idle для in-flight bus-буферов, FTS5 page cache и search-результатов. Превышение 150 MiB начнёт мешать другим процессам в tmux — это потолок, при котором clients still feels lightweight.

### Как измеряется

`test/perf/memory_test.go` содержит два теста:

1. **`TestMemoryBudget_Idle`** — собирает app через `internal/app.Build` с production wiring (bus, repo, LiveService, DegradationDetector, DBSizeMonitor, search pipeline) без MTProto-клиента и без traffic. Ждёт 5 секунд stabilization → `runtime.GC()` × 2 → читает `runtime.MemStats.HeapAlloc`.
2. **`TestMemoryBudget_Active`** — то же что выше + seed corpus (10 чатов × 1000 сообщений) + 30-секундная активная нагрузка: 1000 mock `MessageReceived` событий через bus (~33 msg/sec) + 10 параллельных search-workers, бьющих в FTS-индекс. После завершения нагрузки и full drain — GC × 2 → HeapAlloc.

Оба теста skip'аются под `-short`. CI запускает их отдельным шагом в `ci.yml`.

### Текущие замеры (M4)

| Сценарий | HeapAlloc | Бюджет   |
|----------|-----------|----------|
| Idle     | ~2.0 MiB  | 50 MiB   |
| Active   | ~2.3 MiB  | 150 MiB  |

Запас огромный (~25× и ~65×). Это намеренно: бюджет должен переживать регрессии следующих стадий (config UI, kitty/iterm sixel preview, дополнительные индексы) без срочного refactor'a.

### Что делать при превышении

1. **Прогнать pprof:**
   ```sh
   go test -memprofile=mem.out -run TestMemoryBudget_Active ./test/perf/
   go tool pprof -top mem.out
   ```
2. **Топ-3 кандидата на регрессию (по опыту):**
   - Подписчики bus, не отписавшиеся при shutdown — buffered channel держит refs на сообщения
   - SQLite-prepared statements, не закрытые в hot path → `*sql.Stmt` пул растёт без bound
   - FTS5 search results — кэширование hit'ов в map[string][]Hit без LRU

---

## Search SLA

### Бюджет: p95 < 100 ms на 100k сообщений

**Обоснование:** UX-floor для "instant search" — 100 ms ощущается мгновенно, 200 ms заметно как pause, 500 ms превращает overlay в blocking dialog. Цель v0.1.0 — не уступать `messages.search` Telegram Desktop при отсутствии сети.

### Как измеряется

`internal/core/search/bench_test.go::BenchmarkSearch100k` сидит 100k синтетических сообщений (20 чатов × 5000) + детерминированный PCG seed → warmup → 100 search-итераций по 4 query-вариантам (`привет`, `hello world`, `тест`, `abc def`). Падает с `b.Fatalf` если `latencies[len*95/100]` > 100 ms.

```sh
make bench
# или:
go test -bench=BenchmarkSearch100k -benchtime=1x ./internal/core/search/
```

### Текущие замеры (M4)

| Метрика | Значение |
|---------|----------|
| p50     | ~38 ms   |
| p95     | ~47 ms   |
| p99     | ~45 ms   |
| budget  | 100 ms   |

Запас ~2×. Регрессии (например, добавление JOIN'а без индекса) ловятся в CI до merge.

### DB size guidance

Trigram-токенайзер даёт overhead **3–5×** от текста сообщений. Реальные оценки:

| Сообщений | Размер `messages_fts` | Размер БД целиком |
|-----------|------------------------|-------------------|
| 10k       | 5–20 MB                | ~30–50 MB         |
| 100k      | 50–200 MB              | ~250–400 MB       |
| 500k      | 250 MB – 1 GB          | ~1–2 GB           |
| 1M        | 500 MB – 1.5 GB        | ~2–3 GB           |

`obs.DBSizeMonitor` эмитит `StorageStateChanged{Reason: "db_size_warning"}` при размере БД > 1 GB. Status bar показывает `⚠ DB N.N GB` — informational, не блокирующий. Workaround: `lazytg reindex --all` после уменьшения `DefaultPerChatLimit` либо удалить и начать с пустой БД.

См. подробнее в [SEARCH.md → Ёмкость БД](SEARCH.md#ёмкость-бд-и-trigram-overhead).

---

## Live-update SLA

### Бюджет: p95 < 500 ms (bus → repo persistence)

**Обоснование:** Telegram Desktop показывает новое сообщение в среднем за ~300–500 ms от tap отправителя до render у получателя. lazytg нацелен не уступать; 500 ms — потолок воспринимаемой "instant" доставки.

### Как измеряется

`test/perf/live_latency_test.go::TestLiveUpdateLatencySLA` стреляет 1000 `MessageReceived` событий через bus батчами по 32 (yield между батчами, чтобы 64-slot buffer не дропал), ждёт полный drain `LiveService` → in-memory store, считает per-event latency как `Save - Publish`. Падает с `t.Fatalf` если p95 > 500 ms.

### Текущие замеры (M4)

p95 ≈ 4 ms — **в 100× быстрее SLA**. Bottleneck — не bus или drain, а fsync на SQLite WAL commit. На спинальном диске задержка вырастет до десятков ms, всё равно с огромным запасом.

---

## Известные ограничения

| Ограничение | Effect | Workaround |
|-------------|--------|------------|
| Тяжёлые чаты (>10k активных сообщений в одном чате) | Viewport jank при скролле | Уменьшить `DefaultPerChatLimit` в config (Stage 4+) или скроллить страницами |
| Полная переиндексация миллиона сообщений | 1–3 минуты | Использовать lazy index (default) — переиндексирует только последние 5000/чат на первый поиск |
| Concurrent SQLite writers | SQLite single-writer; параллельные write блокируют друг друга | Архитектура lazytg уже single-writer (LiveService drain): не запускать `lazytg reindex` параллельно с TUI |
| Trigram overhead | Индекс ~3-5× от размера текста | Принимаем как trade-off за language-agnostic search; см. SEARCH.md |
| Spike GC во время большого backfill | RSS может временно подскочить выше budget | Backfill ограничен per-chat → spike короткий; budget полицирует steady-state, а не peak |

---

## Запуск всех gate'ов локально

```sh
# memory budgets (45 sec)
go test -v -run "TestMemoryBudget" ./test/perf/...

# search p95 (10-30 sec, зависит от диска)
make bench

# live-update p95 (1-2 sec)
go test -v -run "TestLiveUpdateLatencySLA" ./test/perf/...

# goroutine leaks (1 sec)
go test -v -run "TestApp_NoGoroutineLeaks" ./test/perf/...
```

CI (`.github/workflows/ci.yml`) запускает всю четвёрку на ubuntu-latest и macos-latest.

---

## Связанная документация

- [`docs/SEARCH.md`](SEARCH.md) — query syntax, FTS5 internals, ёмкость БД
- [`docs/ARCHITECTURE.md`](ARCHITECTURE.md) — слои core / storage / ui, depguard
- [`docs/SECURITY.md`](SECURITY.md) — модель угроз (rate-limit, debug-bundle, permissions)
- План v0.1.0: [`docs/plans/lazytg-v0.1.0.md`](plans/lazytg-v0.1.0.md)
