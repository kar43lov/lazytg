# Security

This document describes the threat model for lazytg, the ban-risk policy specific to running an unofficial Telegram client, and the disclosure process for security issues.

## Scope of this document

lazytg is a **userbot** in Telegram terms — it logs in with the same MTProto credentials a regular user would, not a bot token. That places it in a stricter security and policy bucket than bot-API integrations. Read this page before deploying lazytg with a primary account.

## Ban-risk warning

> Telegram explicitly reserves the right to put unofficial clients under observation and to limit or terminate accounts that exhibit unusual behaviour. After the August 2024 enforcement uptick around the Durov case, the practical risk of running a custom client on a primary account has grown.

Concretely:

- Use lazytg with a **secondary, throwaway test account first**. Validate the workflows you care about. Only then consider attaching a primary account.
- The send path runs through a hard built-in rate-limit guard (`max 10 messages/sec`, burst 30 — `internal/core/security/send_ratelimit.go`). The guard covers every path that **creates** a message: text sends (`coresync.SendService`), file uploads (`files.UploadService`) and forwarding (`coresync.ActionService`) all wait on the same token bucket. Not user-tunable. Editing, deleting and reacting are deliberately outside it — each acts on a message that already exists and costs one request per deliberate keypress, so a token bucket in front of "undo my reaction" would make the interface feel broken without changing the traffic a human produces.
- Two request paths added alongside the message actions are worth naming because neither is paced by a guard and neither needs one. Reactions and typing indicators are **received** only: they arrive on the update stream, which the server pushes whether or not a client looks. lazytg never sends a typing notification of its own — doing so would mean a request every few seconds for as long as somebody is writing, which is exactly the "machine-like pattern" this section is about. Inline images (`i`) fetch a photo only when asked, through the same download path and cache as `o` and `Ctrl-D`; nothing is prefetched.
- lazytg avoids "machine-like" patterns: no message scraping at high rate, no automated mass actions, no message editing loops. New features that introduce such patterns will not be accepted upstream.
- Telegram's official policy on `api_id` / `api_hash` is documented at <https://core.telegram.org/api/obtaining_api_id>. Read it.
- No lazytg build ships credentials, releases included — you supply your own `api_id` / `api_hash`. Observation applies to accounts logging in through *any* unofficial client, so this does not make you more or less visible; what it changes is who carries the consequences, which is you and nobody else (see below).

If your account is restricted, lazytg cannot help you get it back. That outcome is on the user.

### Credentials: why releases ship without them

Releases carry no `api_id` of their own. The build machinery to embed a pair
exists — `.goreleaser.yaml` injects `LAZYTG_RELEASE_API_ID` /
`LAZYTG_RELEASE_API_HASH` through `-ldflags` when those repository secrets are
set — and it is deliberately left unset. The reasoning, stated plainly so the
decision can be revisited rather than inherited:

- **A key in a public binary is a published key.** `strings lazytg | grep -E
  '^[0-9a-f]{32}$'` prints the `api_hash` of any build that embeds one, without
  knowing what to look for. Obfuscation would be theatre.
- **The block is collective and permanent.** Telegram blocks a published
  `api_id` with `API_ID_PUBLISHED_FLOOD`, and every user of that build loses
  login at the same moment — including the maintainer. Recovery means a new
  application, which means a new phone number (*"each number can only have one
  api_id connected to it"*), and a new release for everyone.
- **Telegram asks for exactly this.** Their own page: *"obtain your own API id
  before you publish your app"*. Shipping one shared key to end users is the
  case that instruction exists to prevent.
- **The cost lands in the right place.** Registering an application is one form
  and about a minute. Users who cannot reach that form (see below) have
  documented alternatives, and each of them is an informed choice by the person
  taking the risk rather than a default imposed on everyone.

What this does *not* protect against is anything about the account itself: an
account logging in through lazytg is under observation regardless of whose
`api_id` it presents.

**Where the form is unreachable.** `my.telegram.org` refuses to issue
applications from some regions — Russia notably, where the login code often
never arrives or the form answers a bare `ERROR`. Registering once over a VPN
produces credentials that keep working afterwards from anywhere, since the pair
belongs to the account rather than to the network. The alternative is receiving
a pair, or a binary with one compiled in, from someone who already has one —
documented in [`INSTALL.md`](INSTALL.md#when-mytelegramorg-will-not-issue-credentials).
Everyone sharing an application shares its blast radius: a block earned by any
of them lands on all of them, and the key holder answers for how it is used.

**Never in this repository, enforced.** `scripts/secret-scan.sh` fails a commit
(lefthook `pre-commit`) and fails CI (`secret-scan` job) on any 32-hex string
outside the documented placeholder. This holds regardless of the decision above:
credentials belong in a shell, in repository secrets, or in a build artefact —
never in source.

`lazytg version` prints which source is in effect (`flags` / `env` /
`embedded` / `none`) and never the values — that line is the first thing to ask
for in a credential-related bug report.

## Threat model

### What we defend

| Asset                       | Threat                                                 | Mitigation                                                                                                                                                  |
|-----------------------------|--------------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------|
| MTProto session keys        | Local malware running as the user; device theft        | Stored in `<config>/secrets.age` (`age`, scrypt, mode 0600). The passphrase is generated once and held in the OS keyring (Keychain / Secret Service / Credential Manager); on headless boxes without D-Bus it is prompted at startup. The session blob is too large for the keyring itself — ~4.2 KB against a 4096-byte limit in the macOS backend. A lost or replaced keyring entry leaves the file undecryptable *by design* — lazytg never re-encrypts it under a different passphrase, because that would overwrite the only copy of the session; see [`TROUBLESHOOTING.md`](TROUBLESHOOTING.md#the-passphrase-does-not-open-this-file-at-startup). |
| `api_hash` env var          | Logs, debug bundles, crash reports                     | Stripped by `RedactingHandler` in `internal/core/obs/redact.go`. Hex strings of length 32+ are masked as `<api_hash>`.                                      |
| Phone numbers               | Logs, debug bundles                                    | Phone-shaped strings (a leading `+` followed by 10–15 digits) are masked as `+***` by the same redactor. Bare numeric runs are left intact so int64 IDs and Unix timestamps survive.                          |
| Message bodies              | Logs, debug bundles                                    | Logger never receives message text by default. The `debug-bundle` command is verified by `internal/core/obs/bundle_grep_test.go` (CI gate) — api_hash hex, session base64 blobs, phone numbers, and message text fixtures are checked against every tar entry. |
| Local SQLite DB             | Device theft                                           | Filesystem permissions enforced by the startup permissions audit (`internal/core/security/permissions.go`): `0600` for `secrets.age`/`lazytg.db` (fail-fast), `0700` for parent dirs (warn-class). The DB itself is unencrypted; CGo SQLCipher is deferred past v0.1 (build tag reserved but not yet wired).            |
| `$EDITOR` invocation        | Hostile env vars influencing the editor               | Env-filter down to `PATH`/`HOME`/`TERM`/`LANG`/`EDITOR` is on the v0.2 list — currently the editor inherits the full env.                                   |
| The user's terminal         | Escape sequences inside text somebody else wrote      | Every string that reaches the screen from Telegram — message body, chat title, author label, attachment filename, search snippet — is stripped of C0/C1 controls, DEL and the bidi overrides by `internal/ui/safetext` at render time. This is not hypothetical: lazytg copies to the clipboard with OSC 52, so the terminal it runs in demonstrably honours OSC, and a filename carrying an OSC 52 sequence would have rewritten the clipboard as the message scrolled into view. The bidi overrides mattered for a second reason — they reverse the display order of a filename, so `.command` can be drawn as `.png` in the badge the user is about to open. The zero-width joiner is deliberately kept: it carries meaning in emoji and several scripts and cannot steer a terminal. |
| The account                 | `contacts.resolveUsername` as a behavioural signal (mass resolution is a known abuse pattern) | The client resolves a handle only when the user typed it into the palette and pressed Enter with no local match — one request per explicit ask, never for a name that appeared in a message, a mention or a list. A flood wait on the call is surfaced in the status bar with its duration and nothing is retried. |
| The user's desktop          | A link with a scheme the desktop has a handler for (`file:`, `tel:`, `x-apple.systempreferences:`, `ssh:`…) | `o` on a link goes through `files.Opener.OpenURL`, which parses the address and refuses everything but `http` and `https` with a host before the platform opener (`open` / `xdg-open`) is looked up; the refusal is a test (`TestOpener_OpenURL_RefusesNonWebSchemes`) rather than a convention. The same check runs on the client side in `openableLink`, so a non-web link is not even offered. The `open` override that tests use for files is deliberately not honoured for links: a link opener that could be pointed at an arbitrary program would turn a message into a command line. |
| The user, reading a link  | A link whose visible words and target disagree (`text_url`) | The host of the target is drawn beside the words in the thread — `click here ⟨evil.example⟩` — because the host is the one part of a link a sender cannot vary to look like something else. The URL is cleaned by `safetext` before it is parsed, so an escape inside it neither reaches the screen nor breaks the parse into showing the raw string. |
| The system viewer (`o`)     | An attachment whose type the sender chose             | The viewer is executed directly, never through a shell, with an absolute path that must exist. Downloads land as `0600` with no execute bit, so a `.command`/`.sh`/`.desktop` cannot run from a double-click path. **Not** currently mitigated: types the platform opens without an execute bit — `.webloc`, `.inetloc`, `.url`, `.terminal`, `.html` — behave as they would from Finder. Pressing `o` is a deliberate user action on a file the user chose to look at, and is treated as equivalent to opening the same attachment from any other client; a deny-list for those extensions is the obvious next step if that judgement changes. |

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
