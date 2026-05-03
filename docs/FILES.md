# Files: download & upload (Stage 3)

Стадия 3 добавляет в lazytg оба направления передачи файлов:

- **download** через `Ctrl+D` (Stage 3 Task 6)
- **upload** через `Ctrl+U` (Stage 3 Task 7, отдельная итерация)

Документ описывает Stage 3 Task 6 — текущая реализация download-pipeline.

## Куда сохраняются файлы

По умолчанию: `~/Downloads/lazytg/<chat_title>/<filename>`.

- Корень переопределяется через env-переменную `LAZYTG_DOWNLOADS`.
- `<chat_title>` санитизируется: символы `/`, `\`, `..`, `:`, `*`, `?`, `"`, `<`, `>`, `|`, `\0`, control chars, leading/trailing dots/spaces заменяются на `_`. Это защищает от path-traversal через имя чата (имя задаёт собеседник) и от переноса на Windows-файловые системы.
- `<filename>` берётся из `DocumentAttributeFilename` если он есть; иначе fallback `document_<id>.bin` для документов и `photo_<id>.jpg` для фото.

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
| Cursor-навигация по сообщениям для выбора media | Stage 3 Task 6: `Ctrl+D` оперирует **последним** сообщением в thread с media. Per-message cursor запланирован на v0.2 |
| Inline media preview (Kitty/iTerm/sixel) | Месяцы работы. v0.3 |
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
internal/core/events/events.go  — FileDownload* типизированные события
internal/ui/panes/thread/       — display [📎 file, KiB] badge + Ctrl+D wiring
internal/ui/statusbar/          — UpsertDownload / RemoveDownload + render `⬇ filename N%`
internal/ui/app/update.go       — handleDownloadRequest → goroutine с timeout 30 минут
```

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
- Search service (Stage 3 Task 3) пока не индексирует media (нет `has:file` фильтра до миграции 0008 на `media_kind` колонку — placeholder в `query_builder.go`)
