# lazytg

> Local-first Telegram TUI with instant FTS5 search. Built for developers who live in tmux+nvim+ssh.

> ⚠️ **Ban-risk warning:** Telegram automatically puts unofficial clients under observation. Use lazytg with a test account first. See [docs/SECURITY.md](docs/SECURITY.md) for details.

[![CI](https://github.com/kar43lov/lazytg/actions/workflows/ci.yml/badge.svg)](https://github.com/kar43lov/lazytg/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/kar43lov/lazytg)](https://goreportcard.com/report/github.com/kar43lov/lazytg)
[![codecov](https://codecov.io/gh/kar43lov/lazytg/branch/main/graph/badge.svg)](https://codecov.io/gh/kar43lov/lazytg)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

<!-- Demo GIF placeholder. Recording recipe: docs/DEMO.md. The line below
     intentionally stays commented out until a real recording lands so
     GitHub does not show a broken-image icon.
![demo](docs/demo.gif)
-->


## What is lazytg

**lazytg** is a local-first Telegram TUI client written in pure Go. The headline feature is *instant search across your entire history*: messages are indexed locally with SQLite FTS5 (trigram tokenizer), so search is offline, fast, and not tied to Telegram's server-side `messages.search` (a known pain point of Telegram Desktop).

Think `lazygit` ergonomics, but for Telegram conversations: keyboard-driven, single static binary, no Electron, lives happily inside `tmux` over `ssh`.

**MVP scope (v0.1.0):** read + reply + files. Authentication (phone+code+2FA), chat list, message history, send text, reply, send/download files, live updates, local search, command palette.

**Target user:** developer who lives in `tmux + nvim + ssh`.

## Features

- 🔍 **Instant local FTS5 search** with trigram tokenizer — works offline, no `messages.search` round-trip.
- ⚡ **p95 ≈ 47 ms** on a 100k-message synthetic corpus, measured on an M4 (`make bench` gates at the 100 ms product SLA; the CI gate is looser because a shared runner is ~2.4× slower — see [docs/PERFORMANCE.md](docs/PERFORMANCE.md)).
- 🔐 **Local-first** — sessions in an `age`-encrypted file whose passphrase lives in the OS keyring, permissions audit refuses `0644` secrets.
- 🛡️ **Built-in ban-risk floor** — 10 msg/sec send rate-limit guard covers both text and media; not user-tunable upward.
- ⌨️ **Emacs/readline keymap by default**, fully overridable through `keymap.toml` (vim-style modal bindings deferred to v0.2).
- 📥📤 **First-class file transfer** — `Ctrl+D` downloads the focused media, `Ctrl+U` attaches a file with progress in the status bar.
- 🧭 **Command palette (`Ctrl+Space`)** with frecency-ranked chat switcher and Unicode-fuzzy matching ("Алёна" === "Алена").
- 🪶 **Pure-Go single binary**, no Electron, no CGo (sqlcipher build is opt-in, deferred for v0.1.x).
- 🔬 **Cosign-keyless signed releases** — every archive ships with a sigstore bundle; `cosign verify-blob` confirms provenance.
- 🛟 **Redacted `debug-bundle`** — single command produces an issue-ready tar.gz, verified in CI to never leak api_hash, session blobs, phone numbers, or message text.

## Status

**Alpha — release-candidate.** All four stages of the [v0.1.0 roadmap](docs/plans/lazytg-v0.1.0.md) have shipped (foundation, TUI, search/files/security, release pipeline). `runTUI` now opens the MTProto session before building the UI and syncs the dialog list, so the TUI reads live Telegram data; every failure path (no session, no network, revoked authorisation, connect timeout) degrades to the cached-only view rather than refusing to start. Not yet exercised against a live account by a maintainer — see `docs/MANUAL_SMOKE.md` and CHANGELOG `Known gaps` for what remains.

Current capabilities:

- `lazytg login` — phone + code + 2FA authentication via [gotd/td](https://github.com/gotd/td); the session is persisted to an `age`-encrypted file, unlocked by a passphrase kept in the OS keyring (or typed at startup on headless boxes).
- `lazytg accounts` — list authenticated accounts (read-only, no Telegram round-trip).
- `lazytg logout --account <phone>` — drop a stored session.
- `lazytg version` — print version, commit, build date.
- `lazytg debug-bundle` — produces a redacted tar.gz with version, config, log tail, db stats, goroutine dump (`docs/SECURITY.md` + `bundle_grep_test.go`).
- `lazytg reindex --all|--chat <id>` — runs the FTS5 backfill for a chat or every chat with progress on stderr.
- 2-pane Bubble Tea TUI: chats + thread, focus cycling, optimistic send, $EDITOR delegation, live updates, reconnect orchestration.
- Dialog sync on start (`messages.getDialogs`, paced and capped at 5 pages / 500 chats by design) plus history backfill when a chat is opened.
- Local search (FTS5 trigram) with operators `from:@user`, `in:#chat`, `before:`/`after:`, `has:file`, `"phrase"`, `-exclusion` (`docs/SEARCH.md`).
- Search overlay (`/`), command palette (Ctrl+Space), file download (Ctrl+D), file upload (Ctrl+U).
- DB-size monitor + permissions audit + 10 msg/s send rate-limit guard (covers both text and media sends).

### Keybindings (TUI)

| Binding         | Action                                |
|-----------------|---------------------------------------|
| Tab / Shift+Tab | cycle focus between panes             |
| Ctrl+Tab / Ctrl+Shift+Tab | next / previous chat (also Alt+N / Alt+P) |
| Enter           | send message                          |
| Alt+Enter       | newline in input                      |
| Ctrl+R          | reply to focused message              |
| Ctrl+E          | open `$EDITOR` with current draft     |
| `/`             | open search overlay                   |
| Ctrl+Space      | command palette (chat switcher L1)    |
| Ctrl+D          | download last media in thread         |
| Ctrl+U          | attach file (upload)                  |
| `?`             | toggle help overlay                   |
| Ctrl+C / Ctrl+Q | quit                                  |

### Mouse

| Gesture                  | Action                                      |
|--------------------------|---------------------------------------------|
| Left click on a chat row | focus the chat list and open that chat      |
| Left click on the thread | focus the thread                            |
| Left click on the input  | focus the composer                          |
| Wheel over the chat list | move the highlight one chat per notch       |
| Wheel over the thread    | scroll, loading older history at the top    |
| Drag over the thread     | select text; release copies it to the clipboard |
| Double click a message   | select and copy that message whole          |
| Drag the pane separator  | move the split between the chat list and the thread |

Selection follows Telegram Desktop: a drag inside one message is
character-exact, and the moment it crosses into another message both are taken
whole — collecting messages, not characters. Copying goes through OSC 52, the
terminal's own clipboard channel, so it works over `ssh` and inside `tmux`
(which needs `set -g set-clipboard on`).

The keyboard remains the primary interface — nothing needs the mouse. Outside
the thread the terminal's own selection still applies, and because lazytg puts
the terminal in mouse-reporting mode it needs the usual override: hold
<kbd>Option</kbd> (iTerm2, Terminal.app) or <kbd>Shift</kbd> (most Linux
terminals) while dragging.

See [docs/SEARCH.md](docs/SEARCH.md) for the search query syntax and [docs/FILES.md](docs/FILES.md) for the download/upload pipeline.

## Roadmap

Anything not on this list is out of scope until it lands here — see the non-goals table in [CLAUDE.md](CLAUDE.md) for the things that are permanently out.

**v0.2** (4–6 weeks after v0.1.0)

- Full vim-mode (normal/insert/visual + basic motions) — shipped whole rather than half, so there is no "why doesn't X work" surface.
- Command palette L2: global commands behind a `>` prefix.
- `expvar` metrics + trace mode + `lazytg debug stats`.
- `$EDITOR` sandbox env-filter (only `PATH`/`HOME`/`TERM`/`LANG`/`EDITOR` pass through).
- Multi-account UI switcher (the `--account` flag already covers the mechanics).
- Windows builds (a separate pile of TUI pain).
- macOS notarization (needs a $99/yr Apple ID).
- GoReleaser `brews:` → `homebrew_casks:` migration (deprecated in `goreleaser check` output; not a v0.1.0 blocker).

**v0.3+**

- Inline media preview (Kitty/iTerm/sixel via `BourgeoisBear/rasterm`).
- tgql — query DSL with saved searches (smart folders).
- Forwarding, edit history, reactions.

**v0.5+** (only if a community shows up)

- Starlark hooks (`google/starlark-go`, pure Go).
- AI layer (Claude API + local Ollama, prompt caching over long history).
- CLI pipe mode — single process, explicitly not a daemon.

## Requirements

- Go ≥ 1.25 to build (`go.mod` toolchain pin).
- On Linux: a running D-Bus session with a Secret Service provider (gnome-keyring, KWallet, etc.) so the keyring can hold the passphrase for you. Headless boxes prompt for it at startup instead.
- SQLite ≥ 3.34 is bundled by `modernc.org/sqlite` — no system SQLite required.
- Telegram API credentials — already embedded in release binaries. Builds from source need your own in `LAZYTG_API_ID` / `LAZYTG_API_HASH` (see <https://my.telegram.org/apps>); the repository carries no key, because an `api_id` published in source is blocked by Telegram permanently.

## Quickstart (2 steps)

```sh
# 1. install — release binaries ship with API credentials embedded
brew install kar43lov/lazytg/lazytg

# 2. log in (phone → code → 2FA)
lazytg login --account +71234567890
# → Telegram sends a code, lazytg prompts for it
# → If 2FA is enabled, lazytg asks for the cloud password (no echo)

lazytg                                 # opens the TUI
```

Other install paths (`.deb`, `.rpm`, manual signed tarball, `go install`)
are documented in [docs/INSTALL.md](docs/INSTALL.md).

## Install

### From source

```sh
git clone https://github.com/kar43lov/lazytg.git
cd lazytg
make build          # → bin/lazytg
```

A source build carries no API credentials — register an application at
<https://my.telegram.org/apps> and export `LAZYTG_API_ID` / `LAZYTG_API_HASH`
before logging in. `lazytg version` reports which credential source is active.

Once a release is tagged, `go install github.com/kar43lov/lazytg/cmd/lazytg@latest` will also work.

### Pre-built binaries

Pre-built archives for `linux` and `darwin` (`amd64` + `arm64`) are published on the [Releases](https://github.com/kar43lov/lazytg/releases) page. SHA256 checksums and cosign keyless signatures are attached to every release. See [docs/VERIFY.md](docs/VERIFY.md) for the verification recipe.

### Encrypted database (deferred past v0.1)

A `sqlcipher` build tag is reserved for the CGo-backed encrypted driver and is **not yet wired** — CGo SQLCipher integration is deferred past v0.1. The database is unencrypted regardless of build tag — rely on filesystem permissions (`0600` files / `0700` dirs, enforced by the startup permissions audit) and OS-level disk encryption.

## Authentication & sessions

The session is stored in `<config>/secrets.age`, encrypted with [age](https://age-encryption.org/). The passphrase that opens it is generated once and kept in your OS keyring (Keychain on macOS, Secret Service on Linux, Credential Manager on Windows), so you are never prompted on a desktop. On a headless server without D-Bus, lazytg asks for the passphrase at startup instead.

The session blob itself does not go in the keyring: a gotd session is ~4.2 KB and the macOS keyring backend refuses anything past 4096 bytes, which used to make sessions impossible to keep on macOS at all.

```sh
lazytg accounts                          # → +71234567890   (active)
lazytg accounts --account +71234567890   # second run does NOT re-prompt
lazytg logout   --account +71234567890   # drop the session blob
```

### Persistent flags

All subcommands accept:

- `--account <phone>` — phone number to operate on.
- `--api-id` / `--api-hash` — override the API credentials for one run (takes precedence over the env vars and the embedded release key; `--api-hash` is visible in `ps`, so prefer `LAZYTG_API_HASH`).
- `--debug` — duplicates JSON logs to stderr in addition to the rotated file sink.
- `--log-level debug|info|warn|error` (default `info`).
- `--polling` — reserved, currently a no-op (wire-up deferred to a v0.1 follow-up; see CHANGELOG `Known gaps`).

Full reference, env vars, and `keymap.toml` overrides live in
[docs/CONFIGURATION.md](docs/CONFIGURATION.md).

### Files lazytg creates

Configuration, data, and cache directories follow the [XDG Base Directory spec](https://specifications.freedesktop.org/basedir-spec/basedir-spec-latest.html) on Linux. macOS uses Apple's user-data conventions where the spec defers to them.

| Purpose         | Linux                               | macOS                                                   |
|-----------------|-------------------------------------|---------------------------------------------------------|
| Config          | `$XDG_CONFIG_HOME/lazytg/`          | `~/Library/Application Support/lazytg/`                 |
| Data (SQLite)   | `$XDG_DATA_HOME/lazytg/`            | `~/Library/Application Support/lazytg/`                 |
| State (logs)    | `$XDG_STATE_HOME/lazytg/`           | `~/Library/Application Support/lazytg/`                 |
| Cache           | `$XDG_CACHE_HOME/lazytg/`           | `~/Library/Caches/lazytg/`                              |

Logs go to `<state>/lazytg.log` with lumberjack rotation (10 MB × 3 backups × 30 days). Phone numbers, session blobs, and `api_hash` strings are scrubbed before write.

## Architecture

3-layer architecture with import-direction enforcement via `depguard`:

- `internal/tg/` — gotd/td wrapper (knows MTProto): client, auth, send, history, updates, polling, files
- `internal/core/` — domain types, storage interfaces, event bus, sync, search, files, security, observability (no gotd, no bubbletea)
- `internal/ui/` — Bubble Tea v2 models, panes (chats/thread/search/attach), input editor, palette, status bar, keymap
- `internal/storage/sqlite/` — SQLite repository (modernc.org/sqlite, pure-Go) with FTS5 trigram index, frecency, dedup tables
- `internal/app/` — manual DI wiring (`Build` for non-MTProto services, `AttachClient` for MTProto-aware ones, `RunBackground` for long-lived goroutines)
- `cmd/lazytg/` — cobra entry point: `tui` (default), `login`, `logout`, `accounts`, `version`, `debug-bundle`, `reindex`

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the full layout, dependency rules, and stack rationale.

## Documentation

User docs:

- [docs/INSTALL.md](docs/INSTALL.md) — every install path (brew, .deb, .rpm, manual, `go install`, build from source).
- [docs/CONFIGURATION.md](docs/CONFIGURATION.md) — env vars, CLI flags, `keymap.toml`, multi-account.
- [docs/SEARCH.md](docs/SEARCH.md) — query operators, FTS5 internals, DB-size guidance.
- [docs/FILES.md](docs/FILES.md) — download/upload pipeline (`Ctrl+D` / `Ctrl+U`).
- [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) — symptom → diagnosis → fix.
- [docs/VERIFY.md](docs/VERIFY.md) — checksum + cosign verification recipes.
- [docs/PERFORMANCE.md](docs/PERFORMANCE.md) — memory budgets, search SLA, live-update SLA.
- [docs/SECURITY.md](docs/SECURITY.md) — threat model, ban-risk policy, disclosure.
- [docs/MANUAL_SMOKE.md](docs/MANUAL_SMOKE.md) — full manual smoke checklist.
- [CHANGELOG.md](CHANGELOG.md) — release notes (Keep a Changelog format).

Developer docs:

- [docs/CONTRIBUTING.md](docs/CONTRIBUTING.md) — dev setup, testing, commit format, PR checklist.
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — package layout, depguard rules, stack rationale.
- [docs/DEMO.md](docs/DEMO.md) — maintainer runbook for recording the demo gif.
- [docs/plans/lazytg-v0.1.0.md](docs/plans/lazytg-v0.1.0.md) — full v0.1.0 roadmap.

Release/maintainer docs:

- [docs/RELEASE_PROCESS.md](docs/RELEASE_PROCESS.md) — alpha → beta → rc → stable runbook with hotfix and rollback.
- [docs/BETA_CHECKLIST.md](docs/BETA_CHECKLIST.md) — 6-step external-tester smoke checklist.
- [docs/RELEASE_ANNOUNCE.md](docs/RELEASE_ANNOUNCE.md) — Show HN / r/commandline / lobste.rs / r/golang announcement drafts.
- [docs/plans/completed/20260503-lazytg-stage4-release.md](docs/plans/completed/20260503-lazytg-stage4-release.md) — Stage 4 release-engineering plan.

## Manual smoke test

The full Stage 1–3 manual smoke checklist lives in [docs/MANUAL_SMOKE.md](docs/MANUAL_SMOKE.md). The minimal foundation walk-through:

```sh
export LAZYTG_API_ID=...
export LAZYTG_API_HASH=...
./bin/lazytg login --account +7XXXXXXXXXX
# enter code from Telegram, then 2FA password if set
./bin/lazytg accounts             # account is listed
./bin/lazytg accounts             # second run — no re-auth
./bin/lazytg reindex --all        # FTS5 backfill for every chat (heavy users)
./bin/lazytg                      # open the TUI
```

## Maintainer notes

### Setup before first release

The release pipeline assumes a few external resources exist before the first stable tag is pushed:

1. **Homebrew tap repository.** Create `kar43lov/homebrew-lazytg` on GitHub manually, with an empty `Formula/` directory. GoReleaser commits the generated `Formula/lazytg.rb` here on every stable release.
2. **PAT for tap pushes.** Generate a Personal Access Token (fine-grained) with `contents: write` on the `kar43lov/homebrew-lazytg` repo. Add it to this repo's secrets as `HOMEBREW_TAP_GITHUB_TOKEN`. The token is **not needed** until the first stable tag — alpha/beta/rc skip the brew upload.
3. **First publish.** The first `git tag v0.1.0 && git push --tags` (without `-alpha`/`-beta`/`-rc` suffix) pushes the formula automatically. From that point on `brew install kar43lov/lazytg/lazytg` works.

### SQLCipher (encrypted DB) build variant

Deferred past v0.1. The `sqlcipher` build tag is reserved for a future CGo-backed driver. Until that driver lands, building with `-tags sqlcipher` fails to compile (deliberately — see `internal/storage/sqlite/driver_sqlcipher.go`). Releases ship the pure-Go variant only.

### Verifying signatures

Every release archive ships a sigstore bundle (`*.sigstore.json`) and `checksums.txt` is signed via cosign keyless OIDC. See [docs/VERIFY.md](docs/VERIFY.md) (Stage 4) for the full verification recipe.

### Regenerating CHANGELOG

`CHANGELOG.md` is generated from Conventional Commit history by [`git-cliff`](https://git-cliff.org/) using `cliff.toml`. Install once:

```sh
brew install git-cliff             # macOS
# or: cargo install git-cliff
```

Before tagging a release, regenerate the Unreleased section:

```sh
git-cliff --tag v0.1.0-alpha.1 --unreleased --prepend CHANGELOG.md
```

Contributors do not need `git-cliff` for normal PR work — see [docs/CONTRIBUTING.md](docs/CONTRIBUTING.md#commit-messages) for the commit-message rules that feed it.

## License

[MIT](LICENSE) © 2026 lazytg contributors.
