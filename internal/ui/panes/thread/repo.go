package thread

import (
	"context"

	"github.com/pgmac/lazytg/internal/core/domain"
)

// Repository is the slice of the storage layer the thread pane reads
// from. Defined as an interface (not a *sqlite.Repo dependency) so unit
// tests can swap in an in-memory fake without spinning up SQLite, and so
// the depguard rules stay clean if a future caching layer wraps the
// real repo.
//
// GetMessages returns rows ordered by date desc / id desc — that is the
// SQL contract enforced by sqlite.Repo. The thread pane reverses the
// slice to render oldest-first (top to bottom reading order); keeping
// the SQL "newest first" gives offset-paging the right shape (page 1 =
// newest 200, page 2 = next 200 oldest).
type Repository interface {
	GetMessages(ctx context.Context, chatID int64, limit, offset int) ([]domain.Message, error)
}

// HistoryProvider mirrors core/sync.HistoryProvider so the thread pane
// can ask the gotd-aware backfiller for older history when the local
// cache turns out to be thin (e.g. a freshly-discovered chat with only a
// dozen cached messages). Stage 2 Task 8 wires the field but leaves the
// invocation as a TODO — Task 11 hooks it into BackfillService once
// peer resolution is in place.
type HistoryProvider interface {
	Fetch(ctx context.Context, peerID, accessHash int64, peerType string, limit, offsetID int) ([]domain.Message, bool, error)
}
