# Architecture

This document describes the runtime topology, package layout, and import-direction rules of lazytg.

## Goals

1. **Local-first.** All persistent state (messages, peers, FTS index) lives in a single SQLite file under `$XDG_DATA_HOME/lazytg/` (or the macOS equivalent). lazytg works offline against the cache.
2. **Pure-Go default.** No CGo in the default build, so cross-compilation for `linux` and `darwin` × `amd64` and `arm64` is a single `go build`. CGo for SQLCipher is deferred past v0.1 behind a build tag — `-tags sqlcipher` deliberately fails to compile until the real driver lands.
3. **Strict layering.** UI does not talk to MTProto, MTProto does not talk to the UI, storage talks to neither. Enforced by `depguard` — see below.
4. **Minimum behavioural footprint vs Telegram.** The auth/send/sync code lives in one place (`internal/tg/`) and is the only layer that issues real RPC. It is small enough to audit for ban-risk indicators end-to-end.

## High-level layout

```
                ┌─────────────────────────┐
                │       cmd/lazytg        │  cobra entry point
                └────────────┬────────────┘
                             │ wires
                ┌────────────▼────────────┐
                │      internal/app       │  manual DI, no framework
                └─┬──────────┬──────────┬─┘
                  │          │          │
       ┌──────────▼─┐  ┌─────▼─────┐  ┌─▼──────────┐
       │ internal/tg │  │internal/ │  │ internal/   │
       │             │  │  core    │  │  ui         │
       │ gotd/td     │  │ domain   │  │ Bubble Tea  │
       │ wrapper     │  │ events   │  │ (stage 2)   │
       │             │  │ sync     │  │             │
       └──────┬──────┘  └─────┬────┘  └────┬────────┘
              │               │            │
              │           ┌───▼────────────▼───┐
              └──────────►│ internal/storage   │
                          │   /sqlite          │
                          │ pure-Go modernc;   │
                          │ sqlcipher via tag  │
                          └────────────────────┘
```

## Package map

All packages below ship in the v0.1 branch (Stages 1–3 complete). Stage labels indicate when a package was introduced.

| Package                          | Stage | Responsibility                                                                                                                       | May import                              | MUST NOT import                          |
|----------------------------------|-------|--------------------------------------------------------------------------------------------------------------------------------------|-----------------------------------------|------------------------------------------|
| `cmd/lazytg`                     | 1–3   | Cobra entry point: `tui` (default), `login`, `logout`, `accounts`, `version`, `debug-bundle`, `reindex`. `attach.go` opens the MTProto session **before** the UI is built and starts the dialog sync; every failure path degrades to the cached-only view. | everything                              | —                                        |
| `internal/app`                   | 2     | Manual DI wiring: `Build` for non-MTProto services, `AttachClient` for gotd-aware ones, `RunBackground` for long-lived goroutines, `CheckPermissions` re-export for CLI subcommands. | `internal/{tg,core,storage,ui}` | —                                        |
| `internal/tg`                    | 1–3   | MTProto-speaking layer: `client`, `auth`, `session`, `send`, `history`, `dialogs` (`messages.getDialogs` + peer decoding + paging cursor), `updates`, `polling`, `floodwait`, `files` (Downloader/Uploader/FilesAdapter/MediaFromMessage). | `internal/core`, `gotd/td`              | `internal/ui`                            |
| `internal/core/events`           | 1     | Typed in-process event bus (publish/subscribe with fan-out under mutex, drop-on-overflow).                                          | stdlib only                             | —                                        |
| `internal/core/domain`           | 1     | Plain Go types: `Account`, `Chat`, `Message`, `MediaInfo`.                                                                          | stdlib only                             | —                                        |
| `internal/core/config`           | 1     | XDG path resolution, secret store abstraction (`KeyringStore`, `AgeFileStore`).                                                     | stdlib, `zalando/go-keyring`, `filippo.io/age` | —                                  |
| `internal/core/obs`              | 1–3   | `slog` factory + redacting handler + lumberjack rotation; debug-bundle producer (`Bundle.Create`); DB-size monitor.                 | stdlib, `lumberjack`                    | —                                        |
| `internal/core/sync`             | 2     | Send / history backfill / live drain / reconnect / degradation detector / token-bucket rate limiter / dialog-list sync (paged, paced, capped at 5 pages by design — see below). | `internal/{core/events,core/domain,storage}` | `gotd/td`, `charmbracelet/bubbletea` |
| `internal/core/search`           | 3     | FTS5 indexer (`Indexer.Backfill`), reindex service (`RunAll`/`Run`), lazy trigger, query parser + builder, search service.          | `internal/{core/events,core/domain,storage}` | `gotd/td`, `charmbracelet/bubbletea` |
| `internal/core/files`            | 3     | Download / upload orchestration: `FileStore`, `DedupCache`, `DownloadService`, `UploadService`, throttled progress emitters.        | `internal/{core/events,core/domain}`    | `gotd/td`, `charmbracelet/bubbletea`     |
| `internal/core/security`         | 3     | Permissions audit (`CheckAtStartup` / `EnforceFatal`), `SendGuard` (TokenBucket wrapper at 10 msg/s).                              | `internal/core/sync`                    | `gotd/td`, `charmbracelet/bubbletea`     |
| `internal/storage/sqlite`        | 1–3   | Repository implementation: migrations 0001–0008, FTS5 trigram index, frecency, dedup tables, outgoing/peers/state repos.            | `internal/core/domain`                  | `internal/ui`, `internal/tg`             |
| `internal/ui/app`                | 2     | Bubble Tea root model, focus orchestration, modal overlay routing.                                                                  | `internal/core` (interfaces), `internal/ui/*` | `gotd/td`                          |
| `internal/ui/{chats,thread}`     | 2     | Stage 2 panes (chats list, thread reader).                                                                                          | `internal/core` (interfaces)            | `gotd/td`                                |
| `internal/ui/{search,attach}`    | 3     | Stage 3 overlays (search results + jump, attach file picker).                                                                       | `internal/core` (interfaces)            | `gotd/td`                                |
| `internal/ui/palette`            | 3     | Command palette L1 (chat switcher with frecency + Unicode-fold).                                                                    | `internal/core` (interfaces)            | `gotd/td`                                |
| `internal/ui/{input,statusbar,keymap,overlay}` | 2 | Input editor (textarea + emacs bindings + history), status bar, configurable keymap, help overlay.                       | `internal/core` (interfaces)            | `gotd/td`                                |

## Dependency-direction enforcement (depguard)

`depguard` runs as part of `golangci-lint` and fails CI on layer violations.

Configured in [`.golangci.yml`](../.golangci.yml):

- Files under `internal/core/**` may not import `github.com/gotd/td` or `github.com/charmbracelet/bubbletea`.
- Files under `internal/ui/**` may not import `github.com/gotd/td`.
- Files under `internal/storage/**` may not import `github.com/kar43lov/lazytg/internal/ui` or `github.com/kar43lov/lazytg/internal/tg`.

Smoke test for the rule: temporarily add an import of `github.com/gotd/td/telegram` from any file under `internal/core/` and run `golangci-lint run` — it must fail.

## Stack rationale (why these libraries)

These choices come out of the v0.1.0 brainstorm + dialectic; the trade-offs are recorded here so future contributors do not relitigate them by accident.

| Concern             | Choice                                            | Rejected (and why)                                                                                                                  |
|---------------------|---------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------|
| Language            | Go ≥ 1.25 (pinned in go.mod)                       | Python+Textual, Rust+ratatui — Go matches the `lazygit` mental model and gives a single binary cross-build with no CGo by default.   |
| MTProto             | `github.com/gotd/td`                              | TDLib over CGo (kills pure-Go cross-build, doesn't help ban-risk); Bot API (insufficient, can't read user history).                 |
| TUI                 | `charmbracelet/bubbletea` v2 + `lipgloss` + `bubbles` (stage 2) | `gocui`, `tview` — small ecosystems, GitLab is migrating away from `tview` to bubbletea; bubbletea has 10k+ apps in production.     |
| SQLite              | `modernc.org/sqlite` (pure Go)                    | `mattn/go-sqlite3` — needs CGo, breaks easy cross-compilation. modernc gives ~75% of CGo performance and supports FTS5 + trigram.    |
| Encrypted DB (planned) | `mutecomm/go-sqlcipher` via build tag `sqlcipher`, **deferred past v0.1** (the tag intentionally fails to compile until the real driver lands) | Encrypting by default — adds a CGo toolchain requirement for every contributor and complicates releases. Opt-in is the right default. |
| Secrets             | `filippo.io/age` file, passphrase in `zalando/go-keyring` | Session blob in the keyring — impossible: gotd sessions are ~4.2 KB and go-keyring's macOS backend caps a secret at 4096 bytes, failing mid-connection. `99designs/keyring` — less active, more complex API. |
| CLI                 | `spf13/cobra`                                     | `urfave/cli` — cobra is the de facto standard, plays nice with viper if we ever need it.                                            |
| Logging             | `log/slog` (stdlib) + `gopkg.in/natefinch/lumberjack.v2` | `zap`, `zerolog` — slog is now stdlib and has structured handlers; lumberjack only handles rotation.                                |
| Release             | `goreleaser` + `cosign` keyless OIDC              | Manual builds — error-prone, no signing.                                                                                            |

## FTS5 search (Stage 3)

- Tokenizer: **`trigram`** (built into SQLite ≥ 3.34, `case_sensitive=0`), language-agnostic — works for Russian without ICU. ICU is not available in modernc/sqlite.
- Lazy index: only the most recent **5000** messages per chat by default. `LazyTrigger` kicks off the first full reindex pass on the first search; `lazytg reindex --all|--chat <id>` runs the same pipeline outside the TUI.
- The live `messages_ai`/`messages_au` triggers skip empty-text rows so service messages do not pollute the index — matches the `Indexer.Backfill` filter.
- `BuildSQL` quotes every user-supplied free-text token as an FTS5 phrase (with embedded `"` doubled), so FTS5 grammar tokens (`AND`/`OR`/`NOT`/`NEAR`/`*`/`(`/`^`/`column:`) in user input become literal trigrams instead of triggering the FTS5 query language.
- WAL mode is enabled to smooth bursts of live updates against the single-writer SQLite database.
- `busy_timeout(5000)` is part of the DSN pragmas and is **not** optional. SQLite permits one writer at a time; without a busy timeout a connection that finds the lock taken fails instantly with `SQLITE_BUSY` rather than waiting, and lazytg writes from four paths concurrently (live drain, history backfill, dialog sync, FTS reindex). Measured before the pragma existed: 973 of 1200 writes were lost outright in a burst of 4 goroutines × 300. The 5s value has room — the largest single write transaction in the app, a 5000-row `Indexer.Backfill`, was measured at 151 ms.
- The degradation probe (`Repo.ProbeWrite`) treats `SQLITE_BUSY` as success. It asks "does storage accept writes", and another connection holding the write lock answers yes. Treating contention as failure let `DegradationDetector` flip the repo into soft read-only mode — every write returning `ErrReadOnly` — whenever a probe landed inside a write burst. Nothing previously detected is lost: no genuine failure code masks to BUSY (5) or LOCKED (6), so any error that does reach the probe is still reported. Which failures reach it is a narrower set than the doc comment used to imply — see the gap below.
- 🔴 **Known gap: the probe does not detect a read-only database.** `BEGIN IMMEDIATE` acquires the write lock but dirties no page, so SQLite never raises `SQLITE_READONLY` and the probe reports healthy storage while every ordinary write fails with "attempt to write a readonly database". `DegradationDetector` therefore misses the one condition it was written for. This predates the contention handling above and is pinned by `TestProbeWrite_KnownGap_ReadOnlyDatabaseUndetected`, whose assertion is inverted so it fails once the gap is closed. The fix is to dirty a page inside the probe transaction — `DELETE FROM chats WHERE 0` was measured to return `SQLITE_READONLY` (8) while touching no rows.
- p95 latency on a 100k-message synthetic corpus is gated by `BenchmarkSearch100k` (`make bench`, run in CI) — the bench self-fails when p95 > 100 ms.
- A spike test (`internal/storage/sqlite/fts5_spike_test.go`) verified the trigram tokenizer works in modernc/sqlite for both Cyrillic and Latin text — this was the principal risk-blocker for the stack.

## Live updates (Stage 2 detail)

- gotd's `updates.Manager` with a `StateStorage` implementation backed by SQLite (≈50 lines).
- A `--polling` flag is reserved on the CLI as a future fallback if the updates manager produces gap problems in the field; in v0.1 it is a no-op (see CHANGELOG `Known gaps`).

## Dialog sync (chat list)

- `internal/tg/dialogs.go` issues `messages.getDialogs` and decodes all four peer shapes (user, basic group, supergroup, channel), storing `access_hash` per peer so later calls can address them.
- Paging is positional (`offset_date` / `offset_id` / `offset_peer`), and the cursor is taken from **one** dialog — the last successfully resolved one. Mixing fields from different dialogs, or letting a zero date through, sends Telegram an `offset_date` of `-62135596800` and restarts the walk from the beginning.
- The walk stops when a page returns a cursor identical to the one that produced it, so a server that stops advancing cannot loop forever.
- `internal/core/sync/dialogs.go` writes peers before chats (a chat row references its peer), and treats a single chat's write failure as skippable rather than fatal.
- **Capped at 5 pages of 100 dialogs with a delay between pages, by design.** The cap and the pacing are ban-risk controls, not an unfinished loop — a client that pulls thousands of dialogs as fast as the API allows is exactly the behavioural signature that gets unofficial clients flagged. Raising it is a policy decision, not a bug fix.

## Security (see [SECURITY.md](SECURITY.md))

- Sessions are stored via the `SecretStore` interface — never as cleartext on disk.
- The startup permissions audit (`internal/core/security/permissions.go`) re-validates `secrets.age` and `lazytg.db` (mode `0600`, fail-fast), `Config` and `State` dirs (mode `0700`, warn-class) on every TUI start and on every CLI subcommand that opens the repo.
- The send path runs through a hard rate-limit guard (`max 10 msg/sec`, burst 30) covering both `coresync.SendService` (text) and `files.UploadService` (media). Not user-tunable.
- The `debug-bundle` command must not include session blobs, `api_hash`, raw message text, contact lists, or peer access hashes — verified by `internal/core/obs/bundle_grep_test.go` in CI.

## Build modes

```sh
make build                       # pure-Go default
make test                        # go test -race ./...
make bench                       # FTS5 search SLA gate (BenchmarkSearch100k)
make lint                        # golangci-lint (depguard rules)
```

The `sqlcipher` build tag is reserved for the CGo-backed encrypted driver. CGo SQLCipher integration is deferred past v0.1; the tag is intentionally absent so that `go build -tags sqlcipher` fails loudly rather than producing a binary that pretends to encrypt.
