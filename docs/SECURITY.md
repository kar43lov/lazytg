# Security

This document describes the threat model for lazytg, the ban-risk policy specific to running an unofficial Telegram client, and the disclosure process for security issues.

## Scope of this document

lazytg is a **userbot** in Telegram terms — it logs in with the same MTProto credentials a regular user would, not a bot token. That places it in a stricter security and policy bucket than bot-API integrations. Read this page before deploying lazytg with a primary account.

## Ban-risk warning

> Telegram explicitly reserves the right to put unofficial clients under observation and to limit or terminate accounts that exhibit unusual behaviour. After the August 2024 enforcement uptick around the Durov case, the practical risk of running a custom client on a primary account has grown.

Concretely:

- Use lazytg with a **secondary, throwaway test account first**. Validate the workflows you care about. Only then consider attaching a primary account.
- The send path runs through a hard built-in rate-limit guard (`max 10 messages/sec`, burst 30 — `internal/core/security/send_ratelimit.go`). The guard covers both text sends (`coresync.SendService`) and file uploads (`files.UploadService`); `messages.SendMedia` waits on the same token bucket as `messages.SendMessage`. Not user-tunable.
- lazytg avoids "machine-like" patterns: no message scraping at high rate, no automated mass actions, no message editing loops. New features that introduce such patterns will not be accepted upstream.
- Telegram's official policy on `api_id` / `api_hash` is documented at <https://core.telegram.org/api/obtaining_api_id>. Read it.
- Release binaries carry lazytg's own `api_id`, shared by everyone who installs one. Observation applies to accounts logging in through *any* unofficial client, so this does not make you more visible than your own key would — but it does mean one shared blast radius (see below). Exporting `LAZYTG_API_ID` / `LAZYTG_API_HASH` opts you out.

If your account is restricted, lazytg cannot help you get it back. That outcome is on the user.

### Shipped credentials: the accepted risk

Release binaries have credentials injected at build time from repository
secrets so that installing lazytg does not require registering an application
first — which is impossible from some countries, where <https://my.telegram.org>
is unreachable without a VPN and blocks application creation over one.

The risks this accepts, stated plainly:

- **Single point of failure.** All release users share one `api_id`. If
  Telegram blocks it, every one of them loses login simultaneously. The escape
  hatch is built in: export your own credentials and the embedded key is
  bypassed, no reinstall needed.
- **Extractable from the binary.** `strings` on a release binary reveals the
  `api_hash`. Obfuscation would be theatre; the same is true of every client
  that ships credentials, official ones included. What actually matters is that
  the key is not in *source*, because Telegram permanently blocks a published
  `api_id` (`API_ID_PUBLISHED_FLOOD`) — the block follows publication, not
  extraction.
- **Not in this repository, enforced.** `scripts/secret-scan.sh` fails a commit
  (lefthook `pre-commit`) and fails CI (`secret-scan` job) on any 32-hex string
  outside the documented placeholder. The credentials live only in repository
  secrets (`LAZYTG_RELEASE_API_ID` / `LAZYTG_RELEASE_API_HASH`) and in built
  artefacts.

`lazytg version` prints which source is in effect (`flags` / `env` /
`embedded` / `none`) and never the values — that line is the first thing to ask
for in a credential-related bug report.

## Threat model

### What we defend

| Asset                       | Threat                                                 | Mitigation                                                                                                                                                  |
|-----------------------------|--------------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------|
| MTProto session keys        | Local malware running as the user; device theft        | Stored in OS keyring (Keychain / Secret Service / Credential Manager). On headless boxes without D-Bus, fall back to `age`-encrypted file gated by a master passphrase prompted at startup. |
| `api_hash` env var          | Logs, debug bundles, crash reports                     | Stripped by `RedactingHandler` in `internal/core/obs/redact.go`. Hex strings of length 32+ are masked as `<api_hash>`.                                      |
| Phone numbers               | Logs, debug bundles                                    | Phone-shaped strings (a leading `+` followed by 10–15 digits) are masked as `+***` by the same redactor. Bare numeric runs are left intact so int64 IDs and Unix timestamps survive.                          |
| Message bodies              | Logs, debug bundles                                    | Logger never receives message text by default. The `debug-bundle` command is verified by `internal/core/obs/bundle_grep_test.go` (CI gate) — api_hash hex, session base64 blobs, phone numbers, and message text fixtures are checked against every tar entry. |
| Local SQLite DB             | Device theft                                           | Filesystem permissions enforced by the startup permissions audit (`internal/core/security/permissions.go`): `0600` for `secrets.age`/`lazytg.db` (fail-fast), `0700` for parent dirs (warn-class). The DB itself is unencrypted; CGo SQLCipher is deferred past v0.1 (build tag reserved but not yet wired).            |
| `$EDITOR` invocation        | Hostile env vars influencing the editor               | Env-filter down to `PATH`/`HOME`/`TERM`/`LANG`/`EDITOR` is on the v0.2 list — currently the editor inherits the full env.                                   |

### What we do **not** defend

| Out of scope                      | Why                                                                                                                                       |
|-----------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------|
| Telegram servers                  | Telegram sees the same content any other client would. lazytg cannot hide message content from Telegram.                                  |
| Root-level local malware          | An attacker with root on your box can read any user-mode key material — keyring, age master key, kernel keyring, RAM. Mitigations are limited to filesystem hygiene. |
| Side-channel attacks              | Out of scope. lazytg is not a hardened security product, it is a desktop TUI client.                                                      |
| End-to-end secret chats           | Not in v0.1.0. `gotd/td` does not yet expose secret chats; if added later, that will be in a separate threat-model entry.                 |

## Filesystem hygiene

- `$XDG_CONFIG_HOME/lazytg/` (and the macOS equivalent) and the data/state/cache dirs are *created* with mode `0700` by `internal/core/config/paths.go`.
- The startup permissions audit (`internal/core/security/permissions.go`) re-validates `secrets.age`, `lazytg.db`, and the parent dirs on every TUI start (`app.Build`) and on every CLI subcommand that opens the repo (`lazytg reindex`, `lazytg debug-bundle`). Findings:
    - `secrets.age` / `lazytg.db` mode wider than `0600` → fail-fast (process aborts).
    - `Config` / `State` dir mode wider than `0700` → warn-class log line, boot proceeds.
    - Missing files on first run → informational, boot proceeds (the audit knows no DB exists yet).

## Logging redaction

The `RedactingHandler` in `internal/core/obs/redact.go` wraps any underlying `slog.Handler` and filters string attribute values. Patterns currently scrubbed (order: session → api_hash → phone — session must run first so a session blob containing a 32-hex sub-run isn't split by the api_hash matcher):

- Hex strings of length 32 or more → `<api_hash>`.
- Standard-base64 strings of 40+ chars (A-Za-z0-9, plus at least one `+` or `/`) → `<session>`. Pure-alphanumeric runs survive (URL slugs, identifiers); the character class is deliberately tight so attribute prefixes such as `api_hash=...` aren't swallowed into a single match.
- Phone numbers (literal `+` followed by 10–15 digits) → `+***`. Bare numeric runs are intentionally left alone: chat/account IDs are int64 and routinely 10+ digits.

Tests live in `internal/core/obs/redact_test.go`. New patterns we discover should be added there, not in ad-hoc places throughout the code.

## debug-bundle policy

The `lazytg debug-bundle` command produces a redacted tar.gz with the following entries: `version.txt`, `config.toml` (api_hash and phone fields scrubbed), `logs.txt` (trailing N lines, redactor applied), `db_stats.txt` (table row counts only, no message text), `goroutines.txt`. The bundle file itself is written `0600`.

- **Never include**: session blobs, `api_hash`, raw message text, contact lists, peer access hashes, the SQLite DB file itself.
- **Verified in CI**: `internal/core/obs/bundle_grep_test.go` seeds api_hash hex / base64 session blob / phone / message-text fixtures into the config + logs + repo and asserts that none of those literal byte sequences appear in any tar entry. A regression that adds an unredacted code path fails this test.
- The grep test backs up the structural invariant — `bundle.go` only ever opens the version, config, log, db-stats, and goroutine inputs; it does NOT walk session/secrets directories.

## Disclosure policy

- **Private channel:** GitHub Security Advisories on the lazytg repository.
- **Response window:** initial acknowledgement within 7 days, fix or mitigation plan within 30 days when feasible.
- **Coordinated disclosure:** 90 days from initial report to public disclosure, unless the issue is being actively exploited or the reporter requests a different timeline.
- We will credit reporters in `CHANGELOG.md` unless they ask to remain anonymous.

Please **do not** open public issues for security bugs. Use GitHub's "Report a vulnerability" button on the Security tab.
