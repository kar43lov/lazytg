package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestKeyringStore_RoundTrip(t *testing.T) {
	keyring.MockInit()

	store := &KeyringStore{Service: "lazytg-test"}

	if _, err := store.Get("missing"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("Get on missing key: want ErrSecretNotFound, got %v", err)
	}
	if err := store.Set("session:+7000", "blob"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := store.Get("session:+7000")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "blob" {
		t.Fatalf("Get: want %q, got %q", "blob", got)
	}
	if err := store.Delete("session:+7000"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get("session:+7000"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("Get after Delete: want ErrSecretNotFound, got %v", err)
	}
	// Idempotent delete.
	if err := store.Delete("session:+7000"); err != nil {
		t.Fatalf("Delete idempotency: %v", err)
	}
}

func TestAgeFileStore_RoundTripAndPersistence(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, secretsFileName)

	prompter := func() (string, error) { return "correct-horse-battery-staple", nil }

	store, err := NewAgeFileStore(path, prompter)
	if err != nil {
		t.Fatalf("NewAgeFileStore: %v", err)
	}

	if _, err := store.Get("session:+7000"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("Get missing: want ErrSecretNotFound, got %v", err)
	}
	if err := store.Set("session:+7000", "secret"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after Set: %v", err)
	}
	if mode := info.Mode().Perm(); mode != fileMode {
		t.Fatalf("file perm: want %#o, got %#o", fileMode, mode)
	}

	// Re-open from disk with the same passphrase to prove persistence.
	store2, err := NewAgeFileStore(path, prompter)
	if err != nil {
		t.Fatalf("NewAgeFileStore reopen: %v", err)
	}
	got, err := store2.Get("session:+7000")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if got != "secret" {
		t.Fatalf("Get after reopen: want %q, got %q", "secret", got)
	}

	if err := store2.Delete("session:+7000"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store2.Get("session:+7000"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("Get after Delete: want ErrSecretNotFound, got %v", err)
	}
}

func TestAgeFileStore_WrongPassphraseFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, secretsFileName)

	good, err := NewAgeFileStore(path, func() (string, error) { return "good-passphrase", nil })
	if err != nil {
		t.Fatalf("NewAgeFileStore: %v", err)
	}
	if err := good.Set("k", "v"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	bad, err := NewAgeFileStore(path, func() (string, error) { return "bad-passphrase", nil })
	if err != nil {
		t.Fatalf("NewAgeFileStore (bad): %v", err)
	}
	_, err = bad.Get("k")
	if err == nil || !strings.Contains(err.Error(), "decrypt") {
		t.Fatalf("Get with wrong passphrase: want decrypt error, got %v", err)
	}
}

func TestAgeFileStore_RejectsLoosePermissions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, secretsFileName)

	if err := os.WriteFile(path, []byte("dummy"), 0o644); err != nil {
		t.Fatalf("write loose file: %v", err)
	}
	_, err := NewAgeFileStore(path, func() (string, error) { return "x", nil })
	if err == nil || !strings.Contains(err.Error(), "insecure permissions") {
		t.Fatalf("expected insecure-permissions error, got %v", err)
	}
}

func TestAgeFileStore_RejectsNilPrompter(t *testing.T) {
	t.Parallel()
	_, err := NewAgeFileStore(filepath.Join(t.TempDir(), "x.age"), nil)
	if err == nil {
		t.Fatalf("expected error on nil prompter")
	}
}

func TestAgeFileStore_EmptyPassphraseFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, secretsFileName)

	store, err := NewAgeFileStore(path, func() (string, error) { return "", nil })
	if err != nil {
		t.Fatalf("NewAgeFileStore: %v", err)
	}
	if err := store.Set("k", "v"); err == nil {
		t.Fatalf("expected error on empty passphrase")
	}
}

func TestNewSecretStore_KeyringPath(t *testing.T) {
	keyring.MockInit()

	store, err := NewSecretStore(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewSecretStore: %v", err)
	}
	if _, ok := store.(*KeyringStore); !ok {
		t.Fatalf("want *KeyringStore, got %T", store)
	}
}

func TestNewSecretStore_FallbackRequiresPrompter(t *testing.T) {
	// Force the keyring backend to fail so we hit the fallback branch.
	keyring.MockInitWithError(errors.New("no D-Bus"))
	t.Cleanup(keyring.MockInit)

	if _, err := NewSecretStore(t.TempDir(), nil); err == nil {
		t.Fatalf("expected error when prompter is nil")
	}

	dir := t.TempDir()
	store, err := NewSecretStore(dir, func() (string, error) { return "p", nil })
	if err != nil {
		t.Fatalf("NewSecretStore (fallback): %v", err)
	}
	if _, ok := store.(*AgeFileStore); !ok {
		t.Fatalf("want *AgeFileStore fallback, got %T", store)
	}
}

func TestPaths_Resolve(t *testing.T) {
	// Override only XDG_*_HOME so we don't touch the real user's dirs. We
	// can't override os.UserCacheDir / os.UserConfigDir on macOS via env,
	// so this test is informational on darwin and exact on linux.
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "cfg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmp, "cache"))

	p, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, dir := range []string{p.Config, p.Data, p.State, p.Cache} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("dir %q not created: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("%q is not a dir", dir)
		}
		if mode := info.Mode().Perm(); mode != dirMode {
			// Best-effort: macOS may have additional ACLs but the
			// permission bits should still be 0700.
			t.Logf("dir %q mode = %#o (want %#o)", dir, mode, dirMode)
			if mode&0o077 != 0 {
				t.Fatalf("dir %q has world/group bits set: %#o", dir, mode)
			}
		}
		if !strings.HasSuffix(dir, appName) {
			t.Errorf("dir %q does not end with appName", dir)
		}
	}
}

// silence unused-import warning if any helper is removed.
var _ fs.FileMode = fileMode
