package config

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
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
// ConfigDir(). Every secret lives here; the keyring holds only the passphrase
// that decrypts it (see NewSecretStore for why sessions cannot go in the
// keyring directly).
const secretsFileName = "secrets.age"

// fileMode is the permission applied to the age-encrypted secrets file. We
// fail-fast if the file on disk has any other permission bits set.
const fileMode os.FileMode = 0o600

// lockSuffix names the sidecar lock file guarding read-modify-write cycles on
// the secrets file across processes. It holds no data.
const lockSuffix = ".lock"

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

// keyringPassphraseKey is the keyring entry holding the age passphrase. It is
// deliberately the only thing lazytg keeps in the OS credential store.
const keyringPassphraseKey = "age-passphrase"

// passphraseBytes is the entropy of a generated age passphrase. 32 random
// bytes is well beyond what scrypt needs and still tiny in the keyring.
const passphraseBytes = 32

// NewSecretStore returns the secret store: secrets live in an age-encrypted
// file, and the OS keyring holds only the passphrase that opens it.
//
// Sessions cannot live in the keyring directly. A gotd session blob is ~4.2 KB
// (auth key plus the full DC configuration), and go-keyring on macOS builds a
// `security -i` command line, refusing anything past 4096 bytes with
// ErrSetDataTooBig. The failure was not graceful: gotd calls StoreSession from
// its connection setup, so a rejected write tore the connection down and the
// in-flight auth-status RPC failed with "engine forcibly closed" — while
// `lazytg login` still printed success, because gotd's Run returned nil. The
// result was an app that could never keep a session on macOS at all. See
// TestKeyringStore_RejectsSessionSizedValue, which pins the limit.
//
// The passphrase is generated once and kept in the keyring, so a desktop user
// is never prompted. Without a keyring (headless Linux, CI, Docker) the caller's
// prompter supplies it, which is the pre-existing behaviour.
//
// The keyring probe uses Set/Delete with a synthetic key because go-keyring's
// Get returns ErrNotFound for both "missing entry" and "no backend at all" on
// some platforms; the only reliable probe is a write.
func NewSecretStore(configDir string, prompter PassphrasePrompter) (SecretStore, error) {
	path := filepath.Join(configDir, secretsFileName)
	ks := &KeyringStore{Service: keyringService}
	if ks.available() {
		return NewAgeFileStore(path, KeyringPassphrase(ks))
	}
	if prompter == nil {
		return nil, errors.New("keyring unavailable and no passphrase prompter provided")
	}
	return NewAgeFileStore(path, prompter)
}

// KeyringPassphrase returns a PassphrasePrompter that reads the age passphrase
// from the keyring, generating and storing one on first use.
//
// A read failure other than "not found" is reported rather than papered over
// with a fresh passphrase: generating a new one would leave the existing
// secrets file undecryptable, turning a transient keyring hiccup into a
// silent logout.
func KeyringPassphrase(ks *KeyringStore) PassphrasePrompter {
	return func() (string, error) {
		pw, err := ks.Get(keyringPassphraseKey)
		switch {
		case err == nil && pw != "":
			return pw, nil
		case err == nil:
			// An empty entry is corrupt rather than absent; replacing it is the
			// only way forward, and it cannot cost anything a valid passphrase
			// would have opened.
		case !errors.Is(err, ErrSecretNotFound):
			return "", fmt.Errorf("keyring: read age passphrase: %w", err)
		}

		buf := make([]byte, passphraseBytes)
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("generate age passphrase: %w", err)
		}
		pw = base64.RawStdEncoding.EncodeToString(buf)
		if err := ks.Set(keyringPassphraseKey, pw); err != nil {
			return "", fmt.Errorf("keyring: store age passphrase: %w", err)
		}
		return pw, nil
	}
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

// AgeFileStore persists secrets as an age-encrypted JSON map on disk. This is
// where every secret lives, including Telegram session blobs, which are too
// large for the OS keyring (see NewSecretStore).
//
// The passphrase is obtained once via the prompter and cached for the lifetime
// of the process. It is not written to disk: on a desktop the prompter reads it
// from the keyring, and without a keyring the next run asks the user again.
type AgeFileStore struct {
	path     string
	prompter PassphrasePrompter

	mu         sync.Mutex
	passphrase string
	cache      map[string]string
	loaded     bool

	// lastRaw is the ciphertext this store last read or wrote. It lets refresh
	// skip a decrypt when no other process has touched the file — scrypt costs
	// ~390ms, and paying it twice per write would be the price of a lock that
	// almost never has contention. nil means "the file was absent".
	lastRaw []byte
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
//
// We persist *before* committing the change to a.cache so that a failed write
// (full disk, revoked permissions, fsync error mid-run) does not leave the
// in-memory and on-disk views diverged. A subsequent Get in the same process
// must not see a value that survives a process restart only by luck.
func (a *AgeFileStore) Set(key, value string) error {
	return a.mutate(func(next map[string]string) bool {
		next[key] = value
		return true
	})
}

// mutate performs one read-modify-write cycle under both the process mutex and
// a cross-process file lock, re-reading the file inside the lock. apply reports
// whether anything changed; false skips the write entirely.
//
// The whole file is one ciphertext, so a write rewrites every key — which makes
// a stale in-memory snapshot destructive rather than merely outdated. Two
// processes sharing the store (the TUI plus a `lazytg login --account other` in
// another terminal, which is a documented flow) would otherwise silently drop
// each other's sessions: whoever wrote last would persist the map as it looked
// when *it* started. Verified before this locking existed —
// TestAgeFileStore_ConcurrentProcessesKeepBothKeys is that regression.
func (a *AgeFileStore) mutate(apply func(next map[string]string) bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	unlock, err := lockFile(a.path + lockSuffix)
	if err != nil {
		return fmt.Errorf("age file store: %w", err)
	}
	defer unlock()

	if err := a.refresh(); err != nil {
		return err
	}

	next := cloneCache(a.cache)
	if !apply(next) {
		return nil
	}
	if err := a.persistMap(next); err != nil {
		return err
	}
	a.cache = next
	return nil
}

// Delete removes a key. Returns nil if the key was already absent so that
// logout flows are idempotent. As with Set, the on-disk write happens before
// the cache is updated, so a persist failure leaves the previous state intact.
func (a *AgeFileStore) Delete(key string) error {
	return a.mutate(func(next map[string]string) bool {
		if _, ok := next[key]; !ok {
			return false
		}
		delete(next, key)
		return true
	})
}

// cloneCache returns a shallow copy of the secrets map. Returned map is
// guaranteed non-nil so callers can mutate without a nil-check.
func cloneCache(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

// refresh brings the cache in line with the file, decrypting only when the
// ciphertext on disk differs from what this store last read or wrote.
//
// Callers hold both the process mutex and the file lock. Comparing raw bytes
// rather than mtime keeps this correct for two writes inside one filesystem
// timestamp tick, and reading a few kilobytes is far cheaper than the scrypt
// derivation it avoids.
func (a *AgeFileStore) refresh() error {
	if !a.loaded {
		return a.ensureLoaded()
	}
	raw, err := os.ReadFile(a.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("age file store: read %q: %w", a.path, err)
	}
	if bytes.Equal(raw, a.lastRaw) {
		return nil
	}
	a.loaded = false
	return a.ensureLoaded()
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
		a.lastRaw = nil
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
		// age's own message here is "no identity matched any of the recipients",
		// which tells a user nothing about what to do. The realistic cause is a
		// keyring entry that was cleared or replaced, and the realistic remedy
		// is to drop the file and log in again — say both, and name neither the
		// passphrase nor any stored value.
		return fmt.Errorf("age file store: decrypt %q: %w — the passphrase does not open this file; "+
			"if the %q entry in the OS keyring was cleared or replaced, the stored sessions cannot be "+
			"recovered: delete the file and log in again", a.path, err, keyringPassphraseKey)
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
	a.lastRaw = raw
	a.loaded = true
	ok = true
	return nil
}

// persistMap serialises the supplied snapshot, encrypts with scrypt and writes
// atomically. Callers stage the desired state in a copy of the cache and only
// commit the copy to a.cache after this returns nil — see Set/Delete.
func (a *AgeFileStore) persistMap(snapshot map[string]string) error {
	plain, err := json.Marshal(snapshot)
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
	// Remember what we wrote so the next refresh recognises the file as ours
	// and skips a redundant decrypt.
	a.lastRaw = buf.Bytes()
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
