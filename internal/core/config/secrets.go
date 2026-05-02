package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"filippo.io/age"
	"github.com/zalando/go-keyring"
)

// keyringService is the service name used in the OS credential store. Keep
// stable across versions so existing users do not lose stored sessions on
// upgrade.
const keyringService = "lazytg"

// secretsFileName is the filename of the age-encrypted JSON store written to
// ConfigDir() when the OS keyring is unavailable (typical on headless Linux).
const secretsFileName = "secrets.age"

// fileMode is the permission applied to the age-encrypted secrets file. We
// fail-fast if the file on disk has any other permission bits set.
const fileMode os.FileMode = 0o600

// ErrSecretNotFound is returned by every SecretStore when the requested key is
// missing. Callers should compare with errors.Is, not direct equality, so the
// implementations are free to wrap.
var ErrSecretNotFound = errors.New("secret not found")

// SecretStore stores small string secrets (Telegram session blobs, API hashes,
// etc.) keyed by an opaque user-supplied identifier.
type SecretStore interface {
	Get(key string) (string, error)
	Set(key, value string) error
	Delete(key string) error
}

// PassphrasePrompter is invoked on first use of AgeFileStore (and after the
// process restarts) to obtain the master passphrase. It is intentionally a
// function so callers can inject a TUI prompt, an env-var, or a hardcoded
// value in tests.
type PassphrasePrompter func() (string, error)

// NewSecretStore tries the OS keyring first and falls back to an
// age-encrypted file when the keyring is unsupported (no D-Bus, etc.).
//
// The probe call uses Set/Delete with a synthetic key because go-keyring's
// Get returns ErrNotFound for both "missing entry" and "no backend at all"
// on some platforms; the only reliable probe is a write.
func NewSecretStore(configDir string, prompter PassphrasePrompter) (SecretStore, error) {
	ks := &KeyringStore{Service: keyringService}
	if ks.available() {
		return ks, nil
	}
	if prompter == nil {
		return nil, errors.New("keyring unavailable and no passphrase prompter provided")
	}
	return NewAgeFileStore(filepath.Join(configDir, secretsFileName), prompter)
}

// KeyringStore is a SecretStore backed by github.com/zalando/go-keyring (which
// in turn talks to Keychain / Secret Service / wincred).
type KeyringStore struct {
	Service string
}

// available probes the keyring backend by writing and deleting a sentinel
// entry. Returning false on any error keeps us defensive: a half-working
// backend is worse than the encrypted-file fallback.
func (k *KeyringStore) available() bool {
	const probeKey = "__lazytg_probe__"
	if err := keyring.Set(k.Service, probeKey, "ok"); err != nil {
		return false
	}
	_ = keyring.Delete(k.Service, probeKey)
	return true
}

// Get returns ErrSecretNotFound when the OS reports no entry, so callers can
// errors.Is against a stable sentinel regardless of backend.
func (k *KeyringStore) Get(key string) (string, error) {
	v, err := keyring.Get(k.Service, key)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", ErrSecretNotFound
		}
		return "", fmt.Errorf("keyring get %q: %w", key, err)
	}
	return v, nil
}

// Set unconditionally overwrites the existing entry — Telegram sessions are
// rotated regularly and we never want to keep a stale value.
func (k *KeyringStore) Set(key, value string) error {
	if err := keyring.Set(k.Service, key, value); err != nil {
		return fmt.Errorf("keyring set %q: %w", key, err)
	}
	return nil
}

// Delete is a no-op (returns nil) when the entry is already gone. This makes
// "logout twice" idempotent.
func (k *KeyringStore) Delete(key string) error {
	if err := keyring.Delete(k.Service, key); err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("keyring delete %q: %w", key, err)
	}
	return nil
}

// AgeFileStore persists secrets as an age-encrypted JSON map on disk. Used as
// fallback when no OS keyring is available (headless Linux, CI, Docker).
//
// The passphrase is obtained once via the prompter and cached for the
// lifetime of the process. We do not persist it; if the process exits, the
// next run will prompt again.
type AgeFileStore struct {
	path     string
	prompter PassphrasePrompter

	mu         sync.Mutex
	passphrase string
	cache      map[string]string
	loaded     bool
}

// NewAgeFileStore returns an AgeFileStore backed by path. The file is created
// lazily on first Set; the prompter is also invoked lazily on first read or
// write. A non-existent file is treated as an empty store.
//
// We refuse to use a file with permission bits other than 0600 — this is the
// fail-fast that the spike calls out for session storage, applied to the
// secrets fallback as well.
func NewAgeFileStore(path string, prompter PassphrasePrompter) (*AgeFileStore, error) {
	if prompter == nil {
		return nil, errors.New("age file store: prompter is required")
	}
	if path == "" {
		return nil, errors.New("age file store: path is required")
	}
	if info, err := os.Stat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("age file store: %q is not a regular file", path)
		}
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			return nil, fmt.Errorf("age file store: insecure permissions %#o on %q (want 0600)", mode, path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("age file store: stat %q: %w", path, err)
	}
	return &AgeFileStore{path: path, prompter: prompter}, nil
}

// Get reads from the in-memory cache, lazily loading and decrypting the file
// on first call. Returns ErrSecretNotFound for unknown keys.
func (a *AgeFileStore) Get(key string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.ensureLoaded(); err != nil {
		return "", err
	}
	v, ok := a.cache[key]
	if !ok {
		return "", ErrSecretNotFound
	}
	return v, nil
}

// Set updates the cache and rewrites the encrypted file atomically (write to
// temp + rename). The passphrase is solicited lazily on first access.
func (a *AgeFileStore) Set(key, value string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.ensureLoaded(); err != nil {
		return err
	}
	a.cache[key] = value
	return a.persist()
}

// Delete removes a key. Returns nil if the key was already absent so that
// logout flows are idempotent.
func (a *AgeFileStore) Delete(key string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.ensureLoaded(); err != nil {
		return err
	}
	if _, ok := a.cache[key]; !ok {
		return nil
	}
	delete(a.cache, key)
	return a.persist()
}

// ensureLoaded prompts for the passphrase (once) and decrypts the file. If
// the file does not exist we keep an empty cache and persist on the next
// write.
//
// On any error past the passphrase prompt we clear a.passphrase so the next
// call re-invokes the prompter — otherwise a single mistyped passphrase would
// lock the store for the rest of the process.
func (a *AgeFileStore) ensureLoaded() error {
	if a.loaded {
		return nil
	}
	if a.passphrase == "" {
		pw, err := a.prompter()
		if err != nil {
			return fmt.Errorf("age file store: prompt passphrase: %w", err)
		}
		if pw == "" {
			return errors.New("age file store: empty passphrase")
		}
		a.passphrase = pw
	}

	ok := false
	defer func() {
		if !ok {
			a.passphrase = ""
			a.cache = nil
		}
	}()

	raw, err := os.ReadFile(a.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		a.cache = make(map[string]string)
		a.loaded = true
		ok = true
		return nil
	case err != nil:
		return fmt.Errorf("age file store: read %q: %w", a.path, err)
	}
	id, err := age.NewScryptIdentity(a.passphrase)
	if err != nil {
		return fmt.Errorf("age file store: identity: %w", err)
	}
	r, err := age.Decrypt(bytes.NewReader(raw), id)
	if err != nil {
		return fmt.Errorf("age file store: decrypt: %w", err)
	}
	plain, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("age file store: read plaintext: %w", err)
	}
	a.cache = make(map[string]string)
	if len(plain) > 0 {
		if err := json.Unmarshal(plain, &a.cache); err != nil {
			return fmt.Errorf("age file store: unmarshal: %w", err)
		}
	}
	a.loaded = true
	ok = true
	return nil
}

// persist serialises the cache, encrypts with scrypt and writes atomically.
func (a *AgeFileStore) persist() error {
	plain, err := json.Marshal(a.cache)
	if err != nil {
		return fmt.Errorf("age file store: marshal: %w", err)
	}
	r, err := age.NewScryptRecipient(a.passphrase)
	if err != nil {
		return fmt.Errorf("age file store: recipient: %w", err)
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, r)
	if err != nil {
		return fmt.Errorf("age file store: encrypt: %w", err)
	}
	if _, err := w.Write(plain); err != nil {
		return fmt.Errorf("age file store: write plaintext: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("age file store: close encrypter: %w", err)
	}

	if err := writeFileAtomic(a.path, buf.Bytes(), fileMode); err != nil {
		return fmt.Errorf("age file store: write %q: %w", a.path, err)
	}
	return nil
}

// writeFileAtomic writes data to a temp file in the same directory, fsyncs the
// file, renames over the destination, and fsyncs the directory so the rename
// itself is durable across power loss. Always sets the requested mode on the
// temp file before the rename so the destination never appears with looser
// bits, even briefly.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".lazytg-secrets-*.age")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	// fsync the parent dir so the rename is durable. Errors here aren't fatal
	// (some filesystems / kernels don't support it) but we still try.
	// dir is filepath.Dir of the caller-supplied path; G304 false positive.
	if d, derr := os.Open(dir); derr == nil { //nolint:gosec // dir is derived from validated caller path
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
