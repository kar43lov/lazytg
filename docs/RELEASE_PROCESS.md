# Release process — runbook для maintainer'а

> Этот документ описывает шаги для выпуска версии lazytg от `v0.1.0-alpha.1` до stable `v0.1.0` (и далее `v0.X.Y`).
>
> **Принцип pre-release pipeline:**
> `v*-alpha.*` → `v*-beta.*` → `v*-rc.*` → `v*` (stable).
> Brew/scoop/.deb publish обновляются **только** для stable. Alpha/beta/rc артефакты лежат в GitHub Releases с флагом `prerelease=true`.

---

## Подготовка (один раз перед первым release)

1. Создать репо `pgmac/homebrew-lazytg` на GitHub (пустой, с одним каталогом `Formula/`).
2. Сгенерировать Personal Access Token с scope `contents:write` на этот репо. Можно классический PAT или fine-grained.
3. Добавить токен в org/repo secrets текущего репозитория `pgmac/lazytg` под именем `HOMEBREW_TAP_GITHUB_TOKEN`. Это используется goreleaser-action.
4. Установить локально (для генерации changelog и snapshot-тестов):
   ```sh
   brew install git-cliff cosign goreleaser
   ```
5. (Опционально) Установить `actionlint` и `act` для local-validation workflow-файлов.

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
   goreleaser release --snapshot --clean --skip=publish --skip=announce
   ```
   В `dist/` должны появиться tar.gz для всех 4 платформ + `.deb` + `.rpm`.

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
   - **brew formula обновляется** в `pgmac/homebrew-lazytg/Formula/lazytg.rb`
   - **.deb / .rpm** доступны в Release assets (publishing в публичные APT/DNF репозитории — отложено на v0.2)

4. Проверить:
   ```sh
   brew update
   brew install pgmac/lazytg/lazytg
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
| Brew formula не обновилась | Проверить `HOMEBREW_TAP_GITHUB_TOKEN` secret + scope `contents:write` на `pgmac/homebrew-lazytg` | Перегенерировать PAT, обновить secret |
| `nfpms` не генерирует .deb | Проверить что `builds: [lazytg]` (только pure-Go ID) | В `.goreleaser.yaml` секция `nfpms[].ids` должна содержать `lazytg` |
| Тег от prerelease.yml не запустил release.yml | GitHub блокирует recursive workflow dispatch для тегов от GITHUB_TOKEN | `gh workflow run release.yml --ref <tag>` (workflow_dispatch добавлен) |
| GitHub Release создан, но без assets | goreleaser завершился до upload — смотреть логи job | Перезапустить workflow вручную через `gh workflow run release.yml --ref <tag>` |
| `cosign verify-blob` падает у пользователя | Mismatch certificate-identity-regexp | Убедиться что pattern в VERIFY.md соответствует реальному pattern в OIDC claim |

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
