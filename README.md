# lazytg

> ⚠️ **Ban-risk warning:** Telegram automatically puts unofficial clients under observation. Use lazytg with a test account first. See [docs/SECURITY.md](docs/SECURITY.md) for details.

[![CI](https://github.com/pgmac/lazytg/actions/workflows/ci.yml/badge.svg)](https://github.com/pgmac/lazytg/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/pgmac/lazytg)](https://goreportcard.com/report/github.com/pgmac/lazytg)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

## What is lazytg

**lazytg** is a local-first Telegram TUI client written in Go. The headline feature is *instant search across your entire history*: messages are indexed locally with SQLite FTS5 (trigram tokenizer), so search is offline, fast, and not tied to Telegram's server-side `messages.search` (a known pain point of Telegram Desktop).

Think `lazygit` ergonomics, but for Telegram conversations: keyboard-driven, single static binary, no Electron, lives happily inside `tmux` over `ssh`.

**MVP scope (v0.1.0):** read + reply + files. Authentication (phone+code+2FA), chat list, message history, send text, reply, send/download files, live updates, local search, command palette.

**Target user:** developer who lives in `tmux + nvim + ssh`.

## Status

**Alpha — work in progress.** This repository is currently in Stage 1 of the [v0.1.0 roadmap](docs/plans/lazytg-v0.1.0.md): foundation only (architecture, storage, auth, CLI, logging, CI). The TUI itself ships in Stage 2.

Current capabilities:

- `lazytg login` — phone + code + 2FA authentication via [gotd/td](https://github.com/gotd/td); session is persisted to OS keyring (with `age`-encrypted file fallback for headless boxes).
- `lazytg accounts` — list authenticated accounts.
- `lazytg logout` — drop a stored session.
- `lazytg version` — print version, commit, build date.
- SQLite storage layer with FTS5 trigram index (verified for both Cyrillic and Latin text).

## Install

### From source (Go ≥ 1.22)

```sh
go install github.com/pgmac/lazytg/cmd/lazytg@latest
```

### Pre-built binaries

Pre-built archives for `linux` and `darwin` (`amd64` + `arm64`) are published on the [Releases](https://github.com/pgmac/lazytg/releases) page. SHA256 checksums and cosign keyless signatures are attached to every release.

### From this repository

```sh
git clone https://github.com/pgmac/lazytg.git
cd lazytg
make build          # → bin/lazytg
```

`make build` produces a pure-Go binary. To opt into CGo-backed SQLCipher (encrypted SQLite database):

```sh
go build -tags sqlcipher -o bin/lazytg ./cmd/lazytg
```

## Quickstart

lazytg uses your own Telegram API credentials. Get them from <https://my.telegram.org/apps>.

```sh
export LAZYTG_API_ID=1234567
export LAZYTG_API_HASH=0123456789abcdef0123456789abcdef

lazytg login --account +71234567890
# → Telegram sends a code, lazytg prompts for it
# → If 2FA enabled, lazytg prompts for the password

lazytg accounts
# → +71234567890   (active)

lazytg logout --account +71234567890
```

The session is stored in your OS keyring (Keychain on macOS, Secret Service on Linux, Credential Manager on Windows). On a headless server without D-Bus, lazytg falls back to a file encrypted with [age](https://age-encryption.org/), gated by a master passphrase you provide at startup.

Configuration, data, and cache directories follow the [XDG Base Directory spec](https://specifications.freedesktop.org/basedir-spec/basedir-spec-latest.html):

| Purpose         | Path                                |
|-----------------|-------------------------------------|
| Config          | `$XDG_CONFIG_HOME/lazytg/`          |
| Data (SQLite)   | `$XDG_DATA_HOME/lazytg/`            |
| State           | `$XDG_STATE_HOME/lazytg/`           |
| Cache           | `$XDG_CACHE_HOME/lazytg/`           |

## Architecture

3-layer architecture with import-direction enforcement via `depguard`:

- `internal/tg/` — gotd/td wrapper (knows MTProto)
- `internal/core/` — domain types, storage interfaces, event bus, sync (no gotd, no bubbletea)
- `internal/ui/` — Bubble Tea models and views (no gotd)
- `internal/storage/sqlite/` — SQLite repository (modernc.org/sqlite by default; SQLCipher via `-tags sqlcipher`)
- `internal/app/` — manual DI wiring
- `cmd/lazytg/` — cobra entry point

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the full layout, dependency rules, and stack rationale.

## Development

- [docs/CONTRIBUTING.md](docs/CONTRIBUTING.md) — dev setup, testing, conventions, PR checklist
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — package layout, depguard rules, stack choices
- [docs/SECURITY.md](docs/SECURITY.md) — threat model, ban-risk policy, disclosure
- [CHANGELOG.md](CHANGELOG.md) — release notes (Keep a Changelog format)
- [docs/plans/lazytg-v0.1.0.md](docs/plans/lazytg-v0.1.0.md) — full v0.1.0 roadmap

## Manual smoke test (foundation)

After Stage 1 is built, the foundation is verified manually as follows (not yet automated — Stage 2 brings the TUI that exercises this end-to-end):

```sh
export LAZYTG_API_ID=...
export LAZYTG_API_HASH=...
./bin/lazytg login --account +7XXXXXXXXXX
# enter code from Telegram, then 2FA password if set
./bin/lazytg accounts             # account is listed
./bin/lazytg accounts             # second run — no re-auth
```

## License

[MIT](LICENSE) © 2026 lazytg contributors.
