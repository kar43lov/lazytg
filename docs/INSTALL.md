# Installing lazytg

> ⚠️ **Ban-risk warning:** Telegram explicitly puts unofficial clients under
> observation. Use lazytg with a **secondary, throwaway test account first**
> before touching your primary one. See [SECURITY.md](SECURITY.md) for the full
> ban-risk policy.

This page covers every supported install path for lazytg v0.1.x and the
one-time API credentials setup. Verifying release artifacts is documented
separately in [VERIFY.md](VERIFY.md); recipes referenced here delegate to it.

---

## Pick your install method

| Method                                     | Best for                                      | Auto-update                          |
|--------------------------------------------|-----------------------------------------------|--------------------------------------|
| Homebrew tap (macOS / Linux)               | Default for macOS users                       | `brew upgrade`                       |
| `.deb` (Debian / Ubuntu / Mint)            | apt-based distros, headless Linux             | manual download of the next release  |
| `.rpm` (Fedora / RHEL / openSUSE)          | dnf/zypper distros                            | manual                               |
| Manual binary archive                      | Anywhere — gives you a checksum + signature  | manual                               |
| `go install`                                | You already have a Go ≥ 1.25 toolchain        | `go install …@latest`                |
| Build from source                          | Hacking on lazytg                              | `git pull && make build`             |

Every release publishes the same set of artifacts; the choice is purely about
delivery preference.

---

## Homebrew (macOS, Linux)

The Homebrew formula is auto-updated by GoReleaser on every **stable** tag
(`v1.2.3`, no suffix). Pre-release tags (`-alpha`, `-beta`, `-rc`) ship to
GitHub Releases but **do not** update the tap.

```sh
brew install pgmac/lazytg/lazytg
brew upgrade lazytg               # later
```

The tap repo is `pgmac/homebrew-lazytg`. The formula installs a single
`lazytg` binary under the active Homebrew prefix.

---

## `.deb` (Debian, Ubuntu, Mint)

```sh
# pick the right arch — amd64 is most common, arm64 for Raspberry Pi 4+ / Ampere
ARCH=amd64
VERSION=0.1.0
curl -fsSLO "https://github.com/pgmac/lazytg/releases/download/v${VERSION}/lazytg_${VERSION}_linux_${ARCH}.deb"
sudo dpkg -i "lazytg_${VERSION}_linux_${ARCH}.deb"
```

The package places the binary at `/usr/bin/lazytg` and copies `LICENSE` +
`README.md` under `/usr/share/doc/lazytg/`.

---

## `.rpm` (Fedora, RHEL, openSUSE)

```sh
ARCH=amd64
VERSION=0.1.0
sudo dnf install "https://github.com/pgmac/lazytg/releases/download/v${VERSION}/lazytg_${VERSION}_linux_${ARCH}.rpm"
# or, on zypper distros:
# sudo zypper install <same URL>
```

Same layout as the `.deb`.

---

## Manual binary archive

Use this when you want to verify the cosign signature before installing,
or when no package manager covers your distro.

```sh
ARCH=amd64                              # or arm64
OS=linux                                # or darwin
VERSION=0.1.0
BASE="https://github.com/pgmac/lazytg/releases/download/v${VERSION}"

curl -fsSLO "${BASE}/lazytg_${VERSION}_${OS}_${ARCH}.tar.gz"
curl -fsSLO "${BASE}/lazytg_${VERSION}_${OS}_${ARCH}.tar.gz.sigstore.json"
curl -fsSLO "${BASE}/checksums.txt"

# 1. Verify the cosign signature — see VERIFY.md for details
cosign verify-blob \
  --bundle "lazytg_${VERSION}_${OS}_${ARCH}.tar.gz.sigstore.json" \
  --certificate-identity-regexp "https://github.com/pgmac/lazytg/.github/workflows/release.yml@refs/tags/v.*" \
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

Requires Go ≥ 1.25 (the `go.mod` toolchain pin).

```sh
go install github.com/pgmac/lazytg/cmd/lazytg@v0.1.0
```

The binary lands in `$(go env GOBIN)` (defaults to `$GOPATH/bin` ≈
`~/go/bin`). Make sure that directory is on `PATH`.

`go install` produces an unsigned binary, so cosign verification is not
applicable. The artifact is cryptographically tied to the upstream module
proxy checksum (`go.sum`) instead.

---

## Build from source

```sh
git clone https://github.com/pgmac/lazytg.git
cd lazytg
make build              # → bin/lazytg
./bin/lazytg version
```

`make build` resolves to `go build -o bin/lazytg ./cmd/lazytg` with
version/commit/date ldflags. See [CONTRIBUTING.md](CONTRIBUTING.md) for the
full dev-loop targets (`make test`, `make lint`, `make bench`, `make tidy`).

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

## One-time setup: API credentials

lazytg uses **your own** Telegram API credentials, not a bundled key. Get
them from <https://my.telegram.org/apps> — log in, create an application,
copy the `api_id` and `api_hash`.

Export the values into your shell. The two env vars are mandatory; lazytg
fails fast if either is missing:

```sh
# add to ~/.zshrc / ~/.bashrc
export LAZYTG_API_ID=1234567
export LAZYTG_API_HASH=0123456789abcdef0123456789abcdef
```

Why your own credentials? Telegram automatically observes accounts that
share an `api_id` with many users; bundling a project-wide key would put
every lazytg user on the same observation list. Reading the official
[obtaining api_id](https://core.telegram.org/api/obtaining_api_id) page
before continuing is recommended.

---

## First login

Run once per account. Phone numbers are E.164 (leading `+`, country code, no
spaces or dashes):

```sh
lazytg login --account +71234567890
# → Telegram sends a code via the official app or SMS, lazytg prompts for it.
# → If 2FA is enabled, lazytg asks for the cloud password (no echo).
```

The session is stored in your OS keyring (Keychain on macOS, Secret Service
on Linux, Credential Manager on Windows). On a headless box without D-Bus,
lazytg falls back to an `age`-encrypted file gated by a master passphrase
you supply at startup — there is no third option, so make sure either
gnome-keyring/KWallet is running, or you are happy typing a passphrase
on every interactive command.

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
