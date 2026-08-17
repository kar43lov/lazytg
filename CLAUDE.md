# CLAUDE.md — lazytg

Файл инструкций для Claude Code: правила, гочи, статус и ссылки. Справочники (архитектура, поиск, безопасность, релиз) живут в `docs/` — здесь их не дублируем, чтобы файл не грузил контекст каждой сессии.

> ⚠️ **Ban-risk warning:** Telegram автоматически ставит unofficial-клиенты под observation (см. [официальную политику](https://core.telegram.org/api/obtaining_api_id)). Использовать lazytg сначала с тестовым аккаунтом. После дела Дурова в августе 2024 enforcement резко вырос. Все технические решения (rate-limit guard, минимизация поведенческого следа, recommended ratelimit) ориентированы на снижение риска.

---

## Что делаем

**lazytg** — Telegram TUI-клиент на Go, направление **«Local-first power-tool»**.

**Pitch:** «Instant search across your entire Telegram history» — локальный SQLite FTS5-индекс, мгновенный поиск офлайн, без зависимости от серверного `messages.search` (известный pain point Telegram Desktop).

**MVP scope:** read + reply + files. Auth phone+code+2FA, список чатов, чтение истории, отправка текста, reply, отправка/скачивание файлов, live-обновления, локальный поиск, командная палитра.

**Целевой пользователь:** разработчик, живущий в tmux+nvim+ssh.

---

## Где что лежит (справочники, не дублировать здесь)

| Нужно | Файл |
|-------|------|
| Раскладка пакетов, карта файлов, обоснование выбора каждой библиотеки, depguard, режимы сборки | [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) |
| Синтаксис поиска, tokenizer, lazy index, ёмкость БД, SLA | [`docs/SEARCH.md`](docs/SEARCH.md) |
| Модель угроз, хранение сессий, вшитые креды, permissions, rate-limit | [`docs/SECURITY.md`](docs/SECURITY.md) |
| Тегирование, cosign, brew tap, alpha→beta→rc→stable | [`docs/RELEASE_PROCESS.md`](docs/RELEASE_PROCESS.md) |
| `make`-таргеты, установка, `lefthook install`, git-cliff | [`README.md`](README.md), [`docs/CONTRIBUTING.md`](docs/CONTRIBUTING.md) |
| Почему решения именно такие (dialectic, отброшенные варианты) | [`docs/plans/lazytg-v0.1.0.md`](docs/plans/lazytg-v0.1.0.md) |
| Что ещё не работает | `CHANGELOG.md` → **Known gaps** |

---

## Гочи (то, что модель ломает, не зная)

- 🔴 **`-tags sqlcipher` намеренно не компилируется** (`internal/storage/sqlite/driver_sqlcipher.go`). Это не забытый файл и не баг сборки: так сделано, чтобы под тегом «encrypted» нельзя было выпустить unencrypted бинарник. Не «дописывать драйвер» — CGo отложен past v0.1. БД unencrypted независимо от тега.
- 🔴 **Кредов Telegram в репозитории нет и быть не может.** Опубликованный в исходниках `api_id` Telegram блокирует навсегда (`API_ID_PUBLISHED_FLOOD`), а ключ у всех пользователей релиза общий — одна утечка ломает логин всем сразу. Разрешение — 3 слоя в `internal/tg.ResolveCredentials`: флаги `--api-id`/`--api-hash` → env `LAZYTG_API_ID`/`LAZYTG_API_HASH` → вшитое при релизе через `-ldflags` из secrets `LAZYTG_RELEASE_API_*`. Половинчатый слой (задан только id или только hash) = ошибка, а не проваливание в следующий. Гейты: `scripts/secret-scan.sh` в lefthook pre-commit + CI job `secret-scan`. `lazytg version` печатает источник, не значения.
- 🔴 **Блоб сессии не влезает в OS keyring — он живёт в `secrets.age`, а в keyring лежит только пароль от файла.** Сессия gotd ~4.2 КБ (auth-key + полный конфиг DC), а macOS-бэкенд `go-keyring` собирает командную строку для `security -i` и отказывается после 4096 байт. Падает это неочевидно: gotd зовёт `StoreSession` внутри установки соединения, поэтому отказ рвёт коннект, RPC умирает с `engine forcibly closed`, а `login` печатает успех, потому что `Run` вернул nil. Не «оптимизировать» обратно в keyring: тест `TestKeyringStore_RejectsSessionSizedValue` фиксирует лимит.
- 🔴 **`busy_timeout(5000)` в `pragmaQuery` и терпимость probe к `SQLITE_BUSY` — не мелочи, а защита от потери записей.** SQLite пускает одного писателя, а lazytg пишет из четырёх мест разом (live drain, backfill, синк диалогов, reindex FTS): без busy_timeout соединение, не получившее лок, роняет запрос мгновенно — замерено 973 потерянных записи из 1200. А если `ProbeWrite` считает `BUSY` отказом, `DegradationDetector` уводит весь репозиторий в soft read-only, и каждая запись возвращает `ErrReadOnly` до следующего probe. Убирать любое из двух нельзя; регрессии закрыты тестами в `internal/storage/sqlite/contention_test.go`.
- **`--polling` — флаг без потребителя** (no-op в v0.1). Wire-up — в `runTUI` рядом с `AttachClient`.
- **Синк диалогов ограничен 5 страницами (500 чатов) и делает паузы между страницами** — это ban-risk-решение, а не недоделка. Снимать cap «чтобы загрузились все чаты» нельзя.
- **`AttachClient` вызывается до построения UI** (`cmd/lazytg/cmd/attach.go`): панели захватывают зависимости в конструкторе, поэтому подключение после сборки UI = мутация полей, которые уже держит горутина Bubble Tea. Разбор гонки — в CHANGELOG.
- **CI впервые запустился только 17.08.2026** (триггеры — `push`/`pull_request` на `main`, вся работа шла в feature-ветке) и сразу дал красный на двух давних дефектах. Оба класса ошибок локально не воспроизводятся, и оба стоит помнить при правках CI:
  - Версии `golangci-lint-action` и линтера обязаны двигаться вместе (`@v6` не принимает v2 вообще, `@v9` требует ≥ `v2.1.0`). Джоба падала на установке, ни одного файла не проверив, — локально шага установки нет, бинарник уже стоит.
  - Контекст из `openTestRepo` капнут 10 секундами и ограничивает **и фикстуру, и проверки**: тест с большим засевом умирает на setup'е с `context deadline exceeded`, что читается как баг отмены. Тяжёлым фикстурам брать `openTestRepoWithBudget`, а тайминги ставить с запасом на раннер, а не по локальному замеру.

---

## UI/UX-решения (после dialectic-анализа)

Зафиксировано; обоснования и отброшенные варианты — в плане.

- **Layout:** 2-pane (chats + thread), WeeChat-style.
- **Ввод:** emacs/readline по умолчанию; vim-mode целиком → v0.2 (половинчатый хуже отсутствующего).
- **Длинные сообщения:** `$EDITOR` по **Ctrl-E**, не как способ ввода по умолчанию.
- **Палитра:** **Ctrl-Space**, L1 chat switcher (top-50 frecency); L2 (`>`-команды) → v0.2.
- **Fuzzy:** Unicode normalization (NFKD + lowercase + drop diacritics) — «Алёна» === «Алена».
- **Inline media:** нет в v0.1, вложение рисуется как `[photo.jpg, 234 KB] press d`.
- **Multi-account:** флаг `--account`; UI switcher → v0.2 (архитектурно готово через `dataDir`).

---

## Текущий статус и планы

Планы: продуктовый — [`docs/plans/lazytg-v0.1.0.md`](docs/plans/lazytg-v0.1.0.md) (4 этапа, прошёл brainstorm + dialectic + Plan-Reviewer APPROVED); выполненные по этапам — [`docs/plans/completed/`](docs/plans/completed/).

**Состояние (17.08.2026):** MTProto подключён к TUI (`cmd/lazytg/cmd/attach.go`), список чатов загружается (`internal/tg/dialogs.go` + `internal/core/sync/dialogs.go`). До этого TUI рисовал пустой локальный кеш: `SaveChat` вызывался только из тестов, загрузки диалогов в коде не было вовсе. Read + send покрыты unit-тестами и офлайн-запуском — **на живом аккаунте не проверялось ни разу**, и до первого smoke функционал рабочим не считать (`docs/MANUAL_SMOKE.md`, тестовый аккаунт — ban-risk).

Остаётся до v0.1.0: живой smoke, `--polling` wire-up, реальный reconnect (`reconnectAdapter.Connect` — заглушка), `updates.Manager` для gap recovery. См. `CHANGELOG.md` → Known gaps.

---

Roadmap (v0.2 / v0.3+ / v0.5+) — в [`README.md` → Roadmap](README.md#roadmap). Вне roadmap не реализуем (см. YAGNI ниже).

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

`make build|test|lint|bench|tidy|clean` — см. [`docs/ARCHITECTURE.md` → Build modes](docs/ARCHITECTURE.md#build-modes). Бутстрап окружения и `lefthook install` — [`docs/CONTRIBUTING.md`](docs/CONTRIBUTING.md#bootstrap). Локальный прогон релиза и `git-cliff` — [`docs/RELEASE_PROCESS.md`](docs/RELEASE_PROCESS.md), [`README.md` → Maintainer notes](README.md#maintainer-notes).

🔴 `lefthook install` в этом клоне **не выполнен** — локальные гейты (gofmt, go vet, `go test -short`, secret-scan) не срабатывают, всё ловится только в CI.

---

## Гайдлайны для Claude Code при работе с проектом

1. **Стек зафиксирован.** Не предлагать миграцию на TDLib, Bubble Tea v1, gocui, tview, mattn/go-sqlite3 — по каждому выбору есть разобранная альтернатива в [`docs/ARCHITECTURE.md` → Stack rationale](docs/ARCHITECTURE.md#stack-rationale-why-these-libraries).
2. **Архитектурные слои (depguard = CI gate).** `internal/core/*` не импортирует `gotd/td` и `charmbracelet/bubbletea`; `internal/ui/*` не импортирует `gotd/td`; `internal/storage/sqlite/*` не импортирует ui и tg. Проверять при ревью, не полагаясь только на линтер.
3. **Tests-first для core.** Каждая задача в плане = тесты (unit/integration/e2e). Целевой coverage: `core` ≥80% (текущее ~81%), `ui` ≥60% (текущее ~79%) — codecov tracks, без hard CI gate.
4. **Conventional commits.** Allowed types (см. `.commitlintrc.yml` type-enum): `feat`, `fix`, `perf`, `security`, `docs`, `refactor`, `test`, `chore`, `build`, `ci`. Breaking changes — через `!`-суффикс (`feat!:`, `fix(scope)!:`), не через тип `breaking`. Enforced через lefthook commit-msg hook (commitlint с bash-fallback) + CI pr-title job (`amannn/action-semantic-pull-request`).
5. **Никаких самостоятельных коммитов.** Коммиты только по явному запросу пользователя.
6. **YAGNI.** Если идея не в roadmap — не реализуем. Plugin API, multi-account UI, voice — отложены явно.
7. **Ban-risk first.** При изменениях auth/send/updates/dialogs думать о поведенческом следе. Rate-limit guard на send (10 msg/s, покрывает и текст, и медиа) не отключается и не поднимается; паузы и cap в синке диалогов — тоже гейт, а не заглушка.
8. **Pure-Go default.** Любой PR с CGo-зависимостью требует обоснования и build tag (см. гочу про `sqlcipher`).
9. **Документация обновляется вместе с кодом.** PR без обновлённого CHANGELOG.md / docs/ — не мерджим.
10. **Платформа разработки macOS (zsh, brew).** Не предлагать Windows-специфичные решения (PowerShell, .bat).

---

## Контекст для fresh-сессий

Этот файл — точка входа: правила, гочи, раскладка ссылок. Обоснования решений он **не** содержит — они в [`docs/plans/lazytg-v0.1.0.md`](docs/plans/lazytg-v0.1.0.md) (секции «Выбранный подход (после dialectic-анализа)» и «Что НЕ делаем никогда»). Решения прошли 5-фазный брейншторм, dialectic-анализ и Plan-Reviewer — если предложение противоречит зафиксированному, сначала читать план, а не переигрывать выбор.
