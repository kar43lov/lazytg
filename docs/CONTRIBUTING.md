# Contributing to lazytg

Thanks for considering a contribution. lazytg is in alpha — APIs, package layout, and even the command set may change. The fastest way to get a PR merged is to read this page first.

## Dev setup

### Toolchain

- Go ≥ 1.22 (we develop on the latest stable; CI runs against 1.22+)
- [`golangci-lint`](https://golangci-lint.run/) — required for any PR
- [`lefthook`](https://github.com/evilmartians/lefthook) — recommended for client-side pre-commit hooks
- [`goreleaser`](https://goreleaser.com/) — only needed if you want to test the release pipeline locally
- macOS or Linux. We do not currently develop on Windows; Windows is a target user platform from v0.2.

### Bootstrap

```sh
git clone https://github.com/pgmac/lazytg.git
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

`make build` produces `bin/lazytg` as a pure-Go binary. To exercise the CGo SQLCipher path:

```sh
go build -tags sqlcipher -o bin/lazytg-sqlcipher ./cmd/lazytg
```

You will need a working C toolchain for the `sqlcipher` tag (Xcode CLT on macOS, `build-essential` + `libsqlcipher-dev` on Debian/Ubuntu).

## Running tests

```sh
make test           # go test -race ./...
make lint           # golangci-lint run
make build          # go build -o bin/lazytg ./cmd/lazytg
make tidy           # go mod tidy
make clean          # rm -rf bin/ dist/ coverage.out
```

The CI matrix runs `go test -race -tags=${matrix.tags} ./...` on `ubuntu-latest` and `macos-latest`, with `tags` ∈ `{default, sqlcipher}`. A PR cannot merge until that matrix is green.

### Integration tests

- Auth flow tests use `gotd/td/tgtest` (in-process Telegram-protocol mock). They do not need real network access.
- Storage tests run against `:memory:` SQLite or a temp directory; nothing leaks outside `t.TempDir()`.
- Coverage gate: `internal/core/...` ≥ 80 % from v0.1.0 onward.

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
breaking!: rename --account flag to --phone
```

Allowed types: `feat`, `fix`, `perf`, `refactor`, `docs`, `test`, `chore`, `build`, `ci`, `security`, `breaking`. The `!` suffix marks breaking changes (also note them in the commit body and `CHANGELOG.md`).

`commitlint` runs as a pre-commit hook (configured in `lefthook.yml`) and on the CI lint job.

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

## Scope discipline

Stage 1 (current) is **foundation only**: bootstrap, layering, storage, auth, CLI, logging, CI, docs. Please do not ship TUI features, search UI, or files transfer in Stage 1 PRs — those belong to Stages 2 and 3. The full plan is in [`docs/plans/lazytg-v0.1.0.md`](plans/lazytg-v0.1.0.md).

When in doubt about scope, open an issue or a discussion first.
