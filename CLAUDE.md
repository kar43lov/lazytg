# CLAUDE.md — lazytg

Файл инструкций для Claude Code. Содержит агрегированную информацию из брейншторма + ссылки на актуальные планы.

> ⚠️ **Ban-risk warning:** Telegram автоматически ставит unofficial-клиенты под observation (см. [официальную политику](https://core.telegram.org/api/obtaining_api_id)). Использовать lazytg сначала с тестовым аккаунтом. После дела Дурова в августе 2024 enforcement резко вырос. Все технические решения (rate-limit guard, минимизация поведенческого следа, recommended ratelimit) ориентированы на снижение риска.

---

## Что делаем

**lazytg** — Telegram TUI-клиент на Go, направление **«Local-first power-tool»**.

**Pitch:** «Instant search across your entire Telegram history» — локальный SQLite FTS5-индекс, мгновенный поиск офлайн, без зависимости от серверного `messages.search` (известный pain point Telegram Desktop).

**MVP scope:** read + reply + files. Auth phone+code+2FA, список чатов, чтение истории, отправка текста, reply, отправка/скачивание файлов, live-обновления, локальный поиск, командная палитра.

**Целевой пользователь:** разработчик, живущий в tmux+nvim+ssh.

---

## Технический стек (зафиксирован)

| Компонент | Выбор | Альтернативы (отброшены) | Обоснование |
|-----------|-------|---------------------------|-------------|
| Язык | Go 1.25+ (pinned в go.mod) | Python+Textual, Rust+ratatui | Идеологически близко lazygit, single binary cross-build без CGo |
| MTProto | `gotd/td` | TDLib через CGo, Bot API | Pure-Go, активный (релизы 2026), iyear/tdl на нём в production. CGo не решает ban-risk |
| TUI | `bubbletea` v2 + `lipgloss` + `bubbles` | gocui, tview, urwid | 10k+ apps, GitLab мигрирует с tview→bubbletea, Elm-архитектура |
| SQLite | `modernc.org/sqlite` | `mattn/go-sqlite3` (CGo) | Pure-Go, FTS5+trigram support, ~75% производительности CGo |
| Шифрование БД | build tag `sqlcipher` зарезервирован, но **не подключён** (отложен past v0.1) | По умолчанию | До реализации CGo-драйвера сборка `-tags sqlcipher` намеренно ломается на этапе компиляции (`internal/storage/sqlite/driver_sqlcipher.go`), чтобы не выпустить unencrypted binary под видом encrypted. БД unencrypted независимо от tag |
| Secrets | `zalando/go-keyring` | `99designs/keyring` | Активнее, проще API. Fallback — `filippo.io/age` |
| CLI | `spf13/cobra` | urfave/cli | Стандарт |
| Release | `goreleaser` + `cosign` keyless OIDC | Manual | Без приватных ключей, GitHub Actions native |

---

## Архитектура (3 слоя, изолированные через depguard)

Все слои реализованы (Stages 1-3 функционал + Stage 4 release infra). Для исторической версии с per-stage метками — `git log` на CLAUDE.md до коммита по итогам Stage 4 review.

```
cmd/lazytg/                                                      cobra entry point
└── cmd/{root,root_cmd,login,logout,accounts,version,debug,
       logger,runtime,reindex,tui}.go

internal/
├── tg/                                                              MTProto (gotd обёртка)
│   └── {client,auth,session,send,history,updates,files,
        floodwait,polling}.go
├── core/                                                            Domain + storage + sync. БЕЗ gotd/bubbletea.
│   ├── events/{events,bus}.go
│   ├── domain/types.go                                          Account/Chat/Message/ChatType/MediaInfo
│   ├── config/{paths,secrets}.go
│   ├── obs/{redact,logger,fanout,bundle,dbsize}.go
│   ├── sync/{history,live,send,ratelimit,reconnect,
        backfill,degradation}.go
│   ├── search/{index,parser,query,query_builder,reindex,
        service,lazy}.go
│   ├── files/{download,upload,store,dedup,progress,
        upload_progress,discard}.go
│   └── security/{permissions,send_ratelimit}.go
├── storage/sqlite/                                              pure-Go modernc; sqlcipher tag намеренно
│   ├── repo.go, migrations.go, driver_modernc.go,                  не компилируется (см. driver_sqlcipher.go)
│   │   driver_sqlcipher.go, files.go, frecency.go,
│   │   outgoing.go, peers.go, state_repo.go
│   └── migrations/0001_init.sql … 0008_messages_media.sql
├── ui/                                                          Bubble Tea v2 panes/overlays/keymap
│   ├── app/, input/, keymap/, overlay/, palette/, statusbar/
│   └── panes/{chats,thread,search,attach}/
└── app/                                                         DI: wire.go (App.Build + AttachClient)
```

**Depguard rules (CI gate):**
- `internal/core/*` НЕ может импортировать `gotd/td` или `charmbracelet/bubbletea`
- `internal/ui/*` НЕ может импортировать `gotd/td`
- `internal/storage/sqlite/*` НЕ может импортировать UI или TG

---

## UI/UX-решения (после dialectic-анализа)

| Решение | Делаем | НЕ делаем | Обоснование |
|---------|--------|-----------|-------------|
| Layout | **2-pane** (chats + thread) WeeChat-style | 3-pane lazygit-style | k4dy/telegramtui (точная копия lazygit-формулы) = 1★ на GitHub. Чат-домен ≠ git-домен (один тип объекта vs много) |
| Ввод | **emacs/readline** по умолчанию | Vim-modality default | 90% времени insert → mode confusion. WeeChat/irssi/mutt не модальные |
| Vim-mode | opt-in в v0.2 | минимальный в v0.1 | Половинчатая реализация даёт багрепорты «почему не работает X» |
| Длинные сообщения | `$EDITOR` по hotkey **Ctrl-E** | `$EDITOR` как default ввод | 200-500ms cold start невыносимо для коротких ответов |
| Палитра | **Ctrl-Space (leader-key)**, L1 chat switcher (top-50 frecency) в v0.1 | Ctrl-P bind, L2 в v0.1 | Ctrl-P конфликт с tmux/readline/Telegram Desktop. L2 → v0.2 для сужения scope |
| Fuzzy | Unicode normalization (NFKD + lowercase + drop diacritics) | sahilm/fuzzy без normalization | "Алёна" === "Алена" — критично для русскоязычной аудитории |
| Inline media | НЕТ в v0.1 | Kitty/iTerm/sixel в v0.1 | Месяцы работы (`tgt` это знает). Vложение → `[photo.jpg, 234 KB] press d` |
| Multi-account | флаг `--account` в v0.1, UI switcher в v0.2 | Полный multi-account UI с v0.1 | Архитектурно подготовлено через `dataDir`, UI — позже |

---

## FTS5 + поиск

- **Tokenizer:** `trigram` (встроенный в SQLite ≥3.34) — language-agnostic, работает для русского без ICU. ICU не поддерживается modernc/sqlite.
- **Lazy index:** последние **5000** сообщений на чат по умолчанию (configurable). Полная история — по требованию через `Reindex`.
- **WAL mode** для smoothing flood-updates. Single-writer SQLite — приемлемо при default cap.
- **DB size monitoring:** warning в status bar при размере БД > 1GB (trigram даёт overhead 3-5× от текста).
- **Query operators:** `from:@user`, `in:#chat`, `before:DATE`, `after:DATE`, `has:file`, phrase `"exact"`, exclusion `-word`.
- **SLA:** p95 search latency <100ms на 100k сообщений (benchmark gate в CI).

---

## Live-updates

- **gotd `updates.Manager`** через `StateStorage` поверх SQLite (5 методов, ~50 строк)
- **Fallback на polling 3s** через флаг `--polling` (флаг зарезервирован, no-op в v0.1 — wire-up в `runTUI` рядом с `AttachClient`, см. CHANGELOG `Known gaps`)
- **SLA:** latency MTProto-update → UI render <500ms p95 (benchmark в этапе 2)

---

## Безопасность

- **Session storage:** `zalando/go-keyring` (Keychain/Secret Service/wincred). Fallback — `filippo.io/age`-encrypted file с master-passphrase из stdin
- **Permissions check** при старте: session/config файлы `0600`, директории `0700` → fail-fast
- **Rate-limit guard на send:** max 10 msg/sec — снижает поведенческий ban-trigger
- **`debug-bundle`** не включает session/api_hash/тексты сообщений (доказано grep-тестом)
- **`$EDITOR` env-filter** (только PATH/HOME/TERM/LANG/EDITOR) → v0.2

---

## Релиз-pipeline

- **Cross-build:** linux+darwin × amd64+arm64. Windows amd64 → v0.2 (отдельная боль с TUI).
- **Pure-Go только.** `-tags sqlcipher` зарезервирован, отложен past v0.1 — сборка под этим тегом намеренно ломается на этапе компиляции (см. `internal/storage/sqlite/driver_sqlcipher.go`). Реальный CGo-драйвер — в следующем релизе.
- **Cosign keyless** через GitHub OIDC (sigstore bundle в release). macOS notarization → v0.2 ($99/год Apple ID).
- **Distribution:** Homebrew tap (`pgmac/homebrew-lazytg`), `.deb` + `.rpm` через nfpm.
- **Pre-release pipeline:** `v0.1.0-alpha.N` → `beta.N` → `rc.N` → `v0.1.0`. Brew/scoop НЕ обновляются для alpha/beta.
- **Changelog:** `git-cliff` + commitlint, conventional commits enforced.

---

## Текущий статус и планы

| Артефакт | Путь | Описание |
|----------|------|----------|
| Полный план продукта | [`docs/plans/lazytg-v0.1.0.md`](docs/plans/lazytg-v0.1.0.md) | 4 этапа × 10 недель × 180 часов. Прошёл deep brainstorm + dialectic + Plan-Reviewer APPROVED |
| Stage 1 план (выполнен) | [`docs/plans/completed/20260502-lazytg-stage1-foundation.md`](docs/plans/completed/20260502-lazytg-stage1-foundation.md) | Foundation: bootstrap + архитектура + storage + auth + CLI + logging + CI + docs |
| Stage 2 план (выполнен) | [`docs/plans/completed/20260502-lazytg-stage2-tui.md`](docs/plans/completed/20260502-lazytg-stage2-tui.md) | TUI 2-pane + send/reply + live-updates + $EDITOR + reconnect |
| Stage 3 план (выполнен) | [`docs/plans/completed/20260503-lazytg-stage3-search-files.md`](docs/plans/completed/20260503-lazytg-stage3-search-files.md) | FTS5 search + files + debug-bundle + security minimal |
| Stage 4 план (выполнен) | [`docs/plans/completed/20260503-lazytg-stage4-release.md`](docs/plans/completed/20260503-lazytg-stage4-release.md) | GoReleaser + cosign + brew + .deb/.rpm + commitlint + memory budgets + user docs |

v0.1.0 готов к тегированию: код-функционал Stages 1-3 + release infra Stage 4. Manual smoke и stable tag — за maintainer'ом (см. [`docs/RELEASE_PROCESS.md`](docs/RELEASE_PROCESS.md)).

---

## Roadmap

### v0.1.0 (выполнен — все 4 этапа shipped)
- **Этап 1:** ✅ Foundation — bootstrap, depguard, storage+FTS5 spike, auth, CLI, logging, CI, docs
- **Этап 2:** ✅ TUI 2-pane + чтение + send/reply + live-updates + $EDITOR + reconnect
- **Этап 3:** ✅ FTS5 + парсер + search UI + палитра L1 + files + debug-bundle + security minimal
- **Этап 4:** ✅ GoReleaser + cosign + brew tap + .deb/.rpm + commitlint + git-cliff + memory budgets + user docs + beta runbook

### v0.2 (4-6 недель после v0.1.0)
- Vim-mode полный (normal/insert/visual + базовые motions)
- Палитра L2 (global commands через `>` префикс)
- expvar metrics + trace mode + `lazytg debug stats`
- $EDITOR sandbox env-filter
- Multi-account UI switcher
- Windows билды

### v0.3+
- Inline media preview (Kitty/iTerm/sixel через `BourgeoisBear/rasterm`)
- tgql — query-DSL с saved searches (smart folders)
- Forwarding messages, edit history, reactions

### v0.5+ (если будет community)
- Starlark hooks (`google/starlark-go`, pure-Go)
- AI-layer (Claude API + Ollama локально, prompt caching на длинной истории)
- CLI pipe-режим (single-process, не daemon)

---

## Что НЕ делаем никогда (явный non-goal, после dialectic)

| Антипаттерн | Почему |
|-------------|--------|
| 3-pane lazygit-style | Категориальная ошибка для чат-домена. k4dy/telegramtui = 1★ |
| Vim-modality default | Mode confusion в input-heavy задаче |
| Ctrl-P bind | Конфликт с tmux/readline/Telegram Desktop |
| `$EDITOR` как default ввод | Latency 200-500ms невыносим |
| CLI daemon отдельно от TUI | Две сессии = device slot конфликты + ban-risk |
| «Helix для общения» pitch | Niche подмножества подмножества (~3% vim-аудитории) |
| Полная индексация всей истории сразу | 10s of GB на активном пользователе |
| Обёртки над gotd (gotgproto и др.) | Лишний слой = боль и баги |
| `internal/core/contracts/` «на будущее» | YAGNI. Plugin API → v0.2+ по реальной потребности |
| Plugin API в v0.1 | Feature creep всегда v∞ |
| Web UI / REST API | Не наша ниша (есть Telegram Web) |
| Voice/video calls | Требуют tgvoip C-library — противоречит pure-Go стеку |

---

## Команды разработки

| Команда | Что делает |
|---------|-----------|
| `make build` | Сборка `bin/lazytg` (pure-Go) |
| `make test` | `go test -race ./...` |
| `make lint` | `golangci-lint run` (включает depguard rules) |
| `make tidy` | `go mod tidy` |
| `make clean` | Очистка `bin/`, `dist/`, coverage |
| `goreleaser release --snapshot --clean --skip=publish,sign` | Локальный прогон release pipeline |
| `lefthook install` | Регистрация pre-commit-хуков (gofmt, go vet, go test -short) + commit-msg (commitlint via npx с bash-fallback) |
| `git-cliff --tag <ver> --unreleased --prepend CHANGELOG.md` | Регенерация секции Unreleased перед тегированием (см. [`docs/RELEASE_PROCESS.md`](docs/RELEASE_PROCESS.md)) |

`commitlint` подключён через `lefthook` commit-msg hook (`.commitlintrc.yml` + bash-fallback) и через `amannn/action-semantic-pull-request@v5` в CI (`ci.yml` job `pr-title`). `git-cliff` подключён через `cliff.toml` для регенерации CHANGELOG перед тегом.

---

## Гайдлайны для Claude Code при работе с проектом

1. **Стек зафиксирован.** Не предлагать миграцию на TDLib, Bubble Tea v1, gocui, tview, mattn/go-sqlite3.
2. **Архитектурные слои.** Любой код в `internal/core/` НЕ должен импортировать gotd или bubbletea (depguard защитит, но проверять при ревью).
3. **Tests-first для core.** Каждая задача в плане = тесты (unit/integration/e2e). Целевой coverage: `core` ≥80% (текущее ~81%), `ui` ≥60% (текущее ~79%) — codecov tracks, без hard CI gate.
4. **Conventional commits.** Allowed types (см. `.commitlintrc.yml` type-enum): `feat`, `fix`, `perf`, `security`, `docs`, `refactor`, `test`, `chore`, `build`, `ci`. Breaking changes — через `!`-суффикс (`feat!:`, `fix(scope)!:`), не через тип `breaking`. Enforced через lefthook commit-msg hook (commitlint с bash-fallback) + CI pr-title job (`amannn/action-semantic-pull-request`).
5. **Никаких самостоятельных коммитов.** Коммиты только по явному запросу пользователя.
6. **YAGNI.** Если идея не в roadmap — не реализуем. Plugin API, multi-account UI, voice — отложены явно.
7. **Ban-risk first.** При любых изменениях auth/send/updates задумываться о поведенческом следе. Rate-limit guard будет добавлен вместе с send-path (Stage 2) и не отключается.
8. **Pure-Go default.** Любой PR с CGo-зависимостью требует обоснования и build tag. Для `sqlcipher` build tag запас сделан (отложен past v0.1, файл `driver_sqlcipher.go` намеренно не компилируется).
9. **Документация обновляется вместе с кодом.** PR без обновлённого CHANGELOG.md / docs/ — не мерджим.
10. **Платформа разработки macOS (zsh, brew).** Не предлагать Windows-специфичные решения (PowerShell, .bat).

---

## Контекст для freshcontextных Claude-сессий

Этот файл — **единственный source of truth** для нового Claude-инстанса, который не видел исходный брейншторм. Если нужен глубокий контекст по какому-то решению (например, почему отброшен 3-pane или почему trigram а не unicode61) — см. полный план [`docs/plans/lazytg-v0.1.0.md`](docs/plans/lazytg-v0.1.0.md), секция «Выбранный подход (после dialectic-анализа)» и «Что НЕ делаем никогда».

Все технические решения в этом файле прошли:
- 5-фазный брейншторм (Прагматик / Инноватор / Радикал)
- Dialectic-анализ (Thesis vs Antithesis с перекрёстным допросом)
- Синтез top-3 идей с выбором пользователя
- Plan-Reviewer (NEEDS REVISION → 10 правок → APPROVED)

Решения **defensible после стресс-теста**, не «первая идея в голове».
