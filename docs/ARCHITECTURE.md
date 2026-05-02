# Architecture

This document describes the runtime topology, package layout, and import-direction rules of lazytg.

## Goals

1. **Local-first.** All persistent state (messages, peers, FTS index) lives in a single SQLite file under `$XDG_DATA_HOME/lazytg/`. lazytg works offline against the cache.
2. **Pure-Go default.** No CGo in the default build, so cross-compilation for `linux` and `darwin` × `amd64` and `arm64` is a single `go build`. CGo is opt-in via the `sqlcipher` build tag for users who want an encrypted database.
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

| Package                       | Responsibility                                                                                     | May import                              | MUST NOT import                                                |
|-------------------------------|-----------------------------------------------------------------------------------------------------|-----------------------------------------|----------------------------------------------------------------|
| `cmd/lazytg`                  | Cobra commands (`login`, `logout`, `accounts`, `version`, `debug-bundle`).                          | everything                              | —                                                              |
| `internal/app`                | Manual constructor wiring (DI without a framework).                                                 | `internal/{tg,core,storage,ui}`         | —                                                              |
| `internal/tg`                 | The only layer that speaks MTProto. Wraps `gotd/td`: client, auth flow, session storage, send, history, updates manager, FloodWait handling, file up/download. | `internal/core`, `gotd/td`              | `internal/ui`                                                   |
| `internal/core`               | Domain types, storage interfaces, event bus, sync logic, search, config, observability, security. Pure logic, no I/O against Telegram or terminals. | `internal/storage` (interface only)     | `gotd/td`, `charmbracelet/bubbletea`                            |
| `internal/core/events`        | Typed in-process event bus (publish/subscribe with fan-out under mutex, drop-on-overflow).          | stdlib only                             | —                                                              |
| `internal/core/domain`        | Plain Go types for `Account`, `Chat`, `Message` exchanged across layers.                            | stdlib only                             | —                                                              |
| `internal/core/config`        | XDG path resolution, secret store abstraction (`KeyringStore`, `AgeFileStore`).                     | stdlib, `zalando/go-keyring`, `filippo.io/age` | —                                                       |
| `internal/core/obs`           | `slog` logger factory, redacting handler, lumberjack rotation.                                      | stdlib, `lumberjack`                    | —                                                               |
| `internal/storage/sqlite`     | Repository implementation. Migrations, PRAGMAs, CRUD. FTS5 index lives here.                        | `internal/core/domain`                  | `internal/ui`, `internal/tg`                                    |
| `internal/ui`                 | Bubble Tea models, views, key bindings, panes, palette. Lives in stage 2.                           | `internal/core` (interfaces)            | `gotd/td`                                                       |

## Dependency-direction enforcement (depguard)

`depguard` runs as part of `golangci-lint` and fails CI on layer violations.

Configured in [`.golangci.yml`](../.golangci.yml):

- Files under `internal/core/**` may not import `github.com/gotd/td` or `github.com/charmbracelet/bubbletea`.
- Files under `internal/ui/**` may not import `github.com/gotd/td`.
- Files under `internal/storage/**` may not import `github.com/pgmac/lazytg/internal/ui` or `github.com/pgmac/lazytg/internal/tg`.

Smoke test for the rule: temporarily add an import of `github.com/gotd/td/telegram` from any file under `internal/core/` and run `golangci-lint run` — it must fail.

## Stack rationale (why these libraries)

These choices come out of the v0.1.0 brainstorm + dialectic; the trade-offs are recorded here so future contributors do not relitigate them by accident.

| Concern             | Choice                                            | Rejected (and why)                                                                                                                  |
|---------------------|---------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------|
| Language            | Go ≥ 1.22                                         | Python+Textual, Rust+ratatui — Go matches the `lazygit` mental model and gives a single binary cross-build with no CGo by default.   |
| MTProto             | `github.com/gotd/td`                              | TDLib over CGo (kills pure-Go cross-build, doesn't help ban-risk); Bot API (insufficient, can't read user history).                 |
| TUI                 | `charmbracelet/bubbletea` v2 + `lipgloss` + `bubbles` (stage 2) | `gocui`, `tview` — small ecosystems, GitLab is migrating away from `tview` to bubbletea; bubbletea has 10k+ apps in production.     |
| SQLite              | `modernc.org/sqlite` (pure Go)                    | `mattn/go-sqlite3` — needs CGo, breaks easy cross-compilation. modernc gives ~75% of CGo performance and supports FTS5 + trigram.    |
| Encrypted DB (opt-in) | `mutecomm/go-sqlcipher` via build tag `sqlcipher` | Encrypting by default — adds a CGo toolchain requirement for every contributor and complicates releases. Opt-in is the right default. |
| Secrets             | `zalando/go-keyring` (with `filippo.io/age` fallback) | `99designs/keyring` — less active, more complex API.                                                                                |
| CLI                 | `spf13/cobra`                                     | `urfave/cli` — cobra is the de facto standard, plays nice with viper if we ever need it.                                            |
| Logging             | `log/slog` (stdlib) + `gopkg.in/natefinch/lumberjack.v2` | `zap`, `zerolog` — slog is now stdlib and has structured handlers; lumberjack only handles rotation.                                |
| Release             | `goreleaser` + `cosign` keyless OIDC              | Manual builds — error-prone, no signing.                                                                                            |

## FTS5 search (Stage 3 detail, scaffolded in Stage 1)

- Tokenizer: **`trigram`** (built into SQLite ≥ 3.34), language-agnostic — works for Russian without ICU. ICU is not available in modernc/sqlite.
- Lazy index: only the most recent **5000** messages per chat by default. Full-history reindex is on demand.
- WAL mode is enabled to smooth bursts of live updates against the single-writer SQLite database.
- A spike test (`internal/storage/sqlite/fts5_spike_test.go`) verifies the trigram tokenizer works in modernc/sqlite for both Cyrillic and Latin text. This was the principal risk-blocker for the stack.

## Live updates (Stage 2 detail)

- gotd's `updates.Manager` with a `StateStorage` implementation backed by SQLite (≈50 lines).
- A `--polling` flag exists as a fallback if the updates manager produces gap problems in the field.

## Security (see [SECURITY.md](SECURITY.md))

- Sessions are stored via the `SecretStore` interface — never as cleartext on disk.
- Session and config files are checked at startup; mode `0600` for files, `0700` for directories. Wider permissions cause a fail-fast.
- Send path has a hard rate-limit guard (`max 10 msg/sec`) to keep behavioural fingerprints low and reduce ban-risk.
- The `debug-bundle` command (Stage 3) MUST NOT include session data, `api_hash`, or message bodies; this is verified by a grep test in CI.

## Build modes

```sh
make build                       # pure-Go, default
go build -tags sqlcipher ./...   # CGo + SQLCipher (encrypted DB)
```

CI runs the test matrix on both tag sets to make sure neither implementation rots.
