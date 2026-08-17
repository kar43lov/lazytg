package files

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// DedupRecord is the minimal persisted view of a previously-downloaded
// file. The repo layer translates between this and its own storage
// type so core/files stays free of database/sql.
type DedupRecord struct {
	FileID     int64
	AccessHash int64
	Path       string
	Size       int64
}

// DedupStore is the storage contract DedupCache depends on. The real
// implementation in internal/storage/sqlite.Repo satisfies this
// interface via two adapter methods. Unit tests provide an in-memory
// fake.
type DedupStore interface {
	GetDownloadedPath(ctx context.Context, fileID int64) (path string, size int64, ok bool, err error)
	SaveDownloadedFile(ctx context.Context, f DedupRecord) error
}

// DedupCache wraps a DedupStore so DownloadService can ask "did I
// already download this file?" without tying its body to a concrete
// repo type. Keeping the cache as a separate object makes the eventual
// switch to a different cache substrate (e.g. content-addressed blob
// store) a one-line change in the wiring layer.
type DedupCache struct {
	store DedupStore
}

// NewDedupCache wires a DedupCache against the given store. Returns an
// error when store is nil so the wiring layer fails loudly rather than
// silently disabling dedup.
func NewDedupCache(store DedupStore) (*DedupCache, error) {
	if store == nil {
		return nil, errors.New("dedup: store is required")
	}
	return &DedupCache{store: store}, nil
}

// Lookup returns (path, true) when fileID was previously downloaded and
// the cached path is still readable on disk. A cache hit whose file has
// since been deleted is treated as a miss so the next Download() call
// re-fetches the bytes — otherwise the user would be promised a path
// that yields ENOENT when they open it.
//
// pathChecker is the function used to verify the on-disk presence; in
// production this is FileStore.Exists. The injection keeps Lookup
// testable without a real filesystem.
func (c *DedupCache) Lookup(ctx context.Context, fileID int64, pathChecker func(string) (bool, error)) (string, bool, error) {
	if fileID == 0 {
		return "", false, nil
	}
	path, _, ok, err := c.store.GetDownloadedPath(ctx, fileID)
	if err != nil {
		return "", false, fmt.Errorf("dedup: lookup %d: %w", fileID, err)
	}
	if !ok {
		return "", false, nil
	}
	if pathChecker == nil {
		return path, true, nil
	}
	exists, err := pathChecker(path)
	if err != nil {
		return "", false, fmt.Errorf("dedup: check %q: %w", path, err)
	}
	if !exists {
		return "", false, nil
	}
	return path, true, nil
}

// Record persists a successful download so future Lookup calls
// short-circuit to it. The wall-clock timestamp is written through
// SaveDownloadedFile inside the repo layer (which defaults to time.Now
// when zero).
func (c *DedupCache) Record(ctx context.Context, rec DedupRecord) error {
	if rec.FileID == 0 {
		return errors.New("dedup: file_id is required")
	}
	if rec.Path == "" {
		return errors.New("dedup: path is required")
	}
	return c.store.SaveDownloadedFile(ctx, rec)
}

// nowFunc abstracts time.Now so the dedup_test.go suite can assert on
// the recorded timestamp without race conditions.
type nowFunc = func() time.Time

var _ nowFunc = time.Now // compile-time anchor for the alias.
