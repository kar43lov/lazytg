# Release process — runbook для maintainer'а

> Этот документ описывает шаги для выпуска версии lazytg от `v0.1.0-alpha.1` до stable `v0.1.0` (и далее `v0.X.Y`).
>
> **Принцип pre-release pipeline:**
> `v*-alpha.*` → `v*-beta.*` → `v*-rc.*` → `v*` (stable).
> Brew/scoop/.deb publish обновляются **только** для stable. Alpha/beta/rc артефакты лежат в GitHub Releases с флагом `prerelease=true`.

---

## Подготовка (один раз перед первым release)

1. Создать репо `kar43lov/homebrew-lazytg` на GitHub (пустой, с одним каталогом `Formula/`).
2. Сгенерировать Personal Access Token с scope `contents:write` на этот репо. Можно классический PAT или fine-grained.
3. Добавить токен в org/repo secrets текущего репозитория `kar43lov/lazytg` под именем `HOMEBREW_TAP_GITHUB_TOKEN`. Это используется goreleaser-action.
4. **Telegram API credentials в repo secrets.** Создать приложение на <https://my.telegram.org/apps> и добавить два secret'а в `kar43lov/lazytg`:
   - `LAZYTG_RELEASE_API_ID` — целое число;
   - `LAZYTG_RELEASE_API_HASH` — 32 hex-символа.

   `release.yml` пробрасывает их в goreleaser, а `.goreleaser.yaml` вшивает через `-ldflags` в бинарники — пользователь скачивает релиз и логинится без регистрации своего приложения. Имена намеренно не совпадают с runtime-переменными `LAZYTG_API_ID` / `LAZYTG_API_HASH`: те, скорее всего, экспортированы в шелле мейнтейнера, и совпадение имён привело бы к тихой публикации личных кредов в релизе.

   🔴 **Эти значения не должны попадать в git.** `scripts/secret-scan.sh` блокирует коммит (lefthook `pre-commit`) и валит CI (job `secret-scan`) на любой 32-hex строке. Опубликованный в исходниках `api_id` Telegram блокирует навсегда (`API_ID_PUBLISHED_FLOOD`) — это отзовётся у всех пользователей релиза разом.

   Что гейт **не** ловит (перечислено в шапке самого скрипта): намеренно разбитый на части или закодированный ключ, hex длиннее 32 символов, содержащий ключ, uppercase-hex, и — главное — уже существующую git-историю. Гейт защищает от случайной вставки, а не от обхода. Сделать `secret-scan` **required status check** в branch protection: без этого PR с утечкой можно смержить, пока job ещё идёт.

   🔴 **Не запускать goreleaser с `--debug` на release-раннере.** GitHub маскирует значения `secrets.*` в логах, но debug-вывод печатает собранную команду `go build` целиком, и на частичных совпадениях маскирование не гарантировано.

   Если secrets не заданы — релиз соберётся, но бинарники будут требовать от пользователя свои креды (`api: none` в `lazytg version`). Это деградация, не поломка.
5. Установить локально (для генерации changelog и snapshot-тестов):
   ```sh
   brew install git-cliff cosign goreleaser
   ```
6. (Опционально) Установить `actionlint` и `act` для local-validation workflow-файлов.

---

## Pre-flight checks (перед каждым release)

1. **Main green.** CI (`ci.yml`) на `main` зелёный.
2. **Coverage gates passed.** В CI-логах последнего merge: `core ≥80%`, `ui ≥60%`.
3. **Benchmark gates passed.**
   - Search: p95 <100ms на 100k msgs (см. CI-job `BenchmarkSearch100k`).
   - Live-updates: p95 <500ms (`BenchmarkLiveUpdateLatency`).
   - Memory: idle <50MB, active <150MB (`TestMemoryBudget_*`).
4. **Stage docs актуальны.** `CHANGELOG.md` Unreleased section не пустой; новые feat/fix/perf entries присутствуют.
5. **Snapshot release passes.**
   ```sh
   goreleaser check
   goreleaser release --snapshot --clean --skip=publish,announce,sign
   ```
   В `dist/` должны появиться tar.gz для всех 4 платформ + `.deb` + `.rpm`.
   `--skip=sign` обязателен локально — cosign keyless OIDC требует GitHub Actions runtime context и без него сборка падает на signs step. Подпись валидируется в release.yml.
6. **Креды вшиты в релизный бинарник.** Шаг `Guard Telegram API credentials` в `release.yml` валит stable-тег при пустых secrets (для alpha/beta/rc — только `::warning`), так что молча уехать без кредов релиз уже не может. Финальная проверка на скачанном артефакте всё равно нужна — guard проверяет наличие secrets, а не то, что goreleaser их применил:
   ```sh
   ./lazytg version | grep api      # → api:    embedded (build embeds credentials: yes)
   ```

   Локальный snapshot по умолчанию даёт `api: none` — это правильно: `LAZYTG_RELEASE_API_*` не заданы на машине мейнтейнера. Проверить инъекцию локально можно, подставив фиктивные значения:
   ```sh
   LAZYTG_RELEASE_API_ID=1 LAZYTG_RELEASE_API_HASH=deadbeef goreleaser build --snapshot --clean --single-target
   ```

---

## Release flow

### Этап A — Alpha (внутреннее тестирование)

1. Обновить `CHANGELOG.md`. `git-cliff --prepend` вставляет новую секцию `## [<version>]` поверх файла, **но не сливает её с существующей `## [Unreleased]`**: вручную-курированный prose остаётся под автогенерированной из коммитов секцией. Поэтому стандартный flow:
   ```sh
   git-cliff --tag v0.1.0-alpha.1 --unreleased --prepend CHANGELOG.md
   # Открыть CHANGELOG.md, перенести содержимое existing [Unreleased] секции
   # в новую [0.1.0-alpha.1] (объединить Added/Fixed/etc.), удалить пустой
   # [Unreleased] заголовок. Curated prose всегда побеждает автогенерированный
   # commit-message список — последний используется только как чеклист "не забыл ли".
   ```
   _Если git-cliff не установлен — отредактировать `CHANGELOG.md` вручную, переименовав `## [Unreleased]` → `## [0.1.0-alpha.1] - YYYY-MM-DD`._

2. Commit изменений:
   ```sh
   git add CHANGELOG.md
   git commit -m "chore(release): prepare v0.1.0-alpha.1"
   ```

3. Создать annotated tag:
   ```sh
   git tag -a v0.1.0-alpha.1 -m "v0.1.0-alpha.1"
   ```

4. Push:
   ```sh
   git push origin main
   git push origin v0.1.0-alpha.1
   ```

5. CI (`release.yml`) автоматически:
   - Запустит goreleaser
   - Соберёт артефакты (pure-Go × 4 платформы — linux/darwin × amd64/arm64; SQLCipher отложен past v0.1)
   - Подпишет через cosign keyless OIDC (sigstore bundle per-archive + checksums.txt)
   - Создаст GitHub Release с `prerelease=true`
   - **НЕ** обновит brew formula (skip_upload по `.Prerelease` template)
   - **НЕ** опубликует .deb/.rpm в публичные репозитории (только как assets в Release)

   _Альтернативно — если тег создан через `gh workflow run prerelease.yml`, release.yml **не триггерится автоматически** (GitHub блокирует recursive workflow dispatch для тегов, запушенных GITHUB_TOKEN). Запустить вручную:_

   ```sh
   gh workflow run release.yml --ref v0.1.0-alpha.1
   ```

6. Проверить GitHub Release вручную:
   - Скачать `lazytg_*_darwin_arm64.tar.gz` + `.sigstore.json`
   - Прогнать `cosign verify-blob` по инструкции из `docs/VERIFY.md`
   - Распаковать и `lazytg version` должна показать `v0.1.0-alpha.1`

7. Прогнать `docs/MANUAL_SMOKE.md` локально на тестовом аккаунте (шаги 1-25).

### Этап B — Beta (external testers)

1. Повторить шаги 1-6 из этапа A с тегом `v0.1.0-beta.1`.

2. Разослать `docs/BETA_CHECKLIST.md` в каналы:
   - Telegram-канал проекта (если есть)
   - Лично знакомым developers, использующим Telegram + tmux
   - r/commandline beta-thread (если уместно)

3. **Wait для ≥3 confirmation.** Собрать issues с лейблом `beta-feedback`. Если есть FAIL — фиксы → `v0.1.0-beta.2` → повтор.

4. Когда есть ≥3 PASS confirmations и нет блокирующих bug reports — переходим к этапу C.

### Этап C — RC (release candidate)

1. Повторить шаги 1-6 с тегом `v0.1.0-rc.1`.

2. Финальный smoke-цикл (как в этапе A, шаг 7).

3. Если RC чистый — переходим к stable. Если есть blocking bugs — фиксы → `v0.1.0-rc.2` → повтор.

### Этап D — Stable

1. Обновить `CHANGELOG.md` финально. Та же мерж-процедура что и в Этапе A — `--prepend` создаёт пустой `[0.1.0]` поверх остаточного `[Unreleased]`; вручную объединить с предыдущими alpha/beta/rc entries в одну итоговую `## [0.1.0]` секцию:
   ```sh
   git-cliff --tag v0.1.0 --unreleased --prepend CHANGELOG.md
   # Слить [0.1.0] + остатки [Unreleased] + всё содержимое предыдущих
   # [0.1.0-alpha.N]/[0.1.0-beta.N]/[0.1.0-rc.N] секций в одну [0.1.0],
   # удалить пустой [Unreleased] и subsumed prerelease-секции (опционально
   # оставить их как историю — оба варианта приемлемы).
   ```

2. Commit + tag + push:
   ```sh
   git add CHANGELOG.md
   git commit -m "chore(release): release v0.1.0"
   git tag -a v0.1.0 -m "v0.1.0"
   git push origin main
   git push origin v0.1.0
   ```

3. CI запустит release.yml. На этот раз `release.prerelease: auto` определит, что суффикса нет → `prerelease=false`:
   - GitHub Release создаётся как stable (не prerelease)
   - **brew formula обновляется** в `kar43lov/homebrew-lazytg/Formula/lazytg.rb`
   - **.deb / .rpm** доступны в Release assets (publishing в публичные APT/DNF репозитории — отложено на v0.2)

4. Проверить:
   ```sh
   brew update
   brew install kar43lov/lazytg/lazytg
   lazytg version  # должно показать v0.1.0
   ```

5. **Анонс.** Открыть `docs/RELEASE_ANNOUNCE.md`, заменить плейсхолдеры (`<version>`, `<demo-link>`, цифры из последних бенчмарков), вычитать tone, опубликовать в выбранные каналы (Show HN / r/commandline / lobste.rs / r/golang).

---

## Hotfix (после stable)

Для срочного фикса (например, `v0.1.0` → `v0.1.1`):

1. Создать ветку `hotfix/v0.1.1` от тега `v0.1.0` (если main ушёл далеко) или прямо от main (если main = v0.1.0).
2. Применить фикс. Тесты + CI зелёные.
3. Обновить `CHANGELOG.md`: новая `## [0.1.1] - YYYY-MM-DD` с одним fix-entry.
4. Tag `v0.1.1` (без alpha/beta — hotfix идёт сразу в stable).
5. Push tag → release.yml → brew/nfpm обновляются.

Hotfix без beta-периода — допустимо для **только** security/data-loss/build-breakage фиксов. Для feature changes — полный alpha → beta → rc → stable.

---

## Откат release (rollback)

GitHub Releases можно пометить как deprecated, но **удалять опубликованные теги — нельзя** (downstream может уже скачать checksums). Если release сломан:

1. Опубликовать hotfix `v0.1.1` (см. выше).
2. В описании сломанного release добавить большой warning со ссылкой на hotfix.
3. Brew tap: запушить новую формулу с указанием `v0.1.1`. `brew upgrade` подтянет автоматически.
4. **Не удалять** GitHub Release сломанной версии — это нарушит cosign verification у людей, кто уже скачал.

---

## Troubleshooting release pipeline

| Симптом | Диагностика | Решение |
|---------|-------------|---------|
| `goreleaser` падает на cosign sign | Проверить что `id-token: write` permission в release.yml | Добавить `permissions: id-token: write, contents: write` в job |
| Brew formula не обновилась | Проверить `HOMEBREW_TAP_GITHUB_TOKEN` secret + scope `contents:write` на `kar43lov/homebrew-lazytg` | Перегенерировать PAT, обновить secret |
| `nfpms` не генерирует .deb | Проверить что `builds: [lazytg]` (только pure-Go ID) | В `.goreleaser.yaml` секция `nfpms[].ids` должна содержать `lazytg` |
| Тег от prerelease.yml не запустил release.yml | GitHub блокирует recursive workflow dispatch для тегов от GITHUB_TOKEN | `gh workflow run release.yml --ref <tag>` (workflow_dispatch добавлен) |
| GitHub Release создан, но без assets | goreleaser завершился до upload — смотреть логи job | Перезапустить workflow вручную через `gh workflow run release.yml --ref <tag>` |
| `cosign verify-blob` падает у пользователя | Mismatch certificate-identity-regexp | Убедиться что pattern в VERIFY.md соответствует реальному pattern в OIDC claim |
| `lazytg version` в релизе показывает `api: none` | Оба secret'а не заданы. Шаг `Guard Telegram API credentials` пропускает это для alpha/beta/rc (только warning) и валит для stable | Добавить оба secret'а, перевыпустить релиз. Пустой шаблон `index .Env` не роняет сборку — отсюда возможность тихой деградации на prerelease |
| `lazytg version` показывает `misconfigured: build-time credentials are malformed` | Задан ровно один из двух secret'ов — половинчатый embedded-слой. Такой бинарник не логинится из коробки и пугает пользователя ошибкой «report this» | Guard валит этот случай на **любом** типе тега. Если релиз всё же уехал — задать второй secret и выпустить patch |
| Пользователи массово получают `API_ID_PUBLISHED_FLOOD` | Релизный `api_id` заблокирован Telegram | Зарегистрировать новое приложение, обновить secrets, выпустить patch-релиз. В release notes — инструкция про `LAZYTG_API_ID`/`LAZYTG_API_HASH` как немедленный обход без ожидания релиза |
| `secret-scan` job красный | В отслеживаемых файлах 32-hex строка | Убрать значение из файлов и из истории коммитов ветки; если это ложное срабатывание — добавить в allowlist `scripts/secret-scan.sh` |

---

## Чек-лист перед merge `release` PR (если используется PR-based flow)

- [ ] CI зелёный
- [ ] Coverage gates passed
- [ ] Benchmark gates passed
- [ ] CHANGELOG.md содержит новую версию
- [ ] `goreleaser check` zero errors
- [ ] Snapshot release собирается локально
- [ ] BETA confirmations ≥3 (для stable)
- [ ] Manual smoke прогон на тестовом аккаунте (для stable / rc)
- [ ] `RELEASE_ANNOUNCE.md` отредактирован (плейсхолдеры, цифры) для stable
