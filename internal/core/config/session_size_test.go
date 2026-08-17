package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// sessionSizedValue is the shape of the payload that broke session storage: a
// gotd session blob measured at 4196 bytes (auth key plus the full DC config).
// Rounded up so the test does not sit exactly on the boundary it is about.
const sessionSizedValue = 4500

// TestSecretStore_HoldsSessionSizedValue is the regression that matters.
//
// Before secrets moved into the age file, NewSecretStore returned the keyring
// directly and a session-sized write failed with ErrSetDataTooBig. Because gotd
// calls StoreSession during connection setup, the rejected write tore the
// connection down, `lazytg login` reported success anyway (gotd's Run returned
// nil), and no session was ever persisted on macOS. A store that cannot hold a
// session is not a session store.
func TestSecretStore_HoldsSessionSizedValue(t *testing.T) {
	dir := t.TempDir()
	prompter := func() (string, error) { return "correct-horse-battery-staple", nil }

	store, err := NewSecretStore(dir, prompter)
	if err != nil {
		t.Fatalf("NewSecretStore: %v", err)
	}

	blob := strings.Repeat("s", sessionSizedValue)
	if err := store.Set("session:+79991112233", blob); err != nil {
		t.Fatalf("Set of a %d-byte session blob: %v", len(blob), err)
	}
	got, err := store.Get("session:+79991112233")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != blob {
		t.Fatalf("round-trip mismatch: got %d bytes, want %d", len(got), len(blob))
	}

	// The blob must be on disk encrypted, not in the keyring and not in clear.
	raw, err := os.ReadFile(filepath.Join(dir, secretsFileName))
	if err != nil {
		t.Fatalf("read secrets file: %v", err)
	}
	if strings.Contains(string(raw), blob) {
		t.Error("secrets file contains the session blob verbatim — it is not encrypted")
	}
}

// TestKeyringStore_RejectsSessionSizedValue pins the limit that forced the
// design, so that "why not just use the keyring?" has an answer that fails a
// test rather than one that has to be re-derived.
//
// macOS only: go-keyring's size guard lives in its darwin backend, where the
// secret is passed through a `security -i` command line capped at 4096 bytes.
// Other platforms have their own limits (wincred: 2500) or none, and asserting
// a failure there would be asserting someone else's implementation detail.
func TestKeyringStore_RejectsSessionSizedValue(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("go-keyring's 4096-byte command-line limit is darwin-specific (running on %s)", runtime.GOOS)
	}
	ks := &KeyringStore{Service: keyringService}
	if !ks.available() {
		t.Skip("no usable keyring in this environment")
	}

	const key = "__lazytg_size_probe__"
	t.Cleanup(func() { _ = ks.Delete(key) })

	err := ks.Set(key, strings.Repeat("s", sessionSizedValue))
	if err == nil {
		t.Skip("this keyring accepted a session-sized value — the limit that motivated the age file no longer applies here")
	}
	if !errors.Is(err, keyring.ErrSetDataTooBig) {
		t.Fatalf("Set failed with %v, want ErrSetDataTooBig — if the failure mode changed, revisit NewSecretStore's reasoning", err)
	}
}

// TestKeyringPassphrase_GeneratesOnceAndReuses covers the passphrase provider:
// the first call must mint and store one, the second must return the same value.
// A provider that generated a fresh passphrase per call would leave the secrets
// file undecryptable on the next start — a silent logout.
func TestKeyringPassphrase_GeneratesOnceAndReuses(t *testing.T) {
	ks := &KeyringStore{Service: keyringService}
	if !ks.available() {
		t.Skip("no usable keyring in this environment")
	}

	// Work against a scratch service so a developer's real passphrase is never
	// read, replaced, or deleted by the test run.
	scratch := &KeyringStore{Service: keyringService + "-test-passphrase"}
	t.Cleanup(func() { _ = scratch.Delete(keyringPassphraseKey) })
	if err := scratch.Delete(keyringPassphraseKey); err != nil {
		t.Fatalf("pre-clean: %v", err)
	}

	provider := KeyringPassphrase(scratch)
	first, err := provider()
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if first == "" {
		t.Fatal("first call returned an empty passphrase")
	}

	second, err := provider()
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if second != first {
		t.Errorf("passphrase changed between calls (%d vs %d bytes) — the secrets file would no longer decrypt",
			len(first), len(second))
	}

	// A fresh provider over the same keyring — the next process start — must
	// also see it.
	third, err := KeyringPassphrase(scratch)()
	if err != nil {
		t.Fatalf("fresh provider: %v", err)
	}
	if third != first {
		t.Errorf("a new provider minted a different passphrase — sessions would not survive a restart")
	}
}

// TestAgeFileStore_ConcurrentProcessesKeepBothKeys pins the cross-process lock.
//
// The store is one ciphertext, so every write rewrites every key. Before the
// file lock and the reload inside it, two processes sharing secrets.age dropped
// each other's sessions silently: the second account logged in through
// `lazytg login --account other` while the TUI was running would vanish on the
// TUI's next session refresh. Each *AgeFileStore below stands in for one
// process — separate instances with separate caches over one file.
func TestAgeFileStore_ConcurrentProcessesKeepBothKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, secretsFileName)
	pw := func() (string, error) { return "shared-passphrase", nil }

	tui, err := NewAgeFileStore(path, pw)
	if err != nil {
		t.Fatalf("store A: %v", err)
	}
	if err := tui.Set("session:+1", "A-session"); err != nil {
		t.Fatalf("A seed: %v", err)
	}

	// A second process starts, sees the file as it is now, and adds an account.
	login, err := NewAgeFileStore(path, pw)
	if err != nil {
		t.Fatalf("store B: %v", err)
	}
	if err := login.Set("session:+2", "B-session"); err != nil {
		t.Fatalf("B set: %v", err)
	}

	// The first process refreshes its own session, as gotd does on reconnect.
	if err := tui.Set("session:+1", "A-session-refreshed"); err != nil {
		t.Fatalf("A refresh: %v", err)
	}

	// A cold reader — the next process start — must see both.
	cold, err := NewAgeFileStore(path, pw)
	if err != nil {
		t.Fatalf("store C: %v", err)
	}
	if got, err := cold.Get("session:+1"); err != nil || got != "A-session-refreshed" {
		t.Errorf("session:+1 = %q, err=%v; want the refreshed value", got, err)
	}
	if got, err := cold.Get("session:+2"); err != nil {
		t.Errorf("session:+2 lost: %v — a write from one process clobbered the other's key", err)
	} else if got != "B-session" {
		t.Errorf("session:+2 = %q, want %q", got, "B-session")
	}
}

// TestAgeFileStore_WrongPassphraseIsNonDestructive covers the recovery case: a
// keyring entry that was cleared or replaced means the provider yields a
// passphrase that no longer opens the file.
//
// The requirement is that this fails rather than "recovers" by overwriting: the
// file is the only copy of the session, and a store that re-encrypts it under a
// new passphrase would turn a keyring hiccup into a permanent logout.
func TestAgeFileStore_WrongPassphraseIsNonDestructive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, secretsFileName)

	original, err := NewAgeFileStore(path, func() (string, error) { return "passphrase-one", nil })
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := original.Set("session:+7", "important-session-blob"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	wrong, err := NewAgeFileStore(path, func() (string, error) { return "passphrase-two", nil })
	if err != nil {
		t.Fatalf("store2: %v", err)
	}
	if _, err := wrong.Get("session:+7"); err == nil {
		t.Error("Get with the wrong passphrase succeeded")
	}
	if err := wrong.Set("session:+7", "clobbered"); err == nil {
		t.Error("Set with the wrong passphrase succeeded — the file was re-encrypted under it")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("the secrets file changed despite the wrong passphrase — silent data loss")
	}

	recovered, err := NewAgeFileStore(path, func() (string, error) { return "passphrase-one", nil })
	if err != nil {
		t.Fatalf("store3: %v", err)
	}
	got, err := recovered.Get("session:+7")
	if err != nil {
		t.Fatalf("the original passphrase no longer opens the file: %v", err)
	}
	if got != "important-session-blob" {
		t.Errorf("value = %q, want it intact", got)
	}
}
