package tg

import (
	"context"
	"errors"
	"fmt"

	"github.com/gotd/td/session"

	"github.com/kar43lov/lazytg/internal/core/config"
)

// SessionStore implements gotd's session.Storage on top of our SecretStore.
// One SecretStore can hold many sessions keyed by phone (or another opaque
// identifier) so multi-account flows just instantiate multiple SessionStores
// pointing at the same underlying secrets backend.
type SessionStore struct {
	Secrets config.SecretStore
	Phone   string
}

// NewSessionStore returns a SessionStore that stores the gotd session blob
// under the key "session:<phone>" inside store.
func NewSessionStore(store config.SecretStore, phone string) *SessionStore {
	return &SessionStore{Secrets: store, Phone: phone}
}

// Key returns the underlying secret-store key used for this session. Exposed
// for tests and for the logout command.
func (s *SessionStore) Key() string {
	return "session:" + s.Phone
}

// LoadSession satisfies gotd's session.Storage. It must return session.ErrNotFound
// (not our ErrSecretNotFound) when there is no saved session, otherwise gotd's
// auth flow treats it as a fatal error.
func (s *SessionStore) LoadSession(_ context.Context) ([]byte, error) {
	v, err := s.Secrets.Get(s.Key())
	if err != nil {
		if errors.Is(err, config.ErrSecretNotFound) {
			return nil, session.ErrNotFound
		}
		return nil, fmt.Errorf("load session for %q: %w", s.Phone, err)
	}
	return []byte(v), nil
}

// StoreSession persists the gotd session blob via the underlying secret store.
func (s *SessionStore) StoreSession(_ context.Context, data []byte) error {
	if err := s.Secrets.Set(s.Key(), string(data)); err != nil {
		return fmt.Errorf("store session for %q: %w", s.Phone, err)
	}
	return nil
}

// Forget deletes the persisted session. Used by `lazytg logout`.
func (s *SessionStore) Forget() error {
	if err := s.Secrets.Delete(s.Key()); err != nil {
		return fmt.Errorf("forget session for %q: %w", s.Phone, err)
	}
	return nil
}
