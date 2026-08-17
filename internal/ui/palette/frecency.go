package palette

import (
	"context"
	"time"
)

// FrecencyStore is the slice of the storage layer the palette
// consumes. Defined as an interface (not a *sqlite.Repo dependency)
// so tests can swap fakes without spinning up SQLite, and so a
// future caching wrapper can drop in without touching the palette.
//
// Top is intentionally limit-bounded (rather than "give me
// everything"): the palette caps its candidate hot-set at a few
// hundred chats to keep the per-keystroke fuzzy pass O(N log N) at a
// predictable N. A user with 5000 dialogs is the upper bound we
// design for, so 200 is a comfortable cap.
type FrecencyStore interface {
	// Top returns up to limit chat ids ranked by precomputed score
	// DESC. Empty slice (not nil panic) when no visits have been
	// recorded yet.
	Top(ctx context.Context, limit int) ([]int64, error)

	// RecordVisit refreshes the score for chatID. Called by the app
	// after a SelectedMsg-driven chat switch lands so the next
	// palette open ranks the just-visited chat higher. The error
	// path is logged-and-swallowed by the app — a transient SQLite
	// hiccup must not block the chat switch the user just asked
	// for.
	RecordVisit(ctx context.Context, chatID int64) error
}

// HotSetLimit is the max number of chat ids the palette pulls from
// FrecencyStore on every Open. 200 is comfortably above the
// "actively-used" working set for power users while keeping the per-
// keystroke fuzzy pass cheap enough that a 5000-chat account does
// not feel laggy.
const HotSetLimit = 200

// repoBackend adapts a *sqlite.Repo (or any type with the same
// method set) to the FrecencyStore contract. The repo method takes
// `now time.Time` so callers can pass deterministic timestamps in
// tests; the palette always uses time.Now() in production so the
// adapter swallows that argument.
//
// Defined as an interface field so this package does not import
// internal/storage/sqlite directly — that import would force every
// `go build ./internal/ui/palette/...` to drag in the SQLite driver,
// inflating test binaries and making the package harder to fake.
type repoBackend interface {
	RecordVisit(ctx context.Context, chatID int64, now time.Time) error
	TopFrecency(ctx context.Context, limit int) ([]int64, error)
}

// repoStore is the production FrecencyStore impl that wraps a
// repoBackend. NewRepoStore is the only legal constructor so callers
// cannot bypass the now=time.Now() injection.
type repoStore struct{ repo repoBackend }

// NewRepoStore returns a FrecencyStore backed by repo. repo is
// typically *sqlite.Repo; tests may pass a fake satisfying the
// repoBackend interface.
func NewRepoStore(repo repoBackend) FrecencyStore { return repoStore{repo: repo} }

func (s repoStore) Top(ctx context.Context, limit int) ([]int64, error) {
	return s.repo.TopFrecency(ctx, limit)
}

func (s repoStore) RecordVisit(ctx context.Context, chatID int64) error {
	return s.repo.RecordVisit(ctx, chatID, time.Now().UTC())
}
