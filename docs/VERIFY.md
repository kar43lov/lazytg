# Verifying release artifacts

Every lazytg release ships with two layers of integrity material:

1. **`checksums.txt`** — SHA-256 of every binary archive and package, plus
   a cosign-keyless signature of the file itself.
2. **Per-archive sigstore bundles** — `*.sigstore.json` next to every
   `tar.gz` archive. The bundle binds the archive's hash to the GitHub
   Actions OIDC identity that produced it.

Both are produced from the same release-pipeline run; either alone is
enough to detect a swapped artifact, but verifying both gives you the
strongest guarantee.

This page walks through both checks step by step.

---

## What you need

- The artifact you are about to install (`lazytg_<v>_<os>_<arch>.tar.gz` or
  a `.deb` / `.rpm` package).
- The matching `checksums.txt` from the same GitHub Release.
- (For per-archive verification) the `*.sigstore.json` bundle next to the
  archive.
- The [`cosign`](https://docs.sigstore.dev/cosign/installation/) CLI:

  ```sh
  brew install cosign                   # macOS
  # or, on Linux/CI runners:
  go install github.com/sigstore/cosign/v2/cmd/cosign@latest
  ```

- `sha256sum` (coreutils on Linux, `brew install coreutils` on macOS — or
  use the bundled `shasum -a 256` and adapt the commands below).

---

## Step 1 — Checksum verification

Download `checksums.txt` from the Release page and verify it next to
your artifacts:

```sh
sha256sum -c --ignore-missing checksums.txt
# expected: <every file you have>: OK
```

`--ignore-missing` lets you verify only the artifact you actually
downloaded; without it, sha256sum complains about the entries you didn't
fetch.

If any line prints `FAILED`, **stop**. Either the archive was corrupted
mid-download (re-fetch and retry) or it was substituted along the way
(open a security advisory; do not install).

### Verifying the checksums file itself

`checksums.txt` is signed via cosign keyless. The release pipeline
publishes a detached signature `checksums.txt.sig` and the OIDC certificate
`checksums.txt.pem` next to it.

```sh
curl -fsSLO "https://github.com/pgmac/lazytg/releases/download/v0.1.0/checksums.txt"
curl -fsSLO "https://github.com/pgmac/lazytg/releases/download/v0.1.0/checksums.txt.sig"
curl -fsSLO "https://github.com/pgmac/lazytg/releases/download/v0.1.0/checksums.txt.pem"

cosign verify-blob \
  --certificate "checksums.txt.pem" \
  --signature   "checksums.txt.sig" \
  --certificate-identity-regexp "https://github.com/pgmac/lazytg/.github/workflows/release.yml@refs/tags/v.*" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
# expected: Verified OK
```

After this passes, the SHA-256 list is trustworthy and Step 1 above is
sufficient for the individual artifacts.

---

## Step 2 — Per-archive cosign verification

The per-archive bundle binds **this exact archive** to the GitHub Actions
OIDC identity. Use it when you want a single command to confirm both
checksum integrity and provenance.

```sh
ARCH=amd64
OS=linux
VERSION=0.1.0
BASE="https://github.com/pgmac/lazytg/releases/download/v${VERSION}"

curl -fsSLO "${BASE}/lazytg_${VERSION}_${OS}_${ARCH}.tar.gz"
curl -fsSLO "${BASE}/lazytg_${VERSION}_${OS}_${ARCH}.tar.gz.sigstore.json"

cosign verify-blob \
  --bundle "lazytg_${VERSION}_${OS}_${ARCH}.tar.gz.sigstore.json" \
  --certificate-identity-regexp "https://github.com/pgmac/lazytg/.github/workflows/release.yml@refs/tags/v.*" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  "lazytg_${VERSION}_${OS}_${ARCH}.tar.gz"
# expected: Verified OK
```

### What "Verified OK" proves

A successful verification states the following, end-to-end:

- The archive's SHA-256 matches what was signed.
- The signing key was bound (via the Sigstore Fulcio CA) to a
  short-lived GitHub Actions OIDC identity at issuance time.
- That identity was specifically the workflow `release.yml` running in
  this repository (`pgmac/lazytg`) under a `v*` tag.

What it does **not** prove:

- That the source code of the workflow is itself benign — only that the
  archive came from this workflow. If you do not trust the maintainer,
  audit `.github/workflows/release.yml` and `.goreleaser.yaml` first.
- That the released binary contains the source you read on `main` —
  GoReleaser drives the build from the tagged commit, but a malicious
  maintainer could push a tag that doesn't match `main`. Inspecting
  `git log` for the tagged commit closes that gap.

---

## Verifying `.deb` / `.rpm` packages

The Linux packages are listed in `checksums.txt` exactly like the
tarballs, so Step 1 covers them. Per-package sigstore bundles are
**not** generated for `.deb` and `.rpm` in v0.1.x — they are derived
artifacts produced by `nfpm` from the Linux tarball, and the upstream
tarball's signature is the canonical attestation.

```sh
sha256sum -c --ignore-missing checksums.txt          # expected: lazytg_<v>_linux_amd64.deb: OK
sudo dpkg -i lazytg_<v>_linux_amd64.deb
```

If you need package-level signatures (e.g. for `apt secure-apt`-style
gating), open an issue describing the workflow you want to enable.

---

## Failure modes — what to do

| `cosign` says…                                      | What it means                                                                          | What to do                                                                                                       |
|-----------------------------------------------------|----------------------------------------------------------------------------------------|------------------------------------------------------------------------------------------------------------------|
| `Error: no matching signatures`                     | The signature does not match this artifact's hash                                       | The archive was modified after signing. Re-download from GitHub Releases and try again; if it still fails, file a security advisory. |
| `Error: certificate identity does not match`         | The workflow that signed this run is not `pgmac/lazytg/.github/workflows/release.yml`  | Either the artifact came from a fork, or the regex above is stale. Confirm the URL points at the canonical repo.   |
| `Error: ... transparency log entry not found`        | The signature's Rekor entry has aged out (Sigstore-side outage)                         | Try again later; this rarely happens. Validate via Step 1 (checksum + checksum-signature) in the meantime.        |
| `Verified OK` but archive contents look wrong       | Archive integrity is good but you grabbed the wrong platform/arch                       | Re-check the filename — should match `lazytg_${VERSION}_${OS}_${ARCH}.tar.gz` for your platform.                  |

A `Verified OK` plus a successful `sha256sum -c` is the gating signal
for installation. **Do not install if either fails.**

---

## Verifying the `lazytg version` output post-install

Once installed, double-check that the binary you run is the one you
verified:

```sh
sha256sum "$(command -v lazytg)"
# compare against the matching entry in checksums.txt (mind the archive
# wrapper if you installed from a tarball — verify the binary checksum
# against `tar -xzOf <archive>` if you want full coverage)
```

`lazytg version` prints the version, commit, and build date that
GoReleaser baked into the binary. If those do not match the GitHub
Release's tag and commit, something is off — re-run Step 1 and Step 2.

---

## Related docs

- [`INSTALL.md`](INSTALL.md) — install paths that delegate to this page.
- [`SECURITY.md`](SECURITY.md) — full threat model and disclosure policy.
- [`CONTRIBUTING.md`](CONTRIBUTING.md) — release-pipeline mechanics
  (the source of every signed artifact).
