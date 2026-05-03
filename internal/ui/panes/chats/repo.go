package chats

import (
	"context"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// Repository is the slice of the storage layer the chats pane consumes.
// Defined as an interface (not a *sqlite.Repo dependency) so tests can
// substitute fakes without spinning up SQLite, and so the depguard rules
// stay clean if a future caching layer wraps the real repo.
type Repository interface {
	// GetChats returns every persisted chat. The storage layer guarantees
	// the rows arrive in pinned-first / last-message-desc order, but the
	// pane re-sorts defensively in chatsLoadedMsg so that fakes returning
	// arbitrary order render correctly too.
	GetChats(ctx context.Context) ([]domain.Chat, error)
}
