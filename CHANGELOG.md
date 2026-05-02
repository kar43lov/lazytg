# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

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

- (none yet)
