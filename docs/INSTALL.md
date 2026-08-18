# Installing lazytg

> ⚠️ **Ban-risk warning:** Telegram explicitly puts unofficial clients under
> observation. Use lazytg with a **secondary, throwaway test account first**
> before touching your primary one. See [SECURITY.md](SECURITY.md) for the full
> ban-risk policy.

This page covers every install path for lazytg v0.1.x and the one-time API
credentials setup. Verifying release artifacts is documented separately in
[VERIFY.md](VERIFY.md); recipes referenced here delegate to it.

---

## Available today

> 🔴 **No release has been tagged yet.** Everything that is served *from* a
> release — Homebrew, `.deb`, `.rpm`, the signed archives, `go install …@latest`
> — has nothing to fetch until the first tag exists, and following those recipes
> now ends in a 404 rather than an install. The pipeline behind them is written
> and tested; what a maintainer has to set up before it can run for the first
> time is in [RELEASE_PROCESS.md](RELEASE_PROCESS.md) → *Подготовка*.

| Method                                     | Best for                                     | Available |
|--------------------------------------------|----------------------------------------------|-----------|
| [Build from source](#build-from-source)    | Anyone with Go ≥ 1.25                        | **now**   |
| [A binary someone built for you](#a-binary-someone-built-for-you) | No Go toolchain, no clone | **now** |
| [Homebrew tap](#homebrew-macos-linux)      | Default for macOS users                      | first release |
| [`.deb`](#deb-debian-ubuntu-mint)          | apt-based distros, headless Linux            | first release |
| [`.rpm`](#rpm-fedora-rhel-opensuse)        | dnf/zypper distros                           | first release |
| [Manual binary archive](#manual-binary-archive) | Anywhere — checksum + signature         | first release |
| [`go install`](#go-install)                | You already have a Go toolchain              | first release |

Once tagged, every release publishes the same set of artifacts and the choice
becomes purely one of delivery preference.

---

## A binary someone built for you

lazytg is pure Go with `CGO_ENABLED=0`, so one machine builds a binary that runs
on any other with a matching OS and architecture: a single ~21 MB file, no
runtime, no shared libraries, nothing to install alongside it.

```sh
chmod +x lazytg
./lazytg version            # read the `api:` line before anything else
```

`lazytg version` answers the only question that decides your next step:

| `api:` line | What it means | What you do |
|---|---|---|
| `embedded` | credentials are compiled into this binary | nothing — run `lazytg login` |
| `env` | `LAZYTG_API_ID` / `LAZYTG_API_HASH` are set in your shell and win over anything compiled in | nothing |
| `none (no credentials …)` | this build carries none | register an app (see [API credentials](#api-credentials)) and export the pair |

### macOS blocks it on first run

A Go binary carries only an ad-hoc signature — `codesign -dv` reports
`adhoc, linker-signed` — and a file that arrived through a browser, AirDrop or a
messenger also carries the quarantine attribute. Gatekeeper puts the two
together and refuses to run it: *"Apple could not verify 'lazytg' is free of
malware that may harm your Mac"*. Clear the attribute:

```sh
xattr -d com.apple.quarantine lazytg
```

Or, without the terminal: try to run it once, then System Settings → Privacy &
Security → **Open Anyway**. Notarised builds that skip this entirely require an
Apple Developer account and belong to the release pipeline, not to a binary
handed over directly.

Linux has no equivalent step — `chmod +x` is the whole ceremony.

### Move it onto PATH

```sh
sudo install -m 0755 lazytg /usr/local/bin/lazytg   # macOS, most Linux
# or, without sudo:
install -m 0755 lazytg ~/.local/bin/lazytg          # ensure ~/.local/bin is on PATH
```

Trust matters more here than with a signed release: you are running a binary
whose provenance is a person, not a checksum. If you would rather verify it
yourself, [build from source](#build-from-source) instead — the recipe is three
commands.

---

## Homebrew (macOS, Linux)

> Available once the first release is tagged — see [Available today](#available-today).


The Homebrew formula is auto-updated by GoReleaser on every **stable** tag
(`v1.2.3`, no suffix). Pre-release tags (`-alpha`, `-beta`, `-rc`) ship to
GitHub Releases but **do not** update the tap.

```sh
brew install kar43lov/lazytg/lazytg
brew upgrade lazytg               # later
```

The tap repo is `kar43lov/homebrew-lazytg`. The formula installs a single
`lazytg` binary under the active Homebrew prefix.

---

## `.deb` (Debian, Ubuntu, Mint)

> Available once the first release is tagged — see [Available today](#available-today).


```sh
# pick the right arch — amd64 is most common, arm64 for Raspberry Pi 4+ / Ampere
ARCH=amd64
VERSION=0.1.0
curl -fsSLO "https://github.com/kar43lov/lazytg/releases/download/v${VERSION}/lazytg_${VERSION}_linux_${ARCH}.deb"
sudo dpkg -i "lazytg_${VERSION}_linux_${ARCH}.deb"
```

The package places the binary at `/usr/bin/lazytg` and copies `LICENSE` +
`README.md` under `/usr/share/doc/lazytg/`.

---

## `.rpm` (Fedora, RHEL, openSUSE)

> Available once the first release is tagged — see [Available today](#available-today).


```sh
ARCH=amd64
VERSION=0.1.0
sudo dnf install "https://github.com/kar43lov/lazytg/releases/download/v${VERSION}/lazytg_${VERSION}_linux_${ARCH}.rpm"
# or, on zypper distros:
# sudo zypper install <same URL>
```

Same layout as the `.deb`.

---

## Manual binary archive

> Available once the first release is tagged — see [Available today](#available-today).


Use this when you want to verify the cosign signature before installing,
or when no package manager covers your distro.

```sh
ARCH=amd64                              # or arm64
OS=linux                                # or darwin
VERSION=0.1.0
BASE="https://github.com/kar43lov/lazytg/releases/download/v${VERSION}"

curl -fsSLO "${BASE}/lazytg_${VERSION}_${OS}_${ARCH}.tar.gz"
curl -fsSLO "${BASE}/lazytg_${VERSION}_${OS}_${ARCH}.tar.gz.sigstore.json"
curl -fsSLO "${BASE}/checksums.txt"

# 1. Verify the cosign signature — see VERIFY.md for details
cosign verify-blob \
  --bundle "lazytg_${VERSION}_${OS}_${ARCH}.tar.gz.sigstore.json" \
  --certificate-identity-regexp "https://github.com/kar43lov/lazytg/.github/workflows/release.yml@refs/tags/v.*" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  "lazytg_${VERSION}_${OS}_${ARCH}.tar.gz"

# 2. Verify the checksum
sha256sum -c --ignore-missing checksums.txt

# 3. Extract and install
tar -xzf "lazytg_${VERSION}_${OS}_${ARCH}.tar.gz"
sudo install -m 0755 lazytg /usr/local/bin/lazytg
```

A failed `cosign verify-blob` is a hard stop — do not install the binary. The
release pipeline only ever signs from the GitHub Actions OIDC identity, so a
mismatched cert means either the artifact was tampered with or the release was
not produced by this repo's `release.yml`.

---

## `go install`

> Available once the first release is tagged — see [Available today](#available-today).


Requires Go ≥ 1.25 (the `go.mod` toolchain pin).

```sh
go install github.com/kar43lov/lazytg/cmd/lazytg@v0.1.0
```

The binary lands in `$(go env GOBIN)` (defaults to `$GOPATH/bin` ≈
`~/go/bin`). Make sure that directory is on `PATH`.

`go install` produces an unsigned binary, so cosign verification is not
applicable. The artifact is cryptographically tied to the upstream module
proxy checksum (`go.sum`) instead.

---

## Build from source

```sh
git clone https://github.com/kar43lov/lazytg.git
cd lazytg
make build              # → bin/lazytg
./bin/lazytg version
```

`make build` is exactly `go build -o bin/lazytg ./cmd/lazytg` — no ldflags of
any kind, which has two consequences worth knowing before you wonder why:

- **`lazytg version` reports `dev`**, with `commit: none` and `built: unknown`.
  Version stamping happens in the release pipeline, not in `make build`.
- **No API credentials are compiled in.** The binary resolves them at *run*
  time from `LAZYTG_API_ID` / `LAZYTG_API_HASH` (or `--api-id` / `--api-hash`),
  so they live in your shell, not in the file. Export them in `~/.zshrc` and
  every build you ever make just works; export them in one terminal only and
  lazytg will start in offline, cache-only mode everywhere else, saying
  `offline` in the status bar and logging `cannot build telegram client`.
  To bake a pair into the binary instead — the only way to hand a working build
  to someone who has no credentials — use the `-ldflags` recipe in
  [Building for someone else](#building-for-someone-else).

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full dev-loop targets
(`make test`, `make lint`, `make bench`, `make tidy`).

---

## Building for someone else

The other side of [A binary someone built for you](#a-binary-someone-built-for-you):
you have the toolchain, they do not. Cross-compilation needs no extra setup —
`CGO_ENABLED=0` plus `GOOS`/`GOARCH` is the whole recipe, and the output is one
self-contained file.

**1. Ask what they run.** `uname -sm` on their machine:

| `uname -sm` output | Build command |
|---|---|
| `Darwin arm64` (Apple Silicon) | `CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o lazytg ./cmd/lazytg` |
| `Darwin x86_64` (Intel Mac) | `CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o lazytg ./cmd/lazytg` |
| `Linux x86_64` | `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o lazytg ./cmd/lazytg` |
| `Linux aarch64` | `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o lazytg ./cmd/lazytg` |

`-trimpath` keeps your local paths out of the binary; `-s -w` drops the symbol
table and DWARF data, which halves the size and is what release builds use.
Windows is not a target — the TUI is written for POSIX terminals — and WSL
counts as Linux. Verify what you produced before sending it:

```sh
file lazytg        # → Mach-O 64-bit executable arm64 / ELF 64-bit LSB executable, x86-64
```

**2. Decide about credentials.** The default — no credentials in the binary,
recipient registers their own app at <https://my.telegram.org/apps> — is the
recommended one: it is a single form, and it keeps your `api_id` yours. If you
would rather hand over something that just works — reasonable for one person you
trust, and the reason the machinery exists even though public releases
deliberately do not use it — compile the pair in:

```sh
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath \
  -ldflags "-s -w \
    -X github.com/kar43lov/lazytg/internal/tg.embeddedAPIID=1234567 \
    -X github.com/kar43lov/lazytg/internal/tg.embeddedAPIHash=0123456789abcdef0123456789abcdef" \
  -o lazytg ./cmd/lazytg

./lazytg version    # must print: api: embedded (build embeds credentials: yes)
```

Know what that shares. An `api_id` identifies the *application*, not the user,
so everyone running that binary shares one identity with you: if Telegram flags
it, it is flagged for all of you at once, and the values are readable straight
out of the file with `strings`. Anyone who exports `LAZYTG_API_ID` /
`LAZYTG_API_HASH` locally overrides them, which is the escape hatch if a
recipient later wants their own.

🔴 **Never put those values in a file you commit.** `scripts/secret-scan.sh`
runs as a pre-commit hook and as a CI job for exactly this reason: an `api_id`
that appears in public source is refused by Telegram permanently
(`API_ID_PUBLISHED_FLOOD`), and that verdict lands on every user of every build
carrying it.

**3. Send it with two sentences.** They will hit both of these otherwise:

- macOS refuses the file on first run and the message accuses it of malware —
  the fix is `xattr -d com.apple.quarantine lazytg`, explained under
  [macOS blocks it on first run](#macos-blocks-it-on-first-run).
- Telegram puts unofficial clients under observation, so a throwaway account
  goes first. The full policy is [SECURITY.md](SECURITY.md); the risk is theirs
  now as much as yours.

---

## SQLCipher build (encrypted database)

Deferred past v0.1. The `sqlcipher` build tag is reserved for a future
CGo-backed driver. Until that driver lands, `go build -tags sqlcipher`
deliberately fails to compile (see
`internal/storage/sqlite/driver_sqlcipher.go`) so that no binary can ship
under the encrypted-DB label without the real implementation. Releases
ship the pure-Go variant only.

Rely instead on:

- the startup permissions audit (refuses `lazytg.db` mode > `0600`), and
- OS-level disk encryption (FileVault on macOS, LUKS on Linux).

---

## API credentials

Every build needs an `api_id` / `api_hash` pair, and lazytg resolves it from
three layers, in order: `--api-id` / `--api-hash` flags → `LAZYTG_API_ID` /
`LAZYTG_API_HASH` environment variables → credentials compiled into the binary.
`lazytg version` prints which layer is in effect.

**No lazytg build carries credentials of its own — releases included, by
choice.** An `api_id` compiled into a public binary is a published `api_id`:
`strings lazytg | grep -E '^[0-9a-f]{32}$'` prints it, and Telegram blocks
published ones permanently, for everyone using that build at the same time
(`API_ID_PUBLISHED_FLOOD`). Their own documentation is explicit — *"obtain your
own API id before you publish your app"*. So lazytg ships the machinery to embed
a pair (see [Building for someone else](#building-for-someone-else)) but the
public release does not use it, and every installation path asks you for your
own credentials once.

**Builds from source do.** The credentials are not in this repository — an
`api_id` found in public source is refused by Telegram forever with
`API_ID_PUBLISHED_FLOOD` — so `go build` and `go install` produce a binary with
no key. Register your own application at <https://my.telegram.org/apps> and
export the values:

```sh
# add to ~/.zshrc / ~/.bashrc
export LAZYTG_API_ID=1234567
export LAZYTG_API_HASH=0123456789abcdef0123456789abcdef
```

`lazytg version` prints which of the three sources is in effect:

```
api:    embedded (build embeds credentials: yes)
```

| Source     | Set by                                  | Precedence |
|------------|-----------------------------------------|------------|
| `flags`    | `--api-id` / `--api-hash`               | highest    |
| `env`      | `LAZYTG_API_ID` / `LAZYTG_API_HASH`     | middle     |
| `embedded` | compiled in at build time with the `-ldflags` recipe in [Building for someone else](#building-for-someone-else). Public releases deliberately do not do this, so a binary reporting `embedded` came from a person, not from the releases page | lowest |

Both halves of a source must be set together. Exporting only `LAZYTG_API_ID`
is an error, not a silent fall-back to the embedded key — otherwise you would
believe you are running on your own credentials while you are not.

`--api-hash` puts a secret in `ps` output and shell history; prefer the env
var and keep the flag for scripted one-offs.

### When my.telegram.org will not issue credentials

The application form is not reachable from everywhere — from Russia in
particular it frequently fails, either by never delivering the login code or by
answering a bare `ERROR` when the application is submitted. That is a Telegram
side restriction; lazytg cannot work around it, and no amount of retrying the
form changes it. What actually works:

1. **Complete the registration through a VPN** in a region where the form works.
   You need it only once — the credentials keep working afterwards, wherever you
   run lazytg from. The pair is tied to the Telegram account, not to the network
   it was created on.
2. **Get a binary from someone who already has credentials.** Anyone holding a
   pair can compile it into a build for you in one command — the recipe is in
   [Building for someone else](#building-for-someone-else). Understand the
   trade for both sides: you share one application identity, so a block earned
   by any of you lands on all of you, and whoever holds the key is the one
   answering for how it gets used.
3. **Build from source with a pair you were given.** Same result as (2) without
   trusting someone else's binary: they hand you the two values, you compile
   them in yourself or export them as environment variables.

Whichever route you take, `lazytg version` tells you where you ended up: `env`,
`embedded` or `none`.

---

### Running on someone else's key

If you are using a binary with credentials compiled in, you are sharing one
application identity with whoever built it and with everyone else they gave it
to. What that means in practice:

- **The blast radius is shared.** If Telegram blocks that `api_id` — for abuse
  by any one of you, or because the key gets flagged as published — everyone
  using that build loses login at the same moment. Your escape hatch is
  immediate and needs no reinstall: export your own `LAZYTG_API_ID` /
  `LAZYTG_API_HASH` and the env layer wins over the compiled-in one.
- **The key holder answers for it.** The `api_id` identifies the application,
  so behaviour flagged as abuse attaches to their key, not to your account.
  Treat it as borrowed.
- **Observation is unrelated to whose key it is.** Telegram puts accounts under
  observation when they log in through any unofficial client, so a shared key
  makes you no more visible than your own would. Read the official
  [obtaining api_id](https://core.telegram.org/api/obtaining_api_id) page and
  decide for yourself.

Registering your own application takes about a minute and isolates you from the
first two. When that is not possible, see
[When my.telegram.org will not issue credentials](#when-mytelegramorg-will-not-issue-credentials).

---

## First login

Run once per account. Phone numbers are E.164 (leading `+`, country code, no
spaces or dashes):

```sh
lazytg login --account +71234567890
# → Telegram sends a code via the official app or SMS, lazytg prompts for it.
# → If 2FA is enabled, lazytg asks for the cloud password (no echo).
```

The session is stored in `<config>/secrets.age`, encrypted with `age`. The
passphrase that opens it is generated once and kept in your OS keyring
(Keychain on macOS, Secret Service on Linux, Credential Manager on Windows),
so a desktop install never prompts. On a headless box without D-Bus you supply
that passphrase at startup instead — there is no third option, so make sure
either gnome-keyring/KWallet is running, or you are happy typing a passphrase
on every interactive command.

The session blob does not go in the keyring itself. A gotd session is roughly
4.2 KB — auth key plus the full datacenter configuration — and the macOS
keyring backend refuses any secret past 4096 bytes, which made sessions
impossible to persist there at all.

Confirm the session is stored:

```sh
lazytg accounts
# → +71234567890   (active)
```

---

## Next steps

- [`CONFIGURATION.md`](CONFIGURATION.md) — config files, env vars, keymap
  override, multi-account flag.
- [`SEARCH.md`](SEARCH.md) — query syntax for the local FTS5 index.
- [`TROUBLESHOOTING.md`](TROUBLESHOOTING.md) — common errors and how to
  recover.
- [`VERIFY.md`](VERIFY.md) — what cosign verification proves and how to
  audit a release artifact end-to-end.
- [`SECURITY.md`](SECURITY.md) — threat model, ban-risk policy, disclosure.

If you want to contribute, [`CONTRIBUTING.md`](CONTRIBUTING.md) has the
dev-setup and PR checklist.
