package sqlite

import "github.com/kar43lov/lazytg/internal/core/domain"

// The reaction column's format lives in the domain package, because the
// search service reads the same column when it builds a jump window and two
// copies of a format is how one of them ends up rendering messages with their
// reactions missing. These are the names this package uses for it.
var (
	encodeReactions = domain.EncodeReactions
	decodeReactions = domain.DecodeReactions
)
