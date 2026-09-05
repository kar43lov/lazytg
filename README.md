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
- 🧼 **Nothing a stranger sends can drive your terminal** — message text, chat titles, author names, filenames and search snippets are stripped of escape sequences and bidi overrides before they are drawn, so a filename cannot rewrite your clipboard or disguise a `.command` as a `.png`.
- ⌨️ **Emacs/readline keymap by default**, fully overridable through `keymap.toml` (vim-style modal bindings deferred to v0.2).
- 📥📤 **First-class file transfer** — a per-message cursor picks the attachment, `Ctrl+D` saves it, `o` (or a click on its badge) opens it in the system viewer, `Ctrl+U` attaches a file, all with progress in the status bar. Photos, videos, voice messages and round video messages are named and timed in the thread rather than shown as anonymous blobs.
- ✍️ **Act on messages, one or many** — `Space` marks, `y` copies, `e` rewrites your own, `d` deletes with a choice of "for me" or "for everyone", `f` forwards to a chat picked in the command palette. Marks make it a batch: mark four messages and one `d` removes all four.
- 🗂 **Your Telegram folders, as tabs** — the folders you already made on your phone narrow the chat list here, `[` and `]` walk between them, and `All` is always the first tab.
- 🔗 **`o` follows links and places too** — on a message with no file, `o` opens the link under the cursor in the browser (a hidden `[text](url)` first, then a marked or bare address), and a shared location opens as a map. Only `http` and `https` ever reach the browser: a message can name any scheme the desktop has a handler for, and none of the others is the sender's to fire. Locations, contacts, polls and dice show up as what they are rather than as blank rows — a poll with its options and tallies, a place with its venue — and a picture you attach goes out as a photo, so it draws in the chat on the other end instead of arriving as a file.
- 🖼 **Photos drawn in the thread** — `i` shows the picture inside the conversation on terminals that speak the Kitty graphics protocol (Ghostty, kitty, WezTerm). Everything else keeps the badge and `o`, which is the honest answer for video: no terminal plays one.
- 🎤 **Voice messages show their shape** — the waveform Telegram sends is drawn in block characters beside the duration, so you can see whether it is "ok" or two minutes of argument before spending the download.
- 📝 **A draft per chat** — what you were half-way through writing stays with the conversation it was written in, reply pointer and all, instead of following you into somebody else's.
- 📖 **"14 new messages" where you stopped reading** — opening a chat with a backlog draws a rule above the first message you have not seen, worked out before the chat is acknowledged and left where it is while you read.
- ↩️ **Follow a reply back** — `p` goes to the message the cursor is answering, `Ctrl+O` comes back, and the pair behaves like an editor's jumplist. Local when the parent is on the page, a window load from the mirror when it is not.
- ✏️ **"typing…" in the status line** — what the other side is doing right now, in Telegram's own words ("recording a voice message…"), for the chat you are reading. Received only: lazytg never announces your own typing.
- 📌 **The pinned message, and who a forward came from** — the newest pinned message in view sits in a bar under the thread's title and moves with `updatePinnedMessages`; a forwarded message carries `↪ forwarded from Name` above its words, the channel and the post's author for a channel post, the header's own name for somebody who hides their account.
- 🤖 **Bot keyboards you can press** — the buttons a bot puts under its message are drawn under it in the bot's own rows; on the message at the cursor `←` / `→` pick one and `Enter` presses it. A callback key goes to the bot (one `messages.getBotCallbackAnswer` per press, off the send guard) and what the bot says lands in the status line; a link key opens the browser (http/https only); a reply-keyboard key drops its text into the composer for you to send; a copy key goes to the clipboard. Payment, web-app, login and game keys are drawn and refused with a word about why. Most bots answer by editing the message, and the edit replaces the keyboard the moment it arrives.
- 🔎 **A chat that is not in the list yet** — type `@name` or a `t.me/name` address into the palette (Ctrl+Space) and Enter opens the person, channel or group behind the handle: one `contacts.resolveUsername` per name you typed, never for anything you did not. The chat lands in the list and, with a forward pending, is a forward target like any other. A handle nobody holds says so in the status bar.
- ✍️ **Drafts from your other devices** — a message half-typed on the phone is waiting in the composer here when you open the chat, formatting and all, and the chat list shows `Draft:` on its row the way every official client does. It comes with the dialog page and moves on `updateDraftMessage`; nothing is sent back — a draft typed here stays here, because saving one costs a request every time you pause, on an account already watched for running an unofficial client. A server draft never replaces words typed in this session.
- ✓✓ **Ticks on what you sent** — one when the server has it, two, in blue, once the other side has read it, on every outgoing message in the thread. The fact behind the second tick is one number per chat that arrives with the dialog page and moves on `updateReadHistoryOutbox`, so a message read on the phone across the room gets its second tick here without a request; the pointer only ever moves forward, because updates arrive in no particular order and a stale one must not take a tick away. Saved Messages draws none: nobody else reads what you write to yourself.
- 🅱️ **Formatting, both ways** — bold, italic, strikethrough, code, code blocks, spoilers and links behind words render the way Telegram's own clients draw them, with the host of a hidden link shown beside the words so "click here" cannot point somewhere it does not say. The composer takes Telegram Desktop's markup (`**bold**`, `__italic__`, `~~strike~~`, `||spoiler||`, `` `code` ``, ```` ```lang ```` blocks, `[text](url)`), and `e` hands a formatted message back to you as markup. A spoiler shows itself when the cursor is on its message.
- 📌 **Saved Messages from day one** — the chat with yourself is in the list whether or not you have written there yet, named the way every official client names it.
- 🔕 **The chat list knows what the phone knows** — the time of the last message on the right, the unread count, the by-hand unread dot, the bell of a muted chat, and a person's `online` / `last seen` in the status line; reading a chat on the phone clears its count here, and a mute or a pin made there shows here. From the list, `m` mutes, `p` pins, `u` marks read or unread — one request each, without opening the chat.
- 🔔 **The terminal bell and the tab title carry the badge** — a message in a chat you are not reading rings the bell (which Ghostty, tmux and iTerm each already turn into whatever you configured), muted chats stay silent, `LAZYTG_NOTIFY=off` silences it all; the terminal's title reads `lazytg (3)` while three are waiting, so a hidden tab still says so.
- 🧾 **Telegram's own lines are in the conversation** — "created the group", "pinned a message", "missed call, 2 minutes", "joined by invite link" — as rows with the person who did it as the sender, so a group that was only ever created no longer looks empty.
- ✏️ **Edits from your other devices land here** — a message rewritten on the phone is rewritten in the thread, in place, marked `edited`, without moving you or counting as new.
- 💬 **Reactions, both ways** — what people put on your messages shows under them with counts and yours boxed; `r` sets or clears your own through the emoji picker.
- 😀 **Emoji without leaving the keyboard** — type `:rocket` and press Tab, press it again to walk the other matches; `Alt+E` opens a picker with categories, search and what you used last.
- 🧭 **Command palette (`Ctrl+Space`)** with frecency-ranked chat switcher and Unicode-fuzzy matching ("Алёна" === "Алена").
- 🪶 **Pure-Go single binary**, no Electron, no CGo (sqlcipher build is opt-in, deferred for v0.1.x).
- 🔬 **Cosign-keyless signed releases** — every archive ships with a sigstore bundle; `cosign verify-blob` confirms provenance.
- 🛟 **Redacted `debug-bundle`** — single command produces an issue-ready tar.gz, verified in CI to never leak api_hash, session blobs, phone numbers, or message text.

## Status

**Alpha — release-candidate.** All four stages of the [v0.1.0 roadmap](docs/plans/lazytg-v0.1.0.md) have shipped (foundation, TUI, search/files/security, release pipeline). `runTUI` now opens the MTProto session before building the UI and syncs the dialog list, so the TUI reads live Telegram data; every failure path (no session, no network, revoked authorisation, connect timeout) degrades to the cached-only view rather than refusing to start. Five live runs against a real account have happened (18-19.08.2026 and 04.09.2026): the basic path, a connection cut with every interface down, two runs of ordinary use, and one over attachments. Every one of them found defects no test had — the last one found two attachments overwriting each other on disk, which no report would have surfaced because the symptom is the cache quietly serving the wrong file. See CHANGELOG `Known gaps` for what is still open and `docs/MANUAL_SMOKE.md` for the checklist those runs corrected.

Current capabilities:

- `lazytg login` — phone + code + 2FA authentication via [gotd/td](https://github.com/gotd/td); the session is persisted to an `age`-encrypted file, unlocked by a passphrase kept in the OS keyring (or typed at startup on headless boxes).
- `lazytg accounts` — list authenticated accounts (read-only, no Telegram round-trip).
- `lazytg logout --account <phone>` — drop a stored session.
- `lazytg version` — print version, commit, build date.
- `lazytg debug-bundle` — produces a redacted tar.gz with version, config, log tail, db stats, goroutine dump (`docs/SECURITY.md` + `bundle_grep_test.go`).
- `lazytg reindex --all|--chat <id>` — runs the FTS5 backfill for a chat or every chat with progress on stderr.
- 2-pane Bubble Tea TUI: chats + thread, focus cycling, optimistic send, $EDITOR delegation, live updates, reconnect orchestration.
- Two-way sync with your other devices: opening a chat marks it read on Telegram, and messages or chats deleted elsewhere disappear here too — including from the search index.
- Dialog sync on start (`messages.getDialogs`, paced and capped at 5 pages / 500 chats by design) plus history backfill when a chat is opened.
- Local search (FTS5 trigram) with operators `from:@user`, `in:#chat`, `before:`/`after:`, `has:file`, `"phrase"`, `-exclusion` (`docs/SEARCH.md`).
- Search overlay (`/`), command palette (Ctrl+Space), a message cursor with per-message download (Ctrl+D) and open-in-viewer (`o`), file upload (Ctrl+U).
- Message actions on the cursor or on a marked set: copy, edit your own, delete for yourself or for everyone, forward to another chat (`Space`, `y`, `e`, `d`, `f`).
- The account's Telegram folders as tabs over the chat list (`[` / `]`), including chat-list folders (shared links), which are matched by their explicit membership only.
- Typing indicators for the open chat, expiring on their own after six seconds because Telegram's "stopped" notification is not reliably sent.
- Reactions read from the messages themselves and kept current by `updateMessageReactions`; `r` sends yours (standard emoji only — premium custom reactions are a sticker, not a character).
- Emoji entry two ways: `:shortcode` completion on Tab in the composer, and an `Alt+E` picker over ~1100 characters with the GitHub/Slack aliases people actually type.
- Inline photos through the Kitty graphics protocol (`i`), auto-detected from the environment and overridable with `LAZYTG_IMAGE_PROTOCOL=kitty|none`.
- DB-size monitor + permissions audit + 10 msg/s send rate-limit guard (covers both text and media sends).

### Keybindings (TUI)

| Binding         | Action                                |
|-----------------|---------------------------------------|
| Tab / Shift+Tab | cycle focus between panes             |
| Ctrl+Tab / Ctrl+Shift+Tab | next / previous chat (also Alt+N / Alt+P) |
| Alt+1 … Alt+9   | open the nth chat in the list as shown |
| Enter           | send message                          |
| Alt+Enter       | newline in input (the composer grows to 4 rows) |
| Ctrl+R          | reply to focused message              |
| Ctrl+E          | open `$EDITOR` with current draft     |
| `/`             | open search overlay                   |
| Ctrl+Space      | command palette (chat switcher L1)    |
| ↑ / ↓ (k / j)   | move the message cursor in the thread |
| Space           | mark / unmark the message at the cursor |
| `y`             | copy the marked messages, or the one at the cursor |
| `←` / `→`, `Enter` | pick and press a key of the bot keyboard under the message at the cursor |
| `l`             | copy the link to the message at the cursor (channels and supergroups; a private chat has none) |
| `e`             | edit your own message at the cursor   |
| `d`             | delete the marked messages, or the one at the cursor |
| `f`             | forward them to another chat (picks the chat in the palette) |
| `r`             | react to the message at the cursor (opens the emoji picker) |
| `p`             | go to the message this one replies to  |
| Ctrl+O          | back to where you jumped from         |
| `i`             | draw the photo at the cursor inside the thread |
| `[` / `]`       | previous / next Telegram folder       |
| `m` / `p` / `u` (in the chat list) | mute or unmute, pin or unpin, mark read or unread |
| Tab (in the composer) | complete the `:shortcode` you are typing, again to cycle |
| Alt+E           | emoji picker                          |
| Ctrl+D          | save the attachment at the cursor     |
| `o`             | open the attachment at the cursor in the system viewer; on a message without a file, the link (or the map for a location) in the browser |
| Ctrl+U          | attach file (upload)                  |
| `?`             | toggle help overlay                   |
| Ctrl+C / Ctrl+Q | quit                                  |

The composer applies Telegram Desktop's markup on send and on edit — `**bold**`, `__italic__`, `~~strike~~`, `||spoiler||`, `` `code` ``, a ```` ``` ```` fence with an optional language on its first line, `[text](url)`. Nothing else, and no escapes: a marker with no partner is sent as typed.

### Mouse

| Gesture                  | Action                                      |
|--------------------------|---------------------------------------------|
| Left click on a chat row | focus the chat list and open that chat      |
| Left click on the thread | focus the thread and put the cursor on that message |
| Left click on an attachment badge | open that attachment in the system viewer |
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
- macOS notarization (needs a $99/yr Apple ID) — until then the cask strips the Gatekeeper quarantine attribute on install.

**v0.3+**

- tgql — query DSL with saved searches (smart folders).
- Inline keyboards for bots, pinned-message bar, forwarded-from header, link previews, the archive as a folder tab, `@` mention completion, forum topics — the ranked list, with everything Telegram Desktop does and where lazytg stands on each, is [`docs/FEATURE_PARITY.md`](docs/FEATURE_PARITY.md).
- Inline photos, forwarding and reactions shipped early, on the `feat/message-actions` branch; sixel/iTerm image protocols are still open.

**v0.5+** (only if a community shows up)

- Starlark hooks (`google/starlark-go`, pure Go).
- AI layer (Claude API + local Ollama, prompt caching over long history).
- CLI pipe mode — single process, explicitly not a daemon.

## Requirements

- Go ≥ 1.26.6 to build (the `go` directive in `go.mod`; older toolchains fetch it automatically under the default `GOTOOLCHAIN=auto`).
- On Linux: a running D-Bus session with a Secret Service provider (gnome-keyring, KWallet, etc.) so the keyring can hold the passphrase for you. Headless boxes prompt for it at startup instead.
- SQLite ≥ 3.34 is bundled by `modernc.org/sqlite` — no system SQLite required.
- Telegram API credentials — every build needs a pair, and **no lazytg build ships one, releases included**. Register an application at <https://my.telegram.org/apps> (one form, one minute) and export `LAZYTG_API_ID` / `LAZYTG_API_HASH`. A key compiled into a public binary is a published key — `strings` prints it — and Telegram blocks published keys permanently for every user of that build at once, which is why releases deliberately carry none. If that form will not issue an application for your number, which is common from some regions, [docs/INSTALL.md](docs/INSTALL.md#when-mytelegramorg-will-not-issue-credentials) lists what actually works. `lazytg version` prints which of the three sources is in effect, so "is it me or the build?" is one command.

## Quickstart

> **Releases are still alphas** (latest: [v0.1.0-alpha.2](https://github.com/kar43lov/lazytg/releases/tag/v0.1.0-alpha.2)),
> so signed archives, `.deb`, `.rpm` and `go install` work, while `brew` does not
> yet: the cask is published from stable tags only. Below are the two paths
> that need nothing but a terminal — build it yourself, or run a binary someone
> built for you; [docs/INSTALL.md](docs/INSTALL.md) covers the rest.

**Build it yourself** — needs Go ≥ 1.26.6 and takes about a minute:

```sh
git clone https://github.com/kar43lov/lazytg.git && cd lazytg
make build                             # → bin/lazytg

# Telegram API credentials: register an app once at https://my.telegram.org/apps
export LAZYTG_API_ID=1234567
export LAZYTG_API_HASH=0123456789abcdef0123456789abcdef

./bin/lazytg login --account +71234567890
# → Telegram sends a code (usually to the Telegram app on your other devices,
#   not by SMS — the prompt says which), then the 2FA password if you have one
./bin/lazytg                           # opens the TUI
```

**Got a binary from someone else** — no Go, no clone:

```sh
chmod +x lazytg
xattr -d com.apple.quarantine lazytg   # macOS only — Gatekeeper blocks it otherwise
./lazytg version                       # check the `api:` line first
./lazytg login --account +71234567890
```

The `api:` line tells you whether you still need credentials of your own:
`embedded` means they are compiled in and you need nothing, `none` means export
`LAZYTG_API_ID` / `LAZYTG_API_HASH` as above. Why macOS blocks an unsigned
binary, and what to do without a terminal, is in
[docs/INSTALL.md → A binary someone built for you](docs/INSTALL.md#a-binary-someone-built-for-you).

Handing a build to someone else is documented in
[Sharing a build](#sharing-a-build); every install path that becomes available
once a release is tagged is in [docs/INSTALL.md](docs/INSTALL.md).

## Install

### From source

```sh
git clone https://github.com/kar43lov/lazytg.git
cd lazytg
make build          # → bin/lazytg
```

`make build` is `go build -o bin/lazytg ./cmd/lazytg`; nothing else is needed,
because the SQLite driver is pure Go (`modernc.org/sqlite`) and `CGO_ENABLED=0`
builds work everywhere. A source build carries no API credentials — register an
application at <https://my.telegram.org/apps> and export `LAZYTG_API_ID` /
`LAZYTG_API_HASH` before logging in. `lazytg version` reports which credential
source is active.

`go install github.com/kar43lov/lazytg/cmd/lazytg@latest` starts working once a
release is tagged; today it has no version to resolve.

### Pre-built binaries

[v0.1.0-alpha.2](https://github.com/kar43lov/lazytg/releases/tag/v0.1.0-alpha.2)
is on the releases page: archives for `linux` and `darwin` × `amd64`/`arm64`,
`.deb` and `.rpm`, SHA256 checksums, and cosign keyless signatures (a sigstore
bundle per archive plus a signed `checksums.txt`). Verification recipes are in
[docs/VERIFY.md](docs/VERIFY.md).

Alpha means what it says: the client behind it has had five live sessions, and
every one of them found something the test suite did not. Homebrew is not
served from these builds — the cask goes to the tap on stable tags only.

### Encrypted database (deferred past v0.1)

A `sqlcipher` build tag is reserved for the CGo-backed encrypted driver and is **not yet wired** — CGo SQLCipher integration is deferred past v0.1. The database is unencrypted regardless of build tag — rely on filesystem permissions (`0600` files / `0700` dirs, enforced by the startup permissions audit) and OS-level disk encryption.

## Sharing a build

lazytg is pure Go with no cgo, so one machine can build a binary for any other
and the result is a single self-contained file — no runtime, no shared
libraries, ~21 MB. Before the first release exists, this is how you give it to
someone:

```sh
# 1. build for their platform — `uname -sm` on their machine says which
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o lazytg ./cmd/lazytg
#   Darwin arm64 → darwin/arm64 · Darwin x86_64 → darwin/amd64
#   Linux x86_64 → linux/amd64  · Linux aarch64 → linux/arm64   (no Windows target)

# 2. send the file, and tell them two things:
#    · macOS refuses it on first run — `xattr -d com.apple.quarantine lazytg`
#    · register an app at https://my.telegram.org/apps for their own credentials
```

Credentials can be compiled in instead, so the recipient needs no setup, and
that is the right call for a build going to one person you trust. It is
deliberately *not* what public releases do: everyone running such a binary
shares one `api_id` with you, and anyone can read it back out with `strings`,
which for a public download means a published key. The full
recipe, both trade-offs, and the macOS quarantine story are in
[docs/INSTALL.md → Building for someone else](docs/INSTALL.md#building-for-someone-else).

Use a test account first, whoever you hand this to: Telegram puts unofficial
clients under observation ([docs/SECURITY.md](docs/SECURITY.md)).

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
- `--api-id` / `--api-hash` — override the API credentials for one run (takes precedence over the env vars and over anything compiled into the binary; `--api-hash` is visible in `ps`, so prefer `LAZYTG_API_HASH`).
- `--debug` — duplicates JSON logs to stderr in addition to the rotated file sink.
- `--log-level debug|info|warn|error` (default `info`).
- `--polling` — poll the 3 most recently active chats every 3 s for messages the push path may have dropped. A net under live updates, not a replacement; off by default (constant background traffic).

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

- [docs/INSTALL.md](docs/INSTALL.md) — what works today (build from source, a binary someone built for you) and every path that opens up once a release is tagged (brew, .deb, .rpm, manual archive, `go install`), plus credentials setup and how to build for someone else's machine.
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

1. **Homebrew tap repository.** Create `kar43lov/homebrew-lazytg` on GitHub manually; GoReleaser creates `Casks/` itself and commits the generated `Casks/lazytg.rb` there on every stable release. (The repo currently holds an empty `Formula/` from the pre-cask setup — vestigial.)
2. **PAT for tap pushes.** Generate a Personal Access Token (fine-grained) with `contents: write` on the `kar43lov/homebrew-lazytg` repo. Add it to this repo's secrets as `HOMEBREW_TAP_GITHUB_TOKEN`. The token is **not needed** until the first stable tag — alpha/beta/rc skip the brew upload.
3. **First publish.** The first `git tag v0.1.0 && git push --tags` (without `-alpha`/`-beta`/`-rc` suffix) pushes the cask automatically. From that point on `brew install kar43lov/lazytg/lazytg` works — on macOS: Homebrew on Linux does not install casks, so Linux stays on the archive, `.deb` or `.rpm`.

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
