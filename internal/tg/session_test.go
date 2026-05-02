package tg

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/session"

	"github.com/pgmac/lazytg/internal/core/config"
)

// fakeSecretStore is a tiny in-memory implementation of config.SecretStore so
// session round-trips can be exercised without touching keychain or disk.
type fakeSecretStore struct {
	data map[string]string
}

func newFakeSecretStore() *fakeSecretStore {
	return &fakeSecretStore{data: make(map[string]string)}
}

func (f *fakeSecretStore) Get(key string) (string, error) {
	v, ok := f.data[key]
	if !ok {
		return "", config.ErrSecretNotFound
	}
	return v, nil
}

func (f *fakeSecretStore) Set(key, value string) error {
	f.data[key] = value
	return nil
}

func (f *fakeSecretStore) Delete(key string) error {
	delete(f.data, key)
	return nil
}

func TestSessionStore_RoundTrip(t *testing.T) {
	t.Parallel()
	store := NewSessionStore(newFakeSecretStore(), "+79990001122")

	ctx := context.Background()
	if _, err := store.LoadSession(ctx); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("LoadSession on empty store: want session.ErrNotFound, got %v", err)
	}

	want := []byte(`{"version":1,"data":"deadbeef"}`)
	if err := store.StoreSession(ctx, want); err != nil {
		t.Fatalf("StoreSession: %v", err)
	}
	got, err := store.LoadSession(ctx)
	if err != nil {
		t.Fatalf("LoadSession after store: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("LoadSession: want %q, got %q", want, got)
	}

	if err := store.Forget(); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if _, err := store.LoadSession(ctx); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("LoadSession after Forget: want session.ErrNotFound, got %v", err)
	}
}

func TestSessionStore_KeyIncludesPhone(t *testing.T) {
	t.Parallel()
	a := NewSessionStore(newFakeSecretStore(), "+79991111111")
	b := NewSessionStore(newFakeSecretStore(), "+79992222222")
	if a.Key() == b.Key() {
		t.Fatalf("Key collision between distinct phones: %q", a.Key())
	}
}
