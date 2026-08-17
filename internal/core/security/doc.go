// Package security implements the gotd-free safety net wired in at app
// boot: a startup permissions audit (Stage 3 Task 9 — fail-fast if
// secrets/database files have looser bits than expected) and a token-
// bucket guard wrapped around outgoing sends to keep us comfortably
// below Telegram's per-second flood thresholds.
//
// MUST NOT import gotd or charmbracelet/bubbletea (enforced via depguard
// in the project's golangci-lint config). The send-rate guard reuses the
// existing internal/core/sync.TokenBucket so behaviour stays consistent
// with the history-backfill limiter and tests have a single rate-limit
// implementation to cover.
package security
