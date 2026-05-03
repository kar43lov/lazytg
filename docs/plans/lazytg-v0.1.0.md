# План: lazytg v0.1.0 — Local-first Telegram TUI

## Цель

Выпустить за **8-10 недель** Telegram TUI-клиент с **локальным FTS5-поиском** по истории, серьёзным OSS-стандартом с первого дня (cosign-signed binaries, brew tap, multi-account через флаг).

## Pitch

> «Instant search across your entire Telegram history» — локальный SQLite FTS5-индекс, мгновенный поиск офлайн, без зависимости от серверного messages.search (известный pain point Telegram Desktop).

## Выбранный подход (после dialectic-анализа)

**Направление B: Local-first power-tool.** Идеи, выжившие после Thesis vs Antithesis с перекрёстным допросом:
- **SQLite FTS5 (trigram tokenizer)** — единственная идея со 100% confidence у обеих сторон. Telegram iOS использует SQLCipher+FTS5 — production reference.
- **2-pane TUI WeeChat-style** (chats list + thread). 3-pane lazygit-style отброшен: k4dy/telegramtui — точная копия идеи — имеет 1★ на GitHub, lazygit-формула не переносится на чат-домен (доказано x83 разрывом тяги).
- **Emacs/readline ввод по умолчанию.** Vim-modality в input-heavy задаче (90% времени insert) даёт mode confusion. WeeChat/irssi/mutt все НЕ модальные.
- **$EDITOR delegation как hotkey Ctrl-E**, НЕ default ввод. Для длинных сообщений. 20 строк Go, win-win.
- **Командная палитра на leader-key (Ctrl-Space)**, НЕ Ctrl-P (конфликт с tmux/readline). В v0.1 — L1 chat switcher (top-50 по frecency). L2 global commands — v0.2.

## Технический стек (зафиксирован)

| Компонент | Выбор | Обоснование |
|-----------|-------|-------------|
| Язык | Go 1.22+ | Идеологически близко lazygit/lazydocker, single binary cross-build без CGo |
| MTProto | `gotd/td` | Pure-Go, активный (релизы 2026), достаточно зрел (iyear/tdl на нём в production) |
| TUI | `bubbletea` v2 + `lipgloss` + `bubbles` | 10k+ apps built with, GitLab мигрирует с tview→bubbletea, Elm-architecture |
| SQLite | `modernc.org/sqlite` | Pure-Go, поддерживает FTS5+trigram, ~75% производительности CGo |
| Secrets | `zalando/go-keyring` | Активнее 99designs/keyring, проще API |
| CLI | `spf13/cobra` | Стандарт de facto |
| Release | `goreleaser` + `cosign` keyless OIDC | Без приватных ключей, GitHub Actions native |
| Шифрование БД | opt-in через build tag `sqlcipher` | CGo SQLCipher для serious users, plain — default |

## Архитектура (3 слоя, изолированные через depguard)

```
cmd/lazytg/                  ← cobra entry point
├── main.go
└── cmd/{root,login,logout,accounts,version,debug-bundle}.go

internal/
├── tg/                      ← MTProto (gotd обёртка)
│   ├── client.go, auth.go, session.go
│   ├── send.go, history.go, updates.go, files.go
│   └── floodwait.go
├── core/                    ← Domain + storage + sync, БЕЗ gotd, БЕЗ bubbletea
│   ├── events/bus.go        ← типизированный pub/sub
│   ├── config/{config,secrets,paths}.go
│   ├── obs/{logger,redact}.go
│   ├── sync/{history,live,send,ratelimit,reconnect}.go
│   ├── search/{index,parser,query}.go
│   ├── files/{download,upload,store}.go
│   └── security/{permissions,ratelimit}.go
├── storage/sqlite/          ← реализация Repo (build tags pure|sqlcipher)
│   ├── repo.go, migrations.go
│   └── migrations/0001_init.sql, 0002_fts.sql
├── ui/                      ← Bubble Tea, БЕЗ gotd
│   ├── app/{model,update,view,keys}.go
│   ├── panes/{chats,thread,search}/...
│   ├── input/{model,emacs,editor}.go
│   ├── palette/{model,registry}.go
│   ├── statusbar/{model,view}.go
│   ├── overlay/help.go
│   └── keymap/{loader,defaults}.go
└── app/wire.go              ← DI без фреймворка, явный конструктор
```

**`depguard` rules** (CI gate):
- `internal/core/*` НЕ может импортировать `gotd/td` или `charmbracelet/bubbletea`
- `internal/ui/*` НЕ может импортировать `gotd/td`
- `internal/storage/sqlite/*` НЕ может импортировать UI или TG

---

## Этап 1: Фундамент (недели 1-2, ~30-40 часов)

### Задачи

**1.1. Bootstrap + tooling**
- `go.mod` (Go 1.22+), `Makefile` (build/test/lint), `.editorconfig`, `.gitignore`
- `.golangci.yml`: errcheck, govet, staticcheck, gosec, revive, gocritic, depguard
- `lefthook.yml`: pre-commit fmt+lint+test
- **Файлы:** `Makefile`, `.golangci.yml`, `lefthook.yml`, `tools/tools.go`
- **Тесты:** depguard как **CI gate** (`golangci-lint run --enable-only depguard` → fail при нарушении)

**1.2. Скелет 3-слойной архитектуры + event bus**
- Пустые пакеты с `doc.go`
- Event bus: типизированные `MessageReceived`, `DialogUpdated`, `AuthStateChanged`, `ConnectionStateChanged` на каналах с fan-out
- **Файлы:** `internal/{tg,core,ui,app}/doc.go`, `internal/core/events/{bus,events}.go`
- **Тесты:** unit `events/bus_test.go` — fan-out, отписка, **goleak** (zero goroutine leaks)

**1.3. Storage (modernc.org/sqlite) + FTS5 spike**
- Pure-Go default; build tag `sqlcipher` → CGo SQLCipher (opt-in)
- WAL on, foreign_keys on, embedded SQL миграции
- Схема v1: `accounts`, `chats`, `messages`, `peers`, `state`, `schema_migrations`
- **FTS5 spike:** в первый день этапа 1 проверить что `CREATE VIRTUAL TABLE ... USING fts5(text, tokenize='trigram')` работает на modernc/sqlite. Если нет — fallback на porter tokenizer ДО продолжения (избегаем переделки на 5-й неделе).
- **Файлы:** `internal/storage/sqlite/{repo,migrations,driver_modernc,driver_sqlcipher}.go`, `internal/storage/sqlite/migrations/0001_init.sql`
- **Тесты:** unit `repo_test.go` (CRUD на `:memory:`), `migrations_test.go` (fresh+idempotent), **`fts5_spike_test.go`** (создать virtual table с trigram, вставить 100 строк, search → проверить hit)

**1.4. Auth flow (gotd/td/examples/auth — copy-paste)**
- phone → code → 2FA, callback `CodePrompter` (UI задаст вопрос)
- Session storage поверх `zalando/go-keyring`
- **Fallback на encrypted file:** `age`-encryption с master-passphrase из stdin при старте (документировать)
- Permissions session-файла `0600` проверяется при старте (fail-fast)
- API_ID/API_HASH из env (`LAZYTG_API_ID`, `LAZYTG_API_HASH`)
- **Файлы:** `internal/tg/{client,auth,session}.go`, `internal/core/config/secrets.go`
- **Тесты:** integration `auth_test.go` через gotd `tgtest`, unit на keyring (mock), **fallback-test:** age-encryption работает без D-Bus

**1.5. Cobra CLI skeleton**
- Команды: `login`, `logout`, `accounts`, `version`, `debug-bundle` (stub), `lazytg` (TUI stub)
- Флаги: `--account`, `--config`, `--debug`, `--log-level`
- **Файлы:** `cmd/lazytg/{main,cmd/{root,login,logout,accounts,version,debug}}.go`
- **Тесты:** unit на парсинг флагов, **smoke на `debug-bundle` cobra-binding** (хотя бы exit 0 со stub-выводом)

**1.6. Logging (slog) + redaction**
- JSON в `~/.local/state/lazytg/logs/lazytg.log` с rotation (`natefinch/lumberjack`)
- Human-readable stderr только при `--debug`
- Контекстные поля: `account_id`, `chat_id`, `request_id`
- Redaction маскирует phone/session/api_hash
- **Файлы:** `internal/core/obs/{logger,redact}.go`
- **Тесты:** unit `redact_test.go` — таблица: phone/session/api_hash → masked

**1.7. GitHub Actions CI**
- `ci.yml`: lint + test (linux/macos × pure/sqlcipher) + race detector + coverage upload
- `release.yml`: snapshot release на PR, tagged release на `v*`
- **Файлы:** `.github/workflows/{ci,release}.yml`, `.goreleaser.snapshot.yaml`
- **Тесты:** PR → CI зелёный, артефакты собираются

**1.8. Документация фундамента**
- `README.md` — **первой строкой ban-risk warning**: «Telegram automatically puts unofficial clients under observation. Use lazytg with a test account first.»
- `docs/ARCHITECTURE.md` — 3 слоя, dependency rules, диаграмма
- `docs/SECURITY.md` — threat model, что НЕ защищаем, ban-risk детально
- `docs/CONTRIBUTING.md` — dev setup
- `LICENSE` MIT

### Acceptance criteria этапа 1

- `lazytg login` проходит до конца на реальном аккаунте (manual smoke + tgtest integration в CI)
- `lazytg accounts` показывает список (multi-account через `--account` флаг)
- CI зелёный на 2 OS × 2 build-tags
- **depguard блокирует** попытку импорта gotd из `core` (доказано failing-тестом)
- **FTS5 trigram spike прошёл** на modernc/sqlite — задача-блокер на этап 3 снята
- Coverage `internal/core` ≥60%
- README + ARCHITECTURE + SECURITY + CONTRIBUTING написаны

---

## Этап 2: TUI + чтение + отправка (недели 3-5, ~50-60 часов)

### Задачи

**2.1. Bubble Tea root model**
- `App` model с двумя panes (ChatsList, Thread), focus state, status bar
- Window resize handling, минимум 80×24, graceful degradation ниже
- **Файлы:** `internal/ui/app/{model,update,view,keys}.go`
- **Тесты:** unit через `teatest` (input msg → state), **тест resize** на 60×20 → graceful

**2.2. Chats pane**
- `bubbles/list` с unread counter, last message preview, timestamp
- Сортировка по pinned/last_message_date, виртуализация
- **Файлы:** `internal/ui/panes/chats/{model,item,view}.go`
- **Тесты:** unit selection/navigation, golden snapshots

**2.3. Thread pane**
- `bubbles/viewport`, рендер сообщений (автор/текст/timestamp/reply-to)
- Inline-форматирование bold/italic/code/link через Lipgloss. **БЕЗ медиа-превью.**
- **Файлы:** `internal/ui/panes/thread/{model,message,view,format}.go`
- **Тесты:** unit на формат (golden), pagination

**2.4. Input field (emacs/readline)**
- `bubbles/textarea` с переопределёнными bindings: Ctrl-A/E/K/U/W, Alt-B/F, history Ctrl-P/N
- Send: Enter; newline: Alt-Enter
- Reply: Ctrl-R на выбранном сообщении
- **Vim-mode полностью отложен на v0.2** (избегаем половинчатой реализации)
- **Файлы:** `internal/ui/input/{model,emacs,history}.go`
- **Тесты:** unit `emacs_test.go` (Ctrl-A/E/K/W → buffer)

**2.5. $EDITOR delegation (Ctrl-E)**
- `tea.ExecProcess`, temp в `os.UserCacheDir()/lazytg/edit-*.md` с `0600`, удаление в defer
- **Файлы:** `internal/ui/input/editor.go`
- **Тесты:** unit с `$EDITOR=cat` — temp создаётся 0600, удаляется

**2.6. Status bar**
- account alias, chat name, unread total, connection state (connecting/online/offline/floodwait countdown)
- **Файлы:** `internal/ui/statusbar/{model,view}.go`
- **Тесты:** golden snapshots разных состояний

**2.7. Help overlay (`?`)**
- Modal со всеми keybindings, esc/q закрывает
- **Файлы:** `internal/ui/overlay/help.go`
- **Тесты:** unit показ/скрытие

**2.8. History sync через MTProto**
- Батч `messages.GetHistory` limit=100, lazy (только видимый чат + N выше/ниже)
- Backfill в фоне с rate-limit (gotd сам ретраит FloodWait)
- **Файлы:** `internal/core/sync/{history,backfill,ratelimit}.go`, `internal/tg/history.go`
- **Тесты:** integration с mock transport (батчинг, дедупликация)

**2.9. Live updates через `gotd updates.Manager`**
- Copy-paste из `gotd/td/examples/updates`
- `StateStorage` поверх SQLite (5 методов, ~50 строк)
- **Fallback на polling 3s** если updates.Manager создаст gap-проблемы — feature flag `--polling`
- События в bus → UI подписан, дедупликация при reconnect
- **Файлы:** `internal/tg/updates.go`, `internal/core/sync/live.go`
- **Тесты:** integration `live_test.go` — событие в bus → repo пишет → emit. **SLA: latency MTProto-update → UI render <500ms p95** (бенчмарк-тест)

**2.10. Send/reply с optimistic UI**
- `pending` → `sent` (success) → `failed` (network error)
- Retry с backoff на network errors, не на validation
- **Файлы:** `internal/core/sync/send.go`, `internal/ui/panes/thread/optimistic.go`
- **Тесты:** integration optimistic flow + retry

**2.11. Reconnect + graceful degradation**
- gotd disconnect → status "offline" + retry с exponential backoff
- **SQLite write failed → log + continue read-only** (явный тест degradation pathway)
- **Файлы:** `internal/core/sync/reconnect.go`
- **Тесты:** unit backoff, **integration: kill tgtest → reconnect; chmod БД 0444 → read-only mode**

**2.12. Keymap configurable**
- `~/.config/lazytg/keymap.toml` overrides defaults
- Conflict detection с понятной ошибкой
- **Файлы:** `internal/ui/keymap/{loader,defaults}.go`
- **Тесты:** unit merge + conflict detection

### Acceptance criteria этапа 2

- TUI запускается, реальные чаты+история отображаются
- Send/reply работает с optimistic update
- **Live-updates latency <500ms p95** от MTProto до UI render (доказано бенчмарком)
- $EDITOR работает (smoke на vim/nano/cat)
- Read-only degradation работает при недоступной записи в БД (доказано тестом)
- Все hotkeys в `?` overlay
- Coverage `core` ≥70%, `ui` ≥50%

---

## Этап 3: FTS5 + файлы + палитра L1 (недели 6-8, ~50-60 часов)

**Урезано** из плана-Качества: палитра только L1 (chat switcher), L2 global commands — v0.2. expvar metrics + trace mode — v0.2. $EDITOR sandbox env-filter — v0.2. Это даёт реалистичные 50-60ч вместо 80ч.

### Задачи

**3.1. FTS5 schema + trigram tokenizer**
- `CREATE VIRTUAL TABLE messages_fts USING fts5(text, tokenize='trigram')`
- Триггеры insert/update/delete для авто-sync с `messages`
- **Файлы:** `internal/storage/sqlite/migrations/0002_fts.sql`, `internal/core/search/index.go`
- **Тесты:** unit trigram corner cases (короткие <3 символов, юникод, emoji)

**3.2. Lazy index + reindex command**
- При первом search или явной команде Reindex прогон через горутину с прогрессом в status bar
- Default: индексировать только последние 5000 сообщений на чат, configurable
- **Graceful cancel** при закрытии TUI (context.Cancel)
- **Файлы:** `internal/core/search/{index,reindex}.go`
- **Тесты:** integration — старт reindex → отмена через context → нет corrupt state

**3.3. DB size monitoring**
- Метрика размера БД, warning в status bar при >1GB
- Документация в `docs/SEARCH.md` про trigram-overhead (3-5× от текста)
- **Файлы:** `internal/core/obs/dbsize.go`
- **Тесты:** unit на triggering thresholds

**3.4. Search query parser**
- Операторы: `from:@user`, `in:#chat`, `before:DATE`, `after:DATE`, `has:file`
- Phrase search `"exact"`, exclusion `-word`
- Парсер ручной (~150 строк)
- **Файлы:** `internal/core/search/{parser,query}.go`
- **Тесты:** unit таблица query → AST, edge cases

**3.5. Search UI**
- Активация `/` (configurable), отдельная overlay-pane
- Live results debounce 150ms, highlight через `snippet()` FTS5
- Jump-to-message → открыть чат + scroll к контексту ±5 сообщений
- **Файлы:** `internal/ui/panes/search/{model,view,results}.go`
- **Тесты:** e2e через teatest — `/query` → результаты → Enter → правильный чат+скролл (snapshot)

**3.6. Командная палитра L1 (Ctrl-Space)**
- **Только L1 в v0.1:** chat switcher по top-50 frecency
- Fuzzy через `sahilm/fuzzy` + **Unicode normalization (NFKD + lowercase + drop diacritics)** для "Алёна"=="Алена"
- Frecency hot-set: только top-200 чатов в индексе (избегаем O(N log N) на 5000)
- L2 global commands → v0.2
- **Файлы:** `internal/ui/palette/{model,registry,frecency}.go`
- **Тесты:** unit fuzzy ranking + Unicode normalization, frecency decay

**3.7. Files: download (Ctrl-D)**
- `gotd downloader.NewDownloader().Download(...)`, прогресс через event bus → status bar
- Сохранение в `~/Downloads/lazytg/<chat>/<filename>` (configurable)
- Дедупликация по file_id (симлинк если уже скачано)
- **Документировать отсутствие resume** для прерванных загрузок (явный non-goal v0.1)
- **Файлы:** `internal/core/files/{download,store}.go`, `internal/tg/files.go`
- **Тесты:** integration mock transport, прогресс events, дедупликация

**3.8. Files: upload (Ctrl-U)**
- `bubbles/filepicker`, send с caption, warning >50MB, hard limit 2GB
- **Файлы:** `internal/core/files/upload.go`, `internal/ui/input/attach.go`
- **Тесты:** integration на загрузку 1MB файла через tgtest

**3.9. `lazytg debug-bundle` (полная реализация)**
- Собирает: logs (последние N строк), config (с redaction!), version, OS/arch, db_size, schema_version
- **НЕ включает:** session files, api_hash, тексты сообщений
- Tar.gz в текущую директорию
- **Файлы:** `cmd/lazytg/cmd/debug.go`, `internal/core/obs/bundle.go`
- **Тесты:** integration **+ grep-тест** на отсутствие api_hash/session/phone в bundle

**3.10. Security minimal**
- Permission checks при старте: `0600` session/config, `0700` директории. Warn или fail.
- Rate-limit guard на send: max 10 msg/sec (защита от ban при автоматизации)
- $EDITOR sandbox + expvar metrics + trace mode → **v0.2**
- **Файлы:** `internal/core/security/{permissions,ratelimit}.go`
- **Тесты:** unit permissions checks, ratelimit token bucket

### Acceptance criteria этапа 3

- Search работает, **p95 <100ms на 100k сообщений** (benchmark gate в CI)
- Files download/upload работают с прогрессом
- Палитра L1 работает с Unicode-fuzzy ("Алёна"=="Алена")
- DB size warning >1GB виден в status bar
- **debug-bundle без секретов** (доказано grep-тестом на api_hash/session/phone)
- Permission checks работают при старте
- Rate-limit включён на send
- Coverage `core` ≥80%

---

## Этап 4: Release (недели 9-10, ~25-35 часов)

### Задачи

**4.1. GoReleaser production**
- Matrix: linux+darwin × amd64+arm64 × (pure | sqlcipher)
- Windows amd64 — **отложить на v0.2** (отдельная боль с TUI keys/colors)
- Archives tar.gz, checksums sha256
- **Файлы:** `.goreleaser.yaml`

**4.2. Code signing — cosign keyless**
- Cosign через GitHub OIDC (sigstore bundle в release)
- macOS notarization → v0.2 (требует $99/год Apple ID)
- Windows signtool → когда добавим Windows билды
- **Файлы:** `.github/workflows/release.yml` (sign jobs), `docs/VERIFY.md`

**4.3. Homebrew tap**
- Отдельный репо `kar43lov/homebrew-lazytg`, GoReleaser auto-PR при тегировании
- Formula: `brew install kar43lov/lazytg/lazytg` (pure default)
- **Файлы:** `.goreleaser.yaml` brew section

**4.4. Linux packages (.deb, .rpm)**
- GoReleaser nfpm
- **Файлы:** `.goreleaser.yaml` nfpm section

**4.5. Changelog automation**
- `git-cliff` + commitlint в pre-commit (conventional commits enforce)
- `cliff.toml`: feat/fix/perf/security/breaking
- Auto-generate CHANGELOG.md в release PR
- **Файлы:** `cliff.toml`, `.commitlintrc.yml`, `CHANGELOG.md`

**4.6. Pre-release pipeline**
- Tags: `v0.1.0-alpha.N` → `beta.N` → `rc.N` → `v0.1.0`
- Brew/scoop НЕ обновляются для alpha/beta
- **Файлы:** `.github/workflows/release.yml` (conditional logic)

**4.7. Issue/PR templates + Security policy**
- Bug report (с обязательным `lazytg debug-bundle` upload, проверяется в форме)
- Feature request, security report (private GitHub Security Advisories)
- PR template с чеклистом (tests/docs/changelog)
- **Файлы:** `.github/ISSUE_TEMPLATE/*.yml`, `.github/PULL_REQUEST_TEMPLATE.md`, `SECURITY.md`

**4.8. Beta-period (1 неделя)**
- Релиз `v0.1.0-beta.1` для **5-10 тестеров**
- Каналы: r/commandline, lobste.rs, GitHub Discussions
- **Формализованный smoke-чеклист** для confirmation:
  - [ ] login прошёл без ошибок
  - [ ] чаты загрузились
  - [ ] прочитал и ответил на 3+ сообщения
  - [ ] скачал и отправил файл
  - [ ] поиск нашёл известное сообщение
  - [ ] перезапуск без re-auth
- Acceptance: **≥3 тестера заполнили чеклист с зелёными галочками**
- Hot-fix → beta.2/rc.1

**4.9. Финальная документация**
- `README.md` — gif/asciinema-cast (15 минут на запись)
- `docs/INSTALL.md` — brew/manual/go-install
- `docs/CONFIGURATION.md` — все опции config + keymap
- `docs/SEARCH.md` — query syntax + DB size guidance
- `docs/TROUBLESHOOTING.md` — частые проблемы, как собрать debug-bundle
- `docs/VERIFY.md` — cosign verify шаги

### Acceptance criteria v0.1.0

- GitHub Release с **подписанными бинарями** (4 артефакта: linux-{amd64,arm64}, darwin-{amd64,arm64} + checksums + sigstore bundles)
- `brew install kar43lov/lazytg/lazytg` работает
- `.deb` и `.rpm` доступны
- **≥3 тестера заполнили формализованный smoke-чеклист**
- CI зелёный, e2e smoke в CI проходит
- Coverage `core` ≥80%, `ui` ≥60%
- Memory budget: idle <50MB, active <150MB (документировано в `docs/PERFORMANCE.md`)

---

## Граф критического пути

```
1.1 → 1.2 → 1.3(+spike) ─┬─→ 1.4 → 1.5 → 1.6 → 1.7 (CI) ─┐
                         │                                 │
                         └─→ 2.1 → 2.2,2.3,2.4 → 2.8 → 2.9 ┴→ 2.10 → 2.11
                                  → 2.5 → 2.6 → 2.7 → 2.12

этап 2 done → 3.1 → 3.2 → 3.3,3.4 → 3.5 → 3.6 → 3.7,3.8 → 3.9 → 3.10

этап 3 done → 4.1 → 4.2 → 4.3,4.4 → 4.5 → 4.6 → 4.7 → 4.8 (beta) → 4.9 → v0.1.0
```

Критический путь: **1.3(+spike) → 1.4 → 2.1 → 2.8 → 2.9 → 2.10 → 3.1 → 3.5 → 4.8** ≈ 8-10 недель при 18ч/неделя.

---

## Реалистичный timeline

| Неделя | Что делаем | Часы |
|--------|------------|------|
| 1 | 1.1, 1.2, 1.3 (+FTS5 spike), 1.6 | 18 |
| 2 | 1.4, 1.5, 1.7, 1.8 (auth + CLI + CI + docs) | 18 |
| 3 | 2.1, 2.2, 2.3, 2.4, 2.6 (TUI скелет + чтение) | 20 |
| 4 | 2.5, 2.7, 2.8, 2.12 (editor + help + sync + keymap) | 18 |
| 5 | 2.9, 2.10, 2.11 (live + send + reconnect) | 20 |
| 6 | 3.1, 3.2, 3.3, 3.4 (FTS5 + parser) | 20 |
| 7 | 3.5, 3.6, 3.7, 3.8 (search UI + palette + files) | 20 |
| 8 | 3.9, 3.10 (debug-bundle + security) | 12 |
| 9 | 4.1-4.7 (release infra) + start beta | 20 |
| 10 | 4.8 beta + 4.9 docs + v0.1.0 release | 14 |

**Итого: 180 часов / 10 недель.** Буфер 1-2 недели на непредвиденное → реалистично **8-12 недель**.

---

## Риски и mitigation

| Риск | Severity | Mitigation |
|------|----------|------------|
| Telegram бан userbot-аккаунта | HIGH | Ban-warning первой строкой README, rate-limit guard, FAQ, рекомендация тестового аккаунта |
| FTS5 trigram у modernc/sqlite не работает | HIGH | **Spike в 1.3 в первый день этапа 1.** Fallback на porter tokenizer |
| gotd ломает API между MTProto layer-changes | MEDIUM | Pin версия в go.mod, integration с tgtest replay-фикстурами |
| Bubble Tea v2 нестабилен | LOW | Pin версия, изоляция в `internal/ui` |
| Keyring не работает headless (без D-Bus) | MEDIUM | Fallback на `age`-encrypted file с master-passphrase из stdin (тест в 1.4) |
| DB size раздувается | MEDIUM | Default cap 5000 msg/chat, monitoring + warning >1GB (3.3), документация SEARCH.md |
| Cosign требует org-level permissions | LOW | Fallback на GPG-signed checksums |
| Live-update latency >500ms | LOW | Бенчмарк-тест в 2.9, fallback на polling 3s через `--polling` флаг |
| 10 недель сорваны | MEDIUM | После недели 5 (этап 2 done) — оценить остаток; если behind schedule, урезать палитру L1 → v0.2 (search + files без палитры — приемлемый MVP) |

---

## Отложенные идеи (v0.2+)

**v0.2 (через 4-6 недель после v0.1.0):**
- Vim-mode полный (normal/insert/visual + базовые motions)
- Палитра L2 (global commands через `>` префикс)
- expvar metrics + trace mode + `lazytg debug stats`
- $EDITOR sandbox env-filter
- Multi-account UI switcher (флаг `--account` уже работает с v0.1)
- Windows билды

**v0.3+:**
- Inline media preview (Kitty/iTerm/sixel graphics protocol через `BourgeoisBear/rasterm`)
- tgql — полноценный query-DSL с saved searches
- Forwarding messages, edit history, reactions
- Sticker picker

**v0.5+ (если будет community):**
- Starlark hooks (события on_message/on_reaction/on_edit) — `google/starlark-go` pure-Go
- AI-layer (саммари каналов через Claude API с prompt caching + Ollama локально)
- CLI pipe-режим как отдельный подкоманды (с тем же device slot, не daemon)

---

## Что НЕ делаем никогда (явный non-goal)

- **3-pane lazygit-style.** Категориальная ошибка для чат-домена. k4dy/telegramtui (точная копия идеи) = 1★. lazygit-формула не переносится (x83 разрыв тяги).
- **Vim-modality default.** Mode confusion в input-heavy задаче (90% времени insert).
- **Ctrl-P bind.** Конфликт с tmux/readline/Telegram Desktop.
- **$EDITOR как default ввод.** Latency 200-500ms невыносим для коротких ответов.
- **CLI daemon отдельно от TUI.** Две сессии = device slot конфликты + повышенный ban-risk.
- **«Helix для общения» pitch.** Niche подмножество подмножества (~3% vim-аудитории).
- **Полная индексация всей истории.** 10s of GB на активном пользователе. Только last 5000/chat в default.
- **Обёртки над gotd** (gotgproto и др.). gotd сам уже обёртка — ещё один слой = боль и баги.
- **`internal/core/contracts/` "на будущее"** для plugin API. Plugin API строго v0.2+, и тогда добавим интерфейсы по реальной потребности.
- **Plugin API в v0.1.** Это feature creep всегда v∞.
- **Web UI / REST API.** Не наша ниша (есть Telegram Web).
- **Voice/video calls.** Требуют tgvoip C-library — противоречит pure-Go стеку.

---

## Валидация

**Plan-Reviewer вердикт (после 1 цикла правок):** APPROVED с учётом 10 правок:

1. ✅ FTS5 spike перенесён в 1.3 (этап 1, день 1) — было риском "5-я неделя".
2. ✅ Vim-mode полностью убран из v0.1 (был "минимальный normal/insert" — половинчато). v0.2.
3. ✅ Honest timeline 8-10 недель (было 6-8). Этап 3 урезан: палитра L2 / expvar / trace / $EDITOR sandbox → v0.2.
4. ✅ `internal/core/contracts/` удалён из плана. Plugin API → v0.2.
5. ✅ Trace mode → v0.2.
6. ✅ Keyring fallback указан конкретно: `age`-encryption с master-passphrase (1.4).
7. ✅ DB size monitoring добавлен (3.3) + документация в SEARCH.md.
8. ✅ Acceptance этапа 2: численный SLA на live-updates latency <500ms p95.
9. ✅ Acceptance этапа 4: формализованный 6-пунктный smoke-чеклист для confirmation.
10. ✅ Tests gaps закрыты: window resize (2.1), SQLite read-only degradation (2.11), reindex cancel (3.2), debug-bundle cobra smoke (1.5).
