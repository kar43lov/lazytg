//go:build unix

package config

import (
	"fmt"
	"os"
	"syscall"
)

// lockFile takes an exclusive advisory lock on path, creating the file if it is
// absent, and returns the unlock function.
//
// The lock lives on a sidecar file rather than on the secrets file itself:
// persistMap replaces the secrets file by rename, which swaps the inode, so a
// lock held on it would protect a file nobody is reading any more.
func lockFile(path string) (unlock func(), err error) {
	// G304: path is not attacker-controlled — the only caller builds it as the
	// store's own secrets path plus lockSuffix, and that path comes from the
	// resolved config dir, never from message or network data.
	//nolint:gosec
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, fileMode)
	if err != nil {
		return nil, fmt.Errorf("open lock %q: %w", path, err)
	}
	// Blocking lock on purpose: the critical section is one decrypt plus one
	// encrypt (a few hundred milliseconds), and a caller that gave up here
	// would be back to losing writes, which is the bug this exists to fix.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %q: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
