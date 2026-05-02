# Security

This document describes the threat model for lazytg, the ban-risk policy specific to running an unofficial Telegram client, and the disclosure process for security issues.

## Scope of this document

lazytg is a **userbot** in Telegram terms — it logs in with the same MTProto credentials a regular user would, not a bot token. That places it in a stricter security and policy bucket than bot-API integrations. Read this page before deploying lazytg with a primary account.

## Ban-risk warning

> Telegram explicitly reserves the right to put unofficial clients under observation and to limit or terminate accounts that exhibit unusual behaviour. After the August 2024 enforcement uptick around the Durov case, the practical risk of running a custom client on a primary account has grown.

Concretely:

- Use lazytg with a **secondary, throwaway test account first**. Validate the workflows you care about. Only then consider attaching a primary account.
- The send path will get a hard built-in rate-limit guard (`max 10 messages/sec`) once the send path itself lands in Stage 2. It exists to keep the behavioural fingerprint of lazytg close to a human user's, not as ergonomics. Stage 1 has no send code, so the guard is not present yet.
- lazytg avoids "machine-like" patterns: no message scraping at high rate, no automated mass actions, no message editing loops. New features that introduce such patterns will not be accepted upstream.
- Telegram's official policy on `api_id` / `api_hash` is documented at <https://core.telegram.org/api/obtaining_api_id>. Read it.

If your account is restricted, lazytg cannot help you get it back. That outcome is on the user.

## Threat model

### What we defend

| Asset                       | Threat                                                 | Mitigation                                                                                                                                                  |
|-----------------------------|--------------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------|
| MTProto session keys        | Local malware running as the user; device theft        | Stored in OS keyring (Keychain / Secret Service / Credential Manager). On headless boxes without D-Bus, fall back to `age`-encrypted file gated by a master passphrase prompted at startup. |
| `api_hash` env var          | Logs, debug bundles, crash reports                     | Stripped by `RedactingHandler` in `internal/core/obs/redact.go`. Hex strings of length 32+ are masked as `<api_hash>`.                                      |
| Phone numbers               | Logs, debug bundles                                    | Phone-shaped strings (a leading `+` followed by 10–15 digits) are masked as `+***` by the same redactor. Bare numeric runs are left intact so int64 IDs and Unix timestamps survive.                          |
| Message bodies              | Logs, debug bundles                                    | Logger never receives message text by default. The `debug-bundle` command (Stage 3) is verified by a grep test to never include message content.            |
| Local SQLite DB             | Device theft                                           | Filesystem permissions only (`0600` files, `0700` dirs). The `age` secrets file is fail-fast at startup if its mode widens. The DB itself is unencrypted; CGo SQLCipher is planned for Stage 3 (build tag reserved but not yet wired).            |
| `$EDITOR` invocation        | Hostile env vars influencing the editor               | Stage 2 will filter the env handed to `$EDITOR` down to `PATH`, `HOME`, `TERM`, `LANG`, `EDITOR` only.                                                      |

### What we do **not** defend

| Out of scope                      | Why                                                                                                                                       |
|-----------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------|
| Telegram servers                  | Telegram sees the same content any other client would. lazytg cannot hide message content from Telegram.                                  |
| Root-level local malware          | An attacker with root on your box can read any user-mode key material — keyring, age master key, kernel keyring, RAM. Mitigations are limited to filesystem hygiene. |
| Side-channel attacks              | Out of scope. lazytg is not a hardened security product, it is a desktop TUI client.                                                      |
| End-to-end secret chats           | Not in v0.1.0. `gotd/td` does not yet expose secret chats; if added later, that will be in a separate threat-model entry.                 |

## Filesystem hygiene

- `$XDG_CONFIG_HOME/lazytg/` (and the macOS equivalent) and the data/state/cache dirs are *created* with mode `0700` by `internal/core/config/paths.go`.
- The `age`-encrypted secrets file is fail-fast: if it exists with a mode wider than `0600` (`mode & 0077 != 0`) the process refuses to read it.
- Re-validating directory modes on every run is planned for Stage 3 hardening; today only the secrets file is fail-fast.

## Logging redaction

The `RedactingHandler` in `internal/core/obs/redact.go` wraps any underlying `slog.Handler` and filters string attribute values. Patterns currently scrubbed (order: session → api_hash → phone — session must run first so a session blob containing a 32-hex sub-run isn't split by the api_hash matcher):

- Hex strings of length 32 or more → `<api_hash>`.
- Standard-base64 strings of 40+ chars (A-Za-z0-9, plus at least one `+` or `/`) → `<session>`. Pure-alphanumeric runs survive (URL slugs, identifiers); the character class is deliberately tight so attribute prefixes such as `api_hash=...` aren't swallowed into a single match.
- Phone numbers (literal `+` followed by 10–15 digits) → `+***`. Bare numeric runs are intentionally left alone: chat/account IDs are int64 and routinely 10+ digits.

Tests live in `internal/core/obs/redact_test.go`. New patterns we discover should be added there, not in ad-hoc places throughout the code.

## debug-bundle policy

The `lazytg debug-bundle` command (full implementation lands in Stage 3) collects logs and configuration to help triage bugs. It is constrained by policy:

- **Never include**: session blobs, `api_hash`, raw message text, contact lists, peer access hashes.
- **May include**: redacted log tail, lazytg version + commit + build date, OS/arch, env vars *whitelisted* (`TERM`, `LANG`, `XDG_*`), schema migration version, build tags.
- Stage 3 will add a grep test in CI that verifies no fixture session/api_hash/text leaks into a generated bundle. Until then the redactor (`internal/core/obs/redact_test.go`) carries the regression coverage for the underlying patterns.

## Disclosure policy

- **Private channel:** GitHub Security Advisories on the lazytg repository.
- **Response window:** initial acknowledgement within 7 days, fix or mitigation plan within 30 days when feasible.
- **Coordinated disclosure:** 90 days from initial report to public disclosure, unless the issue is being actively exploited or the reporter requests a different timeline.
- We will credit reporters in `CHANGELOG.md` unless they ask to remain anonymous.

Please **do not** open public issues for security bugs. Use GitHub's "Report a vulnerability" button on the Security tab.
