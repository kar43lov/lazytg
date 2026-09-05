# Configuration

lazytg v0.1.x is configured through a small surface: CLI flags, environment
variables, and one optional file (`keymap.toml`). A general `config.toml`
file is on the v0.2 roadmap — the `--config` flag reserves the namespace
but is currently a no-op (see [Reserved options](#reserved-options) below).

This page documents what actually exists in v0.1.

---

## File locations (XDG)

Resolved by [`internal/core/config/paths.go`](../internal/core/config/paths.go).
Directories are created with mode `0700`; the startup permissions audit
re-validates them.

| Purpose       | Linux                         | macOS                                                    |
|---------------|-------------------------------|----------------------------------------------------------|
| Config        | `$XDG_CONFIG_HOME/lazytg/`    | `~/Library/Application Support/lazytg/`                  |
| Data (SQLite) | `$XDG_DATA_HOME/lazytg/`      | `~/Library/Application Support/lazytg/`                  |
| State (logs)  | `$XDG_STATE_HOME/lazytg/`     | `~/Library/Application Support/lazytg/`                  |
| Cache         | `$XDG_CACHE_HOME/lazytg/`     | `~/Library/Caches/lazytg/`                               |

If your distro does not set `XDG_*_HOME`, the defaults are
`~/.config/lazytg`, `~/.local/share/lazytg`, `~/.local/state/lazytg`,
`~/.cache/lazytg`. You can override any of them by exporting the
corresponding env var before launching lazytg.

Files that may exist inside these directories:

- `<config>/keymap.toml` — optional keymap overrides (this page).
- `<config>/secrets.age` — where session blobs live (the keyring holds only its passphrase);
  encrypted with [age](https://age-encryption.org). The passphrase is generated
  once and read from the OS keyring; only a box without a keyring prompts for
  it at startup. Mode `0600` (audited).
- `<config>/secrets.age.lock` — empty sidecar used for the cross-process write
  lock (`flock`). Safe to delete while lazytg is not running; it is recreated on
  the next write.
- `<data>/lazytg.db` — SQLite database (messages, FTS5 index, accounts,
  reindex state). Mode `0600` (audited).
- `<data>/lazytg.db-wal`, `<data>/lazytg.db-shm` — SQLite WAL companions.
- `<state>/lazytg.log` — JSON-Lines log file with lumberjack rotation
  (10 MB × 3 backups × 30 days). Phone numbers, session blobs, and
  `api_hash` strings are scrubbed before write.

---

## Environment variables

| Variable | Effect |
|----------|--------|
| `LAZYTG_NOTIFY` | `off` silences the terminal bell that rings for a message in a chat you are not reading. Anything else, or unset, leaves it on. Muted chats never ring. |


| Variable          | Purpose                                                                                                | Default / required           |
|-------------------|--------------------------------------------------------------------------------------------------------|------------------------------|
| `LAZYTG_API_ID`   | Telegram MTProto API ID. Get from <https://my.telegram.org/apps>.                                       | **required** — no lazytg build ships one, releases included |
| `LAZYTG_API_HASH` | Telegram MTProto API hash (32 hex chars).                                                              | **required** — unless the binary was built with a pair compiled in |
| `LAZYTG_DOWNLOADS`| Root directory for `Ctrl+D` file downloads. The chat title becomes a sub-folder; the filename is sanitised. | `~/Downloads/lazytg/`  |
| `EDITOR`          | Command launched by `Ctrl+E` to compose long messages. Inherits the rest of the env (sandbox is v0.2). | `vi` if unset                |
| `XDG_CONFIG_HOME` | Override for the config directory base.                                                                 | platform default             |
| `XDG_DATA_HOME`   | Override for the data directory base.                                                                  | platform default             |
| `XDG_STATE_HOME`  | Override for the state directory base.                                                                 | platform default             |
| `XDG_CACHE_HOME`  | Override for the cache directory base.                                                                 | platform default             |

### API credentials: three sources

`internal/tg/client.go::ResolveCredentials` checks three layers on every
command that opens a Telegram session (`login`, the TUI). `accounts` does not
need them; `version` reports them without requiring them.

| Precedence | Source     | Set by                                                                 |
|------------|------------|------------------------------------------------------------------------|
| 1          | `flags`    | `--api-id` / `--api-hash`                                              |
| 2          | `env`      | `LAZYTG_API_ID` / `LAZYTG_API_HASH`                                    |
| 3          | `embedded` | `-ldflags` injection at build time. Public releases deliberately leave this empty (see [SECURITY.md](SECURITY.md#credentials-why-releases-ship-without-them)), so in practice a binary has this layer only when someone built it for you |

Rules that follow from this:

- **Both halves or neither.** Setting only `LAZYTG_API_ID` is an error, not a
  fall-through to the next layer. A silent fall-through would let you believe
  you are running on your own credentials when you are not — a difference that
  only becomes visible as an unexplained ban.
- **Source builds have no layer 3.** The repository carries no credentials
  (`API_ID_PUBLISHED_FLOOD` is permanent), so `make build` output requires
  layer 1 or 2. The startup error says exactly this.
- **`--api-hash` leaks into `ps`.** It exists for scripted one-offs; the env
  var is the normal path.

Check what is in effect with `lazytg version`:

```
api:    env (build embeds credentials: yes)
```

The value is the source name, never the credentials. Release binaries report
`none` — that is the intended state, not a misconfiguration — and ask for the
env vars.

---

## CLI flags

### Persistent flags (apply to every subcommand)

| Flag                             | Effect                                                                                            |
|----------------------------------|---------------------------------------------------------------------------------------------------|
| `--account <phone>`              | E.164 phone of the account to operate on (`+79991112233`). Selects the session for multi-account. |
| `--debug`                        | Duplicate JSON logs to stderr. Otherwise logs go only to the rotated state-dir file.              |
| `--log-level debug\|info\|warn\|error` | Log verbosity. Default `info`.                                                                |
| `--polling`                      | Poll the 3 most recently active chats every 3 s for messages the live update path may have dropped. Runs *in addition* to push updates, not instead of them; off by default because it is constant background traffic. |
| `--config <path>`                | **Reserved** — no-op in v0.1, kept so existing scripts do not break when `config.toml` lands in v0.2. |

### Subcommand flags

| Subcommand        | Flags                                                                                |
|-------------------|--------------------------------------------------------------------------------------|
| `lazytg login`    | (no extra flags) — uses `--account` from persistent set.                             |
| `lazytg logout`   | (no extra flags) — `--account` is required.                                          |
| `lazytg accounts` | (no extra flags) — read-only, no Telegram round-trip.                                |
| `lazytg reindex`  | `--all` (every chat) or `--chat <id>` (single chat by Telegram peer id).             |
| `lazytg debug-bundle` | `--out <path>` — destination tar.gz. Default: `./lazytg-bundle-<timestamp>.tar.gz`. |
| `lazytg version`  | (no extra flags) — prints `version`, `commit`, `build_date`.                          |

Run `lazytg <command> --help` for the live, generated reference; the table
above is just a map.

---

## Keymap (`keymap.toml`)

The TUI keymap is the only file-based override available today. lazytg
loads `<config>/keymap.toml` at startup. A missing file falls back to
defaults silently; a malformed file aborts the boot with an explicit
error so a typo cannot quietly mask a binding.

### Defaults

The defaults follow the emacs/readline tradition. Vim-style modal
bindings are deliberately deferred to v0.2 — see
[CLAUDE.md → UI/UX-решения](../CLAUDE.md#uiux-решения-после-dialectic-анализа)
for the rationale.

| TOML key          | Default chord(s)          | Action                                              |
|-------------------|---------------------------|-----------------------------------------------------|
| `send`            | `enter`                   | Send the current draft                              |
| `newline`         | `alt+enter`               | Insert a newline in the input                       |
| `reply`           | `ctrl+r`                  | Reply to the message at the cursor                  |
| `open_editor`     | `ctrl+e`                  | Open `$EDITOR` with the draft                       |
| `toggle_help`     | `?`                       | Toggle the help overlay                             |
| `focus_next`      | `tab`                     | Cycle focus to the next pane                        |
| `focus_prev`      | `shift+tab`               | Cycle focus to the previous pane                    |
| `next_chat`       | `ctrl+tab`, `alt+n`       | Open the next chat in the list                      |
| `prev_chat`       | `ctrl+shift+tab`, `alt+p` | Open the previous chat                              |
| `scroll_up`       | `ctrl+b`, `pgup`          | Scroll the focused pane up                          |
| `scroll_down`     | `ctrl+f`, `pgdown`        | Scroll the focused pane down                        |
| `search`          | `/`                       | Open the search overlay                             |
| `open_palette`    | `ctrl+space`, `ctrl+@`    | Open the command palette                            |
| `download`        | `ctrl+d`                  | Save the attachment at the cursor                   |
| `open_media`      | `o`                       | Open the attachment at the cursor in the system viewer |
| `show_image`      | `i`                       | Draw the photo at the cursor inside the thread      |
| `attach`          | `ctrl+u`                  | Attach (upload) a file                              |
| `mark_message`    | `space`                   | Mark / unmark the message at the cursor             |
| `copy_message`    | `y`                       | Copy the marked messages, or the one at the cursor  |
| `press_button`    | `enter`                   | Press the chosen key of the bot keyboard under the message at the cursor (`←`/`→` pick it) |
| `copy_link`       | `l`                       | Copy the link to the message at the cursor (`t.me/<username>/<id>` or `t.me/c/<id>/<id>`; none for a private chat or a basic group) |
| `edit_message`    | `e`                       | Edit your own message at the cursor                 |
| `delete_msg`      | `d`                       | Delete the marked messages, or the one at the cursor |
| `forward_message` | `f`                       | Forward them to a chat picked in the palette        |
| `react_message`   | `r`                       | React to the message at the cursor                  |
| `jump_to_reply`   | `p`                       | Go to the message this one replies to               |
| `jump_back`       | `ctrl+o`                  | Back to where you jumped from                       |
| `next_folder`     | `]`                       | Next Telegram folder                                |
| `prev_folder`     | `[`                       | Previous Telegram folder                            |
| `emoji_picker`    | `alt+e`                   | Open the emoji picker                               |
| `complete_emoji`  | `tab`                     | In the composer: complete the `:shortcode` under the cursor |
| `mute_chat`       | `m`                       | In the chat list: mute / unmute the highlighted chat |
| `pin_chat`        | `p`                       | In the chat list: pin / unpin it                    |
| `toggle_unread`   | `u`                       | In the chat list: mark it read / unread             |
| `quit`            | `ctrl+c`, `ctrl+q`        | Quit                                                |

Two keys answer to two actions on purpose, told apart by which pane has the
focus: `tab` completes an emoji shortcode in the composer and cycles focus
everywhere else; `p` pins in the chat list and follows a reply in the thread.
Rebinding one of a pair leaves the other alone.

Each key may be a single chord (`ctrl+r`) or any value `parseKey` recognises;
see `internal/ui/keymap/parse.go` for the named-key dictionary.

`next_chat` / `prev_chat` work from any pane, the composer included, and wrap
around the list in the order it is displayed (pinned first, then by last
message). Note that `ctrl+tab` only reaches an application in terminals that
implement key disambiguation — the Kitty keyboard protocol, supported by
Ghostty, kitty, WezTerm and recent iTerm2. Elsewhere the terminal sends a plain
`tab`, which cycles focus instead; that is why `alt+n` / `alt+p` are bound as
portable aliases, and either can be rebound below.

### Overriding bindings

Create `<config>/keymap.toml` with a `[bindings]` table. Only the keys you
override need to appear; everything else stays on the default.

```toml
[bindings]
# Swap reply and open_editor
reply       = "ctrl+e"
open_editor = "ctrl+r"

# Move the palette off Ctrl+Space (which some terminal multiplexers grab)
open_palette = "ctrl+p"

# Use Esc as quit
quit = "esc"

# Chat cycling on a terminal that swallows ctrl+tab
next_chat = "ctrl+n"
prev_chat = "ctrl+p"
```

Behaviour:

- Unknown binding names (typos like `revply`) abort startup with an error
  listing the allowed names.
- Unparseable chords (`ctrl+wat`) abort startup with the offending value.
- Conflicts between bindings (two actions sharing a chord) abort startup
  with a multi-line conflict report.
- Help-overlay descriptions are preserved verbatim — overriding `reply`
  to `ctrl+e` keeps the overlay text "reply" next to the new chord.

The file is hot-checked only at startup; restart lazytg to pick up edits.

---

## Multi-account

lazytg already stores multiple accounts in the same SQLite database
(`accounts` table). Switching is done with the persistent `--account`
flag:

```sh
lazytg login --account +71234567890
lazytg login --account +71234567891

lazytg accounts
# → +71234567890   (active)
# → +71234567891   (active)

lazytg --account +71234567891          # opens TUI for the second account
```

A dedicated UI switcher (Ctrl+1 / Ctrl+2 inside the TUI) is on the v0.2
roadmap. Until then, attach the `--account` flag every time you need to
operate on a non-default phone.

`lazytg logout --account <phone>` drops the session blob from `secrets.age`
(or the age-encrypted file) without touching the cached database — your
locally indexed messages survive logout, only the auth state is purged.

---

## Reserved options

`--config <path>` parses cleanly today but does nothing. The plan is to
introduce a TOML schema in v0.2 covering the knobs currently hard-coded:

- `[logging]` — level, debug, log file path override.
- `[storage]` — SQLite file path, WAL tuning.
- `[search]` — `max_messages_per_chat` (currently
  `search.DefaultPerChatLimit`, hard-coded), trigram options.
- `[updates]` — polling interval (`internal/tg/polling.go`
  `defaultPollingInterval = 3 * time.Second`).
- `[security]` — `send_rate_limit` (currently
  `security.DefaultSendRate = 10/sec`, `security.DefaultSendBurst = 30`),
  reduceable but not increasable.
- `[downloads]` — root path (already overridable via `LAZYTG_DOWNLOADS`).
- `[editor]` — env-filter list (currently the editor inherits the full
  env; sandbox is v0.2).

The fields exist as Go-level constants today; expect them to surface as
TOML keys in the v0.2 release.

---

## Related docs

- [`INSTALL.md`](INSTALL.md) — installing the binary.
- [`SEARCH.md`](SEARCH.md) — query syntax for the local FTS5 index.
- [`FILES.md`](FILES.md) — download/upload pipeline (`Ctrl+D` / `Ctrl+U`).
- [`SECURITY.md`](SECURITY.md) — threat model, redaction rules, ban-risk policy.
- [`TROUBLESHOOTING.md`](TROUBLESHOOTING.md) — common errors and recovery steps.
