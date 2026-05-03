# Contributing to lazytg

Thanks for considering a contribution. lazytg is in alpha — APIs, package layout, and even the command set may change. The fastest way to get a PR merged is to read this page first.

## Dev setup

### Toolchain

- Go ≥ 1.25 (pinned in `go.mod`; CI uses `go-version-file: go.mod`)
- [`golangci-lint`](https://golangci-lint.run/) — required for any PR
- [`lefthook`](https://github.com/evilmartians/lefthook) — recommended for client-side pre-commit hooks (gofmt, go vet, go test -short)
- [`goreleaser`](https://goreleaser.com/) — only needed if you want to test the release pipeline locally
- macOS or Linux. We do not currently develop on Windows; Windows is a target user platform from v0.2.

### Bootstrap

```sh
git clone https://github.com/kar43lov/lazytg.git
cd lazytg
go mod download

# install golangci-lint (one-time)
brew install golangci-lint                       # macOS
# or follow https://golangci-lint.run/usage/install/

# (optional) install lefthook + register hooks
brew install lefthook
lefthook install

make build
```

`make build` produces `bin/lazytg` as a pure-Go binary. `make bench` runs the FTS5 search p95 SLA gate (`BenchmarkSearch100k` — fails the build if p95 > 100 ms on a 100k-message synthetic corpus); CI runs the same target on Linux. The `sqlcipher` build tag is reserved for the CGo-backed encrypted driver and remains unwired (CGo SQLCipher is deferred past v0.1) — the database is unencrypted regardless of build tag.

## Running tests

```sh
make test           # go test -race ./...
make lint           # golangci-lint run
make build          # go build -o bin/lazytg ./cmd/lazytg
make tidy           # go mod tidy
make clean          # rm -rf bin/ dist/ coverage.out
```

The CI matrix runs `go test -race ./...` on `ubuntu-latest` and `macos-latest`. A PR cannot merge until that matrix is green.

### Integration tests

- Auth flow tests use a hand-rolled `auth.FlowClient` mock (in-process, no MTProto). The `gotd/td/tgtest` SRP server is not yet wired — see Stage 1 plan task 4 deviation.
- Storage tests run against a temp directory; nothing leaks outside `t.TempDir()`.
- Coverage gate: `internal/core/...` targets ≥ 80 % from v0.1.0 onward — tracked in codecov, not a hard CI gate (`fail_ci_if_error: false` in `.github/workflows/ci.yml`).

### Manual smoke (real Telegram)

Some workflows are not covered by automated tests in v0.1.0. After major changes to `internal/tg/auth.go`, `internal/tg/session.go`, or the keyring/age fallback, run:

```sh
export LAZYTG_API_ID=...
export LAZYTG_API_HASH=...
./bin/lazytg login --account +7XXXXXXXXXX
./bin/lazytg accounts
./bin/lazytg accounts        # second run — must not re-prompt
./bin/lazytg logout --account +7XXXXXXXXXX
```

Use a **secondary, throwaway** Telegram account for this. See [SECURITY.md](SECURITY.md) for ban-risk policy.

## Code style

- Run `gofmt -s` and `goimports`. The Makefile + golangci-lint formatter section will catch you if you forget.
- Exported identifiers need doc comments (`revive: exported`).
- Tests live next to the code they test: `foo.go` ↔ `foo_test.go`. Use table-driven tests where it reduces duplication.
- Errors carry context: `fmt.Errorf("read session for %q: %w", phone, err)`. No bare `return err` past two levels of stack.
- No naked `panic` outside `init()` and tests. Validate inputs at API boundaries; trust internal calls.

### Layering rules — enforced by depguard

- `internal/core/...` MUST NOT import `github.com/gotd/td` or `github.com/charmbracelet/bubbletea`.
- `internal/ui/...` MUST NOT import `github.com/gotd/td`.
- `internal/storage/...` MUST NOT import `internal/ui` or `internal/tg`.

These are enforced via `golangci-lint`'s `depguard` linter; see [`docs/ARCHITECTURE.md`](ARCHITECTURE.md). Violations fail CI.

## Commit messages

We use [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add age-encrypted secret store fallback
fix: tighten session-file permission check on macOS
perf: batch FTS5 inserts during initial reindex
security: redact api_hash in slog handler
feat!: rename --account flag to --phone
```

Allowed types (`.commitlintrc.yml`): `feat`, `fix`, `perf`, `security`, `docs`, `refactor`, `test`, `chore`, `build`, `ci`. The trailing `!` suffix (`feat!:`, `fix(scope)!:`) marks breaking changes — also describe them in the commit body and reflect them in `CHANGELOG.md` under `### Breaking`.

Subject line is capped at 100 characters; the body (if present) must be separated from the subject by a blank line.

### Local enforcement

`lefthook.yml` registers a `commit-msg` hook that lints the message before the commit lands. With `lefthook install` already run (see [Bootstrap](#bootstrap)) the hook fires automatically on every `git commit`.

The hook prefers `commitlint` via `npx --no-install --package=@commitlint/cli`. If `npx` (or the package) is unavailable the hook falls back to a bash regex with the same type set and 100-char subject cap, so first-time contributors are not blocked by missing Node tooling. To run the rich rule set explicitly:

```sh
brew install node                  # one-time
npx --package=@commitlint/cli -- commitlint --from=origin/main --to=HEAD
```

### CI enforcement

`ci.yml` runs `amannn/action-semantic-pull-request` on every PR — the **PR title** must conform to Conventional Commits (the action ignores individual squash-commit subjects, so the title is the source of truth when squashing). `Merge` / `Revert` / `fixup!` commits are exempt from the local hook.

### Changelog generation

`CHANGELOG.md` is generated from commit history by [`git-cliff`](https://git-cliff.org/) using `cliff.toml` in the repo root. The maintainer regenerates it before tagging:

```sh
brew install git-cliff             # macOS, one-time (or `cargo install git-cliff`)
git-cliff --tag v0.1.0-alpha.1 --unreleased --prepend CHANGELOG.md
```

Contributors do not need `git-cliff` installed for normal PR work; the CHANGELOG is regenerated as part of the release process. If you want to preview what your commits will look like, `git-cliff --unreleased` prints the would-be section to stdout without touching the file.

## Pull request checklist

Before requesting review, please confirm:

- [ ] PR title follows Conventional Commits.
- [ ] `make test` passes locally.
- [ ] `make lint` passes locally (zero warnings).
- [ ] New behaviour has tests (unit and/or integration).
- [ ] If you touched a public API, a `## [Unreleased]` entry was added to [`CHANGELOG.md`](../CHANGELOG.md).
- [ ] If you touched architecture or stack choices, [`docs/ARCHITECTURE.md`](ARCHITECTURE.md) is updated in the same PR.
- [ ] If you touched auth, secrets, or send paths, [`docs/SECURITY.md`](SECURITY.md) is reviewed for impact.
- [ ] No CGo dependency introduced without a `//go:build <tag>` gate and a justification in the PR description.
- [ ] No new feature lives outside the v0.1.0 roadmap unless it has been agreed in an issue first.

## Release pipeline

Releases are produced by GoReleaser via `.github/workflows/release.yml`, which fires on any `v*` tag pushed to the repository. The pipeline distinguishes pre-release tags from stable releases:

| Tag shape                | GitHub Release | Brew formula update | `.deb` / `.rpm` assets | Use it for                                    |
|--------------------------|----------------|---------------------|------------------------|-----------------------------------------------|
| `v0.1.0-alpha.N`         | prerelease     | **skipped**         | attached as assets     | Internal smoke; not for outside testers       |
| `v0.1.0-beta.N`          | prerelease     | **skipped**         | attached as assets     | External beta testers (≥ 3, see `docs/BETA_CHECKLIST.md`) |
| `v0.1.0-rc.N`            | prerelease     | **skipped**         | attached as assets     | Final candidate before stable                 |
| `v0.1.0` (no suffix)     | stable         | **published**       | attached as assets     | Public release                                |

Detection is automatic — `release.prerelease: auto` in `.goreleaser.yaml` derives the prerelease flag from the SemVer suffix, and `brews[].skip_upload: '{{ if .Prerelease }}true{{ end }}'` short-circuits the homebrew tap update for any non-stable tag. To check whether a given run touched brew, scan the GoReleaser log for `homebrew tap formula` (stable) or `skipping homebrew publish` (prerelease).

### Cutting a pre-release tag

Two paths are supported:

1. **`Create prerelease tag` workflow (recommended).** In the GitHub UI go to *Actions → Create prerelease tag → Run workflow*, pick `alpha`, `beta`, or `rc`, optionally override the base version. The workflow computes the next available `v<base>-<kind>.N` and pushes the annotated tag.

   ⚠️ Tags pushed by the workflow's default `GITHUB_TOKEN` do **not** trigger `release.yml` automatically (GitHub blocks recursive workflow dispatch). After the prerelease workflow finishes, run the release manually:

   ```sh
   gh workflow run release.yml --ref <NEW_TAG>
   ```

   See `docs/RELEASE_PROCESS.md` step 5 and the notice printed at the end of the prerelease workflow run.
2. **Manual.** From a clean checkout of `main`:

   ```sh
   git tag -a v0.1.0-beta.2 -m "v0.1.0-beta.2"
   git push origin v0.1.0-beta.2
   ```

Either way, only `release.yml` ever publishes to GitHub Releases — local `goreleaser release --snapshot` (used in `.github/workflows/snapshot.yml`) skips publish/sign by design.

### Cutting a stable release

Stable tags (`vMAJOR.MINOR.PATCH` without suffix) are intentionally **manual only** — there is no `Create stable tag` workflow. The maintainer must explicitly opt in:

```sh
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

Pre-conditions before tagging stable (see [`docs/RELEASE_PROCESS.md`](RELEASE_PROCESS.md)):

- `main` is green on CI (lint + test matrix + search SLA gate).
- Coverage gates passed: `internal/core` ≥ 80 %, `internal/ui` ≥ 60 %.
- `CHANGELOG.md` Unreleased section has been promoted to the new version (auto-generated via `git-cliff` from Stage 4 onward).
- The `HOMEBREW_TAP_GITHUB_TOKEN` repo secret is configured and points at a PAT with `contents:write` on `kar43lov/homebrew-lazytg` — without it, the brew step in `release.yml` will fail.
- (Stage 4) ≥ 3 external testers have completed `docs/BETA_CHECKLIST.md` for the latest `vX.Y.Z-beta.*`.

## Scope discipline

Stages 1–3 of the v0.1.0 roadmap have shipped (foundation, TUI, search/files/security). Stage 4 (release pipeline + alpha/beta cycle) is in progress. Please keep PRs scoped to current-stage work and to the Stage 2 carry-over wiring (MTProto attach in `runTUI`, `BackfillService.Start`, `--polling` consumer, `reconnectAdapter.Connect`). The full plan is in [`docs/plans/lazytg-v0.1.0.md`](plans/lazytg-v0.1.0.md).

When in doubt about scope, open an issue or a discussion first.
