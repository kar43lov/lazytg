<!--
PR title MUST follow Conventional Commits, e.g.:
  feat: add age-encrypted secret store fallback
  fix: tighten session-file permission check on macOS
  perf: batch FTS5 inserts during initial reindex
  security: redact api_hash in slog handler
  breaking!: rename --account flag to --phone
-->

## Summary

<!-- 1–3 sentences. What and why. -->

## Related issue / plan task

<!-- Closes #N, or refers to docs/plans/...md task X. -->

## Changes

<!-- Bullet list of the meaningful changes in this PR. -->

-

## Checklist

- [ ] PR title follows Conventional Commits
- [ ] `make test` passes locally
- [ ] `make lint` passes locally (zero warnings)
- [ ] `depguard` rules still pass (no `internal/core/...` → `gotd/td` or `bubbletea`; no `internal/ui/...` → `gotd/td`; no `internal/storage/...` → `internal/ui` or `internal/tg`)
- [ ] New behaviour has tests (unit and/or integration)
- [ ] `CHANGELOG.md` updated under `## [Unreleased]` if user-visible behaviour changed
- [ ] `docs/ARCHITECTURE.md` updated if package layout or stack choices changed
- [ ] `docs/SECURITY.md` reviewed if auth, secrets, send, or logging paths changed
- [ ] No new CGo dependency without a `//go:build <tag>` gate and justification
- [ ] Stays within the v0.1.0 roadmap scope (or has explicit agreement otherwise)

## Test plan

<!-- How a reviewer can verify this works. Commands, manual steps, expected output. -->

-
