# Files: download & upload (Stage 3)

Стадия 3 добавляет в lazytg оба направления передачи файлов:

- **download** через `Ctrl+D` (Stage 3 Task 6)
- **upload** через `Ctrl+U` (Stage 3 Task 7)

Оба пути живут в `internal/core/files/` (gotd-free orchestration: `download.go`, `upload.go`, `store.go`, `dedup.go`, `progress.go`) и подключаются к gotd через `internal/tg/files.go` (`Downloader`/`Uploader`/`FilesAdapter`/`MediaFromMessage`). `UploadService` подключается к тому же `security.SendGuard` (10 msg/s, burst 30), что и `coresync.SendService` — отправка медиа считается так же, как отправка текста, по поведенческому fingerprint.

## Куда сохраняются файлы

По умолчанию: `~/Downloads/lazytg/<chat_title>/<filename>`.

- Корень переопределяется через env-переменную `LAZYTG_DOWNLOADS`.
- `<chat_title>` санитизируется: символы `/`, `\`, `..`, `:`, `*`, `?`, `"`, `<`, `>`, `|`, `\0`, control chars, leading/trailing dots/spaces заменяются на `_`. Это защищает от path-traversal через имя чата (имя задаёт собеседник) и от переноса на Windows-файловые системы.
- `<filename>` берётся из `DocumentAttributeFilename` если он есть; иначе fallback по виду вложения — `video_note_<id>.mp4`, `voice_<id>.ogg`, `video_<id>.mp4`, `animation_<id>.mp4`, `audio_<id>.mp3`, `sticker_<id>.webp`, `photo_<id>.jpg`, и `document_<id>.bin` для всего остального. Расширение важно не только для читаемости: системный просмотрщик выбирает приложение именно по нему.
- 🔴 **Имена не уникальны, и это учтено.** Telegram не спрашивает у отправителя имя файла: любое видео с телефона приезжает как `video.mp4`. До 04.09.2026 такие вложения писались в один путь — второе скачивание молча затирало первое, а таблица дедупа после этого держала два `file_id` на один файл и отдавала по старшему чужие байты (воспроизведено на реальном зеркале: восемь вложений, одно имя). Теперь `FileStore.Reserve` подбирает свободное имя в браузерном стиле — `video.mp4`, `video (2).mp4`, … — и **занимает** его, создавая `.partial` с `O_EXCL`: две загрузки, стартовавшие одновременно, не могут выбрать одно имя.

Папки создаются с `0700`, файлы с `0600` — соответствует базовому требованию SECURITY.md, медиа не утекает на multi-user системе.

## Дедупликация

Каждый успешный download записывается в таблицу `downloaded_files` (миграция 0007). Повторный `Ctrl+D` на том же сообщении (тот же Telegram `file_id`) → возвращает кэшированный путь без повторного скачивания. Это критично для:

- больших файлов (1+ ГБ): экономит время и трафик
- медленных каналов: не приходится ждать снова
- offline-режима: файл уже на диске, открывается без сети

Если файл удалён пользователем (`rm ~/Downloads/lazytg/.../report.pdf`) — следующий `Ctrl+D` обнаруживает отсутствие через `os.Stat` и заново скачивает.

## События в шине

Download-цикл публикует:

| Событие | Когда |
|--------|-------|
| `FileDownloadStarted{FileID, ChatID, Path, Filename, Size}` | После dedup-checks, до начала записи |
| `FileDownloadProgress{FileID, BytesDownloaded, TotalBytes}` | Каждые 1 MiB или 5% (whichever first) |
| `FileDownloadCompleted{FileID, Path, Size}` | После rename `.partial → final` |
| `FileDownloadFailed{FileID, Err}` | На любой ошибке (рендер, FS, MTProto) |

Statusbar подписан на эти события, отрисовывает `⬇ filename 47%` в правой части. Несколько одновременных загрузок — показывается одна (наименьший `FileID`); v0.2 даст expanded `⬇ 3 files` чип.

## v0.1 ограничения (документированные не-цели)

| Что НЕ работает | Почему |
|-----------------|--------|
| Resume прерванной загрузки | gotd-downloader не возобновляет сессию по offset; реализация требует client-side reassembly. Будет в v0.3 |
| ~~Cursor-навигация по сообщениям для выбора media~~ | **Закрыто 04.09.2026.** Курсор по сообщениям (`↑`/`↓`, `k`/`j`, клик) есть; `Ctrl+D` и `o` берут вложение под курсором или ближайшее выше него. Нетронутый курсор стоит на последнем сообщении, поэтому старое поведение сохранилось |
| Inline media preview (Kitty/iTerm/sixel) | v0.3. Просмотр закрыт иначе: `o` отдаёт файл системному просмотрщику. Для кружочка и видео это единственный честный ответ — терминал видео не рисует |
| Upload (`Ctrl+U`) | Stage 3 Task 7 — отдельная итерация |
| Файлы > 2 ГБ | gotd поддерживает, но в Telegram free hard-limit ~2 ГБ. Файл скачается, обработка лимитов на стороне сервера |
| FILE_REFERENCE_EXPIRED auto-refresh | v0.1 возвращает `tg.ErrFileReferenceExpired` ошибку. v0.2 переавтоматически перезапросит сообщение через `messages.getMessages` и повторит |

## Архитектура

```
internal/tg/files.go            — gotd wrapper (Downloader, MediaFromMessage)
internal/core/files/store.go    — on-disk path resolution + sanitization
internal/core/files/dedup.go    — кэш downloaded_files
internal/core/files/download.go — orchestration: dedup → mkdir → tmp → progress → rename → record
internal/core/files/progress.go — throttler (1 MiB / 5%)
internal/core/files/open.go     — Opener: запуск системного просмотрщика (exec без shell)
internal/core/events/events.go  — FileDownload* типизированные события
internal/ui/panes/thread/       — бейдж [⏺ video note, 0:07, 1.2 MiB], курсор по сообщениям, Ctrl+D / o wiring
internal/ui/statusbar/          — UpsertDownload / RemoveDownload + render `⬇ filename N%`
internal/ui/app/update.go       — handleDownloadRequest / handleOpenRequest → goroutine с timeout 30 минут
```

## Просмотр вложения

`o` (или клик по строке бейджа) скачивает вложение, если его ещё нет на диске, и отдаёт путь системному просмотрщику. Повторное нажатие бесплатно: дедуп отдаёт кэшированный путь, сеть не трогается.

- Программа по умолчанию: `open` на macOS, `xdg-open` на *BSD/Linux. Переопределяется через `LAZYTG_OPEN_CMD` — например `LAZYTG_OPEN_CMD="mpv --loop"` для кружочков, которые в официальном клиенте играют по кругу.
- Просмотрщик запускается **напрямую**, без shell: аргумент — имя файла, выбранное отправителем, и shell сделал бы его пунктуацию исполняемой. Путь обязан быть абсолютным и существовать (иначе просмотрщик прочитал бы ведущий дефис как флаг).
- `stdin`/`stdout`/`stderr` просмотрщику не отдаются: программа, пишущая в терминал, закрасила бы отрисованный тред, а читающая stdin — украла бы клавиши.
- lazytg не ждёт закрытия окна и не отменяет просмотрщик при выходе из скачивания: контекст загрузки к этому моменту уже отменяется, и он убил бы только что открытое окно.
- Просмотрщика может не быть (неизвестная платформа, кривой `LAZYTG_OPEN_CMD`). Тогда причина пишется в лог один раз при старте, а `o` работает как обычное скачивание.

Зависимости расположены так:

- `internal/core/files` НЕ импортирует `gotd/td` или `bubbletea` (depguard enforce)
- `internal/tg/files.go` — единственное место где gotd-типы (`InputDocumentFileLocation`, `Photo`, `Document`) переводятся в `domain.MediaInfo`
- Тесты `internal/core/files` используют fake `Downloader` — не нуждаются в gotd

## Manual smoke

```bash
# Запустить TUI
LAZYTG_DOWNLOADS=/tmp/lazytg-downloads lazytg

# В чате с media-сообщением (последнее в thread):
# 1. Перейти в thread (Tab несколько раз)
# 2. Нажать Ctrl+D
# 3. Проверить статусбар: `⬇ filename N%`
# 4. После завершения: cat /tmp/lazytg-downloads/<chat>/<filename>
# 5. Повторный Ctrl+D — мгновенно, без сети
```

## SLA

- p95 latency `Ctrl+D` press → first byte: <500ms на typical home link (зависит от gotd handshake)
- progress event throttle: ≤ 200 events / sec для одного файла (1 MiB порог при 200 MB/s)
- chmod после rename — гарантированно `0600` независимо от umask пользователя

## Связи

- `core/sync/live.go` сохраняет `Media` поле из `MessageReceived` событий — media поступает в БД и через live-updates, и через history backfill
- Search-фильтр `has:file` (Task 6 → миграция 0008 + `media_kind` колонка) реально работает: `query_builder.go` эмитит `m.media_kind IS NOT NULL`, документ-сообщения попадают в выдачу, текстовые исключаются
- `UploadService` уважает `security.SendGuard` (тот же token bucket 10 msg/s, что и для текстовых send): `messages.SendMedia` ждёт токена так же, как `messages.SendMessage` (см. CLAUDE.md ban-risk пункт 7)
