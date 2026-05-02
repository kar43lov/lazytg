# Security

This document describes the threat model for lazytg, the ban-risk policy specific to running an unofficial Telegram client, and the disclosure process for security issues.

## Scope of this document

lazytg is a **userbot** in Telegram terms — it logs in with the same MTProto credentials a regular user would, not a bot token. That places it in a stricter security and policy bucket than bot-API integrations. Read this page before deploying lazytg with a primary account.

## Ban-risk warning

> Telegram explicitly reserves the right to put unofficial clients under observation and to limit or terminate accounts that exhibit unusual behaviour. After the August 2024 enforcement uptick around the Durov case, the practical risk of running a custom client on a primary account has grown.

Concretely:

- Use lazytg with a **secondary, throwaway test account first**. Validate the workflows you care about. Only then consider attaching a primary account.
- The send path has a hard built-in rate-limit guard (`max 10 messages/sec`) which **must not** be disabled. It exists to keep the behavioural fingerprint of lazytg close to a human user's, not as ergonomics.
- lazytg avoids "machine-like" patterns: no message scraping at high rate, no automated mass actions, no message editing loops. New features that introduce such patterns will not be accepted upstream.
- Telegram's official policy on `api_id` / `api_hash` is documented at <https://core.telegram.org/api/obtaining_api_id>. Read it.

If your account is restricted, lazytg cannot help you get it back. That outcome is on the user.

## Threat model

### What we defend

| Asset                       | Threat                                                 | Mitigation                                                                                                                                                  |
|-----------------------------|--------------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------|
| MTProto session keys        | Local malware running as the user; device theft        | Stored in OS keyring (Keychain / Secret Service / Credential Manager). On headless boxes without D-Bus, fall back to `age`-encrypted file gated by a master passphrase prompted at startup. |
| `api_hash` env var          | Logs, debug bundles, crash reports                     | Stripped by `RedactingHandler` in `internal/core/obs/redact.go`. Hex strings of length 32 are masked as `<api_hash>`.                                       |
| Phone numbers               | Logs, debug bundles                                    | Phone-shaped strings (`\+?\d{10,15}`) are masked as `+***` by the same redactor.                                                                            |
| Message bodies              | Logs, debug bundles                                    | Logger never receives message text by default. The `debug-bundle` command (Stage 3) is verified by a grep test to never include message content.            |
| Local SQLite DB             | Device theft                                           | Default mode: filesystem permissions only (`0600` files, `0700` dirs, fail-fast on wider modes). Heavy users: `-tags sqlcipher` build → encrypted DB.       |
| `$EDITOR` invocation        | Hostile env vars influencing the editor               | Stage 2 will filter the env handed to `$EDITOR` down to `PATH`, `HOME`, `TERM`, `LANG`, `EDITOR` only.                                                      |

### What we do **not** defend

| Out of scope                      | Why                                                                                                                                       |
|-----------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------|
| Telegram servers                  | Telegram sees the same content any other client would. lazytg cannot hide message content from Telegram.                                  |
| Root-level local malware          | An attacker with root on your box can read any user-mode key material — keyring, age master key, kernel keyring, RAM. Mitigations are limited to filesystem hygiene. |
| Side-channel attacks              | Out of scope. lazytg is not a hardened security product, it is a desktop TUI client.                                                      |
| End-to-end secret chats           | Not in v0.1.0. `gotd/td` does not yet expose secret chats; if added later, that will be in a separate threat-model entry.                 |

## Filesystem hygiene

lazytg checks the following at startup and refuses to run if anything is wrong:

- `$XDG_CONFIG_HOME/lazytg/` and similar dirs must be mode `0700`.
- Any session/age file must be mode `0600`.

If `os.Stat` returns a mode where `mode & 0077 != 0`, lazytg fails with a clear message. We do not silently fix permissions — we want the user to see that something tried to widen them.

## Logging redaction

The `RedactingHandler` in `internal/core/obs/redact.go` wraps any underlying `slog.Handler` and filters string attribute values. Patterns currently scrubbed:

- Phone numbers — `\+?\d{10,15}` → `+***`.
- Long base64-ish strings (>40 chars) → `<session>`.
- Hex strings of length 32 → `<api_hash>`.

Tests live in `internal/core/obs/redact_test.go`. New patterns we discover should be added there, not in ad-hoc places throughout the code.

## debug-bundle policy

The `lazytg debug-bundle` command (full implementation lands in Stage 3) collects logs and configuration to help triage bugs. It is constrained by policy:

- **Never include**: session blobs, `api_hash`, raw message text, contact lists, peer access hashes.
- **May include**: redacted log tail, lazytg version + commit + build date, OS/arch, env vars *whitelisted* (`TERM`, `LANG`, `XDG_*`), schema migration version, build tags.
- A grep test in CI verifies that no fixture session/api_hash/text leaks into a generated bundle.

## Disclosure policy

- **Private channel:** GitHub Security Advisories on the lazytg repository.
- **Response window:** initial acknowledgement within 7 days, fix or mitigation plan within 30 days when feasible.
- **Coordinated disclosure:** 90 days from initial report to public disclosure, unless the issue is being actively exploited or the reporter requests a different timeline.
- We will credit reporters in `CHANGELOG.md` unless they ask to remain anonymous.

Please **do not** open public issues for security bugs. Use GitHub's "Report a vulnerability" button on the Security tab.
