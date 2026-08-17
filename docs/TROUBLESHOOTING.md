# Troubleshooting

Common failure modes and how to recover. Format throughout: **Symptom →
Diagnosis → Fix.** If you hit something not listed here, [collect a
debug-bundle](#collecting-a-debug-bundle) and open an issue.

---

## Authentication & login

### `FLOOD_WAIT_<N>` during login

**Symptom.** `lazytg login` aborts with an error mentioning `FLOOD_WAIT`
and a number of seconds.

**Diagnosis.** Telegram's anti-abuse layer is rate-limiting your IP/account
for repeated authentication attempts. Common triggers: too many login
retries in a short window, multiple devices logging in simultaneously,
or a previous login attempt that aged out without finishing.

**Fix.**

1. Wait the indicated number of seconds (`gotd/td` decodes the value out of
   the error). lazytg does not auto-retry login flows — it would risk
   compounding the wait.
2. Try again from the same IP. Switching networks resets the
   per-connection bucket but Telegram tracks per-account too.
3. If the wait grows on every attempt rather than shrinking, your account
   may already be under observation — see [SECURITY.md → Ban-risk
   warning](SECURITY.md#ban-risk-warning) and **stop using lazytg for that
   account** until the wait clears for at least 24 h.

---

### "no Telegram API credentials available"

**Symptom.** Any subcommand that opens a Telegram session refuses to start.

**Diagnosis.** This binary was built from source. Release binaries carry
credentials injected at build time; the repository does not contain them,
because an `api_id` found in public source is blocked by Telegram forever.
Confirm with:

```sh
lazytg version | grep api      # → api:    none (no credentials — …)
```

**Fix.** Either install a release binary, or register your own application:

```sh
export LAZYTG_API_ID=...        # from https://my.telegram.org/apps
export LAZYTG_API_HASH=...
```

Add them to your shell rc so future sessions inherit them. The `accounts`,
`version`, and `debug-bundle` subcommands do **not** need the credentials —
only `login` and the TUI do.

---

### "misconfigured: LAZYTG_API_ID is set but LAZYTG_API_HASH is empty"

**Symptom.** `lazytg version` reports `misconfigured: …`; login refuses to
start even though the binary is a release build with embedded credentials.

**Diagnosis.** Deliberate. Credential layers are all-or-nothing: setting one
half never falls through to the next layer, because silently running on the
embedded key while you believe you are on your own is a difference that shows
up only as an unexplained ban weeks later.

**Fix.** Set both halves, or unset both to fall back to the embedded key:

```sh
unset LAZYTG_API_ID LAZYTG_API_HASH   # → back to the embedded credentials
```

---

### `API_ID_PUBLISHED_FLOOD` during login

**Symptom.** Login fails with `API_ID_PUBLISHED_FLOOD`, followed by
instructions pointing at my.telegram.org.

**Diagnosis.** Telegram has seen this `api_id` in public source and refuses
end-user logins with it, permanently. Check whose key you are on:

```sh
lazytg version | grep api
```

`embedded` means the shipped release key burned — every release user is
affected at once, and it needs a maintainer-side rotation ([open an
issue](https://github.com/kar43lov/lazytg/issues)). Anything else means the
credentials you supplied have been published somewhere.

**Fix (either case, immediate).** Register your own application at
<https://my.telegram.org/apps> and export the pair — that bypasses the
embedded key without reinstalling:

```sh
export LAZYTG_API_ID=...
export LAZYTG_API_HASH=...
```

---

### `API_ID_INVALID` during login

**Symptom.** Login fails immediately with `API_ID_INVALID`.

**Diagnosis.** The `api_id` and `api_hash` do not belong to the same
application — the most common cause is copying the hash from one app and the
id from another, or a truncated paste (`api_hash` is exactly 32 hex chars).

**Fix.** Re-copy both values from the same application block at
<https://my.telegram.org/apps>.

---

### 2FA password prompt rejects a correct password

**Symptom.** `2FA password:` prompt accepts your input, then login fails
with `PASSWORD_HASH_INVALID`.

**Diagnosis.** Most often a Unicode normalisation mismatch (one terminal
sends precomposed characters, the password was originally typed
decomposed, or vice versa). lazytg deliberately does not strip
whitespace from the prompt — Telegram allows leading/trailing whitespace
in 2FA passwords.

**Fix.**

1. Reset your 2FA password from the official Telegram client to a value
   that contains only ASCII characters and no leading/trailing spaces.
2. Re-run `lazytg login --account +<phone>`.
3. If the issue persists with an ASCII password, capture a
   `--log-level debug --debug` log and open an issue (the redactor will
   strip the password before write).

---

## Storage & permissions

### "permissions audit failed: …" / process aborts at startup

**Symptom.** TUI or `lazytg reindex` exits with an error like
`secrets.age has mode 0644, want 0600` or `lazytg.db has mode 0640,
want 0600`.

**Diagnosis.** The startup permissions audit
(`internal/core/security/permissions.go`) refuses to open a session
when sensitive files are world- or group-readable. This is intentional —
it catches an attacker (or backup tool) widening permissions and
prevents lazytg from operating on compromised state.

**Fix.**

```sh
chmod 0600 ~/.local/share/lazytg/lazytg.db          # Linux data dir
chmod 0600 ~/.config/lazytg/secrets.age 2>/dev/null  # only if it exists
chmod 0700 ~/.config/lazytg ~/.local/share/lazytg ~/.local/state/lazytg
```

On macOS the equivalent paths are `~/Library/Application Support/lazytg/`
for both data and config.

If you intentionally need the file readable by a sibling tool (e.g., a
shared backup process), the right answer is to keep the audit happy and
copy the file out of band, not to relax the audit.

---

### "the passphrase does not open this file" at startup

**Symptom.** Every command that touches secrets fails with
`age file store: decrypt "…/secrets.age": … — the passphrase does not
open this file`.

**Diagnosis.** Sessions live in `secrets.age`, encrypted with a random
passphrase that lazytg generated once and keeps in the OS keyring under
`age-passphrase` (service `lazytg`). The file and the keyring entry have
drifted apart: the entry was deleted from Keychain Access / `secret-tool`,
overwritten by another tool, or the file was copied in from a machine with
a different passphrase.

lazytg deliberately does **not** re-encrypt the file under the passphrase
it currently has — that would silently destroy the only copy of your
sessions. Nothing is lost while you investigate.

**Fix.**

1. If the entry was moved rather than lost, restore it and retry:

   ```sh
   security find-generic-password -s lazytg -a age-passphrase -w   # macOS
   secret-tool lookup service lazytg username age-passphrase       # Linux
   ```

   Restoring the original passphrase makes the file readable again.
2. If the passphrase is genuinely gone, the sessions are unrecoverable —
   scrypt has no backdoor. Delete the file and log in again; the local
   message index in `lazytg.db` is untouched:

   ```sh
   rm ~/.config/lazytg/secrets.age        # macOS: ~/Library/Application Support/lazytg/
   lazytg login --account +79991112233
   ```

   Telegram will still list the old device under Settings → Devices. Revoke
   it there: lazytg cannot, having lost the session it would need to do so.

---

### "DB locked" error from `lazytg reindex` or the TUI

**Symptom.** A subcommand fails with `database is locked`.

**Diagnosis.** SQLite is single-writer. Another `lazytg` process
(another TUI, a forgotten `reindex --all` running in `tmux`, or a
crashed instance whose WAL is still in place) holds the writer lock.

**Fix.**

1. `pgrep -af lazytg` — find every running lazytg process. Stop the one
   you don't need (`kill <pid>`; do **not** start with SIGKILL, lazytg
   flushes WAL on graceful shutdown).
2. If no lazytg processes are running but the lock persists, the WAL
   companion files are stale. Close all consumers and delete:

   ```sh
   rm ~/.local/share/lazytg/lazytg.db-wal
   rm ~/.local/share/lazytg/lazytg.db-shm
   ```

   The next launch reopens the DB, replays nothing (WAL was orphaned),
   and writes a fresh WAL pair.
3. If you regularly hit this, you might be running a TUI and a CLI
   subcommand in parallel — that is unsupported. Run reindex jobs while
   the TUI is closed.

---

### Status bar shows `⚠ DB N.N GB`

**Symptom.** The TUI status bar shows a warning about database size.

**Diagnosis.** `obs.DBSizeMonitor` emits a `db_size_warning` event when
the SQLite file grows past 1 GB. The trigram FTS5 index is overhead-heavy
(3–5× the underlying message text), so this is informational, not
fatal — see [PERFORMANCE.md → DB size guidance](PERFORMANCE.md#db-size-guidance).

**Fix.** Decide whether you actually need the deep history:

- Lower the per-chat backfill cap (currently
  `search.DefaultPerChatLimit = 5000`, configurable in v0.2 via
  `config.toml`). Until then, edit and rebuild from source.
- Or wipe the DB and start fresh: stop lazytg, `rm
  ~/.local/share/lazytg/lazytg.db*`, log in again — the indexer
  re-fetches only the recent window.

---

## TUI & rendering

### TUI looks broken: no colours, missing borders, garbled glyphs

**Symptom.** Pane borders render as `qxqq`, statusbar shows escape
sequences, colours are off.

**Diagnosis.** Your terminal does not advertise (or does not implement)
true-colour + Unicode. Bubble Tea v2 expects at least a 256-colour
terminal with UTF-8 locale.

**Fix.**

```sh
echo $TERM        # want xterm-256color, alacritty, screen-256color, tmux-256color, …
echo $LANG        # want a UTF-8 locale, e.g. en_US.UTF-8
locale            # double-check the LC_* family
```

Common remedies:

- Inside `tmux`: add `set -g default-terminal "tmux-256color"` and
  `set -ag terminal-overrides ",alacritty:RGB"` (or the matching
  `:Tc` flag) to your `tmux.conf`.
- Over SSH: forward `LANG`/`LC_*` (`SendEnv LANG LC_*` in
  `~/.ssh/config`) or set them on the remote box.
- On terminals that lack 24-bit colour support, switch to one that does
  (Alacritty, iTerm2, Ghostty, WezTerm, Kitty all qualify).

---

### Ctrl+Space does nothing (cannot open the command palette)

**Symptom.** The default `Ctrl+Space` binding for `open_palette` is
inert.

**Diagnosis.** Some terminal multiplexers and IMEs swallow `Ctrl+Space`
before it reaches lazytg. tmux, for example, uses it for some buffer
operations under certain configs. Older terminals report the chord as
the NUL byte (`Ctrl+@`) — lazytg already binds both spellings, but only
if your terminal sends one of them.

**Fix.** Override the binding in `~/.config/lazytg/keymap.toml`:

```toml
[bindings]
open_palette = "ctrl+p"        # or any other free chord
```

If you use Ctrl+P for something else (Telegram Desktop also uses it for
chat search), pick a binding that does not collide with your shell or
multiplexer.

---

## Search

### Search returns nothing for messages you know exist

**Symptom.** `/<query>` opens the overlay but no rows render, even though
the messages are visible in the thread pane.

**Diagnosis.** Most often the FTS5 index has not been backfilled for
that chat. lazytg lazy-indexes only the most recent
`search.DefaultPerChatLimit = 5000` messages per chat on first poll —
older messages need an explicit reindex.

**Fix.**

```sh
lazytg reindex --all          # everything; slow on heavy DBs
# or
lazytg reindex --chat <id>    # one chat only; the chat id appears in the TUI
```

Verify the index is populated:

```sh
lazytg debug-bundle
tar -xzf lazytg-bundle-*.tar.gz db_stats.txt
cat db_stats.txt              # look for messages_fts row count
```

If the row count is zero after a reindex, capture a `--log-level debug`
log of the reindex run and open an issue.

---

### Query operator returns no matches even though the operand is right

**Symptom.** `from:@alice hello` finds nothing, but messages from `@alice`
containing `hello` are visibly in the thread.

**Diagnosis.** The `from:` operator matches on the stored `username`
(without the leading `@`), not the display name. Telegram users who set
themselves as "Alice" but never picked a `@username` will not match
`from:@…`. Same for `in:#chatname` against private chats.

**Fix.** Confirm the username via the TUI status bar (open the chat,
the title shows `@alice`) before forming the query, or use a free-text
fragment instead.

---

## Files (download / upload)

### `Ctrl+D` does nothing on a message that has a photo

**Symptom.** Press `Ctrl+D` on a focused message with a media attachment;
no progress appears in the status bar.

**Diagnosis.** The download path requires:

- An attached MTProto session (the binary running with a real Telegram
  connection — NOT a cache-only TUI).
- A media kind lazytg recognises (photos, documents, voice notes).
  Inline link previews are not "files".

**Fix.** Confirm in the status bar that lazytg is online (no
`⚠ offline` indicator). Verify the message actually has a downloadable
attachment by re-opening the chat in Telegram Desktop. If both check
out, capture a `--log-level debug` log around the keypress and open an
issue.

---

### Downloads land in an unexpected directory

**Symptom.** `Ctrl+D` succeeds but the file ends up under
`~/Downloads/lazytg/...` instead of the path you expected.

**Diagnosis.** The default root is `~/Downloads/lazytg/<chat>/<filename>`.
Override with:

```sh
LAZYTG_DOWNLOADS=/tmp/lazytg-downloads lazytg
```

Per-chat sub-directories use the chat title (sanitised — slashes and
control characters stripped). See [`FILES.md`](FILES.md).

---

## Account / ban-risk

### "Account banned" or all calls fail with `USER_DEACTIVATED_BAN`

**Symptom.** Every Telegram call fails immediately with a deactivation
error after working fine yesterday.

**Diagnosis.** Telegram restricted or terminated the account. Usually
the result of: too many automated-looking actions, mass message sends,
shared/leaked `api_hash`, or just being unlucky after the post-2024
enforcement uptick.

**Fix.** lazytg cannot help recover the account — that is between you
and Telegram.

1. Email `recover@telegram.org` from the email associated with the
   account, describing the use case ("personal-use TUI client for
   reading my own messages"). Honest framing helps; references to
   bots/scrapers do not.
2. **Stop using a potentially-flagged primary account with lazytg.**
   See [SECURITY.md → Ban-risk warning](SECURITY.md#ban-risk-warning)
   for the project's stance and the built-in mitigations (rate-limit
   guard, no scraping patterns).
3. If you continue, switch to a secondary throwaway test account and
   rebuild trust with low-volume manual usage.

---

## Collecting a debug-bundle

When opening an issue, attach a `lazytg debug-bundle` artifact. The
bundle is a tar.gz with the following entries (everything redacted —
session blobs, `api_hash`, and message text are removed before write,
verified by `internal/core/obs/bundle_grep_test.go` in CI):

```
version.txt        # binary version, commit, build date
config.toml        # config file as it sits on disk (placeholder if absent)
logs.txt           # tail of the rotated log file, redactor applied
db_stats.txt       # per-table row counts (no message text)
goroutines.txt     # full goroutine dump
```

Generate it:

```sh
lazytg debug-bundle                     # writes ./lazytg-bundle-<timestamp>.tar.gz
lazytg debug-bundle --out /tmp/x.tar.gz # custom path
```

The bundle file itself is mode `0600`. **Confirm before attaching:**

```sh
tar -tzvf lazytg-bundle-*.tar.gz                # list entries
tar -xzf lazytg-bundle-*.tar.gz                 # extract
# eyeball logs.txt — there should be no api_hash hex / phone numbers
```

If your bundle exceeds GitHub's attachment limit (≈25 MB), upload the
file to a private gist and link it in the issue.

---

## Related docs

- [`INSTALL.md`](INSTALL.md) — installation paths and one-time setup.
- [`CONFIGURATION.md`](CONFIGURATION.md) — env vars, keymap, multi-account.
- [`SECURITY.md`](SECURITY.md) — threat model and ban-risk policy.
- [`PERFORMANCE.md`](PERFORMANCE.md) — memory budgets, search SLA, DB size guidance.
- [`SEARCH.md`](SEARCH.md) — query syntax and FTS5 internals.
