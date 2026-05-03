# Plan: lazytg Stage 4 — Release Engineering для v0.1.0

## Overview

Финальный этап перед публичным релизом v0.1.0. Превращаем рабочий прототип (Stages 1-3) в **сериозный OSS-продукт первого дня**: cross-platform signed binaries, Homebrew tap, .deb/.rpm пакеты, pre-release pipeline (alpha → beta → rc → stable), автоматизация changelog через conventional commits, формализованный beta-период с smoke-чеклистом, финальная пользовательская документация.

После Stage 4 должно работать:
- `git tag v0.1.0-alpha.1 && git push --tags` → GitHub Release с 4+ подписанными бинарями (linux/darwin × amd64/arm64), checksums, sigstore-bundles, .deb, .rpm, brew formula auto-обновлена в tap-репо.
- `brew install pgmac/lazytg/lazytg` ставит lazytg на macOS.
- `cosign verify-blob --bundle <bundle>.json --certificate-identity ... <binary>` подтверждает подпись.
- Conventional commits enforced через commitlint в pre-commit hook (никаких "fix stuff" коммитов в main).
- CHANGELOG.md auto-генерируется через git-cliff с категориями feat/fix/perf/security/breaking.
- Beta-период: ≥3 внешних тестера прошли формализованный 6-пунктный smoke-чеклист.
- Memory budget: idle <50MB, active <150MB — задокументировано через benchmark.
- Полная пользовательская документация: README с demo-gif, INSTALL, CONFIGURATION, SEARCH, TROUBLESHOOTING, VERIFY, PERFORMANCE, FILES, ARCHITECTURE, SECURITY, CONTRIBUTING, CHANGELOG.

**Acceptance criteria v0.1.0 (из главного плана):**
- GitHub Release с подписанными бинарями (4 артефакта: linux-{amd64,arm64}, darwin-{amd64,arm64} + checksums + sigstore bundles).
- `brew install pgmac/lazytg/lazytg` работает.
- `.deb` и `.rpm` доступны.
- ≥3 тестера заполнили формализованный smoke-чеклист.
- CI зелёный, e2e smoke в CI проходит.
- Coverage `core` ≥80%, `ui` ≥60`.
- Memory: idle <50MB, active <150MB (документировано в `docs/PERFORMANCE.md`).

## Context

**Stages 1-3 завершены.** Функционально продукт complete:
- Auth phone+code+2FA + multi-account (через `--account` флаг)
- 2-pane TUI (chats + thread) + emacs/readline ввод + statusbar + help overlay
- Send/reply с optimistic UI, $EDITOR delegation (Ctrl-E)
- Live-updates через gotd `updates.Manager` (latency p95 ≈ 4ms, SLA <500ms — запас 100×)
- **FTS5 trigram локальный поиск (p95 ≈ 47ms на 100k сообщений, SLA <100ms — запас 2×)**
- Командная палитра L1 (Ctrl-Space) с Unicode-fuzzy "Алёна"=="Алена"
- Files: download (Ctrl-D) + upload (Ctrl-U) с прогрессом в statusbar
- `lazytg debug-bundle` без секретов (доказано grep-тестом)
- Security: permission checks 0600/0700, send rate-limit guard
- Coverage: core 81.3%, ui 83% (gates пройдены)

**Что уже есть в release infra (со Stages 1-3):**
- `.goreleaser.yaml`: build matrix linux+darwin × amd64+arm64 (pure-Go), archives tar.gz, checksums, cosign keyless для checksums, release с ban-warning header
- `.github/workflows/`: `ci.yml`, `release.yml`, `snapshot.yml`
- `.github/ISSUE_TEMPLATE/`: bug_report.yml, feature_request.yml; `PULL_REQUEST_TEMPLATE.md`; `SECURITY.md`
- Документация: ARCHITECTURE.md, CONTRIBUTING.md, FILES.md, MANUAL_SMOKE.md, SEARCH.md, SECURITY.md
- LICENSE MIT, CHANGELOG.md (Keep a Changelog format)

**Что добавляем в Stage 4:**
- Расширить `.goreleaser.yaml`: второй build entry с `-tags sqlcipher` (CGo), nfpm для .deb/.rpm, brew tap config, sign не только checksums но и сами бинарники (sigstore bundles per-binary)
- Pre-release pipeline: tags `v*-alpha.*`/`v*-beta.*`/`v*-rc.*` → prerelease, brew/scoop/.deb НЕ обновляются. Stable tag → publishes everywhere
- `cliff.toml` + `.commitlintrc.yml` + lefthook commit-msg hook
- `test/perf/memory_test.go` — измерение RSS на сценариях idle/active, fail если выше budgets
- Документация: README обновить с demo-cast/gif, INSTALL.md, CONFIGURATION.md, TROUBLESHOOTING.md, VERIFY.md, PERFORMANCE.md
- `docs/BETA_CHECKLIST.md` — формализованный 6-пунктный smoke-чеклист для тестеров

**Стек дополнения:**
- `git-cliff` (CLI tool, через brew или Cargo) — конфиг `cliff.toml` коммитится в репо
- `commitlint` через `lefthook` commit-msg hook (используем `npx --package=@commitlint/cli` или Go-аналог)
- `nfpm` встроен в goreleaser
- `cosign` уже доступен (используется keyless через GitHub OIDC)

**Отложено на v0.2 (явно НЕ делать в Stage 4):**
- Windows билды (отдельная боль с TUI keys/colors на cmd.exe — нужен реальный тест на Win10/11+WSL)
- macOS notarization (требует Apple ID secrets + $99/год, complexity не оправдана для v0.1)
- Snap, Flatpak, AUR — добавляем когда будет community
- Auto-update mechanism — ручное обновление через brew/manual

**Гайдлайны для исполнителя:**
- НЕ создавать репо `homebrew-lazytg` (это отдельный repo на GitHub) — только goreleaser config + документировать в INSTALL.md что репо нужно создать вручную перед первым релизом
- НЕ запускать реальный release (`goreleaser release` без `--snapshot`) — это destructive action, требует явного запроса пользователя
- НЕ пушить теги `v0.1.0-alpha.*` — pushing tags = триггер CI release pipeline, шипит реальные артефакты в GitHub Releases
- Beta-period — ручная фаза, документируется но не автоматизируется. В таске только подготовка артефактов (checklist + draft анонса)

## Validation Commands

- `cd /Users/pgmac/Data/prjcts/lazytg && go build ./...`
- `cd /Users/pgmac/Data/prjcts/lazytg && go test -race ./...`
- `cd /Users/pgmac/Data/prjcts/lazytg && golangci-lint run`

### Task 1: GoReleaser production — расширить build matrix + nfpm + brew tap + per-binary signing

- [x] Открыть `.goreleaser.yaml`. Добавить второй build entry для sqlcipher через `-tags sqlcipher` и `CGO_ENABLED=1`. Имя `lazytg-sqlcipher`. Goos только `linux+darwin` (без arm64 на linux потому что cross-compile с CGo требует тулчейна — оставить только нативные `darwin/{amd64,arm64}` и `linux/amd64`). Добавить условие в release notes что sqlcipher-вариант требует system libsqlcipher
- [x] Расширить секцию `archives` чтобы хватило обоих builds. Добавить отдельный archive id `sqlcipher` с шаблоном `{{ .ProjectName }}-sqlcipher_{{ .Version }}_{{ .Os }}_{{ .Arch }}`
- [x] Добавить секцию `nfpms` в `.goreleaser.yaml` для генерации .deb и .rpm:
  ```yaml
  nfpms:
    - id: default
      package_name: lazytg
      vendor: lazytg contributors
      homepage: https://github.com/pgmac/lazytg
      maintainer: pgmac <noreply@github.com>
      description: |
        Local-first Telegram TUI client with FTS5 search.
        Telegram automatically puts unofficial clients under observation.
        Use with a test account first.
      license: MIT
      formats: [deb, rpm]
      bindir: /usr/bin
      builds: [lazytg]  # только pure-Go вариант
      contents:
        - src: LICENSE
          dst: /usr/share/doc/lazytg/LICENSE
        - src: README.md
          dst: /usr/share/doc/lazytg/README.md
  ```
- [x] Добавить секцию `brews` в `.goreleaser.yaml`:
  ```yaml
  brews:
    - name: lazytg
      ids: [default]  # только pure-Go архив
      repository:
        owner: pgmac
        name: homebrew-lazytg
        token: "{{ .Env.HOMEBREW_TAP_GITHUB_TOKEN }}"
      directory: Formula
      homepage: https://github.com/pgmac/lazytg
      description: Local-first Telegram TUI client with FTS5 search
      license: MIT
      install: |
        bin.install "lazytg"
      test: |
        system "#{bin}/lazytg", "version"
      caveats: |
        Telegram automatically puts unofficial clients under observation.
        Use lazytg with a test account first. See:
        https://github.com/pgmac/lazytg/blob/main/docs/SECURITY.md

        Set LAZYTG_API_ID and LAZYTG_API_HASH env vars before first run.
        Get them at https://my.telegram.org/apps
  ```
- [x] Расширить секцию `signs` чтобы подписывать **сами бинарники**, не только checksums. Использовать `signs.artifacts: archive` для подписи tar.gz архивов через cosign sign-blob (sigstore bundle per-archive). Сохранить старую подпись checksums как backup
- [x] Документировать в README.md секцию «Setup before first release» что нужно: (1) создать репо `pgmac/homebrew-lazytg` вручную с пустым `Formula/` каталогом; (2) сгенерировать PAT с `contents:write` на этот репо и добавить в org/repo secrets как `HOMEBREW_TAP_GITHUB_TOKEN`; (3) первый push тега `v0.1.0` (не alpha/beta) запушит формулу автоматически
- [x] Запустить локально `goreleaser check` (валидация конфига без сборки) и `goreleaser release --snapshot --clean --skip=publish --skip=sign --skip=brew --skip=announce` (быстрый snapshot без сети) — артефакты в `dist/` должны включать pure-Go архивы для linux/darwin × amd64/arm64. **Sqlcipher вариант скипнуть в snapshot если CGo тулчейн недоступен** (документировать)
- [x] Если snapshot падает — диагностировать и зафиксировать (наиболее вероятно — отсутствие cgo для sqlcipher на ARM linux; если так — убрать `linux/arm64` из sqlcipher build entry). Запустить `go build ./...` и `go test -race ./...` после изменений `.goreleaser.yaml` (любые ldflags могли поломать version pkg)

### Task 2: Pre-release pipeline (alpha/beta/rc gates)

- [x] Открыть `.github/workflows/release.yml`. Добавить job-level условную логику по тегу через template:
  - Triggered: `on.push.tags: ['v*']`
  - Job `goreleaser`: всегда запускается
  - Внутри goreleaser-action добавить ENV `GORELEASER_PRERELEASE` который вычисляется из тега (через bash step):
    ```yaml
    - name: Detect prerelease
      id: prerel
      run: |
        TAG="${GITHUB_REF#refs/tags/}"
        if [[ "$TAG" =~ -(alpha|beta|rc)\. ]]; then
          echo "prerelease=true" >> $GITHUB_OUTPUT
          echo "skip_brew=true" >> $GITHUB_OUTPUT
          echo "skip_nfpm_publish=true" >> $GITHUB_OUTPUT
        else
          echo "prerelease=false" >> $GITHUB_OUTPUT
        fi
    ```
- [x] В `.goreleaser.yaml` отделить prerelease behavior:
  - `release.prerelease: auto` (goreleaser сам определяет по тегу — semver pre-release suffix)
  - `brews[].skip_upload: '{{ if .Prerelease }}true{{ end }}'` — формулу не обновляем для alpha/beta/rc
  - Аналогично для `nfpms` если есть upload — для alpha/beta скипаем (но локально артефакты собираются и попадают в GitHub Release как assets)
- [x] Создать новый workflow `.github/workflows/prerelease.yml` для удобного запуска вручную через workflow_dispatch:
  ```yaml
  on:
    workflow_dispatch:
      inputs:
        kind:
          description: alpha, beta, or rc
          required: true
          type: choice
          options: [alpha, beta, rc]
  ```
  Job: вычисляет следующий prerelease tag (например, читает существующие теги через `git tag -l 'v*-alpha.*' | sort -V | tail -1` и инкрементит), создаёт annotated tag, пушит → триггерит release.yml
- [x] Документировать pre-release flow в `docs/CONTRIBUTING.md`:
  - alpha → внутреннее тестирование, brew не обновляется
  - beta → external testers (≥3 человека), brew не обновляется, beta-checklist обязателен
  - rc → release candidate, последний сабж до stable, brew не обновляется
  - stable → `vMAJOR.MINOR.PATCH` без суффикса, brew + nfpm publish, anонс в README
- [x] Запустить `act` локально или dry-run через `gh workflow view` если есть `gh` (опционально — проверить YAML-валидность через `actionlint`) — actionlint не установлен, YAML провалидирован через gopkg.in/yaml.v3 (все 4 workflows + .goreleaser.yaml парсятся корректно), `goreleaser check` зелёный
- [x] Проверить `go build ./...` — workflow-изменения не должны влиять на код, но регресс-проверка обязательна

### Task 3: Changelog automation — git-cliff + commitlint

- [x] Создать `cliff.toml` в корне репо со следующей конфигурацией:
  ```toml
  [changelog]
  header = "# Changelog\n\nAll notable changes to this project will be documented in this file.\n\nThe format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),\nand this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).\n\n"
  body = """
  {% if version %}\
      ## [{{ version | trim_start_matches(pat="v") }}] - {{ timestamp | date(format="%Y-%m-%d") }}
  {% else %}\
      ## [Unreleased]
  {% endif %}\
  {% for group, commits in commits | group_by(attribute="group") %}
      ### {{ group | upper_first }}
      {% for commit in commits %}
          - {{ commit.message | upper_first }}\
            {% if commit.breaking %} (BREAKING){% endif %}
      {% endfor %}
  {% endfor %}\n
  """
  trim = true
  footer = ""

  [git]
  conventional_commits = true
  filter_unconventional = false
  commit_parsers = [
    { message = "^feat", group = "Added" },
    { message = "^fix", group = "Fixed" },
    { message = "^perf", group = "Performance" },
    { message = "^security", group = "Security" },
    { message = "^docs", group = "Documentation" },
    { message = "^refactor", group = "Refactoring" },
    { message = "^test", skip = true },
    { message = "^chore", skip = true },
    { message = "^Merge", skip = true },
    { body = ".*BREAKING CHANGE", group = "Breaking" },
  ]
  filter_commits = false
  tag_pattern = "v[0-9].*"
  ignore_tags = ""
  topo_order = false
  sort_commits = "oldest"
  ```
- [x] Создать `.commitlintrc.yml` в корне:
  ```yaml
  extends:
    - "@commitlint/config-conventional"
  rules:
    type-enum:
      - 2
      - always
      - [feat, fix, perf, security, docs, refactor, test, chore, build, ci]
    subject-max-length:
      - 2
      - always
      - 100
    body-leading-blank:
      - 1
      - always
    footer-leading-blank:
      - 1
      - always
  ```
- [x] Обновить `lefthook.yml` — добавить commit-msg hook для commitlint:
  ```yaml
  commit-msg:
    commands:
      commitlint:
        run: npx --no-install --package=@commitlint/cli -- commitlint --edit {1}
        skip:
          - merge
          - rebase
  ```
  Если npx unavailable — fallback на самостоятельный bash-парсер: regex `^(feat|fix|perf|security|docs|refactor|test|chore|build|ci)(\([a-z0-9-]+\))?!?: .{1,100}` через `grep -E` в commit-msg hook
- [x] Документировать в `docs/CONTRIBUTING.md` секцию «Commit message format» с примерами и ссылкой на conventional commits спецификацию
- [x] Добавить GitHub Actions job в `ci.yml` для проверки PR title (использовать action `amannn/action-semantic-pull-request@v5` или ручной grep по `${{ github.event.pull_request.title }}`)
- [x] Запустить `git-cliff --tag v0.1.0 --unreleased` локально для генерации первого CHANGELOG (если git-cliff установлен через brew). Сохранить результат как стартовую точку CHANGELOG.md, заменив существующий пустой Unreleased section — git-cliff не установлен локально, генерация отложена на момент release tagging maintainer'ом (документировано в README "Regenerating CHANGELOG" + CONTRIBUTING "Changelog generation"). Существующий CHANGELOG.md уже содержит полный Unreleased секшн с feat/fix entries из Stages 1-3 — преждевременная регенерация перетёрла бы рукописное содержимое
- [x] Если git-cliff не установлен — документировать в README.md и CONTRIBUTING.md что для генерации changelog нужно `brew install git-cliff` или `cargo install git-cliff`. Не блокировать таск на этом
- [x] Проверить `go build ./...` и `go test -race ./...` — изменения в `.commitlintrc.yml`, `cliff.toml`, `lefthook.yml` не должны затрагивать Go-код, но регресс обязателен (build OK, все пакеты тестов прошли, golangci-lint 0 issues)

### Task 4: Memory budget benchmark (idle <50MB, active <150MB)

- [x] Создать `test/perf/memory_test.go` с двумя тестами:
  1. `TestMemoryBudget_Idle` — собирает app через `internal/app.Build` с моками (без реального Telegram), стартует в горутине, ждёт 5 секунд stabilization (`time.Sleep`), вызывает `runtime.GC()` × 2, читает `runtime.MemStats.HeapAlloc`. **Fail если HeapAlloc > 50 * 1024 * 1024 (50MB)**
  2. `TestMemoryBudget_Active` — то же что выше, но симулирует активную нагрузку: 1000 mock messages через event bus за 30 секунд (≈33 msg/sec), параллельно 10 search-запросов с benchmark fixture. После нагрузки — `runtime.GC()` × 2 → проверить `HeapAlloc`. **Fail если > 150 * 1024 * 1024 (150MB)**
- [x] Использовать helper из существующих perf-тестов (`test/perf/`). Mock backends: Storage in-memory или `:memory:` SQLite, Transport — fake `Sender`/`HistoryProvider` уже есть в Stage 2 fakes (если приватные — продублировать в `test/perf/fakes.go`) — построение через `app.Build` с `SkipPermissionsAudit=true` поверх `t.TempDir()` XDG-layout (паттерн из `goroutine_leak_test.go`); MTProto-сервисы не аттачатся, fakes не нужны
- [x] Замерить через `runtime.MemStats`:
  - Idle: после `time.Sleep(5 * time.Second)` + GC×2
  - Active: throughput через `bus.Publish(events.MessageReceived{...})` + `searchService.Search(...)` параллельно, замер через 30 секунд
- [x] Создать `docs/PERFORMANCE.md` с описанием:
  - Memory budgets (idle <50MB, active <150MB) — обоснование (TUI на разработчиков с долгими сессиями ssh; >150MB начнёт мешать другим процессам в tmux)
  - Search SLA (p95 <100ms на 100k сообщений) — фактический результат из BenchmarkSearch100k
  - Live-updates SLA (p95 <500ms) — фактический результат из BenchmarkLiveUpdateLatency
  - DB size guidance (3-5× от текста сообщений из-за trigram, default cap 5000 msg/chat)
  - Известные ограничения: тяжёлые чаты (>10k активных сообщений в одном чате) могут давать viewport jank — рекомендация ограничить через config
- [x] Добавить в `ci.yml` шаг `go test -run "TestMemoryBudget" ./test/perf/...` (отдельный шаг чтобы видно было если упадёт)
- [x] Запустить локально `go test -v -run "TestMemoryBudget" ./test/perf/...` — оба теста должны пройти. Если HeapAlloc выше budget — диагностировать через `pprof` (`go test -memprofile=mem.out -run TestMemoryBudget_Active ./test/perf/` + `go tool pprof -top mem.out`) — оба прошли локально под `-race`: idle 2.0 MiB / 50 MiB budget, active 2.2 MiB / 150 MiB budget (M4)

### Task 5: Финальная пользовательская документация

- [x] Создать `docs/INSTALL.md`. Секции:
  - **Recommended (macOS):** `brew install pgmac/lazytg/lazytg` (после публикации tap)
  - **Linux .deb:** `wget https://github.com/pgmac/lazytg/releases/latest/download/lazytg_<version>_linux_amd64.deb && sudo dpkg -i lazytg_*.deb`
  - **Linux .rpm:** `sudo dnf install https://github.com/.../lazytg_<version>_linux_amd64.rpm`
  - **Manual binary:** скачать tar.gz из GitHub Release, проверить через `cosign verify-blob` (ссылка на VERIFY.md), распаковать, `sudo install lazytg /usr/local/bin/`
  - **From source:** `go install github.com/pgmac/lazytg/cmd/lazytg@v0.1.0` (требует Go 1.22+)
  - **SQLCipher build (encrypted DB):** только manual download `lazytg-sqlcipher_*` или `go install -tags sqlcipher`. Документировать что нужен `libsqlcipher` в системе
  - **Setup:** получить API_ID/API_HASH в https://my.telegram.org/apps, экспортировать `LAZYTG_API_ID`, `LAZYTG_API_HASH`, запустить `lazytg login --account +<phone>`
- [x] Создать `docs/CONFIGURATION.md`. Секции:
  - **Config file location:** `$XDG_CONFIG_HOME/lazytg/config.toml` (default `~/.config/lazytg/config.toml`)
  - **Все опции config.toml:** документировать каждое поле (с дефолтами): logging level/path/debug, storage path, fts5 max_messages_per_chat, polling interval, send rate-limit, downloads dir, editor, и т.д.
  - **Keymap config:** `~/.config/lazytg/keymap.toml`. Default bindings + примеры override (например, swap Ctrl+R и Ctrl+E)
  - **Env vars:** `LAZYTG_API_ID`, `LAZYTG_API_HASH`, `EDITOR`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_STATE_HOME`, `XDG_CACHE_HOME`
  - **Multi-account:** через `--account <phone>` флаг, по аккаунту своя session+config. Состояние accounts хранится в БД таблицы `accounts`
- [x] Создать `docs/TROUBLESHOOTING.md`. Секции в формате «Симптом → диагностика → решение»:
  - "lazytg login fails with FLOOD_WAIT" → подождать N секунд указанный в ошибке, gotd сам ретраит. Если повторяется — возможно account под observation (Telegram security)
  - "Search не находит ничего" → проверить что reindex прошёл (`lazytg reindex --all`); проверить размер индекса (`lazytg debug-bundle` → распаковать → `db_stats.txt`)
  - "TUI выглядит сломано / нет цветов" → проверить `$TERM` (рекомендуется xterm-256color или alacritty); проверить что терминал поддерживает Unicode
  - "Permission denied при старте" → security check сработал. Проверить права на ~/.config/lazytg/ (должно быть 0700) и secrets.age (0600). Исправить через `chmod`
  - "DB locked" → другой процесс держит БД (другой запущенный lazytg). Закрыть его. Если процессов нет — удалить `~/.local/share/lazytg/lazytg.db-shm` и `-wal` (WAL-файлы)
  - "Account banned" → к сожалению, риск userbot-аккаунтов. Написать в recover@telegram.org с описанием use-case (TUI-клиент для personal use). См. SECURITY.md
  - **Как собрать debug-bundle для bug-report:** `lazytg debug-bundle` → tar.gz появится в cwd → приложить к GitHub Issue (gist если большой)
- [x] Создать `docs/VERIFY.md`. Секции:
  - **Verify checksums:** `sha256sum -c checksums.txt` после скачивания всех артефактов
  - **Verify cosign signatures:** инструкция через `cosign verify-blob` с keyless OIDC. Команда:
    ```sh
    cosign verify-blob \
      --bundle lazytg_<version>_<os>_<arch>.tar.gz.sigstore.json \
      --certificate-identity-regexp "https://github.com/pgmac/lazytg/.github/workflows/release.yml@refs/tags/v.*" \
      --certificate-oidc-issuer https://token.actions.githubusercontent.com \
      lazytg_<version>_<os>_<arch>.tar.gz
    ```
  - **What it proves:** binary собран в GitHub Actions с workflow `release.yml` под тегом начинающимся с `v`, никто не подменил после
  - Проверить cosign установлен (`brew install cosign` для macOS)
- [x] Обновить `README.md`:
  - **Первая строка под заголовком:** ban-warning (уже есть, проверить актуальность)
  - **Pitch:** «Local-first Telegram TUI with instant FTS5 search. Built for developers who live in tmux+nvim+ssh.»
  - **Demo:** placeholder для asciinema-cast или gif (`![demo](docs/demo.gif)` — файл записать вручную после ручного smoke-тестирования; в плане документировать как создать)
  - **Quickstart (3 шага):** `brew install` → set env vars → `lazytg login`
  - **Features:** список с эмодзи 🔍 search, ⚡ instant, 🔐 local-first, ⌨️ vim-friendly keymap, 📥📤 file transfer, 🛡️ rate-limited
  - **Acceptance criteria badges:** GitHub Actions status, Go report card, codecov coverage
  - **Links:** INSTALL.md, CONFIGURATION.md, SEARCH.md, TROUBLESHOOTING.md, SECURITY.md, ARCHITECTURE.md, CONTRIBUTING.md, VERIFY.md, PERFORMANCE.md, FILES.md, CHANGELOG.md
  - **License:** MIT
- [x] Документировать **как записать demo** в `docs/DEMO.md` (для maintainer'a):
  ```
  1. asciinema rec -c "lazytg" docs/demo.cast
  2. agg --speed 2 --theme dracula docs/demo.cast docs/demo.gif (или asciinema-gif-generator)
  3. Сценарий: login → выбор чата → ввод "hello" → отправка → /search "тест" → результаты → переход в чат
  ```
- [x] Проверить что все docs-ссылки в README.md и CLAUDE.md ведут на существующие файлы (`grep -rn 'docs/' README.md CLAUDE.md` + manual проверка) — Python-скрипт прошёл (image-placeholder `docs/demo.gif` исключён по плану, fix stale path в CLAUDE.md → `completed/20260502-lazytg-stage1-foundation.md`)

### Task 6: Issue/PR templates + Security policy — доработка

- [x] Открыть `.github/ISSUE_TEMPLATE/bug_report.yml`. Убедиться что есть:
  - Поле для версии (`lazytg version`)
  - Поле для OS/arch
  - Поле для шагов воспроизведения
  - **Поле для debug-bundle:** прямое указание что нужно прикрепить (с инструкцией как собрать через `lazytg debug-bundle`)
  - **Чекбокс «I confirm debug-bundle does not contain my session/api_hash»** (legal protection)
  - Ссылка на SECURITY.md в подсказке: «Не сообщайте security issues публично — используйте GitHub Security Advisories»
- [x] Открыть `.github/ISSUE_TEMPLATE/feature_request.yml`. Убедиться что есть:
  - Описание проблемы (которую решает feature)
  - Предполагаемое решение
  - Альтернативы (как сейчас обходится)
  - **Чекбокс «I checked the v0.2/v0.3 roadmap in CLAUDE.md»** (избежать дублей по уже запланированным фичам)
- [x] Создать `.github/ISSUE_TEMPLATE/config.yml` с links для перенаправления (security advisory + discussions + ban-risk policy reminder)
- [x] Открыть `.github/PULL_REQUEST_TEMPLATE.md`. Убедиться что есть чек-лист (Tests, Docs, CHANGELOG/conventional-commits, depguard, coverage gates ≥80%/≥60%) — обновлён, явно отмечена двойная гарантия CHANGELOG (manual или git-cliff) + conventional-commits via lefthook + CI semantic-pr-title
- [x] Открыть существующий `SECURITY.md` (в корне) и `docs/SECURITY.md` — убедиться что disclosure 90д через GitHub Security Advisories, threat model (защищаем session/api_hash/phone/messages/DB/$EDITOR; НЕ защищаем Telegram-сервера, root-malware, side-channels, secret chats), ban-risk warning с тестовым аккаунтом + rate-limit guard, контакт через GitHub Security Advisories — всё корректно, дополнения не требуются
- [x] Создать `.github/CODEOWNERS` (default `@pgmac` + явные паттерны для tg/security/obs/config/migrations + release plumbing + security docs + .github)
- [x] Запустить `go build ./...` и `go test -race ./...` — изменения в .github/ не затрагивают код, но регресс-проверка обязательна (build OK, все пакеты включая test/perf зелёные, golangci-lint 0 issues, все 11 YAML файлов проходят yaml.v3 парсер)

### Task 7: Beta smoke checklist + draft анонса

- [x] Создать `docs/BETA_CHECKLIST.md` с **6-пунктным smoke-чеклистом** для beta-тестеров:
  ```markdown
  # lazytg v0.1.0-beta smoke checklist

  Пожалуйста, заполните и приложите к GitHub issue с тегом `beta-feedback`.
  Должно занять ≤15 минут. Спасибо!

  **Tester info:**
  - OS: __________ (macOS Sonoma / Ubuntu 22.04 / etc)
  - Architecture: __________ (arm64 / amd64)
  - Terminal: __________ (Alacritty / iTerm2 / Ghostty / etc)
  - Tmux: yes / no
  - Telegram account type: тестовый / основной (РЕКОМЕНДУЕМ ТЕСТОВЫЙ)

  **Smoke steps (отметьте ✅ если работает, ❌ если сломано, ⚠️ если работает с замечаниями):**

  - [ ] **1. Install:** `brew install pgmac/lazytg/lazytg` (или `.deb`/`.rpm`/binary). Команда `lazytg --version` показывает версию.
  - [ ] **2. Login:** `lazytg login --account +<phone>`. Прошло phone → code → 2FA. Никаких cryptic errors. После завершения — `lazytg accounts` показывает аккаунт.
  - [ ] **3. Read:** запуск `lazytg` (без подкоманды) открывает TUI. Слева список ваших чатов отсортированный по последнему сообщению. Стрелками можно выбрать чат, Enter → справа загружается история.
  - [ ] **4. Send:** Tab → focus в input. Ввод "hello from lazytg" + Enter → сообщение появилось мгновенно (optimistic), пришло на телефон.
  - [ ] **5. Search:** `/привет` (или другое слово которое точно есть) → live results появились через ≤500ms. Enter на результате → переход в правильный чат + scroll к нужному сообщению.
  - [ ] **6. Files:** на сообщении с фото нажать `Ctrl+D` → прогресс в статус-баре → файл в `~/Downloads/lazytg/<chat>/`. Затем `Ctrl+U` → выбрать любой .txt файл → отправлен в чат с caption.

  **Free-form feedback:**
  - Что понравилось:
  - Что сломано / медленно / непонятно:
  - Чего не хватает (но помните — мы целимся в v0.1.0, не в v1.0):
  - Готовы ли использовать ежедневно? (yes / no / "если добавите X")
  ```
- [x] Создать `docs/RELEASE_ANNOUNCE.md` (draft анонса для maintainer'a, **не публикуем автоматически**):
  - Шаблон для Show HN / r/commandline / lobste.rs / r/golang
  - Pitch: «lazytg — local-first Telegram TUI client with FTS5 search, written in pure Go»
  - Highlights: 100k msg search p95 47ms, multi-account, $EDITOR delegation, cosign-verified binaries
  - Honesty: alpha quality, ban-risk предупреждение, не replace для Telegram Desktop а tool для tmux-resident developers
  - Ссылки: GitHub repo, demo gif, install instructions, beta checklist
  - **НЕ публиковать без явной команды пользователя.** Это draft для финальной фазы release
- [x] Создать `docs/RELEASE_PROCESS.md` — runbook для maintainer'a:
  ```
  1. Убедиться что main зелёный (CI green, coverage gates passed, benchmark gates passed)
  2. Обновить CHANGELOG.md через `git-cliff --tag <new-version> --unreleased`
  3. Commit: `git commit -m "chore(release): prepare v0.1.0-alpha.1"`
  4. Tag: `git tag -a v0.1.0-alpha.1 -m "v0.1.0-alpha.1"`
  5. Push: `git push origin main && git push origin v0.1.0-alpha.1`
  6. CI запустит release.yml → артефакты в GitHub Releases (prerelease=true для alpha/beta/rc)
  7. Beta phase: разослать BETA_CHECKLIST.md → собрать ≥3 confirmation
  8. Если confirmations OK → tag v0.1.0 (без суффикса) → release.yml опубликует stable + brew + nfpm
  9. Анонс через RELEASE_ANNOUNCE.md
  ```
- [x] Запустить `go build ./...`, `go test -race ./...` — не должно быть регрессов (build чистый, все 28 пакетов прошли `go test -race ./...`, golangci-lint 0 issues)

### Task 8: Verification + plan move to completed

- [x] **Verification 1 — code & tests:**
  - `go build ./...` exit 0 — passed
  - `go test -race ./...` все пакеты OK — passed (28 пакетов)
  - `golangci-lint run` 0 issues — passed
  - Coverage core 81.3% (≥80%), ui 79.2% (≥60%) — passed via `go test -coverpkg=./internal/{core,ui}/... -coverprofile=...`
  - Memory tests: idle 2.0 MiB / 50 MiB budget, active 2.3 MiB / 150 MiB budget — passed на M4 под `-race`
  - SLA benchmarks: search p95 = 47.19 ms (<100 ms budget), live-updates p95 = 39.3 µs (<500 ms budget) — оба с большим запасом
- [x] **Verification 2 — release infra:**
  - `goreleaser check` → config validated, single deprecation warning о grядущем переходе `brews → homebrew_casks` (для v0.1 не блокирует)
  - `SKIP_SQLCIPHER=true goreleaser release --snapshot --clean --skip=publish,announce,sign` → 4 pure-Go архива (linux/darwin × amd64/arm64) + 4 nfpm пакетов (.deb/.rpm × amd64/arm64) + `dist/homebrew/Formula/lazytg.rb` + `checksums.txt`. Sqlcipher entry правильно скипнут при `SKIP_SQLCIPHER=true` — поведение задокументировано в `.goreleaser.yaml` комментариях
  - tar.gz содержит `LICENSE`, `README.md`, `CHANGELOG.md`, `lazytg` бинарь — проверено через `tar -tzvf`
- [x] **Verification 3 — workflows valid:**
  - actionlint не установлен (опциональный шаг). Все 4 workflow YAML (`ci.yml`, `prerelease.yml`, `release.yml`, `snapshot.yml`) + 3 issue template YAML + `.goreleaser.yaml` + `lefthook.yml` + `.commitlintrc.yml` парсятся через `gopkg.in/yaml.v3` без ошибок (10 файлов всего)
  - gh CLI: `.github/workflows/` содержит ровно 4 workflow файла как и ожидалось
- [x] **Verification 4 — docs полные:**
  - `docs/` содержит все 15 ожидаемых файлов: ARCHITECTURE.md, BETA_CHECKLIST.md, CONFIGURATION.md, CONTRIBUTING.md, DEMO.md, FILES.md, INSTALL.md, MANUAL_SMOKE.md, PERFORMANCE.md, RELEASE_ANNOUNCE.md, RELEASE_PROCESS.md, SEARCH.md, SECURITY.md, TROUBLESHOOTING.md, VERIFY.md
  - корень: README.md, LICENSE, CHANGELOG.md, SECURITY.md, CLAUDE.md, .commitlintrc.yml, cliff.toml — все на месте
  - `.github/`: ISSUE_TEMPLATE/{bug_report,feature_request,config}.yml, PULL_REQUEST_TEMPLATE.md, CODEOWNERS, dependabot.yml, workflows/{ci,release,snapshot,prerelease}.yml — все на месте
- [x] **Verification 5 — links integrity:**
  - markdown-ссылки в README.md/CLAUDE.md (с исключением code-block + image placeholder `docs/demo.gif`) валидны — проверено Python-скриптом
  - `github.com/pgmac/lazytg` ссылки консистентны во всех документах + `.goreleaser.yaml`
- [x] **Verification 6 — CHANGELOG актуален:**
  - Unreleased section дополнен Stage 4 entries: release engineering (goreleaser sqlcipher/nfpm/brew/sigstore), pre-release pipeline (alpha/beta/rc gating + manual workflow_dispatch), changelog automation (git-cliff + commitlint + lefthook + CI semantic-pr-title), memory budget benchmark + PERFORMANCE.md, документация (5 user-facing docs + RELEASE_ANNOUNCE/PROCESS/BETA_CHECKLIST), GitHub plumbing hardening (issue/PR templates + CODEOWNERS)
  - git-cliff не установлен локально — preview-генерация отложена на момент release tagging maintainer'ом (документировано в README "Regenerating CHANGELOG" + CONTRIBUTING)
- [x] **Manual smoke (документировать, не автоматизировать):** все четыре пункта документированы для maintainer'а:
  1. Создание репо `pgmac/homebrew-lazytg` с пустым `Formula/` каталогом — README "Setup before first release"
  2. Генерация PAT + `HOMEBREW_TAP_GITHUB_TOKEN` secret — README "Setup before first release"
  3. Demo asciinema-cast → gif — `docs/DEMO.md` со сценарием
  4. Локальный smoke по `docs/MANUAL_SMOKE.md` — 16-пунктный чеклист уже существует (Stage 2/3 артефакт)
- [x] Move plan to `docs/plans/completed/20260503-lazytg-stage4-release.md` — выполняется в этой же итерации
