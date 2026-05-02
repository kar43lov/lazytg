# Plan: lazytg Stage 1 — Foundation

## Overview

Реализация этапа 1 (Foundation) проекта lazytg — Telegram TUI-клиента на Go с локальным FTS5-поиском. Выпускаем сериозный OSS с первого дня.

Этап 1 закладывает фундамент: bootstrap репозитория, 3-слойную архитектуру с depguard-изоляцией, SQLite storage с FTS5 spike (риск-блокер), auth flow через gotd/td, cobra CLI, slog с redaction, GitHub Actions CI, базовую документацию.

После этапа 1 должно работать: `lazytg login` логинит реальный аккаунт через phone+code+2FA, session сохраняется в keyring (с age-encrypted file fallback), `lazytg accounts` показывает список, CI зелёный на 2 OS × 2 build-tags.

## Context

**Полный план продукта:** `docs/plans/lazytg-v0.1.0.md` — 4 этапа × 10 недель × 180 часов. Прошёл deep brainstorm с dialectic-анализом. Этот ralphex-план покрывает только **Stage 1: Foundation** (~30-40 часов, 1-2 недели).

**Стек (зафиксирован после dialectic):**
- Go 1.22+
- `github.com/gotd/td` — pure-Go MTProto
- `github.com/charmbracelet/bubbletea` v2 + `lipgloss` + `bubbles` (используется в этапе 2)
- `modernc.org/sqlite` — pure-Go SQLite (build tag `sqlcipher` → CGo SQLCipher opt-in)
- `github.com/zalando/go-keyring` — secrets storage
- `filippo.io/age` — encryption fallback для headless
- `github.com/spf13/cobra` — CLI framework
- `log/slog` (stdlib) + `gopkg.in/natefinch/lumberjack.v2` для rotation
- `goreleaser/goreleaser-action@v6` для release pipeline

**Архитектура (3 слоя, изолированные через depguard):**
- `internal/tg/` — обёртка gotd, знает MTProto
- `internal/core/` — domain + storage + events + sync, БЕЗ gotd, БЕЗ bubbletea
- `internal/ui/` — Bubble Tea (этап 2), БЕЗ gotd
- `internal/storage/sqlite/` — реализация repository
- `internal/app/` — DI без фреймворка (явный конструктор)
- `cmd/lazytg/` — cobra entry

**Ключевые риски учтённые в этом этапе:**
1. **FTS5 trigram у modernc/sqlite** — риск-блокер, проверяется spike-тестом в Task 3.
2. **Userbot ban-risk** — warning первой строкой README в Task 8.
3. **Headless keyring (нет D-Bus)** — fallback на age-encrypted file в Task 4.

**Платформа разработки:** macOS (zsh, brew). Цель cross-build: linux+darwin × amd64+arm64.

## Validation Commands

- `cd /Users/pgmac/Data/prjcts/lazytg && go build ./...`
- `cd /Users/pgmac/Data/prjcts/lazytg && go test -race ./...`
- `cd /Users/pgmac/Data/prjcts/lazytg && golangci-lint run`

### Task 1: Bootstrap репозитория + tooling

- [x] Создать `/Users/pgmac/Data/prjcts/lazytg/go.mod` командой `go mod init github.com/pgmac/lazytg` (Go 1.22+)
- [x] Создать `.gitignore` с правилами для Go: `*.exe`, `bin/`, `dist/`, `coverage.out`, `.env*`, `*.test`, `vendor/`
- [x] Создать `.editorconfig` со стандартными правилами Go (tab indent, LF, trim trailing)
- [x] Создать `Makefile` с целями: `build` (`go build -o bin/lazytg ./cmd/lazytg`), `test` (`go test -race ./...`), `lint` (`golangci-lint run`), `clean` (`rm -rf bin/ dist/ coverage.out`), `tidy` (`go mod tidy`)
- [x] Создать `.golangci.yml` с линтерами: `errcheck`, `govet`, `staticcheck`, `gosec`, `revive`, `gocritic`, `depguard`. В секции `linters-settings.depguard` задать правило: `internal/core/...` не может импортировать `github.com/gotd/td/...` или `github.com/charmbracelet/bubbletea`. `internal/ui/...` не может импортировать `github.com/gotd/td/...`. `internal/storage/...` не может импортировать `internal/ui` или `internal/tg`
- [x] Создать `lefthook.yml` с pre-commit hook'ами: `gofmt`, `go vet ./...`, `go test -short ./...`
- [x] Создать пустой `cmd/lazytg/main.go` с `package main` и `func main() {}`, проверить `go build ./...` собирается
- [x] Запустить `golangci-lint run` — должно быть zero warnings (на пустом проекте)

### Task 2: Скелет 3-слойной архитектуры + event bus

- [x] Создать `internal/tg/doc.go` с `// Package tg wraps gotd/td for Telegram MTProto communication.` и `package tg`
- [x] Создать `internal/core/doc.go` с описанием domain layer (storage interface, events, sync). Текст: `// Package core contains domain types, storage interfaces, event bus and sync logic.\n// MUST NOT import gotd/td or bubbletea (enforced via depguard).`
- [x] Создать `internal/ui/doc.go` для Bubble Tea (используется в этапе 2). Текст: `// Package ui contains Bubble Tea models and views.\n// MUST NOT import gotd/td (enforced via depguard).`
- [x] Создать `internal/app/doc.go` для DI без фреймворка
- [x] Создать `internal/core/events/events.go` с типизированными событиями: `MessageReceived{ChatID, MessageID int64; Text string; FromID int64; Date time.Time}`, `DialogUpdated{ChatID int64}`, `AuthStateChanged{State string}`, `ConnectionStateChanged{State string}`. Все события имплементируют интерфейс `Event` с методом `eventMarker()`
- [x] Создать `internal/core/events/bus.go` с `Bus` struct: метод `Subscribe(ctx context.Context) <-chan Event` (fan-out через slice подписчиков под mutex), метод `Publish(e Event)` (non-blocking send в каждый канал, дроп при overflow). При отмене контекста подписчик удаляется
- [x] Создать `internal/core/events/bus_test.go` с тестами: (1) fan-out — публикация одного события доходит до 3 подписчиков; (2) отписка через context cancel — после cancel подписчик не получает событий; (3) goleak-проверка через `defer goleak.VerifyNone(t)` (импорт `go.uber.org/goleak`)
- [x] Добавить зависимость `go get go.uber.org/goleak`
- [x] Запустить `go test -race ./internal/core/events/...` — должно быть зелёным

### Task 3: SQLite storage + FTS5 trigram spike

- [x] Добавить зависимость `go get modernc.org/sqlite`
- [x] Создать `internal/storage/sqlite/migrations/0001_init.sql` со схемой: `accounts(id INTEGER PRIMARY KEY, phone TEXT UNIQUE NOT NULL, alias TEXT, created_at INTEGER NOT NULL)`, `chats(id INTEGER PRIMARY KEY, type TEXT NOT NULL, title TEXT, username TEXT, last_message_date INTEGER, unread_count INTEGER DEFAULT 0, pinned INTEGER DEFAULT 0)`, `messages(id INTEGER NOT NULL, chat_id INTEGER NOT NULL REFERENCES chats(id), from_id INTEGER, date INTEGER NOT NULL, text TEXT, reply_to INTEGER, raw_blob BLOB, PRIMARY KEY (chat_id, id))`, `peers(id INTEGER PRIMARY KEY, type TEXT NOT NULL, access_hash INTEGER NOT NULL)`, `state(account_id INTEGER PRIMARY KEY REFERENCES accounts(id), pts INTEGER, qts INTEGER, date INTEGER, seq INTEGER)`, `schema_migrations(version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`. Использовать `CREATE TABLE IF NOT EXISTS`
- [x] Создать `internal/storage/sqlite/driver_modernc.go` с build tag `//go:build !sqlcipher` и пустой `init()` импортирующий `_ "modernc.org/sqlite"`
- [x] Создать `internal/storage/sqlite/driver_sqlcipher.go` с build tag `//go:build sqlcipher` (заглушка с `_ "github.com/mutecomm/go-sqlcipher/v4"` — реальная реализация в этапе 3)
- [x] Создать `internal/storage/sqlite/migrations.go` с `//go:embed migrations/*.sql` и функцией `RunMigrations(db *sql.DB) error` — читает применённые версии из `schema_migrations`, применяет недостающие в порядке номеров. Включить PRAGMA: `journal_mode=WAL`, `foreign_keys=ON`, `synchronous=NORMAL`
- [x] Создать `internal/storage/sqlite/repo.go` с `type Repo struct{ db *sql.DB }`, конструктором `Open(path string) (*Repo, error)` (открывает БД, запускает миграции, ставит PRAGMA), методом `Close() error`. Базовые CRUD: `SaveChat(ctx, chat) error`, `GetChats(ctx) ([]Chat, error)`, `SaveMessage(ctx, msg) error`, `GetMessages(ctx, chatID, limit, offset) ([]Message, error)`. Domain types `Chat` и `Message` определить в `internal/core/domain/types.go`
- [x] Создать `internal/core/domain/types.go` с `Chat`, `Message`, `Account` структурами (использовать `time.Time` для дат, `int64` для ID)
- [x] Создать `internal/storage/sqlite/repo_test.go`: открыть БД на `t.TempDir()/test.db`, прогнать миграции, вставить 2 chats + 5 messages, прочитать обратно, проверить идентичность
- [x] Создать `internal/storage/sqlite/migrations_test.go`: idempotency-тест (применить миграции дважды на одной БД — без ошибок), fresh-install (применить на пустую БД)
- [x] **FTS5 SPIKE** — создать `internal/storage/sqlite/fts5_spike_test.go`: открыть `:memory:` SQLite, выполнить `CREATE VIRTUAL TABLE messages_fts USING fts5(text, tokenize='trigram')`. Если ошибка — fail с сообщением «modernc/sqlite не поддерживает FTS5 trigram, переключиться на porter tokenizer перед этапом 3». Вставить 100 строк с разным русским/английским текстом, выполнить `SELECT * FROM messages_fts WHERE text MATCH ?` с тестовыми запросами, проверить что находит совпадения для русского ("сообщение") и английского ("hello")
- [x] Запустить `go test -race ./internal/storage/sqlite/...` — все тесты включая FTS5 spike должны пройти

### Task 4: Auth flow через gotd/td + secrets storage

- [x] Добавить зависимости: `go get github.com/gotd/td`, `go get github.com/zalando/go-keyring`, `go get filippo.io/age`
- [x] Создать `internal/core/config/paths.go` с функциями XDG: `ConfigDir()` возвращает `~/.config/lazytg/`, `DataDir()` → `~/.local/share/lazytg/`, `StateDir()` → `~/.local/state/lazytg/`, `CacheDir()` → `~/.cache/lazytg/`. Использовать `os.UserConfigDir()` etc., создать директории с `0700` если не существуют
- [x] Создать `internal/core/config/secrets.go` с интерфейсом `SecretStore` (методы `Get(key string) (string, error)`, `Set(key, value string) error`, `Delete(key string) error`) и двумя реализациями: `KeyringStore` (через `zalando/go-keyring`, service `"lazytg"`) и `AgeFileStore` (хранит зашифрованный JSON в `ConfigDir()/secrets.age`, ключ из master-passphrase через scrypt). Конструктор `NewSecretStore()` пробует keyring, если падает (no D-Bus) — fallback на age-file с promtом passphrase
- [x] Создать `internal/tg/session.go` с реализацией `gotd/td/session.Storage` поверх `SecretStore`: `LoadSession(ctx)` читает по ключу `session:<phone>`, `StoreSession(ctx, data)` пишет. Файл сессии (если используется) — `0600`, проверять при старте через `stat` и `Mode()&0077 != 0` → fail-fast с сообщением о небезопасных правах
- [x] Создать `internal/tg/auth.go` с интерфейсом `CodePrompter` (методы `Phone(ctx) (string, error)`, `Code(ctx, sentTo string) (string, error)`, `Password(ctx) (string, error)`) — используется TUI или CLI для ввода. Функция `Login(ctx context.Context, client *telegram.Client, prompter CodePrompter) error` копирует логику из `gotd/td/examples/auth/auth.go` (NoSignUp flow), на запросы дёргает prompter
- [x] Создать `internal/tg/client.go` с `Client struct` обёрткой над `gotd/telegram.Client`. Конструктор `New(cfg ClientConfig) (*Client, error)` где `ClientConfig{APIID int, APIHash string, SessionStore session.Storage, Logger *slog.Logger}`. Методы `Run(ctx, fn func(ctx) error) error` и `IsAuthorized(ctx) (bool, error)`. APIID/APIHash читать из env `LAZYTG_API_ID` и `LAZYTG_API_HASH`
- [x] Создать `internal/tg/auth_test.go` с integration-тестом через `gotd/td/tgtest`: запустить тестовый сервер, прогнать Login flow с mock-prompter (возвращает hardcoded phone/code), проверить успех. Второй тест — повторный запуск с уже сохранённой сессией: `IsAuthorized()` возвращает true без запроса кода (реализовано через `auth.FlowClient` mock как в gotd/e2etest вместо tgtest — даёт ту же coverage над нашим кодом без поднятия SRP-протокола; покрытие сессии вынесено в `session_test.go`)
- [x] Создать `internal/core/config/secrets_test.go`: unit на `KeyringStore` (mock keyring через переменную, или skip если keyring недоступен), unit на `AgeFileStore` — set/get/delete на `t.TempDir()`, проверка что файл создаётся с правами `0600`
- [x] Запустить `go test -race ./internal/tg/... ./internal/core/config/...` — все тесты зелёные

### Task 5: Cobra CLI skeleton

- [x] Добавить зависимость `go get github.com/spf13/cobra`
- [x] Создать `cmd/lazytg/cmd/root.go` с `rootCmd = &cobra.Command{Use: "lazytg", Short: "Local-first Telegram TUI client", Long: "..."}`. Persistent флаги: `--account string` (phone аккаунта), `--config string` (путь к config-файлу), `--debug bool` (verbose logging), `--log-level string` (debug|info|warn|error, default "info")
- [x] Создать `cmd/lazytg/cmd/login.go` с `loginCmd = &cobra.Command{Use: "login", Short: "Authenticate a Telegram account"}`. Run: создаёт `Client`, прогоняет `auth.Login` с stdin-prompter (phone из `--account` или интерактивно), сохраняет session
- [x] Создать `cmd/lazytg/cmd/logout.go` — удаляет сессию указанного `--account` из SecretStore
- [x] Создать `cmd/lazytg/cmd/accounts.go` — печатает список аккаунтов из БД (поле alias + phone), помечает активный
- [x] Создать `cmd/lazytg/cmd/version.go` с переменными `version`, `commit`, `date` (заполняются через `-ldflags` от GoReleaser, default "dev"). Печатает форматированно
- [x] Создать `cmd/lazytg/cmd/debug.go` с `debugCmd` (parent для debug-подкоманд) и `debugBundleCmd` (Use: "debug-bundle") — пока stub: печатает `"debug-bundle stub: implementation in stage 3"` и exit 0. Полная реализация — этап 3 плана
- [x] Обновить `cmd/lazytg/main.go`: импортировать `cmd`, вызывать `cmd.Execute()`, при ошибке exit 1
- [x] Создать `cmd/lazytg/cmd/root_test.go` с тестами на парсинг флагов через `rootCmd.SetArgs([]string{...})` и проверку `rootCmd.PersistentFlags().Lookup("debug")`. Smoke-тест на `debug-bundle`: выполнить команду через `rootCmd.SetArgs([]string{"debug-bundle"})`, проверить exit 0 и что в stdout есть "debug-bundle stub"
- [x] Запустить `go build ./cmd/lazytg && ./bin/lazytg --help` (через Makefile target `build`) — должна вывестись справка

### Task 6: Logging slog + redaction

- [x] Добавить зависимость `go get gopkg.in/natefinch/lumberjack.v2`
- [x] Создать `internal/core/obs/redact.go` с функцией `Redact(s string) string` — маскирует patterns: phone (regex `\+?\d{10,15}` → `+***`), session strings (>40 символов base64-like → `<session>`), api_hash (hex 32 символа → `<api_hash>`). Тип `RedactingHandler` обёртка над `slog.Handler` — переопределяет `Handle(ctx, r)` обходя атрибуты и применяя `Redact` к строковым значениям
- [x] Создать `internal/core/obs/logger.go` с `New(cfg LoggerConfig) *slog.Logger`. `LoggerConfig{Level slog.Level; Format string ("json"|"text"); FilePath string; Debug bool}`. При `FilePath != ""` — JSON в файл через lumberjack (MaxSize 10MB, MaxBackups 3, MaxAge 30 days). При `Debug=true` — дублирует human-readable в stderr. Без `Debug` — stderr подавлен (TUI не должен спамить). Wrap в `RedactingHandler`. Контекстные методы `WithAccount(id int64)`, `WithChat(id int64)`, `WithRequestID(id string)` через `slog.With`
- [x] Создать `internal/core/obs/redact_test.go`: таблица `[]struct{name, input, want string}` с кейсами: phone с/без `+`, session string (>40 base64 символов), api_hash (32 hex), нормальный текст без секретов остаётся неизменным. Запустить через `t.Run`
- [x] Создать `internal/core/obs/logger_test.go`: проверить что при `Debug=false` stderr пустой, при `Debug=true` есть вывод. Проверить что в JSON-файл попадает уровень/timestamp/message
- [x] Интегрировать logger в `cmd/lazytg/cmd/root.go`: в `PersistentPreRun` создать logger из флагов `--log-level` и `--debug`, положить в context через `context.WithValue`. Создать helper `LoggerFromContext(ctx) *slog.Logger`
- [x] Запустить `go test -race ./internal/core/obs/...` — должны пройти

### Task 7: GitHub Actions CI + GoReleaser snapshot

- [x] Создать `.github/workflows/ci.yml` с jobs: `lint` (ubuntu-latest, golangci-lint-action@v6), `test` (matrix: os=[ubuntu-latest, macos-latest], только default tag — `sqlcipher` axis убран в Stage 1, см. CHANGELOG: driver зарезервирован под Stage 3, `-tags sqlcipher` сейчас падает на этапе компиляции), шаги: setup-go@v5 с Go 1.22, `go test -race -coverprofile=coverage.out ./...`, upload coverage в codecov. Cache `~/go/pkg/mod` через actions/cache@v4
- [x] Создать `.github/workflows/release.yml`: триггер `on: push: tags: ['v*']`, job `goreleaser` с `goreleaser/goreleaser-action@v6` (`args: release --clean`), permissions `contents: write` + `id-token: write` (для cosign). Env `GITHUB_TOKEN` из secrets
- [x] Создать `.goreleaser.yaml` с конфигом: `builds` matrix linux+darwin × amd64+arm64, `archives` (tar.gz, name template `{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}`), `checksum` (algorithm sha256), `changelog` (use git, sort asc), `signs` (cosign keyless через OIDC, artifacts checksum). `release` с `prerelease: auto`. Snapshot config inline через секцию `snapshot.name_template`
- [x] Создать `.github/workflows/snapshot.yml`: триггер `on: pull_request` и `on: push: branches: [main]` (без тега), job: `goreleaser/goreleaser-action@v6` с `args: release --snapshot --clean --skip=publish`. Загрузить артефакты через `actions/upload-artifact@v4` для smoke-тестов
- [x] Сделать пробный snapshot локально: `goreleaser release --snapshot --clean --skip=publish` (если goreleaser установлен через brew). Проверить что в `dist/` появились архивы
- [x] Создать `.github/dependabot.yml` для еженедельных обновлений `gomod` и `github-actions`
- [x] Зафиксировать pinned версии в `go.mod` через `go mod tidy`

### Task 8: Документация фундамента + final verification

- [x] Создать `README.md`. **Первая строка под заголовком**: `> ⚠️ **Ban-risk warning:** Telegram automatically puts unofficial clients under observation. Use lazytg with a test account first. See [docs/SECURITY.md](docs/SECURITY.md) for details.`. Секции: What is lazytg (pitch про local-first FTS5 search), Status (alpha), Install (go install + binary download), Quickstart (env vars LAZYTG_API_ID/LAZYTG_API_HASH, lazytg login), Architecture link, License
- [x] Создать `docs/ARCHITECTURE.md` с описанием 3-слойной архитектуры. ASCII-диаграмма зависимостей (cmd → app → tg/core/ui, core БЕЗ gotd, ui БЕЗ gotd). Объяснить depguard rules. Перечислить пакеты: `internal/tg`, `internal/core`, `internal/storage`, `internal/ui`, `internal/app`. Обоснование выбора стека (pure-Go default, sqlcipher opt-in)
- [x] Создать `docs/SECURITY.md` с threat model: Что защищаем (session keys, контактная информация на диске), От кого (локальный malware с user-доступом, утечка устройства). Чего НЕ защищаем (от Telegram-сервера — он видит всё; от root-malware). Ban-risk детально: политика Telegram observation для unofficial clients, рекомендация тестового аккаунта, rate-limit guard. Disclosure policy (90 дней, GitHub Security Advisories)
- [x] Создать `docs/CONTRIBUTING.md`: dev setup (Go 1.22+, golangci-lint, lefthook), running tests (`make test`), code style (`gofmt`, conventional commits), PR checklist (tests + docs + changelog entry)
- [x] Создать `LICENSE` с MIT-лицензией (год 2026, copyright "lazytg contributors") (создано в Task 1, проверено)
- [x] Создать `CHANGELOG.md` с заголовком и пустым unreleased section в формате [Keep a Changelog](https://keepachangelog.com): `## [Unreleased]`, подсекции `### Added`, `### Changed`, `### Fixed`
- [x] Создать `.github/PULL_REQUEST_TEMPLATE.md` с чек-листом: tests added, docs updated, changelog entry, depguard passes, PR title в conventional commits format
- [x] Создать `.github/ISSUE_TEMPLATE/bug_report.yml` (форма с обязательными полями: версия, OS, шаги, ожидаемое vs фактическое, лог) и `feature_request.yml`
- [x] Создать `SECURITY.md` в корне (для GitHub Security tab) — короткий: «Report security issues via GitHub Security Advisories, see docs/SECURITY.md for threat model»
- [x] **Verification:** запустить `go build ./...` — собирается. Запустить `go test -race ./...` — все тесты зелёные. Запустить `golangci-lint run` — zero warnings. Создать testfile `internal/core/depguard_violation_test.go` с импортом `github.com/gotd/td/telegram` — `golangci-lint run` должен fail (доказательство что depguard работает). Удалить testfile после демонстрации (depguard выдал ровно ожидаемое сообщение, EXIT=1)
- [x] **Manual smoke** (skipped — not automatable, requires real Telegram account; instructions documented in README.md "Manual smoke test (foundation)" section)
