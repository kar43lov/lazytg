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

- [ ] Создать `internal/tg/files.go` с типом `Downloader{client *telegram.Client; log *slog.Logger}`. Метод `Download(ctx, location FileLocation, w io.Writer) (size int64, err error)` где `FileLocation{ID int64; AccessHash int64; Reference []byte; DC int}`. Использует `gotd downloader.NewDownloader().WithPartSize(...).Download(...)`. Прогресс через `progress.Reader` или callback (зависит от gotd API — изучить)
- [ ] Создать `internal/core/files/store.go` с `FileStore{root string; log *slog.Logger}` где `root = ~/Downloads/lazytg` (configurable через config). Метод `Path(chatTitle, filename string) string` — возвращает `<root>/<sanitized_chat_title>/<filename>` (sanitize: replace `/`, `\`, `..` на `_`). Метод `Exists(path) bool`. Метод `EnsureDir(path) error` (mkdirall с `0700`)
- [ ] Создать `internal/core/files/dedup.go` с `DedupCache` — мап `file_id -> path` в БД. Добавить миграцию `0007_files.sql`:
  ```sql
  CREATE TABLE IF NOT EXISTS downloaded_files (
      file_id INTEGER PRIMARY KEY,
      access_hash INTEGER,
      path TEXT NOT NULL,
      size INTEGER,
      downloaded_at INTEGER NOT NULL
  );
  ```
  Методы repo: `GetDownloadedPath(ctx, fileID) (string, bool, error)`, `SaveDownloadedFile(ctx, fileID, accessHash, path, size) error`
- [ ] Создать `internal/core/files/download.go` с `DownloadService{tg DownloaderInterface; store *FileStore; repo storage.Repo; bus *events.Bus; log *slog.Logger}` где `DownloaderInterface` определён в `core/files/` (без зависимости от gotd). Метод `Download(ctx, fileID int64, accessHash int64, ref []byte, dc int, chatTitle, filename string) (path string, err error)`:
  1. Если уже скачан (`repo.GetDownloadedPath`) — symlink или копия + return path сразу
  2. EnsureDir + создать tmp-файл `<path>.partial`
  3. Эмитить `FileDownloadStartedMsg{FileID, Path, Size}` в bus
  4. Прогресс: callback из gotd → эмитить `FileDownloadProgressMsg{FileID, BytesDownloaded, TotalBytes}` каждые 5% или 1MB
  5. По завершении: rename `.partial` → final, `repo.SaveDownloadedFile`, эмитить `FileDownloadCompletedMsg{FileID, Path}`
  6. На ошибке: удалить `.partial`, эмитить `FileDownloadFailedMsg{FileID, Err}`
- [ ] Добавить события в `internal/core/events/events.go`: `FileDownloadStartedMsg`, `FileDownloadProgressMsg`, `FileDownloadCompletedMsg`, `FileDownloadFailedMsg` (все с `eventMarker()`)
- [ ] Добавить hotkey в keymap defaults: `Download` = `Ctrl+D`. В thread pane на KeyMsg Download → если выбранное сообщение содержит media → эмитировать `tea.Cmd` который вызовет `DownloadService.Download`
- [ ] Расширить `domain.Message` полем `Media *MediaInfo` где `MediaInfo{FileID int64; AccessHash int64; Reference []byte; DC int; Filename string; Size int64; MimeType string}`. Обновить storage layer: добавить колонки в `messages` (миграция `0008_messages_media.sql`). Парсить в `tg/history.go` и `tg/updates.go` из gotd `MessageMediaDocument`/`MessageMediaPhoto`
- [ ] Обновить `internal/ui/panes/thread/format.go`: при `Media != nil` показывать `[📎 filename, 234 KB]` (или `[🖼 photo.jpg]` для photo)
- [ ] Обновить `internal/ui/statusbar/model.go`: добавить поле `downloads map[int64]downloadProgress` где `downloadProgress{percent int; filename string}`. Подписан на `FileDownload*` events. Рендер: при наличии активных загрузок — `⬇ filename 47%` вместо обычного состояния
- [ ] Создать `internal/core/files/download_test.go`:
  1. Mock DownloaderInterface — возвращает фиксированный bytes.Reader
  2. Скачивание → файл создан, `SaveDownloadedFile` вызван, события `Started/Progress/Completed` эмитированы в правильном порядке
  3. Дубль того же fileID → возвращает existing path без повторного скачивания
  4. Ошибка скачивания → `.partial` удалён, событие `Failed`
  5. `chatTitle` с `/` → sanitized
- [ ] Создать `internal/tg/files_test.go` через tgtest: симуляция upload.GetFile → Download собирает байты в writer, прогресс callback вызван
- [ ] Документировать в `docs/SEARCH.md` (или создать `docs/FILES.md`): non-goal v0.1 — нет resume для прерванных загрузок (придётся скачивать с нуля). Будет в v0.3
- [ ] Запустить `go test -race ./internal/tg/... ./internal/core/files/...` — зелёное

### Task 7: Files: upload (Ctrl-U)

- [ ] Расширить `internal/tg/files.go` с `Uploader{client *telegram.Client; log *slog.Logger}`. Метод `Upload(ctx, path string, progress func(uploaded, total int64)) (inputFile tg.InputFileClass, err error)`. Использует `gotd uploader.NewUploader().FromPath(...)` или аналог
- [ ] Расширить `internal/core/files/upload.go` с `UploadService{tg UploaderInterface; sender SendServiceInterface; bus *events.Bus; log *slog.Logger}`. Метод `SendFile(ctx, chatID int64, path string, caption string) (localID string, err error)`:
  1. Проверить размер файла. Warning >50MB (эмитить событие `FileUploadWarning{Size, Threshold}`). Hard limit 2GB → ошибка `ErrFileTooLarge`
  2. Эмитить `FileUploadStartedMsg{Path, Size}`
  3. Upload через `tg.Upload` с прогресс callback → `FileUploadProgressMsg`
  4. Использовать `sender.SendMedia(...)` (новый метод) с полученным inputFile + caption + chatID
  5. Эмитить `FileUploadCompletedMsg` или `FileUploadFailedMsg`
- [ ] Расширить `internal/tg/send.go`: метод `SendMedia(ctx, chatID int64, file tg.InputFileClass, caption string, replyTo int) (messageID int, err error)` через `messages.SendMedia(InputMediaUploadedDocument)` (или Photo если расширение image-like)
- [ ] Добавить события: `FileUploadStartedMsg`, `FileUploadProgressMsg`, `FileUploadCompletedMsg`, `FileUploadFailedMsg`, `FileUploadWarning`
- [ ] Создать `internal/ui/input/attach.go` с `AttachModel{filepicker filepicker.Model; visible bool; chatID int64; uploadService UploadServiceInterface}`. Конструктор `NewAttach(...)`. Метод `Open(chatID) tea.Cmd` — открывает file picker (из `bubbles/filepicker`)
- [ ] Update в attach: `tea.KeyMsg` Esc → close, Enter на selected file → `uploadService.SendFile(...)` через tea.Cmd, close attach
- [ ] Подключить в `internal/ui/input/`: keymap `Attach` = `Ctrl+U`, при KeyMsg Attach → открыть AttachModel overlay
- [ ] Обновить statusbar: подписан на `FileUpload*` events, показывает `⬆ filename 73%` при активной загрузке
- [ ] Создать `internal/core/files/upload_test.go`:
  1. Mock Uploader — записать вызов с path
  2. SendFile с файлом 100 KB → upload вызван, sender.SendMedia вызван с InputFile, события Started/Progress/Completed
  3. Файл 60 MB → событие Warning + продолжается upload
  4. Файл 2.5 GB → ErrFileTooLarge, upload не вызван
  5. Несуществующий путь → ошибка с понятным сообщением
- [ ] Создать `internal/tg/files_upload_test.go` через tgtest: симуляция upload.SaveFilePart → Upload собирает байты, callback вызван, возвращает InputFile
- [ ] Запустить `go test -race ./internal/tg/... ./internal/core/files/...` — зелёное

### Task 8: DB size monitoring + полный debug-bundle + grep-test

- [ ] Создать `internal/core/obs/dbsize.go` с `DBSizeMonitor{repo storage.Repo; bus *events.Bus; threshold int64; interval time.Duration; log *slog.Logger}`. Конструктор `New(repo, bus, log)` с defaults: threshold=1GB, interval=60s. Метод `Run(ctx)` — каждые `interval` стат БД-файла (через `os.Stat` + path из repo), если размер > threshold — эмитить `StorageStateChanged{Mode: "rw", Reason: "db_size_warning", DBSizeMB: int}` (расширить Reason полями для ясности)
- [ ] Расширить event `StorageStateChanged` в events/events.go: добавить поле `DBSizeMB int` (опциональное, 0 если N/A)
- [ ] Добавить метод `repo.DBPath() string` для получения текущего файла БД
- [ ] Обновить `internal/ui/statusbar/model.go`: при получении `StorageStateChanged{Reason: "db_size_warning", DBSizeMB > 1024}` показать индикатор `⚠ DB 1.4 GB` рядом с storage mode
- [ ] Создать `docs/SEARCH.md`:
  - Описание query syntax (операторы from/in/before/after/has, phrases, exclusions)
  - DB size guidance: «trigram tokenizer даёт overhead 3-5× от текста сообщений. На 100k сообщений ожидайте ~50-200 MB FTS-индекса. На 1M — ~500MB-1.5GB. Default cap: последние 5000 сообщений на чат. Для глубокой истории — `lazytg reindex --all`.»
  - SLA: p95 <100ms на 100k сообщений (бенчмарком гарантировано в CI)
  - Limitations: нет stemming для русского (трёхграммный токенизатор работает language-agnostic, но "сообщение" ≠ "сообщения" в exact-match — нужно искать оба или использовать prefix-search)
- [ ] Расширить `internal/core/obs/bundle.go` (полная реализация — заменить stub из Stage 1):
  ```go
  type Bundle struct{ Logger *slog.Logger; Cfg ConfigSource; Version VersionInfo; Repo storage.Repo; LogPath string }
  func (b *Bundle) Create(ctx, outPath string) (string, error)
  ```
  Tar.gz содержимое:
  - `version.txt` — version + commit + date + Go version + OS/arch
  - `config.toml` — config с **redaction** (через redact.go обработать значения)
  - `logs.txt` — последние 1000 строк из `LogPath` (lumberjack rotated logs)
  - `db_stats.txt` — `db_size_bytes`, `messages_count`, `chats_count`, `accounts_count`, `schema_version`, `fts_index_size_bytes`
  - `goroutines.txt` — `runtime.Stack(buf, true)` для дебага
  - **НЕ ВКЛЮЧАЕТ:** session-files (`*.session`, `secrets.age`), api_hash (env), phone, текст сообщений, имена чатов
- [ ] Реализовать команду `cmd/lazytg/cmd/debug.go` (заменить stub): `debug-bundle [--out <path>]`. Default out = `lazytg-bundle-<timestamp>.tar.gz` в cwd. После создания печатает путь
- [ ] **Grep-тест безопасности.** Создать `internal/core/obs/bundle_grep_test.go`:
  1. Setup: создать БД с 5 messages (тексты: "secret api_hash test", "session content", "+79991234567"), config с api_hash="abc123def", session-файл с содержимым "session-bytes-here"
  2. Создать bundle через `Bundle.Create`
  3. Распаковать tar.gz в tmp dir
  4. Для каждого файла в bundle: прочитать содержимое, ассертить что НЕ содержит:
     - `api_hash="abc123def"` (значение)
     - `session-bytes-here`
     - `+79991234567` (тестовый телефон в сообщении)
     - Текст "secret api_hash test"
     - Текст "session content"
  5. **Если найдено что-то из списка → t.Fatalf**
- [ ] Создать `internal/core/obs/dbsize_test.go`:
  1. Маленькая БД (<1GB) → события не эмитятся
  2. Симулировать большую БД (mock os.Stat) → событие `StorageStateChanged{Reason: "db_size_warning"}` эмитится
  3. Threshold configurable
- [ ] Запустить `go test -race ./internal/core/obs/... ./cmd/lazytg/cmd/...` — зелёное

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
