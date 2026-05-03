# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Stage 4 release engineering: extended `.goreleaser.yaml` with `nfpms` section producing `.deb`/`.rpm` packages from the pure-Go matrix, `brews` section auto-publishing the formula to `pgmac/homebrew-lazytg` on stable tags, per-archive cosign sigstore-bundle signing alongside the existing `checksums.txt` signature. Pure-Go single-binary build only — SQLCipher (CGo) remains deferred past v0.1; the `sqlcipher` build tag in `internal/storage/sqlite/driver_sqlcipher.go` deliberately fails to compile so that no binary can ship under the encrypted-DB label without the real driver.
- Stage 4 pre-release pipeline: `release.yml` reacts to `on: push: tags: v*` and runs goreleaser with `release.prerelease: auto` plus `brews[].skip_upload: '{{ if .Prerelease }}true{{ end }}'`, keeping the Homebrew tap pinned to stable releases. New `.github/workflows/prerelease.yml` adds a manual `workflow_dispatch` entry point that auto-increments and pushes the next `alpha`/`beta`/`rc` tag — note that GitHub blocks recursive workflow dispatch for tags pushed by the default `GITHUB_TOKEN`, so `release.yml` exposes a `workflow_dispatch` with a `ref` input so the maintainer can trigger it explicitly via `gh workflow run release.yml --ref <tag>`.
- Stage 4 changelog automation: `cliff.toml` (git-cliff config — Keep-a-Changelog header, conventional-commit parsers grouping `feat`→Added, `fix`→Fixed, `perf`→Performance, `security`→Security, `BREAKING CHANGE`→Breaking) and `.commitlintrc.yml` (`@commitlint/config-conventional` extending the allowed type-enum to `feat,fix,perf,security,docs,refactor,test,chore,build,ci`). Local enforcement via `lefthook.yml` `commit-msg` hook (npx commitlint with bash-regex fallback); CI enforcement via `amannn/action-semantic-pull-request@v5` job in `ci.yml`.
- Stage 4 memory budget gate: `test/perf/memory_test.go` runs the full `app.Build` wiring against an in-process XDG layout and asserts `runtime.MemStats.HeapAlloc` ≤ 50 MiB after 5 s idle and ≤ 150 MiB after 30 s of synthetic active load (1000 events + 10 concurrent search probes). Wired into `ci.yml` as a dedicated step; budgets and SLA results are documented in `docs/PERFORMANCE.md`.
- Stage 4 user documentation suite: `docs/INSTALL.md` (brew tap + `.deb`/`.rpm` + manual binary + `go install` + sqlcipher build), `docs/CONFIGURATION.md` (`config.toml`/`keymap.toml`/env vars/multi-account), `docs/TROUBLESHOOTING.md` (FLOOD_WAIT, search-empty, broken TUI, permission-denied, DB-locked, account-banned, debug-bundle), `docs/VERIFY.md` (sha256 + cosign keyless OIDC verification recipe), `docs/PERFORMANCE.md` (memory + search + live-update budgets), `docs/DEMO.md` (asciinema-cast → gif recipe for maintainer), `docs/BETA_CHECKLIST.md` (6-point smoke for external testers), `docs/RELEASE_ANNOUNCE.md` (Show HN / r/commandline / lobste.rs / r/golang draft), `docs/RELEASE_PROCESS.md` (alpha → beta → rc → stable runbook with hotfix and rollback procedures). README rewritten with the local-first pitch, 3-step quickstart, feature emoji bar, and a documentation index.
- Stage 4 GitHub plumbing hardening: `bug_report.yml` now requires a debug-bundle attachment plus two affirmation checkboxes ("no leaked api_hash/session" + "not a security issue — those go to GHSA"); `feature_request.yml` adds a v0.2/v0.3 roadmap-checked checkbox to deflect duplicates; new `.github/ISSUE_TEMPLATE/config.yml` redirects security to GHSA and everything else to Discussions; PR template gains explicit conventional-commit + coverage-gate rows; `.github/CODEOWNERS` claims `internal/tg/`, `internal/core/security/`, `internal/core/obs/`, `internal/core/config/`, `internal/storage/sqlite/migrations/`, `.goreleaser.yaml`, `.github/workflows/`, and `docs/SECURITY.md` for explicit review.
- Stage 3 search: SQLite FTS5 trigram index with parser supporting `from:@user`, `in:#chat`, `before:`/`after:` (UTC), `has:file`, `"phrase"`, `-exclusion`. Lazy index of last 5000 messages/chat (`internal/core/search/{index,reindex,lazy,service,query_builder,parser}.go`); full reindex via the new `lazytg reindex --all|--chat <id>` CLI. p95 < 100 ms on 100k messages, gated by `BenchmarkSearch100k` in CI (`make bench` step on Linux).
- Stage 3 search overlay: opens with `/`, ↑/↓ + Enter jumps to chat, Esc cancels (`internal/ui/panes/search`). Free-text input is FTS5-quoted so grammar tokens (`OR`, `*`, `(`, `^`, `column:`) become literal trigrams.
- Command palette L1 (Ctrl+Space, also Ctrl+@): top-50 chat switcher ranked by frecency with NFKD/lowercase/diacritic normalisation ("Алёна" === "Алена"); persisted via migration `0006_frecency.sql` and `internal/ui/palette/`.
- File download via Ctrl+D: gotd Downloader → tmp → 0600 rename; events `FileDownload{Started,Progress,Completed,Failed}`. Dedup table `downloaded_files` (migration `0007_files.sql`), root via `LAZYTG_DOWNLOADS`, default `~/Downloads/lazytg/<chat>/<filename>`.
- File upload via Ctrl+U: attach overlay (path picker + caption), events `FileUpload{Started,Progress,Warning,Completed,Failed}`. Routes through MTProto `messages.SendMedia`. Send rate-limit guard (10 msg/s) is consulted on the upload path too — file uploads share the text-send ban-risk ceiling.
- DB-size monitor: status-bar warning `⚠ DB N.N GB` when `lazytg.db` exceeds 1 GiB (configurable via `obs.DBSizeConfig`).
- Send rate-limit guard: 10 msg/s, burst 30 (`internal/core/security/send_ratelimit.go`); not user-tunable, ban-risk mitigation. Wired into both `coresync.SendService` (text) and `files.UploadService` (media).
- Startup permissions audit (`internal/core/security/permissions.go`): fail-fast on `secrets.age`/`lazytg.db` not 0600 or `Config`/`State` dirs not 0700 (warn-class). Exposed as `app.CheckPermissions` so `lazytg reindex` and `lazytg debug-bundle` enforce the same floor.
- Full debug-bundle (replaces Stage 1 stub): tar.gz with `version.txt`, `config.toml`, redacted log tail, `db_stats.txt`, `goroutines.txt`; bundle file itself written 0600. `bundle_grep_test.go` asserts api_hash/session-blob/phone/message-text never leak.
- Migrations 0005_fts.sql, 0006_frecency.sql, 0007_files.sql, 0008_messages_media.sql.
- Domain types extended with `MediaInfo` and `Message.Media`.
- New events: `ReindexProgress`, `FileDownload*`, `FileUpload*`, `StorageStateChanged{Reason: ReasonDBSizeWarning}`, `SearchJumpRequested`.
- `make bench` target running BenchmarkSearch100k as a CI gate.
- docs/SEARCH.md, docs/FILES.md.
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
- Configurable keymap via TOML at `<config>/keymap.toml`; conflict detection at startup (with stable error formatting).
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

- The `sqlcipher` build tag and its CI matrix axis. CGo SQLCipher integration is deferred past v0.1 — until then a stub silently using the unencrypted `modernc.org/sqlite` driver would have been a security misrepresentation.

### Fixed

- Search parser: a lowercase `key:value` token whose key did not match any known operator (e.g. `food:burger`) was silently dropped on its way out of `Parse`. The token now survives as plain free text — preserves URL-style and unknown-operator inputs verbatim.
- Search query builder: phrases containing a literal `"` produced `\"`-escaped MATCH expressions that FTS5 cannot parse. Switched to FTS5's doubled-quote escape (`""`).
- FTS5 live-write trigger now skips empty/NULL `messages.text` rows, matching `Indexer.Backfill`'s filter so service messages do not pollute the index.
- `TokenBucket.Wait` no longer leaks short-lived timers on ctx cancellation; switched from `time.After` to `time.NewTimer` + `Stop`.
- Send retry loop clamps FloodWait `retry_after` to a 1 s floor so a server (or buggy mock) returning 0 cannot spin the loop at the SendGuard ceiling.
- `progressThrottler` and `uploadProgressThrottler` now drop their per-id state on each transfer's terminal event — long-running TUI sessions no longer leak one map entry per completed file.
- Debug-bundle `goroutines.txt` no longer truncates at a fixed 1 MiB cap — the buffer doubles up to a 64 MiB safety bound so a busy app with thousands of goroutines does not silently lose frames mid-trace.
- Debug-bundle `db_stats.txt` rewrites the user's home prefix to `~` so the bundle does not leak the OS username via the absolute db path.
- `DBSizeMonitor` now sums the SQLite WAL/SHM side files into the threshold check — a heavy live-update session whose total footprint crosses 1 GiB no longer escapes the warning because the main `lazytg.db` file alone stayed under cap.
- Lazy reindex goroutine now recovers from panics and logs them at error level instead of vanishing silently — failures are still surfaced via bus events for normal errors, but a programmer mistake (nil deref, etc.) is no longer invisible.
- `TestDownloadService_Failure` glob now walks the actual store root instead of a fresh `t.TempDir()` — earlier the assertion was vacuous.
- Optimistic-UI flicker race: `applyOutgoingState{Pending}` is now a no-op so the bus event cannot insert an empty `[⏳]` row before `SendDispatchedMsg` (carrying the message body) lands.
- `SendFailedMsg` no longer clobbers a fresher draft or retargeted reply pointer when the user moved on while the failed send was in flight — the failure is dropped and surfaced as a warn log.
- Configurable `ScrollUp`/`ScrollDown` chords (default `ctrl+b`/`ctrl+f`) now actually scroll the thread viewport when the thread or chats pane is focused; previously the chord was advertised in `keymap.toml` and the help overlay but no code consumed it.
- `LiveService.persist` no longer republishes `DialogUpdated` onto its own subscription — the chats pane reacts to `MessageReceived` directly, which removes the buffer halving and the resulting potential for dropped events under burst.
- `?` (default `ToggleHelp` chord) no longer hijacks the input pane or the chats filter: when the input pane is focused or the chats list is in filter-input mode the keystroke falls through to the focused widget. Previously a question mark could not be typed into the message body or chat-list filter.
- Reply-preview truncation in the input pane is now rune-aware: a Cyrillic / CJK reply target longer than 50 codepoints is cut on a codepoint boundary instead of being byte-sliced mid-UTF-8. The byte-slice path emitted invalid UTF-8 to the terminal for the project's primary audience (Russian-speaking users).
- Optimistic row no longer disappears between `Sent` and the live echo: `applyOutgoingState{Sent}` flips the row's state in place (rendering as plain text without the `[⏳]` glyph) and `applyIncoming` removes it on the server-echo `MessageReceived`. Private 1:1 chats — where Telegram emits `UpdateShortSentMessage` instead of a follow-up `UpdateNewMessage` — therefore keep the user's message visible until manual reload.
- Inverted-race guard: a `SendDispatchedMsg` arriving after a terminal `Sent` or `Failed` bus event no longer re-creates a Pending row. The thread now records every finalised localID and short-circuits the late insert that would otherwise leave a phantom `[⏳]` no future event could resolve.
- Async send failures (FloodWait, validation, network retries exhausted) now restore the textarea body and reply pointer just like the synchronous-error path. The input pane stashes the dispatched body keyed by `LocalID` and reacts to `OutgoingMessageStateChanged{Failed}` on the bus; previously the CHANGELOG promise of "Failed sends restore the draft" only held when SendService returned an error before publishing — i.e. for the rare optimistic-store-write failure surface.
- Repeated `Ctrl-D` on the same media no longer spawns overlapping `DownloadService` goroutines that would race over the shared `<path>.partial` and corrupt the output. The app now reserves the `FileID` for the lifetime of the goroutine; a second chord while the first is in flight is a quiet no-op (debug-logged).
- Search overlay `Enter` now loads the matched message's ±5 context window via `search.Service.JumpContext` and scrolls the thread to the hit — even when the target message is older than the freshly-loaded initial-page slice. Previously the jump opened the chat at its newest 200 messages and the deferred `ScrollTo` silently no-op'd whenever the target was not in that window (the prime FTS5 use case for old hits). On a `JumpContext` error the path falls back to the legacy `OpenChat + deferred ScrollTo` so the user still lands in the chat.
- Search-jump no longer strands the user in an isolated ±5-message slice: the thread pane now exposes symmetric forward pagination (`Repository.GetMessagesAfter` + `applyPaginationNewer` + `AtBottom + hasNewer` trigger). Scroll-down at the bottom of the loaded window walks the model toward the present so messages newer than the window are reachable without re-opening the chat.
- Search-jump now resists stale repo loads: a slow `OpenChat` fetch (or in-flight older-page pagination) for the same chat can no longer clobber the freshly-installed jump window. The thread model carries a `loadGen` counter bumped on every `OpenChat` / `SwitchTo` / `LoadJumpWindow`; `loadCmd` / `paginateCmd` / `paginateNewerCmd` capture it at scheduling time and the matching apply-handlers drop messages whose generation no longer matches.

### Known gaps (Stage 2 + Stage 3 follow-up)

- `runTUI` in `cmd/lazytg/cmd/tui.go` does not yet call `App.AttachClient` and does not start a `tg.Client.Run` loop. The TUI binary therefore renders the local SQLite cache only — sends, live updates, history backfill, reconnect orchestration **and Stage 3 file download/upload (Ctrl-D / Ctrl-U)** require this wiring to be added before either stage's acceptance criteria are met against a real Telegram session. The UI app already nil-checks `Downloader`/`Uploader` so the chords degrade to quiet no-ops instead of crashing.
- Search overlay renders `chat=<id>` instead of the chat title — the overlay does not yet take a chat-title source. Cosmetic but UX-degrading on the killer feature.
- `from:@username` resolves through `chats.username` — a user whose row is not in `chats` (most group/channel senders) silently produces zero hits. Documented in `docs/SEARCH.md`; full fix needs a peer-username index.
- `in:#chatname` only accepts a single whitespace-bounded token, so multi-word chat titles cannot be expressed via the operator (`in:#general` works, `in:#"Family Group"` errors and `in:#Family Group` filters by `Family` only). Parser v0.1 splits tokens before applying operators; quoted-value-after-operator support is deferred to Stage 4+. Workaround: filter by `username` when the chat has one. Documented in `docs/SEARCH.md`.
- `BackfillService.Start` is constructed by `AttachClient` but never invoked. Once `runTUI` wires the gotd Run loop, the cmd layer must call `app.Backfill.Start(bgCtx)` to drain the enqueue channel.
- `--polling` flag is plumbed into `App.Polling` but no consumer reads it: `PollingFallback` is constructed nowhere outside its tests. Wire-up belongs in `runTUI` next to `AttachClient`.
- `reconnectAdapter.Connect` in `internal/app/wire.go` is a deliberate no-op stub. Real reconnect orchestration (re-running `Client.Run` with the saved session) lives in the same follow-up as the MTProto wiring above.
- Foreign-key cascade (`messages.chat_id REFERENCES chats(id)`): `LiveService.persist` does not yet upsert a chat row before saving the first message from an unknown chat. Once a `DialogsService` populates the `chats` table on login (or an upsert is added to `LiveService`), the FK will hold.
