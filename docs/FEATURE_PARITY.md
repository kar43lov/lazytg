# Feature parity: Telegram Desktop vs lazytg

An inventory of what Telegram Desktop (TD) does, grouped the way its
settings and menus group it, against what lazytg does on the
`feat/message-actions` branch as of 2026-09-05. The point is to see the
whole surface at once: what is done, what is half done, what is next, and
what is out on purpose.

Legend: ✅ done · 🟡 partial · ⬜ not yet, possible · 🚫 non-goal (reason in the row).

Status rule: a row is ✅ only if the thing works end to end on the wire,
not just in a fixture. Rows marked "live-checked" were exercised against a
real account in the tmux harness (`docs/MANUAL_SMOKE.md` §19); the rest are
covered by tests only.

## Summary

| Area | TD features | ✅ | 🟡 | ⬜ | 🚫 |
|------|-------------|----|----|----|----|
| Account & login | 8 | 4 | 1 | 2 | 1 |
| Chat list | 14 | 12 | 1 | 1 | 0 |
| Reading a conversation | 16 | 14 | 2 | 0 | 0 |
| Writing | 15 | 10 | 1 | 4 | 0 |
| Media & files | 14 | 8 | 2 | 3 | 1 |
| Acting on messages | 12 | 9 | 0 | 3 | 0 |
| Groups & channels | 12 | 2 | 1 | 7 | 2 |
| Search | 6 | 5 | 0 | 1 | 0 |
| Notifications | 6 | 4 | 0 | 2 | 0 |
| Privacy & security | 9 | 4 | 0 | 3 | 2 |
| Calls & live | 5 | 1 | 0 | 0 | 4 |
| Bots & payments | 7 | 2 | 0 | 2 | 3 |
| Stickers, emoji, GIFs | 6 | 2 | 1 | 2 | 1 |
| Stories, premium, misc | 8 | 0 | 0 | 2 | 6 |
| **Total** | **138** | **77** | **9** | **32** | **20** |

Roughly: half of TD is here, a quarter is reachable and worth doing, the
rest is either video/voice (no terminal plays it) or product surface
(stories, premium, payments) a power-tool client has no business in.

## Account & login

| Feature | Status | Notes |
|---------|--------|-------|
| Phone + code + 2FA login | ✅ | `lazytg login`; live-checked. |
| QR login | ⬜ | `auth.exportLoginToken`; a QR in a terminal is a block-character grid, doable. |
| Multiple accounts | 🟡 | `--account` flag switches data dirs; no in-app switcher (v0.2). |
| Log out / active sessions list | ⬜ | `auth.logOut`, `account.getAuthorizations`. |
| Session stored encrypted | ✅ | `age` file, passphrase in the OS keyring. |
| Saved Messages | ✅ | In the list from day one; live-checked. |
| Profile edit (name, bio, photo) | ⬜ | Out of the v0.1 scope; `account.updateProfile` is one call. |
| Proxy (SOCKS5 / MTProto) | 🚫 | Deferred; the user's OS-level VPN covers the need this project has. Revisit if asked. |

## Chat list

| Feature | Status | Notes |
|---------|--------|-------|
| Dialog list, pinned first, recency order | ✅ | Capped at 500 chats (ban-risk), live-checked. |
| Unread counter, by-hand unread dot | ✅ | Live-checked. |
| Muted badge, mute/unmute | ✅ | `m`; TD-style "forever". |
| Pin/unpin | ✅ | `p`. |
| Mark read/unread | ✅ | `u`. |
| Online / last seen of the other party | ✅ | In the status line; live-checked. |
| Time of the last message | ✅ | Right column. |
| Folders as tabs | ✅ | `[` / `]`; `All` first. |
| Archive | ✅ | Folder 1 walked after the main list (two pages), `Archive` tab, `updateFolderPeers`, `exclude_archived` honoured in folders. |
| Draft preview (`Draft:`) | ✅ | From the server; nothing sent back. |
| Typing indicator in the list | 🟡 | Shown in the status line for the open chat, not on other rows. |
| Chat switching by number | ✅ | `Alt+1..9`. |
| Frecency palette switcher | ✅ | `Ctrl+Space`. |
| New chat by @username / t.me link | ✅ | Palette; live-checked (`@telegram`). |

## Reading a conversation

| Feature | Status | Notes |
|---------|--------|-------|
| History with pagination both ways | ✅ | Live-checked. |
| Live updates + gap recovery | ✅ | `updates.Manager`; live disconnect checked 2026-08-19. |
| Author, time, "edited" | ✅ | |
| Formatting (bold, italic, code, pre, strike, spoiler, links) | ✅ | Spoilers reveal on cursor; `text_url` shows its host. Live-checked. |
| Reply quotes, jump to the replied message and back | ✅ | `p` / `Ctrl+O`. |
| Service lines (joined, pinned, call, …) | ✅ | |
| Reactions with counts | ✅ | |
| Read ticks on your messages (✓ / ✓✓) | ✅ | Migration 0016; none in Saved Messages. Live-checked. |
| "N unread messages" divider | ✅ | |
| Day separators | ✅ | |
| Unread counter clears when read here | ✅ | `messages.readHistory` on open, off the send guard. |
| Pinned message bar | 🟡 | Migration 0018; bar under the pane title from the newest pinned message in the loaded window, moved by `updatePinnedMessages`. A pinned message older than the window is not fetched. |
| Forwarded-from header | ✅ | `↪ forwarded from Name` above the words; hidden senders by the header's name, channel posts with the author. |
| Message threads / comments under channel posts | ⬜ | `messages.getReplies`; a second history view. |
| Link previews (web page cards) | 🟡 | `[🔗 Site — Title] o to open`; the description and the picture are not drawn. |
| Polls (read) | ✅ | Question, options with shares, total, as text; searchable. |

## Writing

| Feature | Status | Notes |
|---------|--------|-------|
| Send text | ✅ | Through the 10 msg/s guard; sender echo mirrors it at once. Live-checked. |
| Markup → entities on send | ✅ | TD's dialect. Live-checked. |
| Reply | ✅ | `Ctrl+R`. |
| Edit your own (with markup round trip) | ✅ | `e`; live-checked. |
| Multiline, `$EDITOR` | ✅ | `Alt+Enter`, `Ctrl+E`. |
| Drafts per chat (local) | ✅ | |
| Drafts synced from other devices | ✅ | Receive only, by design. |
| Drafts saved to the server | 🚫→⬜ | Skipped on purpose: `messages.saveDraft` on every pause is a behavioural signal. Could be offered as an opt-in with a long debounce. |
| Emoji picker, `:shortcode` completion | ✅ | |
| Mentions (`@user` completion) | ⬜ | Needs a member list; `messages.getFullChat` / `channels.getParticipants`. |
| Scheduled messages | ⬜ | `schedule_date` on `messages.sendMessage`; a date parser in the composer. |
| Silent send | ⬜ | `silent` flag; trivial once there is a way to say it. |
| Send as a reply to a specific quote | ⬜ | `quote_text` on the reply header (newer layers). |
| Typing indicator sent | 🚫 | Never sent; ban-risk decision. |
| Voice message recording | 🚫 | No audio input path in a terminal; not planned. |

## Media & files

| Feature | Status | Notes |
|---------|--------|-------|
| Download at the cursor | ✅ | `Ctrl+D`, unique on-disk names, dedup by `file_id`. Live-checked. |
| Open in the system viewer | ✅ | `o`, click. |
| Upload a file | ✅ | `Ctrl+U`; pictures go as photos. |
| Photos inline | ✅ | Kitty protocol only (Ghostty/kitty/WezTerm); not in tmux. |
| Voice waveform | ✅ | Block characters. |
| Video / video notes | 🟡 | Badge + system viewer; a terminal plays no video. |
| Stickers | 🟡 | Badge with the emoji; static ones could draw inline like photos. |
| GIFs / animations | ⬜ | Badge only; the first frame could draw like a photo. |
| Locations, venues | ✅ | `o` opens the map. |
| Contacts | ✅ | Name and number. |
| Dice | ✅ | |
| Shared media overlay (photos/files/links per chat) | ⬜ | `messages.search` with a filter, or the local mirror once media kinds are indexed. |
| Albums (grouped media) | ⬜ | `grouped_id`; each item is a row today, which is readable but not what TD shows. |
| Link previews | see Reading | |

## Acting on messages

| Feature | Status | Notes |
|---------|--------|-------|
| Select several | ✅ | `Space`. |
| Copy text | ✅ | `y`, OSC 52. |
| Copy link to message | ✅ | `l`. |
| Edit | ✅ | |
| Delete for me / for everyone, per peer kind | ✅ | |
| Forward | ✅ | Through the send guard. |
| React | ✅ | Counts from the server, never local arithmetic. |
| Reply | ✅ | |
| Jump to the replied message | ✅ | |
| Pin a message | ⬜ | `messages.updatePinnedMessage`. |
| Report | ⬜ | `messages.report`; low value for this audience. |
| Translate | ⬜ | `messages.translateText`; server-side, one call. |

## Groups & channels

| Feature | Status | Notes |
|---------|--------|-------|
| Read and write in groups, supergroups, channels | ✅ | |
| Join by @username / invite link | 🟡 | Resolving opens a public channel for reading; `channels.joinChannel` / `messages.importChatInvite` to actually join is not wired. |
| Leave | ⬜ | `channels.leaveChannel`, `messages.deleteChatUser`. |
| Member list, admins | ⬜ | |
| Create group / channel | ⬜ | |
| Group info (description, members count) | ⬜ | |
| Permissions, bans, admin rights | 🚫 | Admin tooling is out of scope for a reading-and-writing client. |
| Topics (forum supergroups) | ⬜ | Topics arrive as replies to a root message; a topic strip like folders would fit the two-pane layout. |
| Slow mode indicator | ⬜ | `SLOWMODE_WAIT_X` is surfaced as an error today, not counted down. |
| Channel comments | see Reading | |
| Sign messages as channel | 🚫 | Admin tooling. |
| Discussion group link | ✅ | Falls out of `l` and `o`. |

## Search

| Feature | Status | Notes |
|---------|--------|-------|
| Local full-text over mirrored history | ✅ | FTS5 trigram, offline, p95 ≈ 47 ms. |
| Jump to a result in context | ✅ | |
| Per-chat and global | ✅ | |
| Fuzzy chat name search | ✅ | Palette, Unicode-folded. |
| Server-side search past the mirror | ✅ | Tab in the overlay: one `messages.searchGlobal` per press, hits mirrored locally, same query syntax minus the local-only filters. |
| Filters (media, links, files) | ⬜ | Local index knows the kinds; a `kind:` prefix in the query syntax. |

## Notifications

| Feature | Status | Notes |
|---------|--------|-------|
| Bell on new message | ✅ | `LAZYTG_NOTIFY=off` to silence. |
| Badge in the tab title | ✅ | |
| Mute per chat | ✅ | |
| Desktop notifications (system tray / notification center) | ✅ | `LAZYTG_NOTIFY=desktop`; `terminal-notifier` / `osascript` / `notify-send` or `LAZYTG_NOTIFY_CMD`, arguments only, never a shell. |
| Mention/reply-only notifications | ⬜ | |
| Mute for 1h/8h/2d | ⬜ | Only "forever" today; a small picker on `m`. |

## Privacy & security

| Feature | Status | Notes |
|---------|--------|-------|
| Encrypted session at rest | ✅ | |
| Terminal-safe rendering of untrusted text | ✅ | `safetext`; OSC 52 abuse closed. |
| Only http(s) links reach the browser | ✅ | |
| Credentials never in the repo or in releases | ✅ | |
| Active sessions, terminate others | ⬜ | `account.getAuthorizations`. |
| Two-step verification setup | ⬜ | Login handles it; setting it up is not offered. |
| Blocked users | ⬜ | |
| Secret chats (E2E) | 🚫 | MTProto 2.0 secret chats are a client-side protocol of their own; not planned. |
| Passcode lock of the app | 🚫 | The OS lock covers a terminal. |

## Calls & live

| Feature | Status | Notes |
|---------|--------|-------|
| Missed/ended call lines in the thread | ✅ | Service messages. |
| Voice calls | 🚫 | tgvoip C library; pure-Go stack. |
| Video calls | 🚫 | Same. |
| Voice chats / live streams | 🚫 | Same. |
| Screen sharing | 🚫 | Same. |

## Bots & payments

| Feature | Status | Notes |
|---------|--------|-------|
| Talk to bots as to anyone | ✅ | |
| Inline keyboards (buttons under bot messages) | ✅ | Migration 0017; `←`/`→`/`Enter`; callback, url, copy kinds act, the rest are refused with a reason. Reply keyboards drop their text into the composer. |
| `/command` menu | ⬜ | `bots.getBotCommands`. |
| Inline bots (`@bot query`) | ⬜ | `messages.getInlineBotResults`. |
| Payments | 🚫 | Not for this client. |
| Mini apps (web apps) | 🚫 | A browser feature. |
| Games | 🚫 | Same. |

## Stickers, emoji, GIFs

| Feature | Status | Notes |
|---------|--------|-------|
| Unicode emoji, shortcodes | ✅ | |
| Receive stickers | ✅ | Badge with the emoji. |
| Send stickers | 🟡 | A sticker is a document; sending one needs a sticker set browser. |
| Custom (premium) emoji | ⬜ | Rendered as the fallback emoji; the document is ignored. |
| GIF search / send | ⬜ | |
| Animated stickers (TGS) | 🚫 | Lottie in a terminal is not a thing. |

## Stories, premium, misc

| Feature | Status | Notes |
|---------|--------|-------|
| Stories | 🚫 | Product surface, not a power-tool feature. |
| Premium features (transcription, badges, …) | 🚫 | |
| Wallet / Stars / gifts | 🚫 | |
| Business accounts | 🚫 | |
| Themes | ⬜ | Terminal colours come from the terminal; a small palette in `config.toml` is the honest scope. |
| Language / localisation | ⬜ | English only. |
| Cloud password recovery | 🚫 | Web flow. |
| Data export | 🚫 | The mirror is SQLite; `sqlite3` is the export tool. |

## What to do next, in order

Ranked by value to the target user (tmux + nvim + ssh) against cost and
ban-risk. Each is one focused PR.

1. ~~Inline keyboards~~ — shipped.
2. ~~Pinned message bar and forwarded-from header~~ — shipped; the bar still does not fetch a pin older than the loaded window.
3. ~~Link previews as a badge~~ — shipped as site and title; the description and picture are still open.
4. ~~Archive as a folder tab~~ — shipped.
5. **Mentions completion** (`@` in the composer) — deferred: the member list means `channels.getParticipants`, a known abuse signal on an unofficial client. The acceptable form is the one without a request — complete from the senders already seen in the open thread and from the chats that have a username — and it is not built yet.
6. **Topics** in forum supergroups — the two-pane layout has room for a topic strip.
7. ~~Desktop notifications (opt-in)~~ — shipped.
8. ~~Server-side search fallback~~ — shipped as Tab in the overlay.

Anything not in this file is out of scope until it is added here; the
permanently-out list is in `CLAUDE.md` → "Что НЕ делаем никогда".
