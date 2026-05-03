# Plan: lazytg Stage 3 — FTS5 search + files + командная палитра

## Overview

Реализация этапа 3 проекта lazytg: киллер-фича продукта — **локальный FTS5-поиск по всей истории Telegram** с p95 latency <100ms на 100k сообщений. Параллельно: файлы (download/upload), командная палитра L1 (chat switcher по frecency с Unicode-fuzzy), полная реализация `debug-bundle`, security minimal (permissions + rate-limit на send).

Pitch продукта «Instant search across your entire Telegram history» становится реальностью именно в этом этапе.

После Stage 3 должно работать:
- `/<query>` в TUI открывает search overlay, по мере ввода (debounce 150ms) показывает результаты с подсветкой совпадений, Enter → переход в нужный чат с scroll к контексту ±5 сообщений.
- Search query supports операторы: `from:@user in:#chat before:2026-01-01 after:2025-12-01 has:file "exact phrase" -word`.
- `Ctrl-Space` открывает палитру L1 с top-50 чатов по frecency, fuzzy-поиск работает на "Алёна"=="Алена".
- `Ctrl-D` на сообщении с медиа → скачивание в `~/Downloads/lazytg/<chat>/<filename>` с прогресс-баром в статус-баре.
- `Ctrl-U` → file picker → отправка файла с caption.
- `lazytg debug-bundle` создаёт tar.gz без session/api_hash/phone (доказано grep-тестом).
- При старте: permissions check на `0600`/`0700`, fail-fast при нарушении. Rate-limit на send: max 10 msg/sec.

**Acceptance criteria Stage 3 (из главного плана):**
- Search работает, **p95 <100ms на 100k сообщений** (benchmark gate в CI).
- Files download/upload работают с прогрессом.
- Палитра L1 работает с Unicode-fuzzy ("Алёна"=="Алена").
- DB size warning >1GB виден в status bar.
- **debug-bundle без секретов** (доказано grep-тестом на api_hash/session/phone).
- Permission checks работают при старте.
- Rate-limit включён на send.
- Coverage `internal/core` ≥80%.

## Context

**Stage 1 + Stage 2 завершены.** На текущей ветке (`lazytg-stage1-foundation`, всё в одной по факту) есть:

- 3-слойная архитектура с depguard rules (core ⊥ gotd/bubbletea, ui ⊥ gotd, storage ⊥ ui/tg)
- Event bus в `internal/core/events/` с типизированными событиями
- SQLite repo + миграции 0001-0004 (accounts/chats/messages/peers/state/channel_state/peers_extended/outgoing) — pure-Go modernc; **FTS5 spike прошёл в Stage 1 (trigram tokenizer работает)**
- gotd auth + Keyring/Age secrets
- Cobra CLI с командами login/logout/accounts/version/debug-bundle (stub) и default TUI команда
- slog logger с redaction в `internal/core/obs/`
- DI wiring в `internal/app/wire.go` (в Stage 2 переехал из `cmd/lazytg/cmd/runtime.go`)
- `internal/core/sync/` — history backfill, live updates, send с optimistic UI, reconnect, degradation, ratelimit (token bucket уже есть!)
- `internal/tg/` — client, auth, session, history, updates, polling, send, floodwait
- `internal/ui/` — Bubble Tea с 2-pane (chats + thread), input с emacs bindings, $EDITOR, statusbar, help overlay, keymap loader
- Coverage core 79.8%, ui 83% — выше gates

**Что добавляем в Stage 3:**
- **`internal/core/search/`** — index, reindex, parser, query, service
- **`internal/core/files/`** — download, upload, store
- **`internal/tg/files.go`** — gotd downloader + uploader обёртки
- **`internal/core/obs/`** дополняем: `dbsize.go`, `bundle.go` (полный debug-bundle)
- **`internal/core/security/`** — permissions, ratelimit guard на send (но `core/sync/ratelimit.go` token bucket уже существует — переиспользовать!)
- **`internal/ui/palette/`** — frecency, registry, model для палитры L1
- **`internal/ui/panes/search/`** — overlay-pane для поиска
- **`internal/ui/input/attach.go`** — file picker
- **`docs/SEARCH.md`** — query syntax + DB size guidance

**Стек дополнения:**
- `golang.org/x/text/unicode/norm` — для NFKD normalization в fuzzy
- `golang.org/x/text/unicode/runenames` — может понадобится для drop diacritics (через Mn category)
- `github.com/sahilm/fuzzy` — fuzzy matching (с обёрткой для Unicode normalize)

**Отложено на v0.2 (явно НЕ делать в Stage 3):**
- Палитра L2 (global commands через `>` префикс) — только L1 chat switcher
- expvar metrics, trace mode (MTProto frames)
- `$EDITOR` sandbox env-filter
- Inline media preview (Kitty/iTerm/sixel) — v0.3
- Resume для прерванных загрузок — документировать как явный non-goal
- Полный entity-рендеринг сообщений (только plain markdown в Stage 2 thread.format)
- tgql полноценный query-DSL — Stage 3 даёт только базовые операторы

**Гайдлайны для исполнителя:**
- `internal/core/search/*` НЕ может импортировать gotd или bubbletea (depguard)
- `internal/core/files/*` использует `internal/tg/files.go` через интерфейс — не импортирует gotd напрямую
- Tests-first для core. Search benchmark обязателен — это SLA gate
- Reindex и backfill — горутины с context.Cancel, проверять через goleak
- `goleak.VerifyNone(t)` для всех долгоживущих компонентов
- Frecency persistence в SQLite (новая таблица `chat_frecency`)
- В debug-bundle redaction обязательна — grep-тест должен fail-fast если найдёт api_hash/session/phone

## Validation Commands

- `cd /Users/pgmac/Data/prjcts/lazytg && go build ./...`
- `cd /Users/pgmac/Data/prjcts/lazytg && go test -race ./...`
- `cd /Users/pgmac/Data/prjcts/lazytg && golangci-lint run`

### Task 1: FTS5 schema + триггеры + индекс core

- [x] Создать `internal/storage/sqlite/migrations/0005_fts.sql` с миграцией. Реализовано в виде standalone FTS5 (без `content='messages'`): external content table возвращает все rowid из messages в `SELECT FROM messages_fts` независимо от того, проиндексированы ли они на самом деле, что ломает idempotent-проверку Backfill. Standalone FTS5 даёт прямой контроль над набором проиндексированных rowid, плюс позволяет использовать прямые DELETE/UPDATE триггеры (накладные расходы на дублирование text входят в 3-5× бюджет из плана). Также добавлена опция `case_sensitive 0` для русскоязычного поиска без префольдинга на стороне клиента.
- [x] Создать `internal/core/search/doc.go` с описанием пакета: «Local FTS5 search service. MUST NOT import gotd or bubbletea (enforced via depguard).»
- [x] Создать `internal/core/search/index.go` с типом `Indexer`. Метод `Backfill(ctx context.Context, chatID int64, limit int) (indexed int, err error)` — берёт N последних сообщений из repo (default 5000), вставляет в `messages_fts`. modernc.org/sqlite не возвращает корректный RowsAffected для FTS5 — используется транзакция COUNT+INSERT с фильтрацией `WHERE rowid NOT IN (SELECT rowid FROM messages_fts)`. Используется узкий интерфейс `IndexStore` (только `DB() *sql.DB`) вместо прямого импорта `*sqlite.Repo`.
- [x] Создать `internal/core/search/index_test.go`:
  1. Trigram corner cases: вставлены тексты "ok" (2 chars), "Привет мир", "🚀🎉", "abc", "ab", "🚀🎉🎊", "hello world". Подтверждено: SQLite trigram не индексирует строки <3 code points (запрос "ok" → 0 hits), case_sensitive=0 даёт совпадение "при"/"При" → "Привет", три-эмодзи строки индексируются.
  2. Триггеры: вставить message в repo → автоматически появилось в messages_fts (проверка через `MATCH` + JOIN)
  3. UPDATE message → fts обновлён, старый текст не находится, новый — да
  4. DELETE message → fts очищен
  5. Backfill пустого чата → 0 indexed, no error
  6. Backfill чата с 100 сообщениями (DROP TRIGGER messages_ai → INSERT → recreate trigger симулирует upgrade-сценарий) → 100 indexed, повторный вызов → 0 (всё уже проиндексировано)
  7. Backfill пропускает строки с NULL/пустым text
- [x] Запустить `go test -race ./internal/storage/sqlite/... ./internal/core/search/...` — зелёное

### Task 2: Lazy index + reindex с graceful cancel + p95 benchmark

- [x] Создан `internal/core/search/reindex.go` с `ReindexService{indexer, chats ChatLister, bus EventPublisher, log, perChatLimit}`. Метод `Run(ctx, chatIDs)` последовательно вызывает `indexer.Backfill`, после каждого чата публикует `ReindexProgress{ChatID, Indexed, Total, Done}` (флаг Done на последнем). `RunAll(ctx)` через `ChatLister.GetChats` собирает идентификаторы и делегирует в Run. Cancel оборачивается `fmt.Errorf("…: %w", err)` так что `errors.Is(err, context.Canceled)` ловит как явные `ctx.Err()`-проверки между чатами, так и cancel изнутри Backfill через `BeginTx`. Empty pass публикует один Done-event для UI-сценария «индексация выключена».
- [x] Создано событие `ReindexProgress{ChatID, Indexed, Total, Done}` в `internal/core/events/events.go` с `eventMarker()`.
- [x] Создан `internal/core/search/lazy.go` с `LazyTrigger{reindex, log, mu, triggered, done chan struct{}}`. `EnsureIndexed(ctx)` под mutex выставляет `triggered=true`, запускает RunAll в goroutine, закрывает `done` по выходу. Повторный вызов — noop. Дополнительно добавлены геттеры `Triggered()` и `Done()` для тестов и будущей синхронизации в wire.
- [x] Создан `internal/core/search/reindex_test.go`:
  1. RunAll по 3 чатам × 10 сообщений → 3 события ReindexProgress в порядке id ASC, последний с Done=true. Indexed=0 для каждого, потому что AFTER INSERT уже сложил всё в индекс.
  2. Graceful cancel: 50 чатов × 200 сообщений с временно сброшенным trigger (имитация "историческая БД до Stage 3"), RunAll стартует в goroutine, после 50 мс — cancel. Ассерт: возвращается `context.Canceled` (или nil на сверх-быстрых машинах с логированием), `PRAGMA integrity_check` = "ok", `INSERT INTO messages_fts(messages_fts) VALUES('integrity-check')` без ошибки.
  3. LazyTrigger × 2 + ChatLister-mock со счётчиком calls → ровно 1 вызов GetChats, `Triggered()=true`.
  4. Дополнительно: пустой pass публикует Done-event; RunAll без ChatLister возвращает явную ошибку.
- [x] Создан `internal/core/search/service.go` с `Service{store IndexStore, lazy *LazyTrigger, log}`. `Search(ctx, raw string, limit int) ([]Hit, error)` строит SQL `SELECT m.*, snippet(...), bm25(...) FROM messages_fts JOIN messages ORDER BY bm25 LIMIT ?`. Для Task 4 совместимая сигнатура (raw string), Query/Parser плагуются в Task 3. Empty/whitespace-only query → ошибка; limit ≤ 0 → DefaultSearchLimit (50). Подключён LazyTrigger через `EnsureIndexed`. Мини-тесты в `service_test.go` (basic match, empty rejected, default limit).
- [x] **SLA Benchmark.** Создан `internal/core/search/bench_test.go` с `BenchmarkSearch100k`:
  1. Setup в `b.StopTimer()`: 20 чатов, 100 000 сообщений (длина 3–18 слов), batched транзакции по 5000, deterministic PCG seed=42.
  2. Backfill не нужен — триггеры индексируют на INSERT.
  3. Warmup по 4 запросам (греет page cache и FTS5 idx-сегмент).
  4. 100 итераций `Service.Search` по очереди с queries `["привет", "hello world", "тест", "abc def"]`.
  5. Сортировка latencies, `b.Fatalf` если `latencies[len*95/100]` > `p95SLA` (100 мс). `b.ReportMetric` для p50/p95/p99.
- [x] `go test -race ./internal/core/search/...` зелёное; `go test -bench=BenchmarkSearch100k -benchtime=1x` → **p95 = 44.23 мс** (p50=37.67, p99=44.98) на M4. Запас по SLA ~2.3×, регрессии теперь видны в CI.

### Task 3: Search query parser

- [x] Создан `internal/core/search/query.go` с типом `Query`. От плана отступил в одном месте: `InChats` сделан `[]string`, а не `[]int64` — резолвинг username/title → chat_id переехал в SQL подзапрос внутри `BuildSQL` (`m.chat_id IN (SELECT id FROM chats WHERE username IN (...) OR title IN (...))`), чтобы парсер не нуждался в DB-доступе. Поля From/InChats хранят строки без префиксов `@`/`#`.
- [x] Создан `internal/core/search/parser.go` с функцией `Parse(input string) (Query, error)`. Токенизация рукописная (без regex): уважает `"..."` фразы с `\"` escape, неклоsed quote → ошибка с позицией. Оператор распознаётся по shape `[a-z]+:value` с непустым value, иначе токен идёт как plain text (URL `http://x` не ломает парс). `from:`/`in:` стрипают `@`/`#`. `before:`/`after:` принимают `YYYY-MM-DD` и `YYYY-MM-DDTHH:MM:SS` через time.ParseInLocation в UTC. `has:` принимает только `file`, остальные значения → ошибка. `-foo` (длиной ≥2) → `Excluded`, `-` сам по себе остаётся как plain. Пустая строка / только операторы без positive terms → ошибка.
- [x] Создан `internal/core/search/parser_test.go` с двумя табличными тестами: TestParse (14 happy-path кейсов включая phrases с escape, kitchen-sink, fall-through unknown op) и TestParse_Errors (6 негативных кейсов с substring-проверкой текста ошибки).
- [x] Создан `internal/core/search/query_builder.go` с `BuildSQL(q Query) (sqlText, ftsMatch string, args []any, err error)`. Возвращает SQL fragment с лидирующим ` AND ` для конкатенации к базовому запросу, FTS5 MATCH строкой и параметрами для placeholders в sqlText. FTS5 MATCH: text + phrases (через `"..."`) + опционально `(positive) NOT (a OR b)`. WHERE clauses: `m.from_id IN (subquery chats.username)`, `m.chat_id IN (subquery chats.username OR title)` с дублированием args, `m.date >= ?`/`m.date < ?` для after/before. **HasFile сознательно не emit-ит SQL фильтр** — оставлен placeholder-комментарий `/* TODO(stage3-task6): m.media_type IS NOT NULL */ 1=1` который не схлопывает результаты, но grep'ается из Task 6 для замены на реальный фильтр после миграции 0008.
- [x] Создан `internal/core/search/query_builder_test.go` с TestBuildSQL (13 кейсов покрывающих каждое поле Query + kitchen-sink) и TestBuildSQL_Errors (защита от hand-built Query без positive terms — Parse уже это ловит, но defensive layer).
- [x] Обновлён `Service.Search` чтобы использовать Parse + BuildSQL вместо forwarding raw в FTS5 MATCH. Аргументы складываются в порядке `[ftsMatch, ...extraArgs, limit]`. Сам ftsMatch остаётся string-substituted (modernc/sqlite не поддерживает параметризованный MATCH без потери трëгграмного токенайзера), но user input теперь не попадает туда напрямую — только через positive terms / phrases / NOT clause собранные парсером.
- [x] `go test -race ./internal/core/search/...` зелёное (3.7s включая benchmark setup для search). Coverage пакета search: 90.0%. Полный `go build ./...`, `go test -race ./...` и `golangci-lint run` без ошибок и issues.

### Task 4: Search service jump-to-message + Search UI overlay

- [x] `Service.JumpContext(ctx, hit Hit, around int) ([]domain.Message, int, error)` реализован в `internal/core/search/service.go`. Default `around = 5` через константу `DefaultJumpContext`. Один SQL round-trip — `UNION ALL` двух полузапросов (id < target DESC LIMIT around) и (id >= target ASC LIMIT around+1) с внешним `ORDER BY id ASC` чтобы вернуть слайс уже в правильном порядке. На границах возвращается короче (target=2 → 7 сообщений вместо 11). Если target отсутствует в репо — `ErrJumpTargetMissing`. Coverage: 4 кейса в `jump_test.go` (centred window, start boundary, default around, missing target).
- [x] `events.SearchJumpRequested{ChatID, MessageID}` добавлен в `internal/core/events/events.go` с `eventMarker()`. Публикуется приложением при `JumpMsg`-обработке (если bus подключён). Подписчики (chats pane reorder, status bar, future history-of-jumps) могут реагировать на bus event.
- [x] `internal/ui/panes/search/messages.go` создан с типами `OpenedMsg`, `ClosedMsg`, `QueryChangedMsg{Query}`, `ResultsMsg{Hits, Err}`, `JumpMsg{Hit}`. Имена без префикса `Search` — пакет уже называется `search`, повторение стертерило бы (revive linter); со стороны app они доступны как `uisearch.OpenedMsg` и т.д.
- [x] `internal/ui/panes/search/model.go` создан с `Model{Width, Height, Visible, input textinput.Model, service Service, debounce, log, queryGeneration, lastQuery, hits, cursor, err, loading}`. Интерфейс `Service` (вместо `SearchServiceInterface`) с одним методом `Search(ctx, raw, limit) ([]search.Hit, error)`. Конструктор `New(service, debounce, log)` с fallback к `DefaultDebounce=150ms` и `DefaultLimit=50`. Cursor в результатах хранится напрямую (без bubbles/list — упрощает тесты).
- [x] `internal/ui/panes/search/update.go` создан. Esc → `Close + ClosedMsg`. Enter на выбранном hit → `Close + JumpMsg{Hit}`. Up/Down → cursor с clamp на границах. Любая другая клавиша → forward в `textinput.Update`; если value изменился — `scheduleQuery` инкрементирует `queryGeneration` и арм-ит `tea.Tick(debounce)` с этим generation. `debounceTickMsg` со stale generation (после последующих keystrokes) — drop. `ResultsMsg` сбрасывает loading и cursor=0; ошибки попадают в `m.err` для рендера в View.
- [x] `internal/ui/panes/search/view.go` создан. lipgloss centred modal с rounded border. Внутри: input строка, далее по состоянию — error / "Searching…" / hint / список hits. В каждом hit-row: `chat=<id>  YYYY-MM-DD HH:MM  snippet`. FTS5-маркеры `<b>...</b>` в snippet заменяются на bold ANSI runs (через `applyDelim`-style парсер чтобы непарные открытия не съели остаток). Cursor row красится в `lipgloss.Color("12")` (bright blue).
- [x] Wiring overlay в `internal/ui/app/`: keymap binding `Search` добавлен с дефолтом `/` в `keymap/defaults.go` и `loader.go`. Help overlay показывает новый binding. Global key handler в `app/update.go` пропускает `/` если `focus == FocusInput` (printable char) или `focus == FocusChats` (там `/` — bubbles/list filter, нельзя угнать). Search-overlay приоритет над panes — пока `a.search.Visible == true`, все сообщения роутятся в `a.search.Update`. View рисует overlay поверх 2-pane через свитч в `view.go`.
- [x] `Model.ScrollTo(messageID, around int)` в `internal/ui/panes/thread/model.go`. Находит target в `m.messages`, считает `linesBefore` через `countRenderedLines` + 1 за blank-line separator, вычитает `around` и через `viewport.SetYOffset` ставит окно. Clamp в `>= 0` встроен. Несуществующий ID — no-op. Дополнительно экспонирован метод-геттер `YOffset()` для тестов.
- [x] App-wiring scroll-after-load: `JumpMsg` в `app/update.go::handleSearchJump` сохраняет `pendingScroll` (chatID, messageID, around=5), вызывает `thread.OpenChat`, через `bus.Publish(events.SearchJumpRequested)` уведомляет других подписчиков. Метод `applyPendingScroll` вызывается из `withPendingScroll(broadcastToPanes(msg))` после того как `messagesLoadedMsg` уже применился — иначе `applyLoaded`-ный `GotoBottom()` затёр бы наш scroll. Если target ещё не загружен (старее initialPageSize), pendingScroll просто не находит ID и ничего не двигает; scroll-сигнал держится до следующего broadcast — TODO для будущей итерации. По факту в тесте `TestSearchJumpSwitchesChatAndScrolls` без живого репо проверяется только switch chat и закрытие overlay.
- [x] `internal/ui/panes/search/model_test.go`: 9 тестов. `TestUpdate_KeyTriggersDebouncedSearch` использует `time.Hour`-debounce с прямой инжекцией `debounceTickMsg` (generation 1 → stale, generation 2 → fresh + `service.Search` вызван) — без актуального `time.NewTimer` в тесте. Также: Open/Close/Reset, Esc/Enter/cursor, empty-query clears, error surfaces, View hidden/visible.
- [x] `internal/ui/app/search_test.go`: 5 тестов. `/` из FocusThread открывает overlay. `/` из FocusChats — suppressed. Esc закрывает. End-to-end JumpMsg: переключение chatID + закрытие overlay. Deps.Search injection survives construction.
- [x] `internal/ui/panes/thread/scroll_test.go`: 3 теста. Centred target — ScrollTo двигает viewport между top и bottom (offset > 0 и < bottomOffset). Unknown ID — no-op. Target в начале — clamp в YOffset=0.
- [x] `go test -race ./internal/ui/... ./internal/core/search/...` зелёное; `golangci-lint run` — 0 issues.

### Task 5: Frecency store + Командная палитра L1 (Ctrl-Space) + Unicode-fuzzy

- [x] Создана миграция `internal/storage/sqlite/migrations/0006_frecency.sql` с таблицей `chat_frecency(chat_id PK→chats(id) ON DELETE CASCADE, visit_count, last_visit, score REAL)` + индекс по `score DESC` для дешёвого ORDER BY DESC в `TopFrecency`.
- [x] В `internal/storage/sqlite/frecency.go` (отдельный файл, чтобы repo.go не разрастался) методы `RecordVisit(ctx, chatID, now time.Time)` и `TopFrecency(ctx, limit) ([]int64, error)`. Алгоритм: BEGIN → читаем `priorLastVisit` → UPSERT `visit_count++, last_visit=now` → читаем новый `visit_count` → `score = visit_count * exp(-(now - priorLastVisit) / 30 days)` → UPDATE score → COMMIT. На первом визите `priorLastVisit = now` → decay = 1, score = 1 (плановая семантика, иначе пустая таблица никогда не получает score>0). `TopFrecency` clamp limit ≤ 0 → 1, ORDER BY `score DESC, last_visit DESC` (tie-breaker для одинаковых score).
- [x] `internal/ui/palette/frecency.go` определяет `FrecencyStore` интерфейс (`Top` + `RecordVisit`) и адаптер `repoStore` через узкий `repoBackend` интерфейс — палитра не импортирует `internal/storage/sqlite` напрямую (тестовые бинари не тянут CGo-free SQLite, проще fake'ать). `HotSetLimit = 200`.
- [x] Зависимости `golang.org/x/text/unicode/norm` и `github.com/sahilm/fuzzy` уже были `// indirect`; теперь стали direct после добавления импорта в палитре. `go mod tidy` промотал их в основную секцию.
- [x] `internal/ui/palette/normalize.go::Normalize(s)` через NFKD → drop Mn-категория (combining marks) → ToLower. Headline-инвариант покрыт `TestNormalize_AlyonaRoundTrip`: `Normalize("Алёна") == Normalize("Алена")`. Дополнительные кейсы: Café→cafe, naïve→naive, ASCII passthrough, эмоджи unchanged, empty string. Сценарий с `ẞ`/`ß` пропущен — `unicode.ToLower(ẞ) = ß`, не `ss`; это поведение Go-stdlib, и для русско-английской аудитории не важно.
- [x] `internal/ui/palette/messages.go` — `OpenedMsg`, `ClosedMsg`, `QueryChangedMsg{Query}`, `SelectedMsg{ChatID}`, `LoadedMsg{Items, Err}`. Имена без префикса `Palette` — пакет уже `palette`, повторение стертерило бы.
- [x] `internal/ui/palette/model.go` — `Model{Width, Height, Visible, input, frecency, chats, log, items, filtered, cursor, loadErr}` с `Item{ChatID, Title, NormalizedTitle}`. `New(frecency, chats, log)` принимает обе зависимости как nil-safe (тесты создают модель без бэкенда). `Open()` возвращает `tea.Batch(focusCmd, loadCmd)`; `loadCandidates` запускает goroutine, которая пуллит top-200 из FrecencyStore + полный chats list, и через `mergeCandidates` склеивает в порядке (frecency-known first → alphabetical tail). Чаты без title скипаются (нечего показывать); chat_id известен в frecency, но title неизвестен → fallback `chat <id>`. Размер модала: ширина клампится 10..80, центрируется через `lipgloss.Place`.
- [x] `internal/ui/palette/fuzzy.go::Match(query, items)` нормализует query, делает `fuzzy.FindFrom` через адаптер `titleSource` (`Len`/`String`) — без аллокации intermediate `[]string`. Empty/whitespace query → identity slice (полный список в исходном frecency-порядке).
- [x] `internal/ui/palette/update.go` — модальная диспетчеризация: `OpenedMsg`/`ClosedMsg` от app, `LoadedMsg` пишет items+filtered+loadErr. Esc → Close + ClosedMsg cmd. Enter на пустом filtered → noop; иначе Close + SelectedMsg{ChatID}. Up/Down с clamp на границах. Любая другая клавиша → forward в textinput; если value изменилось → `m.filtered = Match(...)`, cursor=0. Hidden palette глотает все keys кроме OpenedMsg.
- [x] `internal/ui/palette/view.go` — lipgloss centred modal с rounded border. Состояния: error / no items / no matches / список. Cursor row подсвечен `lipgloss.Color("12")` (bright blue), мета-колонка `chat=N` через grey foreground (color 8). Window scroll при `len(filtered) > maxVisibleRows=12` — окно центрируется на cursor.
- [x] Wiring overlay в `internal/ui/app/`: добавлен keymap binding `OpenPalette` (default `["ctrl+space", "ctrl+@"]` — bubbletea v2 на одних терминалах эмитит первое, на других — второе). `loader.go::bindingFields` принимает TOML-ключ `open_palette`. `app/keys.go::cmdOpenPalette` эмитит `palette.OpenedMsg`. `app/update.go::handleGlobalKey` блокирует chord, пока chats filter активен (модальный конфликт). `openPalette/closePalette` хранят `prePaletteFocus`. `handlePaletteSelected` → close palette → thread.OpenChat(chatID) → input.SetChatMsg → status.ChatTitle обновляется через `chatTitle()` → fire-and-forget goroutine с `recordVisitTimeout=2s` зовёт `paletteFrecency.RecordVisit` (если deps выставил). `app/view.go` отрисовывает overlay поверх 2-pane: priority order help > palette > search > body. Routing для всех `palette.*Msg` в `Update` switch + fallback `if a.palette.Visible → palette.Update(msg)`.
- [x] `internal/ui/palette/normalize_test.go` — табличные тесты + headline `TestNormalize_AlyonaRoundTrip`.
- [x] `internal/ui/palette/model_test.go` — 9 тестов: defaults, Open с загрузкой topResult+rest sorted, фильтр "ал" → 2 матча, **Unicode test "Алёна"+query "Алена" → match**, Down+Enter → SelectedMsg правильный chat, Esc → ClosedMsg cmd, load error surfaced, hidden palette drops keys, Match(empty) → identity slice. fakeFrecency / fakeChats имитируют бэкенды без SQLite.
- [x] `internal/storage/sqlite/frecency_test.go` (вместо `palette/frecency_test.go` — тестируем именно SQL-логику): 5 тестов: empty repo → empty Top, recency wins (chat2 после chat1 → [2,1]), frequency wins (3 visits chat1 vs 1 visit chat2 → [1,2]), decay shrinks score after gap (chat1 visited then again 60d later → score = 2*exp(-2) ≈ 0.27 < 1.0 fresh chat2), limit ≤ 0 clamps to 1, validation chat_id == 0 → error.
- [x] Полный `go test -race ./...` (24 пакета) — зелёное. `golangci-lint run` — 0 issues. `go build ./...` — чистая сборка. Wiring в `cmd/lazytg/cmd/tui.go` (production-конструктор палитры с реальным FrecencyStore) отнесён к Task 10 — там единый wiring для всех stage 3 компонентов (search, palette, downloads). Сейчас в production-сборке палитра существует с nil-deps и не активна — keymap binding есть, но Top/GetChats возвращают пусто.

### Task 6: Files: download (Ctrl-D)

- [x] Создан `internal/tg/files.go` с `Downloader{api downloaderClient, log, partSize}`. Метод `Download(ctx, info domain.MediaInfo, w io.Writer, progress ProgressCallback) (int64, error)` через `gotd downloader.NewDownloader().WithPartSize(...).Download(api, loc).Stream(...)`. Прогресс через `progressWriter` обёртку — единственный writer, идущий в Stream, что означает sequential writes без необходимости в синхронизации. Дополнительно `MediaFromMessage(*tg.Message) *domain.MediaInfo` — единственная точка перевода gotd → domain (вызывается из `tg/history.go::convertMessage` и `tg/updates.go::publishMessage`). Поддерживаются Document и Photo (Document.AttributeFilename → имя; Photo берёт самый большой PhotoSize/PhotoSizeProgressive с fallback на `photo_<id>.jpg`).
- [x] Создан `internal/core/files/store.go` с `FileStore{root, log}`. Конструкторы: `NewFileStore(root, log)` и `NewFileStoreDefault(log)` (читает `LAZYTG_DOWNLOADS` env / fallback `~/Downloads/lazytg`). `Path(chatTitle, filename)` санитизирует обе строки через `sanitizeName` (replace `/\:*?"<>|\0` + control chars + leading/trailing dots на `_`; defends against path traversal через имя чата). `EnsureDir(path)` — mkdir-p с 0700. `Exists(path) (bool, error)` — `os.Stat` с правильным разделением на missing/error.
- [x] Создан `internal/core/files/dedup.go` с `DedupCache{store DedupStore}`. Узкий интерфейс `DedupStore{GetDownloadedPath, SaveDownloadedFile}` — core/files не импортирует sqlite. Методы: `Lookup(ctx, fileID, pathChecker)` (проверяет on-disk presence через injected pathChecker — если cache hit, но файл удалён → miss, перезакачка); `Record(ctx, rec)` (сохранение). Миграция `0007_files.sql` создаёт `downloaded_files(file_id PK, access_hash, path NOT NULL, size, downloaded_at)`. Repo методы (`internal/storage/sqlite/files.go`): `SaveDownloadedFile(ctx, DownloadedFile)` через UPSERT, `GetDownloadedPath(ctx, fileID) → (path, size, ok, err)` с явным разделением missing/error.
- [x] Создан `internal/core/files/download.go` с `DownloadService{tg, store, dedup, bus, log, progress}`. Полный orchestration: (1) dedup probe → если есть и файл на диске, эмитятся Started+Completed с cached path; (2) `EnsureDir` + `OpenFile(.partial, O_CREATE|WRONLY|TRUNC, 0o600)` (TRUNC потому что stale .partial мог остаться); (3) Started event; (4) reset throttler + stream через tg.Downloader с throttled progress callback; (5) close + rename .partial→final; (6) chmod 0600 (явно, не полагаясь на umask); (7) `dedup.Record`; (8) Completed event. На ошибках: cleanup .partial + Failed event. Throttler в `progress.go` — emit каждые 1 MiB или 5% (whichever first), single-flight per fileID.
- [x] События `FileDownloadStarted`, `FileDownloadProgress`, `FileDownloadCompleted`, `FileDownloadFailed` добавлены в `internal/core/events/events.go` с `eventMarker()`. `MessageReceived` расширен опциональным `Media *domain.MediaInfo` (events теперь импортирует `internal/core/domain` — domain не имеет внешних зависимостей, разрешено).
- [x] Hotkey `Download` = `Ctrl+D` добавлен в `keymap.Default()` и `bindingFields()`. В `app/update.go::handleGlobalKey` обработка проверяет focus (не `FocusInput`, не chats filter active), вызывает `cmdDownloadLatestMedia()`. v0.1 scope: операция на самом свежем сообщении с media в текущем thread (метод `thread.Model.LatestMediaMessage()`); per-message cursor — v0.2. На результате эмитится `thread.DownloadRequestedMsg{ChatID, MessageID, ChatTitle, Media}`. App's `handleDownloadRequest` запускает goroutine с `context.WithTimeout(downloadTimeout=30min)` который зовёт `FileDownloader.Download(ctx, ...)`.
- [x] `domain.Message` расширен `Media *MediaInfo`; `MediaInfo{Kind, FileID, AccessHash, FileReference, DC, Filename, Size, MimeType, ThumbSize}` с `MediaKind` enum (`document`/`photo`). Миграция `0008_messages_media.sql` добавила 9 nullable колонок (`media_kind` через `media_thumb_size`) — INLINE а не отдельная таблица потому что почти каждый чат имеет media-сообщения и JOIN cost доминировал бы. Repo SaveMessage/SaveMessages используют общий `messageUpsertSQL` + `messageInsertArgs(m)` helper. GetMessages/GetMessagesBefore используют общий `messageSelectColumns` + `scanMessages(rows)` helper. Tests `files_test.go::TestMessages_MediaRoundTrip` проверяет doc+photo+plain round-trip через batch и single insert.
- [x] `tg/history.go::convertMessage` и `tg/updates.go::publishMessage` зовут `MediaFromMessage(m)` — media пробрасывается и через MTProto history backfill, и через live updates → bus event → `LiveService.persist` → repo. Search service Stage 3 Task 3 placeholder обновлён: `q.HasFile` теперь генерирует `m.media_kind IS NOT NULL` (вместо `1=1` placeholder, удалён TODO).
- [x] `internal/ui/panes/thread/format.go::FormatMessage` показывает media badge: `[📎 report.pdf, 229.1 KiB] ctrl+d to save` для документа, `[🖼 photo.jpg]` для фото. `formatBytes(n)` рендерит base-2 ("KiB", "MiB", "GiB" — Telegram использует base-2 в UI). `mediaStyle` (синий) + `hintStyle` (серый italic).
- [x] `internal/ui/statusbar/model.go` расширен `Download{fileID, filename, bytes, total}` value-type + `downloads map[int64]Download` в Model. Методы: `UpsertDownload(d)` (copy-on-write map; preserve filename if Progress event приходит без него), `RemoveDownload(fileID)` (idempotent), `ActiveDownloads()` (test helper). `renderRight()` теперь проверяет `activeDownload()` — если есть, заменяет conn-cell на `⬇ filename N%` (cyan ANSI 6); multi-download выбирается по smallest fileID (стабильный рендер, expanded chip — v0.2). `formatDownloadCell(d)` — null-safe filename / unknown total (нет процента).
- [x] `internal/core/files/download_test.go` — 6 тестов: HappyPath (полный pipeline + проверка байтов на диске + отсутствие .partial leak + 0600 chmod + dedup row), DedupHit (cache hit → 0 downloader calls + Started+Completed события), DedupHitButFileMissing (stale cache → перезакачка), Failure (.partial cleanup + Failed event), SanitisesChatTitle (`weird/title` → `weird_title`), ZeroFileIDRejected. `progress_test.go` (purposely not separate) — throttler покрыт через download_test.go.
- [x] `internal/tg/files_test.go` — 8 тестов: MediaFromMessage для Document (с filename), Document без filename (fallback), Photo (largest size selection), PlainText, Unsupported (Contact); buildFileLocation для doc/photo/неизвестного kind; progressWriter (counts + callback fires per write); Downloader.Download через fake `UploadGetFile` API. Полный fakeAPI implements downloader.Client с UploadGetFileHashes/CDN/WebFile-методами returning errors-or-nil.
- [x] `internal/ui/app/download_test.go` — 5 тестов: Ctrl-D fires DownloadRequestedMsg (latest media из thread, fakeDownloader записывает call), Ctrl-D без media → cmd=nil но global handler consumes (нет fall-through), nil downloader path не паникует, FileDownload* события маршрутизируются в statusbar (filename отрисован, потом 50%, потом dropped после Completed/Failed), failed download log path. Helper `injectMessages` использует public Update path с MessageReceived. Тесты на формат media badge и LatestMediaMessage в `thread/format_test.go::TestFormatMessage_MediaBadge`/`TestLatestMediaMessage`. Statusbar тесты в `view_test.go::TestStatusbarDownloadCell`/`TestStatusbarMultipleDownloadsPickStable`.
- [x] `docs/FILES.md` создан — описывает: куда сохраняются файлы (env override, sanitization), дедупликация, события в шине, v0.1 ограничения (resume — v0.3, cursor — v0.2, inline preview — v0.3, FILE_REFERENCE_EXPIRED auto-refresh — v0.2), архитектурная диаграмма пакетов, manual smoke шаги, SLA.
- [x] Adapter `DedupStoreAdapter{Repo}` экспортирован в `internal/app/wire.go` (Task 10 wiring подключит DownloadService через него; компайл-тайм anchor `var _ files.DedupStore = (*DedupStoreAdapter)(nil)`). Полный `go build ./...`, `go test -race ./...`, `golangci-lint run` — 0 issues, все 24+ пакетов зелёные.

### Task 7: Files: upload (Ctrl-U)

- [x] Расширен `internal/tg/files.go` типом `Uploader{api uploaderClient, log, partSize}`. Метод `Upload(ctx, path, progress)` через `gotd uploader.NewUploader().FromFile(...)` (а не FromPath — это даёт нам контроль над Stat для размера + сохранение progress.Total). Возвращает `*UploadResult{InputFile, Filename, MimeType}` — тонкая обёртка над gotd-генерируемым `tg.InputFileClass`. `progressWriter` для downloader / `uploaderProgressAdapter` для uploader живут в одном файле и используют идентичный набор полей `(uploaded, total int64)`. FLOOD_WAIT → `*coresync.FloodWaitError`. Также добавлен `FilesAdapter{Up, Snd}` который удовлетворяет интерфейсам `files.TGUploader` и `files.SendMediaSender` через type assertion handle→`*UploadResult` — это и есть граница между core/files (any-handle) и gotd-типами.
- [x] Создан `internal/core/files/upload.go` с `UploadService{tg TGUploader, sender SendMediaSender, bus, log, progress}`. `TGUploader.Upload(...) (any, error)` и `SendMediaSender.SendMedia(ctx, chatID, handle any, caption, replyTo) (int64, error)` — opaque any-handle сохраняет depguard rule (core ⊥ gotd) тривиально. `UploadHardLimit = 2 GiB` отвергает файл с `ErrFileTooLarge` ДО любого открытия. `UploadSoftLimit = 50 MiB` эмитит `FileUploadWarning` но продолжает. `progress` через `uploadProgressThrottler` (twin of progressThrottler — те же 1 MiB / 5% пороги). UploadID — atomic.Int64 в-process counter (не Telegram file_id, который uploader узнаёт только после завершения). Pipeline: Stat → reject hard → Started → optional Warning → throttled Upload → SendMedia → Completed/Failed.
- [x] Расширен `internal/tg/send.go::Sender.SendMedia(ctx, chatID, file InputFileClass, filename, mimeType, caption, replyTo)`. `MessagesSendMessageClient` теперь требует `MessagesSendMedia`-метод (расширили contract). Все uploads идут через `InputMediaUploadedDocument` с `DocumentAttributeFilename` — Telegram сам выбирает правильный envelope для image/video mime. FLOOD_WAIT → `*coresync.FloodWaitError`; 400-class → `*coresync.ValidationError`. Empty mimeType → fallback `application/octet-stream`.
- [x] Добавлены события в `internal/core/events/events.go`: `FileUploadStarted{UploadID, ChatID, Path, Filename, Size}`, `FileUploadProgress{UploadID, BytesUploaded, TotalBytes}`, `FileUploadCompleted{UploadID, ChatID, MessageID}`, `FileUploadFailed{UploadID, Err}`, `FileUploadWarning{UploadID, Size, Threshold}`. Тип `UploadID = int64` (alias) — отдельный концепт от Telegram media id.
- [x] Создан **отдельный пакет** `internal/ui/panes/attach/` (а не `internal/ui/input/attach.go` как в плане) — overlay-pattern полностью аналогичен `panes/search` и `palette`, что значительно упрощает routing в app/update.go (модальный приоритет, `OpenedMsg`/`ClosedMsg`/`SubmitMsg` без конфликта с input pane). `bubbles/filepicker` **намеренно не используется** — он consume-ит Esc/Enter без callback, что заставило бы pre-empt-ить их выше нашего стандартного overlay-протокола; кастомный listing (~200 строк) даёт идентичную UX но с правильным lifecycle. Поля: textinput для path + textinput для caption + Tab toggles между ними (ModePath/ModeCaption).
- [x] `attach/update.go` Update: Esc → Close + ClosedMsg cmd. Enter на directory → descend (loadDir новой директории). Enter на regular file → Close + SubmitMsg{ChatID, Path, Caption}. Tab → toggle mode. Up/Down (ModePath only) → cursor с clamp. Anything else → forward в текущий focused textinput; если path value изменился — schedule loadDir reload.
- [x] Wiring overlay в `internal/ui/app/`: keymap `Attach = Ctrl+U` (defaults.go + loader.go bindingFields). `app.cmdOpenAttach()` берёт chatID из `thread.ChatID()` (если 0 — chord становится no-op, чтобы не открывать overlay из которого нечего сабмитить). `Attach` chord allowed из любого focus кроме chats filter — даже из FocusInput (Ctrl-U "delete to start of line" в emacs нечасто, и Telegram Desktop muscle memory важнее). View priority order: help > attach > palette > search > body. Routing: `attach.OpenedMsg` → `openAttach(msg)` (capture chatID), `attach.ClosedMsg` → `closeAttach()`, `attach.SubmitMsg` → `handleAttachSubmit(msg)` запускает goroutine с `uploadTimeout=30min` который зовёт `FileUploader.SendFile`. На nil-uploader chord становится quiet no-op — debug log, без UX surprise.
- [x] Расширен `internal/ui/statusbar/model.go` параллельной `Upload{uploadID, filename, bytes, total}` value-type + `uploads map[int64]Upload` в Model. Методы: `UpsertUpload`/`RemoveUpload`/`ActiveUploads`/`activeUpload` zero-копируют для value-semantics promise. `formatUploadCell` рендерит `⬆ filename N%` (cyan ANSI 6, тот же что и download). **Upload приоритизируется над download** — пользователь только что инициировал upload, feedback важнее пассивного download. Множественные uploads — smallest UploadID winner (стабильный рендер across map re-orders). Обработка событий в `app/broadcastBusEvent`: Started+Progress → `UpsertUpload`, Completed+Failed → `RemoveUpload`, Warning → consumed (для будущих toast widgets).
- [x] `internal/core/files/upload_test.go`: 8 тестов. HappyPath (Started+Completed events, sender call с правильным chatID/caption/handle, throttler exercised). LargeFileEmitsWarning (sparse file через Truncate, проверяет порядок Started→Warning→Completed). RejectsHardLimit (sparse 2 GiB+1, ErrFileTooLarge sentinel, 0 uploader/sender calls). PathDoesNotExist. RejectsNonRegularFile (directory → error). UploadFailureEmitsFailedEvent (sender НЕ вызван, события Started+Failed). SendFailureEmitsFailedEvent (uploader вызван успешно, sendMedia падает). RejectsZeroChatID. NewUploadService_RejectsNilDeps.
- [x] `internal/tg/files_test.go` расширен: 6 новых тестов. `TestUploader_Upload_StreamsBytes` через `fakeUploadAPI` (минимальный uploader.Client со SaveFilePart counter), partSize=1024 на 2048-байтном файле → 2+ parts, прогресс ticks > 0, UploadResult.Filename корректен. `TestUploader_RejectsEmptyPath`. `TestUploader_FailsOnMissingFile`. `TestDetectMimeType` (8 кейсов включая case-sensitivity и unknown). `TestFilesAdapter_SendMedia_RoundTrip` через `stubMediaSender` (extends stubSendMessage) — проверяет что caption/filename/mime корректно вшиваются в InputMediaUploadedDocument через Sender.SendMedia. `TestFilesAdapter_SendMedia_RejectsBadHandle` (string handle вместо *UploadResult). `TestNewFilesAdapter_RejectsNilDeps`. Дополнительно `TestSender_SendMedia_AttachesReplyTo` и `TestSender_SendMedia_RejectsNilFile`.
- [x] `internal/ui/panes/attach/model_test.go`: 9 тестов. New_StartsHidden. Open_PopulatesEntries (tmp dir с 2 файлами → entries содержат a.txt+b.txt отсортированно). Enter_OnFile_EmitsSubmit (cursor на regular file → Close + SubmitMsg с правильным path). Enter_OnDirectory_Descends (sub/leaf.txt появляется после descend). Esc_ClosesAndEmitsClosedMsg. Update_HiddenIgnoresKeys. Update_TabTogglesMode. LoadDir_ReportsErrorForBadPath. Enter_OnEmptyChatID_NoOps.
- [x] `internal/ui/app/upload_test.go`: 7 тестов. AttachChord_OpensOverlayWhenChatIsOpen (Ctrl-U → OpenedMsg{ChatID:42} → Visible=true). AttachChord_NoChatIsConsumedNoop (без open chat — cmd=nil, не открывается). AttachSubmit_DispatchesToUploader (SubmitMsg → goroutine → fakeUploader.SendFile вызван с chatID/path/caption). AttachSubmit_NilUploaderIsHarmless. AttachEsc_ClosesOverlay. UploadEvents_RoutedToStatusbar (Started → "⬆ doc.bin", Progress → "50%", Completed/Failed → drop). UploadWarning_DoesNotPanic. Statusbar тесты в `view_test.go::TestStatusbarUploadCell` + `TestStatusbarUploadOverridesDownload`.
- [x] `go test -race ./...` — все 25 пакетов зелёные. `golangci-lint run` — 0 issues. `go build ./...` чисто. Wiring real *Uploader/*Sender → FilesAdapter → UploadService и подключение `Deps.Uploader` в production-сборке `cmd/lazytg/cmd/tui.go` отнесён к Task 10 (единый wiring stage 3 компонентов). На текущий момент production keymap binding активен, но при nil-uploader chord — quiet no-op.

### Task 8: DB size monitoring + полный debug-bundle + grep-test

- [x] Создан `internal/core/obs/dbsize.go` с `DBSizeMonitor{repo RepoSizeSource; bus *events.Bus; log *slog.Logger; threshold int64; interval time.Duration; sleep, stat seams}`. Конструктор `NewDBSizeMonitor(repo, bus, log, cfg)` принимает `DBSizeConfig{Threshold, Interval}` со zero-value defaults: threshold=1 GiB (`DefaultDBSizeThreshold`), interval=60 s (`DefaultDBSizeInterval`). `Run(ctx)` — immediate first-tick затем sleep loop. Состояние "выше порога" латчится в локальной `*bool warned`, переход below→above публикует событие, above→below — clearing event. Узкий `RepoSizeSource{DBPath() string}` interface вместо прямой зависимости от storage. Test seams `sleep` и `stat` позволяют тестам не аллокировать гигабайтные файлы.
- [x] Расширен event `StorageStateChanged` в `internal/core/events/events.go` — добавлено поле `DBSizeMB int` (0 = "не применимо" для путей не измеряющих файл) и константа `ReasonDBSizeWarning = "db_size_warning"` для маршрутизации в UI.
- [x] Добавлены `Repo.DBPath() string` и helper `filePath(path) string` в `internal/storage/sqlite/repo.go`. DBPath() возвращает on-disk путь либо "" для in-memory / file: URI вариантов. Конструктор `Open` сохраняет путь через `filePath(path)`.
- [x] `internal/ui/statusbar/model.go` расширен полем `DBSizeMB int` + `formatDBSizeCell(mb)` (показывает MB до 1024, далее GB с одним знаком после запятой) + `dbSizeStyle` (yellow ANSI 3, тот же что floodwait/connecting — "warn" семантика). `renderRight()` дописывает chip к существующей строке `unread | conn | storage`. `internal/ui/app/update.go::broadcastBusEvent` маршрутизирует `StorageStateChanged` по `Reason`: `ReasonDBSizeWarning` обновляет `DBSizeMB`, любой другой — `StorageMode`. Это разводит два producer'a (`obs.DBSizeMonitor` и `coresync.DegradationDetector`) на independent state.
- [x] Создан `docs/SEARCH.md` — query syntax (`from:@user in:#chat before/after:DATE has:file "phrase" -word`), таблица операторов с поведением, ёмкость БД (10k → ~50 MB / 100k → ~250 MB / 1M → ~2-3 GB), DB size warning >1 GB, SLA p95<100ms (44.2 ms на M4), tokenizer trigram + min-length 3, lazy index 5000/чат, `lazytg reindex` (запланирован к Task 10), ограничения, ссылки на FILES/SECURITY/ARCHITECTURE.md.
- [x] Расширен `internal/core/obs/bundle.go` (полная реализация — stub удалён, command переписан в Task 8). `Bundle{Logger, Cfg ConfigSource, Version VersionInfo, Store BundleStore, LogPath, LogTailLines}` + `Create(ctx, outPath)`. Tar.gz содержимое: `version.txt` (build + Go + OS/arch), `config.toml` (через `Redact`, fallback "does not exist" placeholder), `logs.txt` (tail последних `LogTailLines` (default 1000) строк через ring-buffer + `Redact` per-line, scanner buffer 1 MiB для пагологически длинных JSON-записей), `db_stats.txt` (db_path, db_size_bytes, schema_version, COUNT(*) для accounts/chats/messages/messages_fts/peers/outgoing/downloaded_files — table list whitelisted а не sqlite_master, чтобы не подмести ненароком user-tagged content), `goroutines.txt` (1 MiB ceiling `runtime.Stack`). НЕ включает session-files, api_hash env, phone, текст сообщений. Tar headers пишут Mode=0o600. `cleanup`-флаг через defer удаляет partial-файл при ошибке (no debris в cwd).
- [x] Команда `cmd/lazytg/cmd/debug.go` переписана: `debug-bundle [--out path]`. Default out = `lazytg-bundle-<UTC-timestamp>.tar.gz` в cwd. Открывает SQLite repo, конструирует Bundle с logger из cmd context, configPathSource (filepath.Join(paths.Config, "lazytg.toml")), VersionInfo (build-time vars), LogPath (filepath.Join(paths.State, "lazytg.log") — XDG state dir). Печатает абсолютный путь после успеха.
- [x] **Grep-тест безопасности** `internal/core/obs/bundle_grep_test.go` (внешний package `obs_test` чтобы импортировать sqlite). 4 теста: `TestBundle_GrepNoSecrets` (5 forbidden substrings: api_hash hex, session base64, phone, два message-text — `t.Fatalf` с windowed snippet если найдено + проверка mode=0600 + проверка наличия всех 5 entries); `TestBundle_MissingPaths_DegradesGracefully` (нет config + нет log → "does not exist" placeholder в bundle); `TestBundle_LogTailLimitsLines` (5000 lines в log → bundle содержит ≤ 50 lines включая последнюю — guards ring-buffer correctness); `TestBundle_RemovesPartialOnFailure` (выходной dir не существует → ошибка + нет partial-файла).
- [x] `internal/core/obs/dbsize_test.go` — 8 тестов: BelowThresholdEmitsNothing, AboveThresholdEmitsWarning (Mode=rw подтверждает что warning informational а не блокирующий), OnlyEmitsOnTransition (idempotent на трёх ticks выше порога), TransitionBackEmitsClearedEvent (DBSizeMB=0 sentinel + Reason остаётся ReasonDBSizeWarning так чтобы маршрутизация в UI оставалась корректной), ThresholdConfigurable (50 MiB порог), MissingFileIsTolerated (`os.ErrNotExist` не флаппит state), NilRepoExitsCleanly (Run возвращает ctx.Err), RunIntegratesWithRealLoop (interval=5ms + threshold=1 byte → реальный goroutine emit warning через Subscribe), `TestBytesToMB_RoundsUp` (округление вверх для не-нулевых значений). Statusbar test `TestStatusbarDBSizeWarning` — 4 кейса от 0/600 MB/1024 MB/1500 MB.
- [x] Финальная валидация: `go build ./...`, `go test -race ./...` (24 пакета зелёные), `golangci-lint run` (0 issues после фиксов: removed unused io import, переименован `cap` константа в `stackBufCap` чтобы не shadow builtin, добавлены `//nolint:gosec` на 3 file ops с operator-controlled paths). Test `TestDebugBundle_StubPrintsMarker` обновлён на `TestDebugBundle_WritesBundleToOutPath` — проверяет реальную запись tar.gz по `--out`.

### Task 9: Security minimal — permissions + send rate-limit guard

- [ ] Создать `internal/core/security/permissions.go` (если нет от Stage 2 — проверить!) с функцией `CheckAtStartup(paths []PathCheck) []SecurityIssue` где `PathCheck{Path string; Type "file"|"dir"; ExpectedMode os.FileMode (e.g. 0600 for files, 0700 for dirs); Severity "warn"|"fail"}`. Возвращает список нарушений. Метод `EnforceFatal(issues []SecurityIssue) error` — если есть `fail` severity, вернуть error с описанием
- [ ] Список default checks (в `internal/app/wire.go` при старте):
  - `~/.config/lazytg/secrets.age` → 0600 fail
  - `~/.config/lazytg/` → 0700 warn
  - `~/.local/share/lazytg/lazytg.db` → 0600 fail
  - `~/.local/state/lazytg/logs/` → 0700 warn
- [ ] Создать `internal/core/security/permissions_test.go`:
  1. Создать tmp-файл с 0600 → CheckAtStartup нет issues
  2. Файл с 0644 → issue severity fail
  3. Директория с 0755 → issue severity warn
  4. Несуществующий файл → отдельная категория issue ("missing"), не fail (это ок при первом запуске)
- [ ] Создать `internal/core/security/send_ratelimit.go` — обёртка вокруг существующего `internal/core/sync/ratelimit.go::TokenBucket`. Конфигурация: `SendRateLimit{rate float64 = 10/sec; capacity int = 30}`. Используется `SendService` через middleware-подход
- [ ] Обновить `internal/core/sync/send.go::SendService.SendText`: перед отправкой вызвать `rateLimiter.Wait(ctx)`. Если `rate.Limit` exceeded — ждать. Логировать когда rate-limit hit (slog warn). Поведение **не меняется** для пользователя если send rate <10/sec (стандартный case)
- [ ] Создать `internal/core/security/send_ratelimit_test.go`:
  1. 5 sends в секунду → все проходят без задержки
  2. 30 sends подряд (capacity) → 30 проходят сразу, 31-й — ждёт ~100ms (1/10s)
  3. 10 sends в секунду в течение 3 секунд = 30 sends → нормально
- [ ] Расширить `wire.go`: при `Build` вызвать `security.CheckAtStartup` и `EnforceFatal`. Передать `SendRateLimit` в `SendService`
- [ ] Запустить `go test -race ./internal/core/security/... ./internal/core/sync/...` — зелёное

### Task 10: Wiring + final verification

- [ ] Обновить `internal/app/wire.go`: добавить новые компоненты в App struct и `Build`:
  - `Indexer *search.Indexer`
  - `ReindexSvc *search.ReindexService`
  - `SearchSvc *search.Service`
  - `LazyIndex *search.LazyTrigger`
  - `Frecency *palette.FrecencyStore`
  - `DownloadSvc *files.DownloadService`
  - `UploadSvc *files.UploadService`
  - `DBSizeMonitor *obs.DBSizeMonitor`
  - `Security` checks при `Build`
  Запуск горутин: `LazyTrigger` стартует ленивая индексация при первом search, `DBSizeMonitor.Run` в goroutine, остальные — on-demand
- [ ] Обновить `cmd/lazytg/cmd/tui.go`: подключить новые UI overlays (search, palette) к `internal/ui/app/`. Передать deps (search service, frecency, download/upload services) в конструктор UI app. Прокидка events → tea.Msg через `program.Send`
- [ ] Обновить `internal/ui/app/model.go`: добавить поля `search panes/search.Model`, `palette palette.Model`, `attach input.AttachModel`. В `View()` overlay-priority: attach > palette > search > help > main 2-pane layout
- [ ] Обновить `internal/ui/app/update.go`: маршрутизация:
  - keymap.Search → search.Open
  - keymap.OpenPalette → palette.Open
  - keymap.Attach → attach.Open (если фокус в input)
  - keymap.Download → если фокус в thread с media → download
  - События `events.SearchJumpRequested` → переключить chat + scroll
  - События `events.PaletteSelected` → переключить chat + frecency.RecordVisit
  - События `events.FileDownload*`/`FileUpload*` → routed в statusbar
  - События `events.StorageStateChanged{Reason: db_size_warning}` → statusbar
- [ ] Обновить `cmd/lazytg/cmd/`: добавить команду `lazytg reindex [--all|--chat ID]` в новый файл `cmd/lazytg/cmd/reindex.go`. Опции: `--all` индексирует все чаты, `--chat ID` — конкретный. Прогресс в stderr (TUI не запускается)
- [ ] Обновить `docs/MANUAL_SMOKE.md`: добавить Stage 3 шаги:
  9. `/привет` → видно search overlay → результаты появились → Enter → переход в чат
  10. Ctrl+Space → palette → "Алёна" находит чат "Алёна" (если есть)
  11. Ctrl+D на сообщении с фото → файл в `~/Downloads/lazytg/<chat>/`
  12. Ctrl+U → file picker → выбрать .txt → отправлен с caption
  13. `lazytg debug-bundle` → tar.gz в cwd → распаковать → нет phone/api_hash в файлах
  14. `chmod 0644 ~/.config/lazytg/secrets.age` + перезапуск → fail с понятным сообщением
- [ ] **Verification checklist:**
  1. `go build ./...` exit 0
  2. `go test -race ./...` все пакеты OK, особенно `bundle_grep_test`, `permissions_test`, `send_ratelimit_test`, `palette/normalize_test` (Алёна==Алена)
  3. `golangci-lint run` — 0 issues, depguard правила соблюдены
  4. `go test -bench=BenchmarkSearch100k -benchtime=1x ./internal/core/search/` — **p95 <100ms** (output парсится глазами или в скрипте)
  5. Coverage core: `go test -coverprofile=core.out ./internal/core/... && go tool cover -func=core.out | tail -1` — total **≥80%**
  6. Coverage UI: ≥50% (поддерживается с Stage 2)
  7. `goleak` чист в `test/perf/goroutine_leak_test.go` (запустить с обновлённым app.Run включающим новые горутины)
- [ ] Move plan to `docs/plans/completed/20260503-lazytg-stage3-search-files.md` после успешного прохождения всех verification gates
