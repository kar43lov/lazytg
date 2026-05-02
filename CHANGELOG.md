# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Stage 2 TUI: 2-pane Bubble Tea v2 (chats + thread) with status bar, modal help overlay (`?`), focus cycling (Tab/Shift+Tab), small-terminal fallback (<80×24).
- `lazytg` (no subcommand) opens the TUI as the default entry point; `lazytg tui` is a hidden alias.
- History sync via gotd `messages.GetHistory` with batch upsert (`Repo.SaveMessages`) and 200-message pagination on scroll-up.
- Live updates via gotd `updates.Manager` over a SQLite-backed `StateStorage` (composite key `(account_id, channel_id)`) with LRU dedup (256 entries) on `(chat_id, message_id)`. SLA: <500ms p95 from MTProto update to repo write, gated by `test/perf/live_latency_test.go`. The chats pane reacts to `MessageReceived` directly (debounced reload coalesces bursts) — `LiveService.persist` does not republish a `DialogUpdated` because that self-fanout halved the effective subscriber buffer.
- `--polling` flag — 3s history-poll fallback (`internal/tg/polling.go`) when updates.Manager creates gaps. Wiring into `runTUI` is deferred — flag is currently a no-op until Stage 2 follow-up lands.
- Send + reply with optimistic UI: `outgoing(local_id PK, state)` table and `OutgoingMessageStateChanged` events drive a pending → sent | failed pill in the thread pane. Failed sends restore the draft *and* the reply pointer so retry preserves the conversation context.
- `OutgoingMessage` rendering in the thread pane (`RenderOptimistic`): `[⏳]` glyph for pending, `[✗]` red glyph for failed (with the failure reason on a trailing line), no glyph for sent (the live echo dedupes the optimistic row via a localID → serverID mapping).
- Hard rate-limit guard on backfill (token bucket 10 req/sec, capacity 30, `internal/core/sync/ratelimit.go`) — ban-risk mitigation, not user-tunable.
- Reconnect manager with exponential backoff (1s → 60s, ±10% jitter, infinite retries by default, `context.Canceled` interpreted as user shutdown). `tg.Client.OnDisconnect()` exposes the disconnect signal as a buffered channel.
- Read-only DB degradation: `DegradationDetector` probes via `BEGIN IMMEDIATE + ROLLBACK` on a dedicated `*sql.Conn` every 30s; `Repo.IsReadOnly()` flag gates writes with `ErrReadOnly`. `StorageStateChanged` event surfaces in the status bar.
- $EDITOR delegation via `Ctrl-E` (`internal/ui/input/editor.go`) — temp file `0600` in user cache, defer-cleanup even on exec failure, fallback to `vi` if `$EDITOR` unset.
- Configurable keymap via TOML at `<config>/lazytg/keymap.toml`; conflict detection at startup (with stable error formatting).
- Input pane: textarea with emacs/readline bindings, in-memory history ring (100 entries, Ctrl-P/N navigation), reply state (Ctrl-R), multi-line via Alt+Enter, `$EDITOR` roundtrip.
- New SQLite migrations: `0002_channel_state.sql`, `0003_peers_extended.sql`, `0004_outgoing.sql`.
- `internal/app/wire.go` — DI composition (`App.Build` for non-MTProto services + `App.AttachClient` for the gotd-aware ones).
- E2E TUI smoke tests (`test/e2e/tui_smoke_test.go`), goroutine-leak gate (`test/perf/goroutine_leak_test.go`), live-latency SLA gate with per-event emit→save deltas.
- Stage 2 manual smoke checklist (`docs/MANUAL_SMOKE.md`, 16 points).
- Stage 1 foundation: bootstrap, 3-layer architecture, depguard rules enforcing import direction.
- SQLite storage layer (modernc/sqlite) with FTS5 trigram tokenizer (verified for Cyrillic and Latin) and connection-pool-wide PRAGMAs (WAL, foreign_keys, synchronous=NORMAL) injected via DSN.
- Schema v1: `accounts`, `chats`, `messages` (with `ON DELETE CASCADE`), `peers`, `state`, `schema_migrations`.
- `Repo` CRUD for accounts/chats/messages.
- Telegram authentication flow via `gotd/td`, session storage with OS keyring and `age`-encrypted file fallback.
- XDG path resolution (`config.Resolve`) creating `0700` dirs; `AgeFileStore` fail-fast on insecure (`!=0600`) permissions.
- In-process typed event bus (publish/subscribe with mutex-guarded fan-out, drop-on-overflow, context-cancel unsubscribe) — scaffolded for Stage 2 sync consumers.
- Cobra CLI skeleton: `login`, `logout`, `accounts` (read-only, no passphrase prompt), `version`, `debug-bundle` (stub).
- Persistent CLI flags `--account`, `--debug`, `--log-level` plumbed into a per-invocation `slog.Logger` in command context.
- `slog` logger with redaction handler (api_hash hex 32+, base64-ish session blobs that contain `+/=_-`, phones with literal `+` prefix) and lumberjack rotation (10 MB × 3 backups × 30 days). Bare numeric IDs and Unix timestamps survive redaction.
- GitHub Actions CI (`lint`, `test` matrix on ubuntu/macos), GoReleaser release pipeline with cosign keyless signing, snapshot workflow on PRs (signing skipped — only tag releases are cosign-signed).
- Dependabot configuration for weekly Go module and GitHub Actions updates.
- Documentation: `README.md`, `docs/ARCHITECTURE.md`, `docs/SECURITY.md`, `docs/CONTRIBUTING.md`.
- PR and issue templates under `.github/`.

### Changed

- (none yet)

### Removed

- The `sqlcipher` build tag and its CI matrix axis. The CGo SQLCipher driver lands in Stage 3 — until then a stub silently using the unencrypted `modernc.org/sqlite` driver would have been a security misrepresentation.

### Fixed

- Optimistic-UI flicker race: `applyOutgoingState{Pending}` is now a no-op so the bus event cannot insert an empty `[⏳]` row before `SendDispatchedMsg` (carrying the message body) lands.
- `SendFailedMsg` no longer clobbers a fresher draft or retargeted reply pointer when the user moved on while the failed send was in flight — the failure is dropped and surfaced as a warn log.
- Configurable `ScrollUp`/`ScrollDown` chords (default `ctrl+b`/`ctrl+f`) now actually scroll the thread viewport when the thread or chats pane is focused; previously the chord was advertised in `keymap.toml` and the help overlay but no code consumed it.
- `LiveService.persist` no longer republishes `DialogUpdated` onto its own subscription — the chats pane reacts to `MessageReceived` directly, which removes the buffer halving and the resulting potential for dropped events under burst.

### Known gaps (Stage 2 follow-up)

- `runTUI` in `cmd/lazytg/cmd/tui.go` does not yet call `App.AttachClient` and does not start a `tg.Client.Run` loop. The TUI binary therefore renders the local SQLite cache only — sends, live updates, history backfill, and reconnect orchestration require this wiring to be added before the Stage 2 acceptance criteria are met against a real Telegram session.
- `BackfillService.Start` is constructed by `AttachClient` but never invoked. Once `runTUI` wires the gotd Run loop, the cmd layer must call `app.Backfill.Start(bgCtx)` to drain the enqueue channel.
- `--polling` flag is plumbed into `App.Polling` but no consumer reads it: `PollingFallback` is constructed nowhere outside its tests. Wire-up belongs in `runTUI` next to `AttachClient`.
- `reconnectAdapter.Connect` in `internal/app/wire.go` is a deliberate no-op stub. Real reconnect orchestration (re-running `Client.Run` with the saved session) lives in the same follow-up as the MTProto wiring above.
- Foreign-key cascade (`messages.chat_id REFERENCES chats(id)`): `LiveService.persist` does not yet upsert a chat row before saving the first message from an unknown chat. Once a `DialogsService` populates the `chats` table on login (or an upsert is added to `LiveService`), the FK will hold.
