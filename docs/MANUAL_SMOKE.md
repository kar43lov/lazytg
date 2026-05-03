# Manual Smoke Checklist (Stages 2 + 3)

Этот чек-лист дополняет автоматизированные unit/integration/e2e тесты в `internal/...` и `test/...` ручной проверкой UX-аспектов TUI, которые невозможно адекватно покрыть headless-тестами (визуальный рендер, latency восприятия, поведение в реальном терминале).

Прогон обязателен перед мерджем Stage 2 в `main` и перед каждым `v0.1.0-alpha.N` релизом.

## Подготовка

1. Соберите бинарь из ветки Stage 2:
   ```sh
   make build
   ./bin/lazytg version  # подтвердить, что собралось
   ```
2. Убедитесь, что есть хотя бы один залогиненный аккаунт (тестовый, не основной — см. ban-risk warning в `CLAUDE.md`):
   ```sh
   LAZYTG_API_ID=... LAZYTG_API_HASH=... ./bin/lazytg login --account +<phone>
   ./bin/lazytg accounts  # должен показать аккаунт со звёздочкой
   ```
3. Откройте лог-файл в отдельном tmux-pane для наблюдения:
   ```sh
   tail -F ~/.local/state/lazytg/lazytg.log  # Linux
   tail -F ~/Library/Application\ Support/lazytg/lazytg.log  # macOS
   ```

## Чек-лист

### 1. Запуск TUI без подкоманды

- [ ] `./bin/lazytg` (без аргументов) — открывается 2-pane TUI
- [ ] В левой панели появляется список чатов (если кешированные данные есть в SQLite — сразу; если нет — пусто, но без crash)
- [ ] Status-bar в нижней строке показывает `<phone> | - | unread 0 | connecting | rw` (или `online` если уже подключились)
- [ ] Терминал в alt-screen режиме (после выхода — терминал восстанавливается, scrollback не загажен)

### 2. Терминал слишком маленький

- [ ] Уменьшите окно терминала до 60×20 (или меньше) → появляется отцентрованное сообщение `Terminal too small (min 80x24)`
- [ ] Увеличите обратно — нормальный layout восстанавливается без перезапуска

### 3. Навигация и фокус

- [ ] `Tab` — фокус циклит `Chats → Input → Thread → Chats`
- [ ] `Shift+Tab` — фокус циклит в обратном направлении
- [ ] Активный pane выделен ярким синим (ANSI 12) на рамке
- [ ] Заголовок Chats pane меняется на `Chats (focused)` при фокусе

### 4. Список чатов

- [ ] Стрелки `↑/↓` или `j/k` (когда фокус на Chats pane) перемещают выделение
- [ ] Pinned чаты (📌) идут первыми
- [ ] Unread-счётчик отображается в скобках после названия (когда > 0)
- [ ] `/` запускает встроенный фильтр bubbles/list (опционально, не критично)

### 5. Открытие чата

- [ ] `Enter` на выбранном чате — справа загружается история сообщений
- [ ] Title чата появляется в status-bar
- [ ] Сообщения отображаются oldest-first (сверху вниз)
- [ ] Viewport начинает с самого свежего сообщения (snap to bottom)

### 6. Pagination

- [ ] Прокрутите Thread pane вверх (`PgUp` или `Ctrl+B`) до самого верха
- [ ] При наличии большего количества истории — догружается следующая страница (200 сообщений)
- [ ] Ваша позиция чтения остаётся стабильной после догрузки (не "прыгает" вниз)

### 7. Ввод и отправка сообщения

- [ ] `Tab` → фокус Input
- [ ] Печатаете текст — отображается в textarea с курсором `›`
- [ ] `Enter` — сообщение уходит, появляется в треде с префиксом `[⏳]` (optimistic, pending)
- [ ] Через ≤500ms префикс пропадает — message confirmed (state=sent)
- [ ] Telegram-клиент на телефоне получает то же самое сообщение
- [ ] textarea очищается после отправки

### 8. Multi-line newline

- [ ] `Alt+Enter` в Input pane — добавляется literal `\n`, можно набирать многострочный текст
- [ ] `Enter` (без Alt) на многострочном тексте — отправляет всё как одно сообщение

### 9. Reply

- [ ] Фокус Thread → выберите сообщение (если поддерживается выделение в треде, иначе reply на последнее)
- [ ] `Ctrl+R` — в Input pane появляется reply-hint `↳ Reply to: <preview>`
- [ ] Печатаете ответ → `Enter` → сообщение уходит как reply (на телефоне видно как replied-to)
- [ ] Reply-pointer сбрасывается после отправки

### 10. $EDITOR delegation

- [ ] `EDITOR=vi ./bin/lazytg` — запускаете
- [ ] В Input `Ctrl+E` → `vi` открывается с текущим содержимым textarea
- [ ] Сохраняете (`:wq`) → возвращаетесь в TUI, textarea содержит отредактированный текст
- [ ] Повторите с `EDITOR=nano` — то же поведение
- [ ] Повторите с `EDITOR=cat` — текст из textarea выводится, после `Ctrl+D` возврат в TUI

### 11. Help overlay

- [ ] `?` (когда не в textarea и не во время фильтрации) — открывается modal с табличкой `Action | Binding`
- [ ] Все hotkeys из default keymap присутствуют (send, newline, reply, open editor, focus next/prev, scroll up/down, quit)
- [ ] `Esc` или `q` или повторное `?` — закрывает overlay
- [ ] Пока overlay открыт — другие keys (`Tab`, печатные символы) игнорируются

### 12. Live updates

- [ ] Откройте чат с человеком, который активен в Telegram
- [ ] Попросите написать сообщение / отправьте себе с другого устройства
- [ ] Сообщение появляется в Thread pane без обновления, ≤500ms latency
- [ ] Если вы были scrolled-up — позиция чтения не меняется (sticky scroll работает)
- [ ] Если вы были на bottom — viewport скроллится к новому сообщению

### 13. Polling fallback (N/A в v0.1)

`--polling` флаг зарезервирован, но в v0.1 — no-op (см. CHANGELOG `Known gaps`).
Шаг пропускается до wire-up в follow-up релизе.

### 14. Reconnect

- [ ] Запустите TUI, дождитесь `online` в status-bar
- [ ] Отключите интернет на 30 секунд
- [ ] Status-bar переключается на `offline` (красный)
- [ ] Включите интернет обратно
- [ ] Status-bar переходит через `connecting` (жёлтый) → `online` (зелёный)
- [ ] Сообщения, пришедшие во время disconnect, доезжают через difference recovery

### 15. Read-only degradation

- [ ] Закройте TUI
- [ ] `chmod 0444 ~/Library/Application\ Support/lazytg/lazytg.db` (macOS) или эквивалент на Linux
- [ ] Запустите TUI заново
- [ ] Status-bar показывает `read-only` в storage-cell
- [ ] Чтение работает (чаты + история отображаются)
- [ ] Попытка отправить сообщение → ошибка graceful (не паника)
- [ ] `chmod 0600` обратно — после ≤30 секунд status flip на `rw` без перезапуска

### 16. Quit

- [ ] `Ctrl+C` или `Ctrl+Q` — TUI завершается чисто
- [ ] Терминал восстанавливается (alt-screen exit)
- [ ] `goroutine leak` отсутствует (можно проверить через `lsof | grep lazytg` после `kill`)

## Stage 3 — Search, Files, Палитра, Безопасность

### 17. Search overlay

- [ ] `/` (когда фокус не в Input/Chats-filter) — открывается search overlay поверх 2-pane
- [ ] Печатаете `привет` → через ≤200ms появляются результаты с подсветкой совпадений
- [ ] `↑/↓` — переключают cursor по результатам
- [ ] `Enter` на результате → переход в нужный чат + viewport позиционирован на сообщение с контекстом ±5
- [ ] `Esc` — overlay закрывается, фокус восстанавливается на исходный pane
- [ ] Operators работают:
  - [ ] `from:@username` — только сообщения от пользователя
  - [ ] `in:#chat` — только в указанном чате
  - [ ] `before:2026-01-01` / `after:2025-12-01` — фильтр по дате
  - [ ] `has:file` — только сообщения с media
  - [ ] `"точная фраза"` — phrase search
  - [ ] `-слово` — exclusion

### 18. Командная палитра L1

- [ ] `Ctrl+Space` (или `Ctrl+@`) — открывается палитра по центру экрана
- [ ] Пустой query → top-50 чатов в порядке frecency (свежие × частые)
- [ ] Печатаете `Алёна` → находит чат "Алена" (Unicode normalize: ё → е)
- [ ] `↑/↓` + `Enter` — переход в чат
- [ ] `Esc` — закрытие
- [ ] После выбора чата frecency обновляется (закрыть-открыть палитру → выбранный чат поднялся вверх)

### 19. Files: download (Ctrl-D)

- [ ] Откройте чат с сообщением, содержащим фото или документ
- [ ] Status-bar в Thread показывает badge типа `[📎 report.pdf, 229.1 KiB] ctrl+d to save`
- [ ] `Ctrl+D` — в status-bar появляется ⬇ chip с прогрессом
- [ ] По завершении файл лежит в `~/Downloads/lazytg/<chat-title>/<filename>` (sanitized: слеши заменены на `_`)
- [ ] Permissions файла = `0600`
- [ ] Повторный `Ctrl+D` на том же сообщении → instant complete (dedup hit)
- [ ] Удалите файл, повторный `Ctrl+D` → перезакачка (stale dedup honoured)

### 20. Files: upload (Ctrl-U)

- [ ] Откройте любой чат, поставьте фокус в Input
- [ ] `Ctrl+U` — открывается attach overlay с file picker (path + caption)
- [ ] `Tab` — переключает между path и caption полями
- [ ] Навигация по директориям через `Enter` на папке
- [ ] `Enter` на regular file → overlay закрывается, в status-bar появляется ⬆ chip
- [ ] По завершении сообщение появляется в треде
- [ ] Большой файл (>50 MiB) — в логах warning, но загрузка продолжается
- [ ] Очень большой файл (>2 GiB) — отказ с понятной ошибкой

### 21. DB size warning

- [ ] При размере `~/.local/share/lazytg/lazytg.db` (Linux) / `~/Library/Application Support/lazytg/lazytg.db` (macOS) > 1 GiB → в status-bar появляется yellow chip типа `⚠ DB 1.2 GB`
- [ ] Возврат под порог — chip исчезает (через ≤60 секунд)

### 22. debug-bundle без секретов

- [ ] `./bin/lazytg debug-bundle` → создаёт `lazytg-bundle-<timestamp>.tar.gz` в cwd
- [ ] Распакуйте: `tar -xzf lazytg-bundle-*.tar.gz -C /tmp/bundle/`
- [ ] `grep -ER "api_hash|^[A-Za-z0-9+/=]{32,}$|<phone>" /tmp/bundle/` → 0 матчей (грэп-тест автоматизирован, но визуальная проверка не повредит)
- [ ] Файл tar.gz имеет permissions `0600`
- [ ] Внутри: `version.txt`, `config.toml` (или placeholder), `logs.txt`, `db_stats.txt`, `goroutines.txt`

### 23. Permission check fail-fast

- [ ] Закройте TUI
- [ ] `chmod 0644 ~/.config/lazytg/secrets.age` (Linux) или эквивалент на macOS
- [ ] Запустите `./bin/lazytg` → fail с понятным сообщением «security: startup audit ... permissions ... actual_mode 0644» и невыходом в TUI
- [ ] `chmod 0600` обратно — TUI запускается нормально

### 24. Send rate-limit (manual stress test)

- [ ] Отправьте 30 сообщений подряд через Input pane (зажав Enter с paste-buffer'ом)
- [ ] Первые ~30 уходят без задержки (burst capacity)
- [ ] Последующие сообщения (31+) уходят со скоростью ~10 msg/sec — заметная задержка между каждой отправкой
- [ ] В логах нет flood-wait ошибок от MTProto

### 25. lazytg reindex

- [ ] `./bin/lazytg reindex --chat 12345` → stderr: `reindexing chat 12345…` → `chat 12345 · N rows · cumulative N` → `done · total N rows indexed`
- [ ] `./bin/lazytg reindex --all` — то же для всех чатов
- [ ] `./bin/lazytg reindex` без флагов → понятная ошибка
- [ ] `./bin/lazytg reindex --all --chat 12345` → понятная ошибка (mutually exclusive)

## Запись результатов

После прохождения чек-листа:

1. Создайте issue (или PR-комментарий) с заголовком `Stage 2 manual smoke <YYYY-MM-DD>`
2. Скопируйте чек-лист, отметьте `[x]` для пройденных пунктов, опишите проблемы для упавших
3. Приложите версию: `./bin/lazytg version` output
4. Если все пункты `[x]` — Stage 2 готов к мерджу / релизу
5. Если есть FAIL — заведите отдельные issues с воспроизведением и блокируйте релиз до фикса

## Notes для ревьюера

- Latency-чувствительные пункты (5, 7, 12) — субъективная оценка. Если есть сомнения — снимите видео экрана с timer'ом и проверьте по кадру.
- `$EDITOR` тест критичен для users в tmux+nvim setup'е (наш target audience).
- Read-only degradation — единственный способ проверить graceful behavior; auto-test покрывает только event-логику.
- Этот чек-лист обновляется при каждом изменении hotkey'ев или поведения паней. Расхождение между документом и кодом — bug.
