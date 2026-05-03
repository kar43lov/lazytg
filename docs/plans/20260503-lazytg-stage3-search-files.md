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

- [ ] Создать `internal/core/search/reindex.go` с `ReindexService{indexer *Indexer; bus *events.Bus; log *slog.Logger; perChatLimit int}`. Метод `Run(ctx context.Context, chatIDs []int64) error` — последовательно для каждого chatID вызывает `indexer.Backfill`, эмитит progress events (`ReindexProgress{ChatID, Indexed, Total, Done bool}`) в bus после каждого чата. На `ctx.Done()` — graceful return с error wrap. Reindex всех чатов: метод `RunAll(ctx)` сначала запрашивает `repo.GetChats()` потом вызывает `Run` со всеми ID
- [ ] Создать событие `ReindexProgress{ChatID int64; Indexed int; Total int; Done bool}` в `internal/core/events/events.go`. Добавить `eventMarker()` метод
- [ ] Создать `internal/core/search/lazy.go` с `LazyTrigger{indexer *Indexer; reindex *ReindexService; bus *events.Bus; mu sync.Mutex; triggered bool}`. Метод `EnsureIndexed(ctx) error` — при первом вызове запускает `RunAll` в горутине (если ещё не triggered), возвращает сразу. Идея: первый поисковый запрос триггерит фоновую индексацию, не блокируется
- [ ] Создать `internal/core/search/reindex_test.go`:
  1. RunAll с 3 чатами по 10 сообщений → progress events {1,10,30,false}, {2,10,30,false}, {3,10,30,true}
  2. **Graceful cancel:** запустить RunAll, через 50ms cancel context → возврат с `context.Canceled`, нет panic, ИНДЕКС НЕ В БРОКЕН-СОСТОЯНИИ (проверить через `PRAGMA integrity_check` и `INSERT INTO messages_fts(messages_fts) VALUES('integrity-check')`)
  3. LazyTrigger дважды → второй вызов не запускает RunAll повторно (проверить через mock indexer call counter)
- [ ] Создать `internal/core/search/service.go` с `Service{repo storage.Repo; indexer *Indexer; lazy *LazyTrigger; log *slog.Logger}`. Метод `Search(ctx, query Query, limit int) ([]Hit, error)` где `Query` — структура из Task 3, `Hit{Message domain.Message; Snippet string; ChatID int64; Score float64}`. Запрос: `SELECT m.*, snippet(messages_fts, 0, '<b>', '</b>', '...', 16) AS snippet, bm25(messages_fts) AS score FROM messages m JOIN messages_fts ON messages_fts.rowid = m.rowid WHERE messages_fts MATCH ? ORDER BY bm25(messages_fts) LIMIT ?` (на этой стадии без операторов; операторы в Task 4)
- [ ] **SLA Benchmark.** Создать `internal/core/search/bench_test.go` с `BenchmarkSearch100k`:
  1. Setup: создать БД на `b.TempDir()`, прогнать миграции, вставить **100 000 сообщений** с разнообразным текстом (русский+английский, длина 50-200 символов, рандомный seed для воспроизводимости)
  2. Прогнать `Indexer.Backfill` для всех (или batched по 10000)
  3. Прогнать 100 итераций `Service.Search` с разнообразными query: "привет", "hello world", "тест", "abc def" — собрать latencies
  4. Посчитать p95 (отсортировать, взять `latencies[int(0.95*len(latencies))]`)
  5. **`b.Fatalf` если p95 > 100ms.** Setup в `b.StopTimer()/StartTimer()` чтобы не учитывать
- [ ] Запустить `go test -race ./internal/core/search/...` — зелёное; `go test -bench=BenchmarkSearch100k -benchtime=1x ./internal/core/search/` — p95 <100ms

### Task 3: Search query parser

- [ ] Создать `internal/core/search/query.go` с типом `Query` структурой:
  ```go
  type Query struct {
      Text       string         // основной FTS5 MATCH expression
      From       []string       // from:@user → user IDs/usernames
      InChats    []int64        // in:#chat → chat IDs (resolved)
      Before     *time.Time     // before:DATE
      After      *time.Time     // after:DATE
      HasFile    bool           // has:file → media != null
      Phrases    []string       // "exact phrases"
      Excluded   []string       // -words → NOT in FTS5
      Raw        string         // оригинальная строка для отладки
  }
  ```
- [ ] Создать `internal/core/search/parser.go` с функцией `Parse(input string) (Query, error)`. Логика:
  1. Токенизация: уважать кавычки `"foo bar"` как один токен, `\"` escape, незакрытая кавычка → ошибка с позицией
  2. Для каждого токена: если матчится regex `^([a-z]+):(.+)$` — оператор; иначе обычный поисковый токен
  3. Операторы:
     - `from:@user` или `from:user` — добавить в `Query.From`
     - `in:#chat` или `in:chat` — добавить в `Query.InChats` (как строка, разрешение в Service)
     - `before:YYYY-MM-DD` или `before:YYYY-MM-DDTHH:MM:SS` — `time.Parse`, ошибка с понятным сообщением если формат неверен
     - `after:` — то же
     - `has:file` — `Query.HasFile = true`. Любые другие значения `has:` → ошибка
  4. Токены, начинающиеся с `-` (длиной ≥2) → `Query.Excluded`
  5. Токены, обёрнутые в `"..."` → `Query.Phrases`
  6. Остальные → склеить через пробел в `Query.Text` (FTS5 MATCH использует AND)
- [ ] Создать `internal/core/search/parser_test.go` с табличными тестами:
  1. `"hello"` → `Query{Text: "hello", Raw: ...}`
  2. `"hello world"` → `Query{Text: "hello world"}` (FTS5 интерпретирует как AND)
  3. `"\"hello world\""` → `Query{Phrases: ["hello world"]}`
  4. `"from:@alice hello"` → `Query{From: ["alice"], Text: "hello"}`
  5. `"from:@alice from:@bob hello"` → `Query{From: ["alice","bob"], Text: "hello"}`
  6. `"before:2025-12-01 after:2025-11-01 спам"` → даты + Text
  7. `"has:file видео"` → `Query{HasFile: true, Text: "видео"}`
  8. `"-spam важное"` → `Query{Excluded: ["spam"], Text: "важное"}`
  9. **Edge cases:** пустая строка → ошибка "empty query"; `"before:invalid"` → ошибка с позицией; незакрытая кавычка `"foo` → ошибка
- [ ] Создать `internal/core/search/query_builder.go` с функцией `BuildSQL(q Query) (sqlText string, ftsMatch string, args []any, err error)`. Конвертирует Query в:
  - FTS5 MATCH expression: `Text` + `Phrases` (через `"..." `) + `NOT (excluded1 OR excluded2)`
  - SQL WHERE clauses для From/InChats/Before/After/HasFile (присоединяется к JOIN с peers/users)
  - Параметры через `?` placeholders
- [ ] Тесты на `BuildSQL` — табличные: для каждого Query проверить полученный sqlText и args
- [ ] Обновить `Service.Search` (из Task 2) чтобы использовать `BuildSQL` для составления запроса
- [ ] Запустить `go test -race ./internal/core/search/...` — зелёное

### Task 4: Search service jump-to-message + Search UI overlay

- [ ] Расширить `internal/core/search/service.go`: метод `JumpContext(ctx, hit Hit, around int) ([]domain.Message, int, error)` где `around` = 5. Возвращает 11 сообщений (5 до + сам + 5 после) из того же чата + индекс target в slice. Используется UI для прокрутки к нужному сообщению с контекстом
- [ ] Добавить событие `SearchJumpRequested{ChatID int64; MessageID int64}` в `internal/core/events/events.go` — UI app слушает и переключает chat + scroll thread pane
- [ ] Создать `internal/ui/panes/search/messages.go` с TUI-сообщениями:
  - `SearchOpenedMsg{}`
  - `SearchClosedMsg{}`
  - `SearchQueryChangedMsg{Query string}`
  - `SearchResultsMsg{Hits []search.Hit; Err error}`
  - `SearchJumpMsg{Hit search.Hit}`
- [ ] Создать `internal/ui/panes/search/model.go` с `Model{input textinput.Model; results list.Model; service SearchServiceInterface; debounce time.Duration; lastQuery string; loading bool; err error}` где `SearchServiceInterface{Search(ctx, raw string, limit int) ([]search.Hit, error)}`. Конструктор `New(service, debounce time.Duration) Model` (default debounce 150ms)
- [ ] Создать `internal/ui/panes/search/update.go`:
  - `tea.KeyMsg`:
    - Esc → `SearchClosedMsg`
    - Enter (на выбранном результате) → `SearchJumpMsg{Hit}` → app конвертирует в `events.SearchJumpRequested`
    - Up/Down/PgUp/PgDn → делегировать в `m.results.Update`
    - Иначе → делегировать в `m.input.Update`
  - После input изменения: установить debounce-таймер через `tea.Tick(150ms, ...)`. Если за это время input не менялся — эмитировать `SearchQueryChangedMsg`
  - `SearchQueryChangedMsg{Query}` → если Query пустой — очистить results, иначе → `tea.Cmd` который вызывает `service.Search(ctx, query, 50)` и возвращает `SearchResultsMsg`
  - `SearchResultsMsg` → обновить `m.results.SetItems(...)`, `m.loading = false`
- [ ] Создать `internal/ui/panes/search/view.go` — overlay в центре экрана: lipgloss border, top-3 строки input, ниже список results. Каждый результат: chat title + snippet (с `<b>` метками заменёнными на ANSI bold) + дата
- [ ] Подключить overlay в `internal/ui/app/`: keymap binding `Search` (default `/`), при активации `m.search.Visible = true`, focus в search overlay (приоритет над panes). При `SearchJumpMsg` → закрыть overlay, эмитировать `events.SearchJumpRequested` через app.bus, thread pane подпишется и сделает `OpenChat(ChatID) → ScrollTo(MessageID, around=5)`
- [ ] Добавить метод `ScrollTo(messageID int64, around int)` в `internal/ui/panes/thread/model.go` — загружает контекст через `service.JumpContext` (или прямо через repo), устанавливает viewport scroll так чтобы target был видим (5 строк сверху, 5 снизу)
- [ ] Создать `internal/ui/panes/search/model_test.go` через teatest:
  1. Init → input пустой, results пустые
  2. KeyMsg "h", "i" → debounce-таймер взведён
  3. После 150ms → `SearchQueryChangedMsg{Query: "hi"}` → service.Search вызван
  4. Вернуть `SearchResultsMsg{Hits: [...]}` → results содержат hits
  5. KeyMsg Down + Enter → `SearchJumpMsg{Hit}` эмитирован
  6. KeyMsg Esc → `SearchClosedMsg`
- [ ] Создать `internal/ui/panes/thread/scroll_test.go`: моки repo с 100 сообщениями, `ScrollTo(messageID=50, around=5)` → viewport показывает сообщения 45-55, target 50 в фокусе
- [ ] Запустить `go test -race ./internal/ui/...` — зелёное

### Task 5: Frecency store + Командная палитра L1 (Ctrl-Space) + Unicode-fuzzy

- [ ] Добавить миграцию `internal/storage/sqlite/migrations/0006_frecency.sql`:
  ```sql
  CREATE TABLE IF NOT EXISTS chat_frecency (
      chat_id INTEGER PRIMARY KEY REFERENCES chats(id),
      visit_count INTEGER NOT NULL DEFAULT 0,
      last_visit INTEGER NOT NULL DEFAULT 0,
      score REAL NOT NULL DEFAULT 0
  );
  CREATE INDEX IF NOT EXISTS idx_chat_frecency_score ON chat_frecency(score DESC);
  ```
- [ ] Добавить в `internal/storage/sqlite/repo.go`: методы `RecordVisit(ctx, chatID int64, now time.Time) error` (UPSERT с обновлением `visit_count++`, `last_visit=now`, `score=calc`) и `TopFrecency(ctx, limit int) ([]int64, error)` (SELECT top N chat_id ORDER BY score DESC). Calculation формулы score: `frequency * recency_decay`, `recency_decay = exp(-age_days / 30)` (через cast в float). Обновлять score только в `RecordVisit` (не пересчитывать на каждый запрос)
- [ ] Создать `internal/ui/palette/frecency.go` с типом `FrecencyStore` (интерфейс — `RecordVisit(ctx, chatID) error`, `Top(ctx, limit int) ([]int64, error)`) и реализацией поверх repo. Hot-set ограничение: limit=200 (защита от O(N log N) на 5000+ чатов)
- [ ] Добавить зависимости: `go get github.com/sahilm/fuzzy`, `go get golang.org/x/text/unicode/norm`
- [ ] Создать `internal/ui/palette/normalize.go` с функцией `Normalize(s string) string`:
  1. NFKD нормализация (`norm.NFKD.String(s)`)
  2. Drop diacritics: пройти по runes, отбросить те, у которых `unicode.Is(unicode.Mn, r)` (Mark, nonspacing — это диакритики)
  3. Lowercase (`strings.ToLower`)
  4. Возвратить
- [ ] Создать `internal/ui/palette/normalize_test.go` с табличными тестами:
  1. `"Алёна"` → `"алена"` (ё → е после NFKD без диакритик? — ВАЖНО: ё в NFKD это `е` + combining diaeresis U+0308; drop Mn убирает диакритику, остаётся "е")
  2. `"Café"` → `"cafe"`
  3. `"naïve"` → `"naive"`
  4. `"Hello"` → `"hello"`
  5. ASCII неизменён
  6. Emoji 🚀 — должно остаться как есть (это не Mn)
- [ ] Создать `internal/ui/palette/messages.go` с TUI-сообщениями: `PaletteOpenedMsg`, `PaletteClosedMsg`, `PaletteQueryChangedMsg{Query}`, `PaletteSelectedMsg{ChatID int64}`
- [ ] Создать `internal/ui/palette/model.go` с `Model{input textinput.Model; items []PaletteItem; filtered []int; frecency FrecencyStore; chats []domain.Chat; visible bool}` где `PaletteItem{ChatID int64; Title string; NormalizedTitle string}`. Конструктор `New(frecency FrecencyStore, repo storage.Repo) Model`. Метод `Open(ctx) tea.Cmd` — загружает top-200 chats через FrecencyStore + дополняет недостающими из repo (если frecency пустая — покажем недавние по last_message_date)
- [ ] Создать `internal/ui/palette/fuzzy.go` с функцией `Match(query string, items []PaletteItem) []int` (возвращает индексы matched items в порядке убывания score). Внутри:
  1. Normalize(query)
  2. Использовать `sahilm/fuzzy.Find(normalizedQuery, normalizedTitles)` где `normalizedTitles` = `[]string{item.NormalizedTitle for item in items}`
  3. Вернуть индексы matched (`fuzzy.Match.Index`)
- [ ] Создать `internal/ui/palette/update.go`:
  - KeyMsg Esc → `PaletteClosedMsg`
  - KeyMsg Enter → `PaletteSelectedMsg{ChatID: m.items[m.filtered[m.cursor]].ChatID}`
  - KeyMsg Up/Down → cursor двигается
  - Иначе → input.Update; после изменения query → `m.filtered = Match(input.Value(), m.items)`, cursor=0
- [ ] Создать `internal/ui/palette/view.go` — overlay по центру: input строка + список filtered items, текущий highlighted
- [ ] Подключить в `internal/ui/app/`: keymap binding `OpenPalette` (default `Ctrl-Space`), при `PaletteSelectedMsg` → переключить chat (как при Enter в chats pane) + вызвать `frecency.RecordVisit`
- [ ] Создать `internal/ui/palette/model_test.go`:
  1. Open → загружены top-50 chats через frecency
  2. Query "ал" в items с titles ["Алёна", "Алексей", "Боб"] → filtered содержит индексы Алёна (0) и Алексей (1) (нормализованные titles "алена", "алексей" оба начинаются на "ал")
  3. **Unicode test:** items с title "Алёна", query "Алена" → найден (после Normalize обе строки = "алена")
  4. KeyMsg Down + Enter → PaletteSelectedMsg с правильным ChatID
- [ ] Создать `internal/ui/palette/frecency_test.go`:
  1. Пустой repo → Top возвращает []
  2. RecordVisit для chat 1, потом для chat 2 (через час), Top(2) → [chat2, chat1] (recency)
  3. RecordVisit для chat 1 трижды, для chat 2 один раз → Top(2) → [chat1, chat2] (frequency)
- [ ] Запустить `go test -race ./internal/ui/palette/...` — зелёное

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
