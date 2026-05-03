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
// slice to render oldest-first (top to bottom reading order). Used for
// the initial chat-open load only.
//
// GetMessagesBefore returns rows with id strictly less than beforeID,
// ordered by id desc. Used for scroll-up pagination because offset-
// based paging races with applyIncoming: a live MessageReceived
// appended between initial load and scroll-up shifts the "skip N rows"
// boundary, leaving a one-message gap in displayed history. Cursor
// paging (id < oldestID) is stable under live appends because the cursor
// pins the boundary to a concrete id.
//
// GetMessagesAfter returns rows with id strictly greater than afterID,
// ordered by id asc. Used for the symmetric scroll-down pagination
// after a search-jump landed the user in the middle of history —
// without it, the loaded ±N context window would strand the user
// because they can scroll up (loading older history via
// GetMessagesBefore) but the messages newer than the window are
// unreachable. Cursor-based for the same race-stability reason as
// GetMessagesBefore.
type Repository interface {
	GetMessages(ctx context.Context, chatID int64, limit, offset int) ([]domain.Message, error)
	GetMessagesBefore(ctx context.Context, chatID, beforeID int64, limit int) ([]domain.Message, error)
	GetMessagesAfter(ctx context.Context, chatID, afterID int64, limit int) ([]domain.Message, error)
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
