# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Stage 1 foundation: bootstrap, 3-layer architecture, depguard rules.
- SQLite storage layer (modernc/sqlite) with FTS5 trigram tokenizer (verified for Cyrillic and Latin).
- Telegram authentication flow via `gotd/td`, session storage with OS keyring and `age`-encrypted file fallback.
- Cobra CLI skeleton: `login`, `logout`, `accounts`, `version`, `debug-bundle` (stub).
- `slog` logger with redaction handler (phone numbers, session strings, api_hash) and lumberjack rotation.
- GitHub Actions CI (`lint`, `test` matrix on ubuntu/macos × default/sqlcipher tags), GoReleaser release pipeline with cosign keyless signing, snapshot workflow on PRs.
- Dependabot configuration for weekly Go module and GitHub Actions updates.
- Documentation: `README.md`, `docs/ARCHITECTURE.md`, `docs/SECURITY.md`, `docs/CONTRIBUTING.md`.
- PR and issue templates under `.github/`.

### Changed

- (none yet)

### Fixed

- (none yet)
