package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// ErrPeerNotFound is returned by PeerRepo.Get when no row matches the
// requested id. Callers should treat the absence as a recoverable
// condition (re-resolve the peer via dialogs.getDialogs) rather than a
// fatal error.
var ErrPeerNotFound = errors.New("peer: not found")

// PeerRepo is the storage-backed implementation of the access_hash cache.
// It exists so the send/history paths can build a tg.InputPeer without
// reaching for the dialogs RPC every time. The struct deliberately wraps
// *sql.DB rather than embedding *Repo: peers is a self-contained table
// and the narrower dependency makes the unit tests trivial.
type PeerRepo struct {
	db *sql.DB
}

// NewPeerRepo wires a PeerRepo onto an existing connection. Callers pass
// repo.DB() so the cache lives in the same SQLite file (and the same
// migrations stack) as messages and chats.
func NewPeerRepo(db *sql.DB) *PeerRepo {
	return &PeerRepo{db: db}
}

// Save upserts a peer row by id. The composite of (type, access_hash) is
// updated on conflict so a re-resolve after the access_hash rotates keeps
// the cache fresh.
func (r *PeerRepo) Save(ctx context.Context, p domain.Peer) error {
	if p.ID == 0 {
		return errors.New("peer: id is required")
	}
	if p.Type == "" {
		return errors.New("peer: type is required")
	}
	_, err := r.db.ExecContext(ctx, `
        INSERT INTO peers (id, type, access_hash)
        VALUES (?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
            type        = excluded.type,
            access_hash = excluded.access_hash
    `, p.ID, string(p.Type), p.AccessHash)
	if err != nil {
		return fmt.Errorf("save peer %d: %w", p.ID, err)
	}
	return nil
}

// Get fetches a peer row by id. Returns ErrPeerNotFound if the row does
// not exist so callers can branch on errors.Is without inspecting the
// (zero-value, nil) shape.
func (r *PeerRepo) Get(ctx context.Context, id int64) (domain.Peer, error) {
	row := r.db.QueryRowContext(ctx, `
        SELECT id, type, access_hash FROM peers WHERE id = ?
    `, id)
	var p domain.Peer
	var typ string
	if err := row.Scan(&p.ID, &typ, &p.AccessHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Peer{}, ErrPeerNotFound
		}
		return domain.Peer{}, fmt.Errorf("query peer %d: %w", id, err)
	}
	p.Type = domain.ChatType(typ)
	return p, nil
}
