// Package config implements XDG-compliant path resolution and the secret store
// abstraction used by the rest of lazytg. It is part of the core layer and must
// stay free of gotd / bubbletea imports (enforced by depguard).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// appName is the leaf directory used inside every XDG base. Centralised so it
// is easy to change (e.g. when forking the project).
const appName = "lazytg"

// dirMode is the permission bit applied to directories created by this package.
// We default to 0700 because they hold sessions and secrets-adjacent state.
const dirMode os.FileMode = 0o700

// Paths bundles the XDG directories used by lazytg. Construct via Resolve so
// each directory is guaranteed to exist with 0700 permissions.
type Paths struct {
	Config string
	Data   string
	State  string
	Cache  string
}

// Resolve returns Paths with all four directories created (or verified) on
// disk. Returns the first error encountered; callers should treat any failure
// here as fatal because secrets storage relies on these locations.
func Resolve() (Paths, error) {
	cfg, err := ConfigDir()
	if err != nil {
		return Paths{}, err
	}
	data, err := DataDir()
	if err != nil {
		return Paths{}, err
	}
	state, err := StateDir()
	if err != nil {
		return Paths{}, err
	}
	cache, err := CacheDir()
	if err != nil {
		return Paths{}, err
	}
	return Paths{Config: cfg, Data: data, State: state, Cache: cache}, nil
}

// ConfigDir returns ~/.config/lazytg (or platform equivalent) and ensures the
// directory exists with 0700 permissions.
//
//revive:disable-next-line:exported // stutter is intentional; "Dir" alone is too generic
func ConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("user config dir: %w", err)
	}
	return ensureDir(filepath.Join(base, appName))
}

// DataDir returns the XDG data directory for lazytg. On Linux this honours
// XDG_DATA_HOME (default ~/.local/share); on darwin/windows it falls back to
// the platform-specific data location.
func DataDir() (string, error) {
	base, err := userDataDir()
	if err != nil {
		return "", fmt.Errorf("user data dir: %w", err)
	}
	return ensureDir(filepath.Join(base, appName))
}

// StateDir returns the XDG state directory. On Linux: $XDG_STATE_HOME or
// ~/.local/state. On other platforms we fall back to DataDir.
func StateDir() (string, error) {
	base, err := userStateDir()
	if err != nil {
		return "", fmt.Errorf("user state dir: %w", err)
	}
	return ensureDir(filepath.Join(base, appName))
}

// CacheDir returns ~/.cache/lazytg (or platform equivalent).
func CacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("user cache dir: %w", err)
	}
	return ensureDir(filepath.Join(base, appName))
}

// ensureDir mkdir -p with mode 0700 and returns the path.
func ensureDir(path string) (string, error) {
	if err := os.MkdirAll(path, dirMode); err != nil {
		return "", fmt.Errorf("mkdir %q: %w", path, err)
	}
	return path, nil
}

// userDataDir mirrors the unexported helper that Go's stdlib uses for
// os.UserCacheDir / os.UserConfigDir but for the data directory.
func userDataDir() (string, error) {
	switch runtime.GOOS {
	case "linux", "freebsd", "netbsd", "openbsd", "dragonfly":
		if v := os.Getenv("XDG_DATA_HOME"); v != "" {
			return v, nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share"), nil
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support"), nil
	case "windows":
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return v, nil
		}
		return "", errors.New("LOCALAPPDATA not set")
	default:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share"), nil
	}
}

// userStateDir resolves the XDG state directory or a sensible per-OS fallback.
func userStateDir() (string, error) {
	switch runtime.GOOS {
	case "linux", "freebsd", "netbsd", "openbsd", "dragonfly":
		if v := os.Getenv("XDG_STATE_HOME"); v != "" {
			return v, nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "state"), nil
	default:
		return userDataDir()
	}
}
