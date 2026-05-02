//go:build !windows

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureDir_TightensExistingLooseMode verifies that ensureDir does not
// silently keep a pre-existing directory at insecure permissions. The XDG
// bases for lazytg may already exist (e.g. another app created
// ~/.config/lazytg at 0755) and os.MkdirAll would not chmod them down.
func TestEnsureDir_TightensExistingLooseMode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "lazytg")

	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := ensureDir(path)
	if err != nil {
		t.Fatalf("ensureDir: %v", err)
	}
	if got != path {
		t.Fatalf("ensureDir returned %q, want %q", got, path)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != dirMode {
		t.Fatalf("permissions not tightened: got %#o, want %#o", mode, dirMode)
	}
}

// TestEnsureDir_CreatesWithCorrectMode covers the fresh-create path: a
// directory that does not yet exist must be created with dirMode.
func TestEnsureDir_CreatesWithCorrectMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lazytg")
	if _, err := ensureDir(path); err != nil {
		t.Fatalf("ensureDir: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != dirMode {
		t.Fatalf("permissions: got %#o, want %#o", mode, dirMode)
	}
}
