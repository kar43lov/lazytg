# Release announcement — draft

> ⚠️ **Этот документ — draft для maintainer'а.** Не публикуем автоматически.
> Используется на финальной фазе release-процесса (см. [RELEASE_PROCESS.md](RELEASE_PROCESS.md), Этап D — Stable, шаг 5).
> Перед публикацией — заменить плейсхолдеры (`<version>`, `<demo-link>`, цифры из последнего benchmark прогона), вычитать tone-of-voice, убедиться в честности (alpha quality, ban-risk).

---

## Pitch (одна строка)

**lazytg — local-first Telegram TUI client with FTS5 search, written in pure Go. For developers who live in tmux+nvim+ssh.**

---

## Highlights

- 🔍 **Instant search across your entire history.** SQLite FTS5 trigram-индекс локально на диске. p95 ≈ 47ms на 100k сообщений (SLA <100ms — запас 2×). Никакого зависания на серверном `messages.search`.
- ⚡ **Live-updates < 5ms p95.** Полученное сообщение → отрисовка в TUI в среднем за 4ms (SLA <500ms — запас 100×). Через `gotd updates.Manager` поверх SQLite-state.
- 🔐 **Local-first.** История + индекс на вашем диске. Сессии в Keychain / Secret Service / wincred (через `zalando/go-keyring`). Encrypted DB (`sqlcipher` build tag) отложена на v0.2 — сейчас полагаемся на permission audit (0600) + OS-level disk encryption.
- ⌨️ **emacs/readline ввод по умолчанию + $EDITOR delegation.** `Ctrl+E` открывает `$EDITOR` для длинных сообщений. Vim-mode — opt-in в v0.2 (намеренно — половинчатая реализация даёт mode confusion, см. dialectic в плане).
- 📥📤 **Files.** `Ctrl+D` — download с прогрессом. `Ctrl+U` — upload с file picker. Permissions автоматически `0600`.
- 🛡️ **Ban-risk-aware.** Send rate-limit guard 10 msg/sec (снижает поведенческий след). `lazytg debug-bundle` без секретов (доказано grep-тестом). Permission check 0600/0700 fail-fast при старте.
- ✅ **Cosign-verified binaries.** Все артефакты подписаны через keyless OIDC из GitHub Actions. Sigstore bundles рядом с tar.gz. `cosign verify-blob` команда в [VERIFY.md](VERIFY.md).
- 📦 **Distribution:** Homebrew tap (`pgmac/homebrew-lazytg`), `.deb`, `.rpm`, raw tar.gz для linux+darwin × amd64+arm64.
- 🪶 **Memory-budgeted.** idle <50MB, active <150MB (CI gate в `test/perf/memory_test.go`). Без surprise-leaks в долгих ssh-сессиях.

---

## Honesty section (важно — не вырезать)

- **Quality:** v0.1.0 — first public release. Прошли 4 этапа (Stages 1-3 функциональные + Stage 4 release engineering), но реальный mileage — за вами.
- **Ban-risk:** Telegram автоматически ставит unofficial-клиенты под observation ([их слова, не наши](https://core.telegram.org/api/obtaining_api_id)). После дела Дурова в августе 2024 enforcement резко вырос. **Тестовый аккаунт — обязательно.**
- **Не replacement для Telegram Desktop.** Если вы хотите stickers, voice/video calls, secret chats, inline media preview — это **не наш target**. Мы — tool для tmux-resident developers, которые хотят поиск по своим перепискам и быстрый текстовый ввод без ухода из терминала. См. ["Что НЕ делаем никогда" в CLAUDE.md](../CLAUDE.md#что-не-делаем-никогда-явный-non-goal-после-dialectic).
- **Windows билды отложены на v0.2.** TUI keys/colors на cmd.exe требуют отдельного тестового цикла, которого у нас в v0.1 не было.
- **Beta-period подтверждён ≥3 внешними тестерами.** Но выборка маленькая. Bug reports особенно ценны в первые недели.

---

## Links

- **GitHub repo:** https://github.com/pgmac/lazytg
- **Demo:** <demo-link> (asciinema-cast или gif — генерируется по [DEMO.md](DEMO.md))
- **Install:** [docs/INSTALL.md](INSTALL.md)
- **Verify signatures:** [docs/VERIFY.md](VERIFY.md)
- **Architecture:** [docs/ARCHITECTURE.md](ARCHITECTURE.md)
- **Security model + ban-risk:** [docs/SECURITY.md](SECURITY.md)
- **Beta checklist:** [docs/BETA_CHECKLIST.md](BETA_CHECKLIST.md)

---

## Channel-specific drafts

### Show HN

> **Show HN: lazytg — Telegram TUI with local FTS5 search (Go)**
>
> Hi HN. I built lazytg because Telegram Desktop's search depends on the server-side `messages.search` API, which is slow and has a known pain-point with cyrillic / mixed-language history. lazytg keeps a local SQLite FTS5 trigram index of your messages — p95 search latency is ~47ms on 100k messages (SLA <100ms).
>
> It's a 2-pane TUI (chats + thread, WeeChat-style — not 3-pane lazygit-style; we tested that and it's a categorical mismatch for chat domain). Pure Go, no CGo by default, single binary cross-built for linux+darwin × amd64+arm64. Sessions in Keychain / Secret Service / wincred via go-keyring.
>
> Architecture is 3-layer with depguard rules in CI (`internal/core` cannot import gotd or bubbletea). Tests-first for core (coverage core 81.3%, ui 79.2%).
>
> **Important caveat:** Telegram automatically puts unofficial clients under observation. We added a send rate-limit guard and a `debug-bundle` command that strips secrets, but the only safe way to try lazytg is with a test account. After the Durov arrest (Aug 2024), enforcement spiked.
>
> Roadmap is honest: v0.1.0 is read+reply+files+search. Vim-mode, inline media, AI-layer — explicitly v0.2+ to avoid feature creep.
>
> https://github.com/pgmac/lazytg

### r/commandline

> **lazytg v0.1.0 — Telegram TUI client with instant local FTS5 search**
>
> Built for the tmux+nvim+ssh crowd. Pure Go, single binary, cosign-verified releases.
>
> What's interesting:
> - Local SQLite FTS5 trigram index → ~47ms p95 search on 100k messages
> - Live-updates p95 ~4ms via gotd's updates.Manager
> - $EDITOR delegation for long messages (Ctrl+E)
> - Multi-account via `--account <phone>` flag
> - 2-pane layout (WeeChat-style), emacs-readline by default, vim-mode opt-in v0.2
>
> What's not there yet:
> - Inline media preview (Kitty/iTerm/sixel) — v0.3
> - Voice/video calls (would require CGo for tgvoip — not happening)
> - Windows builds — v0.2
>
> ⚠️ Telegram puts unofficial clients under observation. Use a test account first.
>
> https://github.com/pgmac/lazytg

### lobste.rs

> **lazytg: Telegram TUI in pure Go with local FTS5 search**
>
> Tags: `go`, `tui`, `release`
>
> Local-first Telegram client built around mtproto (gotd/td) and bubbletea. Architectural notes:
>
> - Three-layer separation enforced via depguard: `core` knows nothing about gotd or bubbletea, `ui` knows nothing about gotd, `storage` knows nothing about either.
> - Pure-Go SQLite (modernc.org/sqlite) — no CGo. SQLCipher (encrypted DB) reserved past v0.1; the build tag deliberately fails to compile until the real driver lands.
> - FTS5 trigram tokenizer (built-in to SQLite ≥3.34) — language-agnostic, works for cyrillic without ICU. Lazy index: last 5000 messages per chat by default.
> - Live-updates via gotd's updates.Manager backed by SQLite StateStorage (~50 LOC).
> - Cosign keyless OIDC for releases — no private keys to manage. Sigstore bundles per-archive.
>
> Coverage: core 81.3%, ui 79.2%, depguard CI gate, search SLA gated in CI (p95 <100ms benchmark fails build).
>
> https://github.com/pgmac/lazytg

### r/golang

> **lazytg — pure-Go Telegram TUI with FTS5 search (gotd/td + bubbletea + modernc/sqlite)**
>
> Released v0.1.0 today. The interesting Go-bits:
>
> - **gotd/td for MTProto** — pure Go, active (releases 2026). Resisted TDLib (CGo, ban-risk doesn't go away with native bindings).
> - **bubbletea v2 + lipgloss + bubbles** — Elm architecture, good fit for chat UI. GitLab is migrating their TUIs from tview → bubbletea, that's a strong signal.
> - **modernc.org/sqlite** — pure Go SQLite port. ~75% perf of mattn/go-sqlite3 but zero CGo. Includes FTS5 + trigram tokenizer.
> - **Three-layer architecture with depguard CI gate** — `internal/core` can't import gotd or bubbletea; `internal/ui` can't import gotd. Caught two regressions in development.
> - **Memory budget gates in CI** — `test/perf/memory_test.go` fails build if idle >50MB or active >150MB.
> - **GoReleaser + cosign keyless OIDC** — no key management. Sigstore bundles. Build matrix: linux+darwin × amd64+arm64.
>
> Coverage core 81.3%, ui 79.2%. lefthook for pre-commit (gofmt, go vet, go test -short). git-cliff + commitlint for changelog.
>
> https://github.com/pgmac/lazytg
