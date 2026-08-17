package files

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// DefaultRootEnv is the env var consulted by NewFileStoreDefault when no
// explicit root is configured. Mirrors $XDG_DOWNLOAD_DIR but lazytg keeps
// its files in a dedicated sub-directory so they cannot collide with
// browser downloads or Finder drag-drops.
const DefaultRootEnv = "LAZYTG_DOWNLOADS"

// DefaultRootSubdir is appended to the user's home directory when no
// override is configured. ~/Downloads/lazytg is the documented location
// — discoverable via Finder and not buried inside ~/.local where users
// rarely look for media.
const DefaultRootSubdir = "Downloads/lazytg"

// dirPerm / filePerm match the SECURITY.md baseline: per-user files
// only, so a multi-user system cannot side-channel Telegram media off
// the local disk via a 0644 file landing in a world-readable directory.
const (
	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600
)

// FileStore decides on-disk paths for downloaded media. It does not own
// or open any files itself — DownloadService passes the resolved path
// into the gotd downloader, which writes the bytes. Keeping
// path-decision and byte-streaming separate makes both halves trivially
// testable.
type FileStore struct {
	root string
	log  *slog.Logger
}

// NewFileStore returns a FileStore rooted at root. root is resolved to an
// absolute path so subsequent Path() callers do not need to depend on
// the process's working directory. The directory itself is *not* created
// here — EnsureDir does that lazily so an unused FileStore (e.g. one
// constructed by `lazytg --help`) does not leave a stub directory tree.
func NewFileStore(root string, log *slog.Logger) (*FileStore, error) {
	if root == "" {
		return nil, errors.New("filestore: root is required")
	}
	if log == nil {
		log = slog.New(discardHandler{})
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("filestore: abs %q: %w", root, err)
	}
	return &FileStore{root: abs, log: log}, nil
}

// NewFileStoreDefault picks the production default location: the
// $LAZYTG_DOWNLOADS env var if set, else <home>/Downloads/lazytg.
func NewFileStoreDefault(log *slog.Logger) (*FileStore, error) {
	if v := os.Getenv(DefaultRootEnv); v != "" {
		return NewFileStore(v, log)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("filestore: user home dir: %w", err)
	}
	return NewFileStore(filepath.Join(home, DefaultRootSubdir), log)
}

// Root returns the absolute root directory the store will write into.
// Exposed so the wiring layer can log it once at startup and so tests
// can assert on the resolved location without round-tripping through a
// subsequent Path() call.
func (s *FileStore) Root() string { return s.root }

// Path returns the absolute path where a file with filename received in
// chatTitle should be written. chatTitle is sanitised (path separators,
// "..", null bytes replaced with '_') so a malicious or merely awkward
// chat name cannot escape the root. filename is sanitised the same way
// so a sender cannot pin a malicious extension into a path the user
// did not authorise.
func (s *FileStore) Path(chatTitle, filename string) string {
	dir := sanitizeName(chatTitle)
	if dir == "" {
		dir = "_"
	}
	name := sanitizeName(filename)
	if name == "" {
		name = "file"
	}
	return filepath.Join(s.root, dir, name)
}

// EnsureDir mkdir-p's the parent directory of path with 0700
// permissions. Returns nil if the directory already exists with the
// required perms.
//
// We deliberately do not enforce 0700 on an existing directory the
// user has loosened on purpose (e.g. they share their Downloads tree
// over Samba): logging a warning here would surface the surprise
// without blocking the download.
func (s *FileStore) EnsureDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return errors.New("filestore: path has no parent")
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("filestore: mkdir %q: %w", dir, err)
	}
	if info, err := os.Stat(dir); err == nil && info.Mode().Perm() != dirPerm {
		s.log.Warn("filestore: directory perms are looser than 0700",
			"path", dir, "mode", info.Mode().Perm())
	}
	return nil
}

// Exists reports whether the path is a regular file the OS can open.
// Permission errors are reported via err so the caller can decide
// (e.g. show a "permission denied" toast) instead of treating them as
// "missing".
func (s *FileStore) Exists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("filestore: stat %q: %w", path, err)
	}
	return info.Mode().IsRegular(), nil
}

// FilePerm is exposed so DownloadService can chmod the freshly-renamed
// final file to the same baseline the rest of the storage pipeline
// uses (0600). Centralising the constant keeps the security promise in
// one place.
func (s *FileStore) FilePerm() os.FileMode { return filePerm }

// sanitizeName replaces every path-special character (/, \, null) and
// every leading/trailing dot or whitespace with '_'. Empty strings are
// returned as empty so the caller can pick a fallback. Path traversal
// segments ("..") are flattened to "_" because filepath.Join already
// collapses them, which would let a sender escape the chat directory.
//
// The sanitisation is intentionally conservative: we accept that some
// otherwise-legal filename characters (e.g. ':' on macOS, '<' on
// Windows) are replaced even when running on a tolerant filesystem. A
// predictable, portable name is more useful than a perfectly-faithful
// one.
func sanitizeName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "." || s == ".." {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '/' || r == '\\' || r == 0:
			b.WriteByte('_')
		case r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|':
			// Windows-illegal characters; replaced unconditionally so
			// a Telegram filename can later round-trip onto a Windows
			// disk via syncthing / dropbox without a rename.
			b.WriteByte('_')
		case r < 0x20:
			// Control characters in filenames are ill-advised on every OS.
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	// Strip leading/trailing dots so the entry is not hidden on Unix
	// and not silently truncated on Windows.
	out = strings.Trim(out, ". ")
	if out == "" || out == "." || out == ".." {
		return ""
	}
	return out
}
