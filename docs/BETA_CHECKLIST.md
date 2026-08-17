# lazytg v0.1.0-beta smoke checklist

> ⚠️ **Ban-risk reminder.** Telegram автоматически ставит unofficial-клиенты под observation
> (см. [официальную политику](https://core.telegram.org/api/obtaining_api_id)).
> **Используйте тестовый аккаунт.** После дела Дурова в августе 2024 enforcement резко вырос.
> Подробнее — [docs/SECURITY.md](SECURITY.md).

Пожалуйста, заполните и приложите к GitHub issue с лейблом `beta-feedback`.
Должно занять ≤15 минут. Спасибо!

---

## Tester info

- **OS:** __________ (macOS Sonoma / Ubuntu 22.04 / Fedora 39 / Debian 12 / etc)
- **Architecture:** __________ (arm64 / amd64)
- **Terminal:** __________ (Alacritty / iTerm2 / Ghostty / Kitty / WezTerm / tmux+xterm / etc)
- **Tmux:** yes / no
- **Telegram account type:** **тестовый** / основной (мы рекомендуем **тестовый**)
- **lazytg version:** __________ (вывод `lazytg version`)

---

## Smoke steps

Отметьте каждый пункт:
- ✅ если работает как описано
- ❌ если сломано (опишите ниже)
- ⚠️ если работает с замечаниями (опишите ниже)

### 1. Install

- [ ] Установка прошла одним из путей:
  - `brew install kar43lov/lazytg/lazytg` (macOS), либо
  - `sudo dpkg -i lazytg_*_linux_amd64.deb`, либо
  - `sudo dnf install lazytg_*_linux_amd64.rpm`, либо
  - manual binary из tar.gz + проверка через `cosign verify-blob` (см. [VERIFY.md](VERIFY.md))
- [ ] `lazytg version` показывает версию (`v0.1.0-beta.X`) без ошибок

### 2. Login

- [ ] Экспортировали `LAZYTG_API_ID` и `LAZYTG_API_HASH` (получены на https://my.telegram.org/apps)
- [ ] `lazytg login --account +<phone>` прошёл сценарий phone → code → 2FA без cryptic errors
- [ ] `lazytg accounts` показывает добавленный аккаунт (со звёздочкой как active)

### 3. Read

- [ ] `lazytg` (без подкоманды) открывает 2-pane TUI
- [ ] Слева — список чатов, отсортированный по последнему сообщению
- [ ] `↑/↓` (или `j/k`) перемещают выделение, `Enter` → справа загружается история
- [ ] Прокрутка вверх (`PgUp` / `Ctrl+B`) подгружает старые сообщения без потери позиции

### 4. Send

- [ ] `Tab` → фокус в Input
- [ ] Печать "hello from lazytg" + `Enter` → сообщение появилось мгновенно (optimistic, префикс `[⏳]`)
- [ ] Через ≤500ms префикс пропадает (state=sent)
- [ ] Сообщение видно на телефоне в Telegram-клиенте
- [ ] textarea очищается после отправки

### 5. Search

- [ ] `/привет` (или другое слово, которое точно есть в истории) → live-results появились через ≤500ms
- [ ] `Enter` на результате → переход в правильный чат + viewport позиционирован на нужное сообщение с контекстом
- [ ] Хотя бы один operator работает: `from:@user`, `in:#chat`, `before:DATE`, `after:DATE`, `has:file`, `"phrase"`, `-word`
- [ ] `Esc` закрывает overlay, фокус восстанавливается

### 6. Files

- [ ] На сообщении с фото или документом нажать `Ctrl+D` → в status-bar появился прогресс-chip
- [ ] По завершении файл лежит в `~/Downloads/lazytg/<chat-title>/<filename>` с permissions `0600`
- [ ] `Ctrl+U` в Input → file picker → выбрать любой `.txt` файл → отправлен в чат
- [ ] Загруженный файл виден в треде и на телефоне

---

## Free-form feedback

**Что понравилось:**

> _(коротко — что работает хорошо, чем приятно пользоваться)_

**Что сломано / медленно / непонятно:**

> _(шаги воспроизведения если возможно; latency в секундах если про perf)_

**Чего не хватает (но помните — мы целимся в v0.1.0, не v1.0):**

> _(см. roadmap v0.2/v0.3 в [CLAUDE.md](../CLAUDE.md), возможно, уже запланировано)_

**Готовы ли использовать ежедневно?**

- [ ] Да, заменяю Telegram Desktop
- [ ] Да, как дополнение к Telegram Desktop / Mobile
- [ ] Только если добавите: __________
- [ ] Нет, потому что: __________

---

## Что делать с этим чек-листом

1. Скопируйте раздел в новый GitHub issue: https://github.com/kar43lov/lazytg/issues/new
2. Лейбл: `beta-feedback`
3. Заголовок: `Beta smoke <OS> <arch> — <вердикт>` (например, `Beta smoke macOS arm64 — pass with notes`)
4. Если есть FAIL — приложите `lazytg debug-bundle` (gzip tarball без секретов, см. [TROUBLESHOOTING.md](TROUBLESHOOTING.md#collecting-a-debug-bundle))

Спасибо за помощь с beta-period! Без ≥3 confirmation мы не релизим stable v0.1.0.
