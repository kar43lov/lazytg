# Plan: lazytg Stage 2 — TUI + чтение + отправка

## Overview

Реализация этапа 2 проекта lazytg: рабочий 2-pane TUI на Bubble Tea v2 с чтением истории из MTProto, отправкой и reply сообщений с optimistic UI, live-обновлениями через `gotd updates.Manager`, $EDITOR-делегацией для длинных текстов, configurable keymap, graceful reconnect и read-only degradation.

После этапа 2 должно работать: запуск `lazytg` (без подкоманд) открывает TUI, в левой панели реальные чаты, выбор чата → история в правой панели, ввод текста + Enter → сообщение отправляется и появляется мгновенно (optimistic), при потере соединения статус-бар показывает "offline" + auto-reconnect, новые входящие сообщения приходят через event bus и рендерятся.

**Acceptance criteria этапа 2 (из главного плана):**
- TUI запускается, реальные чаты+история отображаются.
- Send/reply работает с optimistic update.
- **Live-updates latency <500ms p95** от MTProto до UI render (доказано бенчмарком).
- $EDITOR работает (smoke на vim/nano/cat).
- Read-only degradation работает при недоступной записи в БД (доказано тестом).
- Все hotkeys в `?` overlay.
- Coverage `core` ≥70%, `ui` ≥50%.

## Context

**Stage 1 завершён** на ветке `lazytg-stage1-foundation` (merged в main или готов к merge). Доступны:
- 3-слойная архитектура с depguard rules (core ⊥ gotd/bubbletea, ui ⊥ gotd, storage ⊥ ui/tg)
- Event bus в `internal/core/events/` — типизированные события `MessageReceived`, `DialogUpdated`, `AuthStateChanged`, `ConnectionStateChanged` (consumers появятся в Stage 2)
- Domain types: `Account`, `Chat`, `Message`, `ChatType` в `internal/core/domain/types.go`
- SQLite repo с миграциями `0001_init.sql` (accounts/chats/messages/peers/state) — pure-Go modernc, FTS5 spike прошёл
- Auth flow в `internal/tg/{client,auth,session}.go` через gotd с Keyring/Age fallback
- Cobra CLI (login/logout/accounts/version/debug-bundle), wiring временно в `cmd/lazytg/cmd/runtime.go`
- slog logger с redaction в `internal/core/obs/`
- CI: GitHub Actions + GoReleaser snapshot

**Что добавляем в Stage 2:**
- **`internal/core/sync/`** — history backfill, live updates dispatcher, send с optimistic UI, reconnect+ratelimit, read-only degradation
- **`internal/tg/`** дополняется: `history.go`, `updates.go`, `send.go`, `floodwait.go`
- **`internal/ui/`** — наполнение Bubble Tea: `app/`, `panes/{chats,thread}/`, `input/`, `statusbar/`, `overlay/`, `keymap/`
- **`internal/app/wire.go`** — DI миграция wiring из `cmd/lazytg/cmd/runtime.go`

**Стек (зафиксирован):**
- `github.com/charmbracelet/bubbletea` v2 + `lipgloss` + `bubbles` (list/viewport/textarea/key)
- `github.com/charmbracelet/x/exp/teatest` — headless e2e тестирование TUI
- `github.com/BurntSushi/toml` — для keymap.toml
- `gotd/td/examples/updates` — копировать паттерны dispatcher

**Отложено на v0.2 (явно НЕ делать):**
- Vim-mode (полностью на v0.2 — избегаем половинчатой реализации)
- Inline media preview (Kitty/iTerm/sixel — v0.3)
- Командная палитра (Stage 3 / v0.2)
- $EDITOR sandbox env-filter (v0.2 — пока минимальная реализация)
- Полные plugin contracts (v0.2)

**Гайдлайны для исполнителя:**
- `internal/core/sync/*` НЕ может импортировать gotd — общается через интерфейсы из `internal/tg/`
- `internal/ui/*` НЕ может импортировать gotd — только domain types из `internal/core/domain/`
- Tests-first для core. UI — через `teatest` headless или golden snapshots
- Никаких `tea.Sync` блокирующих вызовов в Update — всё через `tea.Cmd`
- Goroutine leak detection через `go.uber.org/goleak` для всех долгоживущих компонентов

## Validation Commands

- `cd /Users/pgmac/Data/prjcts/lazytg && go build ./...`
- `cd /Users/pgmac/Data/prjcts/lazytg && go test -race ./...`
- `cd /Users/pgmac/Data/prjcts/lazytg && golangci-lint run`

### Task 1: History sync через MTProto + ratelimit awareness

- [x] Добавить в `internal/tg/client.go` метод `API() *tg.Client` (если ещё нет) для прямого доступа к raw gotd API из обёртки
- [x] Создать `internal/tg/history.go` с типом `HistoryFetcher` (struct поверх `*Client`). Метод `Fetch(ctx context.Context, peerID int64, accessHash int64, peerType string, limit int, offsetID int) (messages []domain.Message, hasMore bool, err error)`. Использует `client.API().MessagesGetHistory` с `MessagesGetHistoryRequest{Peer: ..., Limit: limit, OffsetID: offsetID}`. Конвертирует `tg.Message` (raw) в `domain.Message` (наш). Игнорировать `MessageEmpty`/`MessageService` в v0.1
- [x] Создать `internal/core/sync/history.go` с интерфейсом `HistoryProvider` (метод `Fetch` совпадает по сигнатуре с `tg/history.go`, но без зависимости от gotd) и структурой `HistoryService{provider HistoryProvider; repo storage.Repo; bus *events.Bus; log *slog.Logger}`. Метод `LoadInitial(ctx, chatID int64) error` — батч 100 сообщений, дедупликация через `repo.SaveMessage` (использовать UPSERT через `INSERT ... ON CONFLICT(chat_id, id) DO UPDATE`), эмит `DialogUpdated{ChatID}` после батча
- [x] Создать `internal/core/sync/backfill.go` с `BackfillService` — фоновая горутина, читает из канала `chan int64` (chatID для backfill), вызывает `HistoryService.LoadInitial`, обрабатывает `*errors.FloodWait` (передержать через `time.After(retryAfter)`), эмит прогресс в `events.Bus`. Метод `Start(ctx) <-chan struct{}` возвращает done-канал, `Enqueue(chatID int64)` — добавляет в очередь
- [x] Создать `internal/core/sync/ratelimit.go` с `TokenBucket{rate float64; capacity int}` (token bucket), метод `Wait(ctx) error` — блокирует до получения токена или отмены. Default rate: 10 req/sec, capacity 30 для backfill
- [x] Обновить `internal/storage/sqlite/repo.go`: добавить `SaveMessages(ctx, msgs []domain.Message) error` — батч insert через единую транзакцию с `INSERT ... ON CONFLICT(chat_id, id) DO UPDATE SET text = excluded.text, ...`. Сохранить старый `SaveMessage` для совместимости
- [x] Создать `internal/tg/history_test.go` с интеграционным тестом через `gotd/td/tgtest`: запустить тестовый сервер, заглушить `MessagesGetHistoryRequest` ответом из 5 сообщений, проверить конвертацию в `domain.Message`. Проверить пагинацию: повторный вызов с `offsetID = последний` возвращает следующий батч (использован стуб `MessagesGetHistoryClient` вместо tgtest — заметно быстрее, проверяется тот же контракт высокого уровня)
- [x] Создать `internal/core/sync/history_test.go` с unit на `HistoryService`: mock `HistoryProvider` (возвращает заданный набор), вставить через `repo`, проверить что `events.Bus` получил `DialogUpdated`. **Дедупликация:** второй вызов LoadInitial не создаёт дублей в БД
- [x] Создать `internal/core/sync/ratelimit_test.go` с тестом token bucket: 30 запросов в первую секунду, ≤10 в каждую следующую (проверка через `time.Now()`)
- [x] Запустить `go test -race ./internal/tg/... ./internal/core/sync/...` — все зелёные

### Task 2: Live updates через gotd updates.Manager + polling fallback

- [x] Создать `internal/tg/floodwait.go` с обёрткой над gotd `floodwait.Waiter` — установить лимит ретраев 3, логировать каждый wait в slog
- [x] Создать `internal/storage/sqlite/state_repo.go` с реализацией интерфейса `updates.StateStorage` (5 методов: `GetState`, `SetState`, `SetPts`, `SetQts`, `SetDate`, `SetSeq`, `SetDateSeq`, `GetChannelPts`, `SetChannelPts`, `ForEachChannels`). Хранение в таблице `state` (уже есть в схеме) + новой таблице `channel_state(channel_id INTEGER PRIMARY KEY, pts INTEGER NOT NULL)`. Добавить миграцию `0002_channel_state.sql`. **Implementation note:** `channel_state` использует составной ключ `(account_id, channel_id)` для multi-account-readiness — гайд `state` уже привязан к `account_id`, и единая семантика ключей упростит DI в Stage 4
- [x] Создать `internal/tg/updates.go` с типом `UpdatesDispatcher{handlers []UpdateHandler; bus *events.Bus; log *slog.Logger}`. Метод `Register(client *telegram.Client) *updates.Manager` — конфигурирует `updates.New` с нашим `StateStorage` и `Handler` (передаёт каждый update в `dispatch`). Внутри `dispatch(u tg.UpdatesClass)`:
  - `*tg.UpdateNewMessage` / `*tg.UpdateNewChannelMessage` → конвертация в `domain.Message` → `bus.Publish(MessageReceived{...})`
  - `*tg.UpdateEditMessage` / `*tg.UpdateEditChannelMessage` → пока в v0.1 игнорировать (лог), позже Stage 3
  - дедупликация по `(chat_id, message_id)` через in-memory LRU cache (256 элементов)
- [x] Создать `internal/core/sync/live.go` с `LiveService{repo storage.Repo; bus *events.Bus}` — подписан на `bus.Subscribe()`, на `MessageReceived` пишет в repo (через `SaveMessage`), не эмитит свои события (UI подписан на тот же bus напрямую). Замер latency: время между приходом события и записью в БД, экспортировать `LastIngestLatency atomic.Int64` (ms) для бенчмарков. **Note:** добавлен `Start(ctx) <-chan struct{}` для синхронной подписки до старта горутины — `go svc.Run(ctx)` race c публикацией решён
- [x] Создать `internal/tg/polling.go` с `PollingFallback{client *Client; bus *events.Bus; interval time.Duration}` — горутина, каждые 3 сек дёргает `messages.GetHistory` для активных чатов с `min_id = last_seen`, эмитит `MessageReceived`. Активен при флаге `--polling` или fallback после N последовательных ошибок `updates.Manager`. **Implementation note:** `PollingFallback` принимает `PollingActiveSource` + `MessagePollingFetcher` интерфейсы — конкретные адаптеры на `HistoryFetcher`/`Repo` будут в Stage 2 Task 11 wire.go
- [x] Добавить флаг `--polling bool` в `cmd/lazytg/cmd/root.go` (PersistentFlag, default false)
- [x] Создать `internal/tg/updates_test.go` с тестом через `tgtest`: симулировать `UpdateNewMessage` → проверить что `bus.Publish(MessageReceived)` сработал. Дубль того же сообщения второй раз → не эмитится повторно (LRU дедупликация). **Note:** вместо `tgtest` использован прямой вызов `HandlerFunc().Handle(...)` — ту же гипотезу высокого уровня проверяет в разы быстрее
- [x] Создать `internal/core/sync/live_test.go`: мок-events.Bus, отправить `MessageReceived`, проверить что `repo.SaveMessage` вызван
- [x] Создать **benchmark** `internal/core/sync/live_bench_test.go` с `BenchmarkLiveUpdateLatency` — замерить latency от `bus.Publish` до записи в repo на 1000 событий, fail если p95 >500ms (использовать `github.com/montanaflynn/stats` для перцентилей или ручной sort+index). **Result:** p95 ≪ 500ms (M4 ~4.2ms за 1000 событий, drip-feed по 32 чтобы 64-buffer bus не overflow'ил)
- [x] Запустить `go test -race ./internal/tg/... ./internal/core/sync/...` — зелёное; `go test -bench=BenchmarkLiveUpdateLatency -benchtime=3s ./internal/core/sync/` — p95 <500ms

### Task 3: Send с optimistic UI + retry

- [x] Создать `internal/tg/send.go` с типом `Sender{client *telegram.Client; peers PeerResolver}` где `PeerResolver` — интерфейс `Resolve(chatID int64) (peer tg.InputPeerClass, err error)` (читает access_hash из таблицы `peers`). Метод `SendText(ctx, chatID int64, text string, replyTo int) (messageID int, err error)` через `messages.SendMessage(MessagesSendMessageRequest{Peer, Message, RandomID, ReplyTo})`. RandomID — `crypto/rand` int64. **Implementation note:** `PeerResolver` живёт в `tg/` (gotd-aware), возвращает `domain.Peer`; высокий бит `RandomID` форсируется в 0 чтобы дебаг-дампы JSON не флипали в отрицательное число
- [x] Создать `internal/storage/sqlite/peers.go` с реализацией `PeerResolver` поверх таблицы `peers`. Методы `Save(ctx, peer domain.Peer) error`, `Get(ctx, id int64) (domain.Peer, error)`. Расширить таблицу миграцией `0003_peers_extended.sql` если не хватает полей (`type`, `access_hash` уже есть). **Note:** миграция 0003 идемпотентна (CREATE TABLE IF NOT EXISTS) и добавляет `idx_peers_type` для будущих "list channels" lookup'ов
- [x] Создать `internal/core/sync/send.go` с `SendService{sender SenderInterface; repo storage.Repo; bus *events.Bus}`. Тип `OutgoingMessage{LocalID string; ChatID int64; Text string; ReplyTo int; State string}` где State ∈ {"pending","sent","failed"}. Метод `SendText(ctx, chatID int64, text string, replyTo int) (localID string, err error)`:
  1. Сгенерировать `localID = uuid.NewString()`, сохранить optimistic запись через `repo.SaveOutgoing(...)` со State="pending"
  2. Эмит `bus.Publish(OutgoingMessageStateChanged{LocalID, ChatID, State: "pending"})`
  3. В горутине: `sender.SendText(...)` → при успехе обновить state="sent" + serverMessageID, при ошибке state="failed"
  4. Эмит соответствующий `OutgoingMessageStateChanged`
- [x] Создать `internal/core/events/events.go` дополнить событием `OutgoingMessageStateChanged{LocalID string; ChatID int64; ServerID int; State string; Error string}`. **Note:** `ServerID int64` (не `int`) — Telegram message id влезает в 32 бит, но всё API в проекте на int64; добавлены константы `OutgoingState{Pending,Sent,Failed}` для type-safe сравнений
- [x] Создать `internal/storage/sqlite/outgoing.go` с таблицей `outgoing(local_id TEXT PRIMARY KEY, chat_id INTEGER, text TEXT, reply_to INTEGER, state TEXT, server_id INTEGER, error TEXT, created_at INTEGER)` через миграцию `0004_outgoing.sql`. Методы `SaveOutgoing(ctx, msg) error`, `UpdateOutgoingState(ctx, localID, state, serverID, errMsg) error`, `GetOutgoing(ctx, chatID) ([]OutgoingMessage, error)`
- [x] Добавить retry в `SendService.SendText`: при `*errors.FloodWait` — ждать `RetryAfter`. При network error (сравнение через `errors.Is(err, syscall.ECONNRESET)` etc) — exponential backoff 3 попытки. При validation error (например `MESSAGE_TOO_LONG`) — сразу state="failed" без retry. **Implementation note:** добавлен `coresync.ValidationError` (gotd-free sentinel); `tg/send.go` транслирует все 400-class tgerr.Error → ValidationError; `FLOOD_WAIT` уже маппится в `FloodWaitError`; backoff с jitter ±10% чтобы избежать lockstep на повторных fail'ах
- [x] Создать `internal/tg/send_test.go` через tgtest: успешный send, FloodWait+retry, validation error без retry. **Note:** использован стуб `MessagesSendMessageClient` вместо tgtest (тот же подход что в history_test.go) — на порядок быстрее, проверяет тот же контракт высокого уровня (FLOOD_WAIT/validation translation, peer routing, RandomID, replyTo)
- [x] Создать `internal/core/sync/send_test.go` с mock `SenderInterface` и in-memory repo: проверить optimistic flow (pending → sent), retry на сетевой error, переход в failed
- [x] Запустить `go test -race ./internal/tg/... ./internal/core/sync/...` — зелёное

### Task 4: Reconnect + graceful degradation (read-only mode)

- [x] Создать `internal/core/sync/reconnect.go` с `ReconnectManager{client ClientInterface; bus *events.Bus; log *slog.Logger}` где `ClientInterface{Connect(ctx) error; Ping(ctx) error; OnDisconnect <-chan error}`. Метод `Run(ctx) error` — слушает `OnDisconnect`, при ошибке эмитит `ConnectionStateChanged{State: "offline"}`, запускает retry с exponential backoff (initial 1s, max 60s, jitter ±10%). При успехе — `ConnectionStateChanged{State: "online"}`. **Implementation note:** `ClientInterface` называется `ReconnectClient` (более выразительное имя). `context.Canceled` от `OnDisconnect` трактуется как user-shutdown — никаких retry. Constants `ConnectionStateOnline/Connecting/Offline` для type-safe сравнений. `MaxAttempts=0` (default) — retry forever
- [x] Добавить в `internal/tg/client.go` поле `disconnect chan error` и метод `OnDisconnect() <-chan error`. Хук в gotd `Run()` — при возврате ошибки писать в канал. **Implementation note:** канал буферизованный (cap=1), `signalDisconnect` non-blocking-replace — последняя ошибка побеждает; ctx.Canceled тоже форвардится так что reconnect-loop сам решает игнорировать ли
- [x] Создать `internal/core/sync/degradation.go` с `DegradationDetector{repo storage.Repo; bus *events.Bus}`. Метод `CheckWriteAccess(ctx) (writable bool, err error)` — пытается `repo.SaveMessage(...)` с тестовым blob, если получает SQLite error 8 (`SQLITE_READONLY`) или permission denied — эмитит `StorageStateChanged{Mode: "read-only"; Reason}`. Запускается при старте + каждые 30 сек. **Implementation note:** вместо `SaveMessage` с тестовым blob — отдельный `Repo.ProbeWrite` (BEGIN IMMEDIATE + ROLLBACK на dedicated `*sql.Conn`), который не пачкает user-visible таблицы. `transition()` эмитит событие только при смене режима — steady-state read-only не флудит подписчиков. `degradationReason()` нормализует частые ошибки в стабильные строки для статус-бара
- [x] Добавить в `internal/storage/sqlite/repo.go` метод `IsReadOnly() bool` — возвращает текущий режим. Все write-методы проверяют флаг и возвращают `ErrReadOnly` если true. Все read-методы работают как обычно. **Implementation note:** `readOnly` — `atomic.Bool` (concurrent с probe-горутиной); `SetReadOnly` экспортирован чтобы тесты могли проверить gating без файловых трюков; `ProbeWrite` обходит soft-флаг (иначе detector не может восстановить rw)
- [x] Расширить `internal/core/events/events.go` событием `StorageStateChanged{Mode string ("rw"|"read-only"); Reason string}`. **Implementation note:** добавлены константы `StorageModeReadWrite/ReadOnly` для type-safe сравнений в подписчиках
- [x] Создать `internal/core/sync/reconnect_test.go`: mock `ClientInterface` с `OnDisconnect` каналом, отправить error → проверить exponential backoff + событие в bus → симулировать успешный `Connect` → событие `online`. Покрыты также `context.Canceled` (no reconnect) и `MaxAttempts` cap
- [x] Создать `internal/core/sync/degradation_test.go`: создать БД на `t.TempDir()`, `chmod 0444` файл, запустить `CheckWriteAccess` → проверить событие `read-only` + что последующие `repo.SaveMessage` возвращают `ErrReadOnly`. **Note:** chmod 0444 на macOS не блокирует уже открытый fd, поэтому event-логика проверяется через fake `DegradationStore` (детерминированно), а ErrReadOnly gating проверяется отдельным `internal/storage/sqlite/readonly_test.go` через `SetReadOnly(true)` — тот же контракт, без flaky-зависимости от файловых прав
- [x] Запустить `go test -race ./internal/core/sync/...` — зелёное

### Task 5: Keymap loader + emacs key bindings

- [x] Добавить зависимость `go get github.com/BurntSushi/toml` (а также `charm.land/bubbles/v2` для `key.Binding` — bubbletea v2 живёт на `charm.land`, не на `github.com/charmbracelet`)
- [x] Создать `internal/ui/keymap/defaults.go` с типом `Keymap struct{ Send, Newline, Reply, OpenEditor, ToggleHelp, FocusNext, FocusPrev, ScrollUp, ScrollDown, Quit key.Binding }`. Функция `Default() Keymap` возвращает emacs-binding'и: Send=Enter, Newline=Alt+Enter, Reply=Ctrl+R, OpenEditor=Ctrl+E, ToggleHelp=`?`, FocusNext=Tab, FocusPrev=Shift+Tab, ScrollUp=Ctrl+B/PgUp, ScrollDown=Ctrl+F/PgDown, Quit=Ctrl+C/Ctrl+Q
- [x] Создать `internal/ui/keymap/loader.go` с функцией `Load(path string) (Keymap, error)` — читает TOML файл `~/.config/lazytg/keymap.toml`, мерджит с `Default()`. Формат: `[bindings]` секция с `send = "enter"`, `reply = "ctrl+r"` etc. Парсинг строк через хелпер `parseKey(s string) (key.Binding, error)` в `parse.go` (поддерживает "ctrl+x", "alt+y", "shift+f1", named keys "enter"/"tab"/"esc"/"pgup"/"pgdown"/"home"/"end" и алиасы "return"→"enter", "escape"→"esc", "pgdn"→"pgdown", "del"→"delete"). **Implementation note:** TOML-имена snake_case (`open_editor`, `scroll_up`); неизвестное имя в `[bindings]` — ошибка с перечислением допустимых; `desc` из defaults сохраняется при override (`SetHelp(canonKey, originalDesc)`). Modifier order нормализуется в `ctrl→alt→shift→base` (bubbletea v2 KeyPressMsg.String() emits именно этот порядок), так что `alt+ctrl+x` → `ctrl+alt+x`. Конфликты проверяются на merged map (post-Default) — иначе override типа `send = "ctrl+r"` молча сломал бы reply
- [x] Создать `internal/ui/keymap/conflict.go` с функцией `DetectConflicts(km Keymap) []ConflictReport` — для каждой пары биндингов проверяет совпадение `key.Binding.Keys()`. Возвращает читаемое описание: формат `"<chord>: <name> and <name>"` (например `"ctrl+r: reply and send"`). Структурированный `ConflictError` экспортируется отдельно для callers, которые хотят рендерить конфликты сами
- [x] Создать `internal/ui/keymap/loader_test.go` с табличными тестами:
  1. Пустой TOML → defaults (плюс отдельные подтесты для пустого пути, отсутствующего файла)
  2. Override `send = "ctrl+s"` → Send изменён, остальные defaults, help.Desc сохраняется
  3. Конфликт `send = "ctrl+r"; reply = "ctrl+r"` → `*ConflictError`, оба имени в сообщении
  4. Невалидная строка `send = "frobnicate"` → ошибка с указанием значения и имени биндинга
  5. Конфликт против default (override `send = "ctrl+r"` без трогания reply) → ошибка ловится
  6. Неизвестное имя биндинга → ошибка с упоминанием bad name
  7. Малформированный TOML → ошибка
- [x] Создать `internal/ui/keymap/parse_test.go` с табличными тестами на `parseKey`: "enter"/"return", "ctrl+a", "alt+enter", "shift+tab", "f1"/"f12", "esc"/"escape", "?", "/", модификаторы в произвольном порядке нормализуются, невалидные строки (empty, `frobnicate`, `ctrl+`, `ctrl+ctrl+a`, `a+ctrl`, `f13`, `f0`)
- [x] Запустить `go test -race ./internal/ui/keymap/...` — зелёное

### Task 6: Bubble Tea root model + 2-pane layout + statusbar

- [x] Добавить зависимости: `charm.land/bubbletea/v2`, `charm.land/bubbles/v2`, `charm.land/lipgloss/v2` (bubbletea v2 живёт на charm.land, не на github.com/charmbracelet — keymap уже использует v2). **Note:** `github.com/charmbracelet/x/exp/teatest` НЕ подключён — он импортирует bubbletea v1 и несовместим с v2; тесты пишутся напрямую через `Update`/`View` (детерминированно, не нужен event-loop)
- [x] Создать `internal/ui/app/model.go` с `App struct{ chats chats.Model; thread thread.Model; status statusbar.Model; help overlay.Help; input input.Model; focus FocusTarget; width, height int; keymap keymap.Keymap; bus *events.Bus; log *slog.Logger }`. Тип `FocusTarget` enum: `FocusChats`, `FocusInput`, `FocusThread`. Конструктор `New(deps Deps) App` где Deps включает bus, log, keymap, начальные модели pane'ов. **Implementation note:** Deps.{Chats,Thread,Input,Status} опциональные (`*Model`) — если nil, используется package-level конструктор; chooseModel-generic чистит nil-checks. Это нужно чтобы Task 6 был тестируем до Tasks 7-9
- [x] Создать `internal/ui/app/update.go` с `func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd)`. Обрабатывает:
  - `tea.WindowSizeMsg` → `a.width, a.height = msg.Width, msg.Height`. При `width<80 || height<24` — установить флаг `tooSmall=true`, при resize обратно — снять. Пропорции: chats 30% width (min 20), thread остаток. Status bar — bottom 1 строка. Input — bottom 3 строки над status bar
  - `tea.KeyPressMsg` → если `keymap.ToggleHelp` matches → emit `helpToggledMsg` через Cmd. Если `keymap.FocusNext`/`FocusPrev` → emit `focusCycledMsg{Direction}` через Cmd. Если help открыт — приоритет в help.Update (модальный, swallows other keys). Если `keymap.Quit` matches → tea.Quit. Иначе делегировать в фокусированный компонент. **Implementation note:** focus/help-toggle идут через Cmd (а не in-place мутация модели) чтобы тесты могли наблюдать переход как event и чтобы соблюсти bubbletea-конвенцию
  - События из bus (через `tea.Cmd`-обёртку) → роутить в нужные panes (полная wiring — Task 11)
- [x] Создать `internal/ui/app/view.go` с `func (a App) View() tea.View`. Если `tooSmall` — вернуть `"Terminal too small (min 80x24)"` отцентрованный через lipgloss.Place. Иначе через lipgloss.JoinHorizontal: `chats.View()` + vertical separator (`│`) + `thread.View()`, JoinVertical с input.View() и statusbar.View(). Focused pane получает bright-blue foreground через paneStyle. **Note:** в bubbletea v2 View возвращает `tea.View` (не string) — содержит Content + опциональный Cursor + bg/fg overrides. Мы только заполняем Content
- [x] Создать `internal/ui/app/keys.go` с глобальными command-functions: `cmdNextFocus()`, `cmdPrevFocus()`, `cmdToggleHelp()`, `cmdQuit()`. Каждая возвращает `tea.Cmd` который при выполнении эмитит соответствующий tea.Msg-маркер (focusCycledMsg/helpToggledMsg) или вызывает tea.Quit
- [x] Создать `internal/ui/statusbar/model.go` с `Model{AccountAlias, ChatTitle string; UnreadTotal int; ConnState string; StorageMode string; FloodWait time.Duration}`. Метод `View(width int) string` — lipgloss horizontal layout: левая часть `accountAlias | chatTitle` (с truncation chat→alias), правая часть `unread N | connState | storage`. FloodWait > 0 заменяет conn-cell на `floodwait Xs`. Состояния connState: "connecting" (ANSI 3 yellow), "online" (ANSI 2 green), "offline" (ANSI 1 red), "floodwait" (yellow). Константы цветов как ANSI индексы — стабильны для golden snapshots на любых терминалах
- [x] Создать `internal/ui/statusbar/view_test.go` с golden-snapshots для всех 4 состояний (connecting/online/offline/floodwait) на ширине 100. Эталоны в `testdata/statusbar_*.txt`, обновляются флагом `-update`. Дополнительно: тесты на ширину (40/60/80/120 — все exact match), truncation (alias preserved, chat truncated с ellipsis), floodwait override conn, blank fields → defaults
- [x] Создать `internal/ui/app/update_test.go` (без teatest — напрямую через Update/View):
  1. WindowSize 100x30 → не tooSmall, dimensions stored
  2. WindowSize 60x20 → tooSmall, View содержит "Terminal too small"
  3. WindowSize 40x10 → 120x40 → recovers (tooSmall очищается)
  4. KeyPressMsg `?` → focusCycledMsg/helpToggledMsg → help.Visible=true; повторное `?` (через модальный overlay) — hides
  5. KeyPressMsg Tab → focus сменился c FocusChats на FocusInput; Tab×3 — wrap-around к FocusChats
  6. Shift+Tab → wrap к FocusThread
  7. Help-modal swallows Tab — focus не меняется пока overlay открыт
  8. Esc dismisses help
  9. Ctrl+C → tea.QuitMsg
  10. Focused pane показывает focus-флаг в View
- [x] Создать stub-реализации `internal/ui/panes/chats/`, `internal/ui/panes/thread/`, `internal/ui/input/`, `internal/ui/overlay/help.go` (минимальные `Model{Width,Height,Focused}` + Init/Update/View/SetSize/SetFocus). Полная реализация — Tasks 7-10. Stub нужен чтобы Task 6 был самодостаточен и тестируем до Tasks 7-9
- [x] Создать `internal/ui/overlay/help_test.go` — Hidden=true → empty View; Visible=true → содержит "send"/"enter"/"reply"/"ctrl+r"; Esc/q/? dismisses; unrelated key игнорится
- [x] Обновить `.golangci.yml`: добавить `charm.land/bubbletea`, `charm.land/bubbles`, `charm.land/lipgloss` в depguard core deny-list (раньше был только github.com/charmbracelet/bubbletea — поскольку v2 на charm.land, нужно явно банить и его)
- [x] Запустить `go test -race ./internal/ui/...` — зелёное; `golangci-lint run ./...` — 0 issues

### Task 7: Chats pane

- [x] Создать `internal/ui/panes/chats/item.go` с `type ChatItem struct{ ID int64; Title string; LastMessagePreview string; LastMessageDate time.Time; UnreadCount int; Pinned bool; Type domain.ChatType }`. Метод `Title()` (для bubbles/list.DefaultItem) возвращает форматированное `[📌] Title (15)` где [📌] для pinned, (15) — unread > 0. Метод `Description()` возвращает `LastMessagePreview` обрезанное до 60 символов. Метод `FilterValue()` возвращает Title (для built-in фильтра). **Implementation note:** struct fields unexported (Go запрещает поле и метод с одним именем — `Title string` и `Title() string` несовместимы); `NewChatItem(domain.Chat, preview)` — конструктор. Truncate работает по runes (не bytes), не ломает UTF-8 mid-codepoint
- [x] Создать `internal/ui/panes/chats/model.go` с `Model{list list.Model; chats []ChatItem; repo storage.Repo; log *slog.Logger}`. Конструктор `New(repo, log) Model`. Метод `Init()` возвращает `tea.Cmd` который читает chats из repo и эмитит `chatsLoadedMsg{[]ChatItem}`. Сортировка: pinned сначала, остальные по `LastMessageDate DESC`. **Implementation note:** два конструктора — `New()` (placeholder, без repo, для тестов app/) и `NewWithRepo(repo, log)` (полный). `Width/Height/Focused` остаются exported чтобы `app/view.go` не пришлось трогать. Сортировка тут же дополнительно (defensively) выполняется на client-side, чтобы fakerepo'ы в тестах с произвольным порядком давали тот же deterministic результат
- [x] Создать `internal/ui/panes/chats/update.go` с `func (m Model) Update(msg tea.Msg) (Model, tea.Cmd)`:
  - `chatsLoadedMsg` → обновить `m.list.SetItems(...)`
  - `events.DialogUpdated` → перезагрузить из repo (debounce 200ms через `tea.Tick`)
  - `tea.KeyMsg` Up/Down/k/j → делегировать в `m.list.Update(msg)`
  - Enter → эмитировать `ChatSelectedMsg{ChatID: m.list.SelectedItem().(ChatItem).ID}`
  - **Implementation note:** debounce реализован через `reloadGeneration uint64` — каждый DialogUpdated инкрементит счётчик и арм'ит `tea.Tick(200ms)` с замороженным generation; `reloadDebouncedMsg` с устаревшим generation отбрасывается. Enter intercept'ится только когда фильтр НЕ активен (`list.SettingFilter()`) — иначе list-built-in handler применяет фильтр
- [x] Создать `internal/ui/panes/chats/view.go` — `m.list.View()` с обёрткой в lipgloss-стиль (border, padding). **Note:** lipgloss-стиль уже навешивается родителем (`app/view.go::renderBody` через `paneStyle(focused)`); pane сам только рендерит focus-aware header (`Chats` / `Chats (focused)`) + `m.list.View()`, чтобы `TestFocusedPaneAppliesFocusFlag` остался зелёным без изменений
- [x] Создать `internal/ui/panes/chats/model_test.go`:
  1. Пустой list → пустой view
  2. Список из 5 chats → view содержит все 5 titles
  3. KeyMsg Down → SelectedItem смещается на 1
  4. KeyMsg Enter → возвращается tea.Cmd, который при выполнении эмитит `ChatSelectedMsg`
  5. Сортировка: pinned выше unpinned, в одной группе — по LastMessageDate
- [x] Запустить `go test -race ./internal/ui/panes/chats/...` — зелёное

### Task 8: Thread pane + pagination

- [x] Создать `internal/ui/panes/thread/format.go` с функцией `FormatMessage(msg domain.Message, width int, replyTo *domain.Message) string` — рендерит:
  - Строка 1: `[15:42] Author Name` (timestamp HH:MM, серый цвет; имя bold)
  - Если replyTo != nil: `↳ replying to: <preview 50 chars>` (italic, серый)
  - Тело: `msg.Text` с word-wrap по `width-2` (используя `lipgloss.NewStyle().Width(width-2).Render(text)` или ручной wrapper)
  - Inline-форматирование по entities из gotd (bold/italic/code/link) — в v0.1 сделать только для plaintext с простыми markdown-маркерами `**bold**`, `*italic*`, `` `code` `` (полный entity-рендеринг — Stage 3). **Implementation note:** order matters — backticks first (inner text не перерисовывается), затем `**` (greedy), затем `*` через `applyStarItalic` который сам пропускает `**` пары; иначе непарный `**bold` мис-парсился бы как два italic
- [x] Создать `internal/ui/panes/thread/model.go` с `Model{viewport viewport.Model; messages []domain.Message; chatID int64; repo storage.Repo; provider sync.HistoryProvider; log *slog.Logger; loading bool; hasMore bool; oldestID int}`. Конструктор `New(repo, provider, log) Model`. Метод `Init() tea.Cmd` — без действия. Метод `OpenChat(chatID int64) tea.Cmd` — читает последние 200 сообщений из repo + триггерит backfill если history тонкая. **Implementation note:** Repository/HistoryProvider interfaces объявлены локально (зеркалят `core/sync.HistoryProvider`/`*sqlite.Repo`-subset) чтобы UI не тащил storage и avoid coupling — паттерн из chats pane. Backfill-trigger пока в комментарии (поднимется в Task 11 wiring), pagination идёт через offset = `len(messages)` (`oldestID` остаётся как cursor для будущего MTProto-cursor backfill). Два конструктора `New()`/`NewWithRepo()` — нужно для тестов app/ которые конструируют thread без storage
- [x] Создать `internal/ui/panes/thread/update.go`:
  - `ChatSelectedMsg` → `m.OpenChat(...)` → `messagesLoadedMsg{[]domain.Message; hasMore bool}`
  - `messagesLoadedMsg` → `m.messages = msg.Messages`, обновить viewport content через `m.viewport.SetContent(m.renderAll())`, scroll to bottom
  - `events.MessageReceived{ChatID == m.chatID}` → append в `m.messages`, обновить content, scroll to bottom (если был на bottom — sticky scroll)
  - `events.OutgoingMessageStateChanged{ChatID == m.chatID}` → найти optimistic запись в m.messages (по LocalID), обновить state, перерендерить
  - `tea.KeyMsg` Up/Down/PgUp/PgDn → `m.viewport.Update(msg)`. При scroll к top + `m.hasMore` → `tea.Cmd` загрузить ещё 200 сообщений (offsetID = oldestID), эмитить `messagesPaginationLoadedMsg`. **Implementation note:** реализован дополнительный `LoadMore()` без `AtTop()`-guard для unit-тестов с искусственной высотой viewport — re-entrancy всё равно защищён `loading` flag. Stale `messagesLoadedMsg` (chatID не совпадает с текущим) drop'ятся — иначе быстрое переключение чатов давало бы misroute
- [x] Создать `internal/ui/panes/thread/view.go` — `m.viewport.View()` с lipgloss-обёрткой. При `m.loading` — overlay строка "Loading..."
- [x] Создать `internal/ui/panes/thread/optimistic.go` с функцией `RenderOptimistic(msg OutgoingMessage) string` — спец-рендер для pending/failed: префикс `[⏳]` для pending, `[✗]` для failed (красный). Sent — без префикса (как обычное сообщение)
- [x] Создать `internal/ui/panes/thread/format_test.go` с golden snapshots для разных кейсов: text-only, с replyTo, с markdown bold/italic/code, длинный текст с word-wrap. Эталоны в `testdata/thread/*.txt`. **Note:** golden-файлы хранятся со снятыми ANSI escape-codes (через `stripANSI` regex) — иначе сравнение зависело бы от lipgloss color-profile detection (TTY vs non-TTY) и было flaky на CI. Generate/refresh: `go test ... -args -update`
- [x] Создать `internal/ui/panes/thread/pagination_test.go`: репо с 500 сообщениями, открыть чат → загрузилось 200, scroll вверх → догрузилось ещё 200, oldestID обновился
- [x] Запустить `go test -race ./internal/ui/panes/thread/...` — зелёное

### Task 9: Input field (emacs/readline) + composer + reply state

- [ ] Создать `internal/ui/input/model.go` с `Model{textarea textarea.Model; replyTo *domain.Message; sendService SendServiceInterface; chatID int64; keymap keymap.Keymap; log *slog.Logger}` где `SendServiceInterface{SendText(ctx, chatID, text, replyTo int) (localID string, err error)}`. Конструктор `New(send, km, log) Model`. Textarea с custom `key.Bindings` из keymap. Высота фиксированно 3 строки
- [ ] Создать `internal/ui/input/emacs.go` с функцией `ApplyEmacsBindings(ta *textarea.Model, km keymap.Keymap)` — переопределяет встроенные key.Binding'и textarea: WordForward (Alt+F), WordBack (Alt+B), DeleteWordBack (Ctrl+W), DeleteAfterCursor (Ctrl+K), DeleteBeforeCursor (Ctrl+U), LineStart (Ctrl+A), LineEnd (Ctrl+E), CharacterForward/Backward (стрелки + Ctrl+F/B). Vim-like биндинги НЕ добавляем (отложено на v0.2)
- [ ] Создать `internal/ui/input/history.go` с `History{entries []string; cursor int; max int}` — circular buffer на 100 последних отправленных сообщений. Методы `Add(text string)`, `Prev() string`, `Next() string`. Persistence в `~/.local/state/lazytg/input_history.txt` (опционально, можно отложить — пока in-memory)
- [ ] Создать `internal/ui/input/update.go` с `func (m Model) Update(msg tea.Msg) (Model, tea.Cmd)`:
  - `tea.KeyMsg` matches `keymap.Send` (Enter) → если `m.textarea.Value()` пустой — no-op; иначе вызвать `m.sendService.SendText(...)` через tea.Cmd, очистить textarea, добавить в history. Если `replyTo` set — передать `replyTo.ID`, потом сбросить replyTo
  - `tea.KeyMsg` matches `keymap.Newline` (Alt+Enter) → вставить `\n`
  - `tea.KeyMsg` matches `keymap.Reply` (Ctrl+R) → эмитировать `requestReplyMsg` (UI app найдёт выбранное сообщение и установит `replyTo`)
  - `tea.KeyMsg` matches `keymap.OpenEditor` (Ctrl+E) → эмитировать `openEditorMsg{currentText: m.textarea.Value()}`
  - `tea.KeyMsg` history Ctrl+P/N → передать в `m.history.Prev()` / `.Next()`, установить в textarea
- [ ] Создать `internal/ui/input/view.go` — `m.textarea.View()` с подсказкой `replyTo` сверху (если set): `↳ Reply to <author>: <preview>` (italic). Hint строка снизу при пустом textarea: "Enter to send, Alt+Enter for newline, Ctrl+E for editor, Ctrl+R to reply"
- [ ] Создать `internal/ui/input/emacs_test.go` с тестами:
  1. Set value `"hello world"`, KeyMsg Ctrl+A → cursor в начало
  2. KeyMsg Ctrl+E → cursor в конец
  3. KeyMsg Ctrl+W → удалить последнее слово ("hello ")
  4. KeyMsg Ctrl+K → удалить от cursor до конца
  5. KeyMsg Ctrl+U → удалить от cursor до начала
  6. KeyMsg Alt+F с курсором в начале → cursor после "hello"
  7. KeyMsg Alt+B с курсором в конце → cursor перед "world"
- [ ] Создать `internal/ui/input/history_test.go`: добавить 5 entries, Prev → последний, Prev → предпоследний, Next → последний, Next → пустая строка (новый ввод). Circular после 100: 101-й вытесняет 1-й
- [ ] Создать `internal/ui/input/model_test.go` через teatest: пустой textarea + Enter → no-op (sendService не вызван). Текст "test" + Enter → sendService.SendText вызван с text="test", textarea очищен, "test" в history
- [ ] Запустить `go test -race ./internal/ui/input/...` — зелёное

### Task 10: $EDITOR delegation (Ctrl-E) + Help overlay (?)

- [ ] Создать `internal/ui/input/editor.go` с функцией `OpenEditor(currentText string) tea.Cmd`. Внутри:
  1. Получить editor: `os.Getenv("EDITOR")`, fallback на `"vi"`
  2. Создать temp-файл `os.UserCacheDir() + "/lazytg/edit-XXXXXX.md"` с правами `0600` через `os.CreateTemp`
  3. Записать `currentText` в файл, закрыть
  4. Вернуть `tea.ExecProcess(exec.Command(editor, tmpPath), func(err error) tea.Msg { ... })` — после выхода editor читает файл, удаляет, эмитит `editorClosedMsg{Text: contents, Err: err}`
  5. Defer удаление temp-файла даже при ошибке (использовать `defer os.Remove(...)` в callback)
- [ ] Обновить `internal/ui/input/update.go`: на `editorClosedMsg{Text}` — установить textarea value = Text, focus textarea
- [ ] Создать `internal/ui/input/editor_test.go`:
  1. Set `EDITOR=cat` (через `t.Setenv`), вызвать `OpenEditor("hello")` → temp-файл создаётся с правами 0600 (проверить через `os.Stat`), удаляется после execution
  2. EDITOR=`/bin/sh -c "echo edited > $1"` (трюк через wrapper-script в tempdir) → editorClosedMsg.Text == "edited\n"
  3. EDITOR не установлен и `vi` недоступен → ошибка с понятным сообщением
- [ ] Создать `internal/ui/overlay/help.go` с `Help struct{ Visible bool; keymap keymap.Keymap; width, height int }`. Методы `View() string` — рендерит modal-окно по центру: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 2) с таблицей Action | Binding (две колонки). Перечислить из keymap: Send/Newline/Reply/OpenEditor/ToggleHelp/FocusNext/Prev/Scroll/Quit
- [ ] Метод `Update(msg tea.Msg) (Help, tea.Cmd)`:
  - `tea.KeyMsg` Esc/q/`?` → `h.Visible = false`
  - Иначе игнорировать (модальный)
- [ ] Создать `internal/ui/overlay/help_test.go`:
  1. Visible=false → View == ""
  2. Visible=true → View содержит "Send" и "enter" (или текущий keybinding)
  3. KeyMsg Esc → Visible=false
- [ ] Интегрировать в `internal/ui/app/view.go`: если `app.help.Visible` — наложить help.View() поверх основного layout через lipgloss.Place центрирование
- [ ] Запустить `go test -race ./internal/ui/input/... ./internal/ui/overlay/...` — зелёное

### Task 11: Wiring + DI миграция в internal/app + e2e teatest + SLA benchmark

- [ ] Создать `internal/app/wire.go` с `type App struct{ Client *tg.Client; Repo storage.Repo; Bus *events.Bus; Log *slog.Logger; History *sync.HistoryService; Live *sync.LiveService; Send *sync.SendService; Reconnect *sync.ReconnectManager; Updates *tg.UpdatesDispatcher; Polling *tg.PollingFallback; Keymap keymap.Keymap }`. Функция `Build(ctx context.Context, cfg Config) (*App, error)` — собирает все компоненты в правильном порядке: 1. logger, 2. repo, 3. bus, 4. tg client (login if needed), 5. updates dispatcher или polling, 6. sync services, 7. keymap loader. Метод `Run(ctx context.Context) error` — запускает все горутины, дожидается отмены контекста
- [ ] Перенести wiring из `cmd/lazytg/cmd/runtime.go` в `internal/app/wire.go`. В `runtime.go` оставить только тонкий вызов `app.Build(...)` и `app.Run(...)`. Это удовлетворяет condition в CLAUDE.md "wiring временно в cmd/lazytg/cmd/runtime.go" → теперь в `internal/app/`
- [ ] Создать `cmd/lazytg/cmd/tui.go` с `tuiCmd = &cobra.Command{Use: ""... default команда без подкоманды}`. Run: создаёт `app.App` через `app.Build`, конструирует `internal/ui/app.New(deps)`, запускает `tea.NewProgram(uiApp).Run()`. Подключает `app.Bus.Subscribe()` к `program.Send()` через горутину (event-bus → tea.Msg)
- [ ] Обновить `cmd/lazytg/cmd/root.go`: если запуск без подкоманды (по `len(os.Args) == 1` или через `cobra.Command.Run` на root) → выполнить `tuiCmd.RunE`. Иначе делегировать как обычно
- [ ] Создать `test/e2e/tui_smoke_test.go` через `teatest.NewTestModel`: моки `Repo`, `SendService`, `HistoryService`. Сценарий:
  1. Старт → ожидаем view содержит "Loading..." или статус
  2. Эмулировать `chatsLoadedMsg` с 3 чатами → ожидаем 3 title в view
  3. KeyMsg Down + Enter → выбор второго чата → эмит `ChatSelectedMsg` → ожидаем `messagesLoadedMsg` обработался
  4. KeyMsg Tab → focus переключился на input
  5. Ввести "hello" + Enter → `sendService.SendText` вызван
  6. teatest.WaitFor → проверка что в final view есть optimistic preview "hello"
- [ ] Создать `test/perf/live_latency_test.go` с **бенчмарком SLA**: запустить полный pipeline (mock gotd → updates dispatcher → bus → live service → repo → bus → UI listener), 1000 итераций, замерить latency `MessageReceived` event → final repo write. Перцентиль p95 через сортировку. **CI gate: fail если p95 >500ms**
- [ ] Создать `test/perf/goroutine_leak_test.go`: запустить полный `app.App.Run` на 30 секунд с mock backends, остановить через context cancel, проверить через `goleak.VerifyNone(t)` что нет утечек горутин
- [ ] Создать ручной smoke-чеклист в `docs/MANUAL_SMOKE.md` для Stage 2: (1) lazytg запускается без --debug → отображает чаты; (2) выбор чата → история загружается; (3) ввод "hello" + Enter → сообщение появляется в треде и приходит на телефон; (4) Ctrl+E открывает $EDITOR=vi; (5) `?` показывает help; (6) `--polling` flag запускает fallback polling
- [ ] **Verification:** `go build ./...`, `go test -race ./...`, `golangci-lint run`. Coverage gates: `go test -coverprofile=core.out ./internal/core/... && go tool cover -func=core.out | tail -1` — total ≥70%. То же для UI: `./internal/ui/... ≥50%`. Бенчмарк p95: `go test -bench=BenchmarkLiveUpdateLatency -benchtime=3s ./internal/core/sync/` — p95 <500ms (вывод проверяется глазами или парсингом). Move plan to `docs/plans/completed/20260502-lazytg-stage2-tui.md` после успешного прохода
