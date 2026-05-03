package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// DownloadedFile is the persistent record of a media file that the user
// has previously downloaded. Used by core/files.DedupCache to short-circuit
// re-downloads of the same file when the user presses Ctrl-D twice.
type DownloadedFile struct {
	FileID       int64
	AccessHash   int64
	Path         string
	Size         int64
	DownloadedAt time.Time
}

// SaveDownloadedFile records a finished download. Idempotent — a second
// call with the same FileID overwrites the previous row, which is the
// desired behaviour when a user re-downloads the same media to a
// different on-disk location (e.g. they moved ~/Downloads).
func (r *Repo) SaveDownloadedFile(ctx context.Context, f DownloadedFile) error {
	if r.readOnly.Load() {
		return ErrReadOnly
	}
	if f.FileID == 0 {
		return errors.New("downloaded_file: file_id is required")
	}
	if f.Path == "" {
		return errors.New("downloaded_file: path is required")
	}
	at := f.DownloadedAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := r.db.ExecContext(ctx, `
        INSERT INTO downloaded_files (file_id, access_hash, path, size, downloaded_at)
        VALUES (?, ?, ?, ?, ?)
        ON CONFLICT(file_id) DO UPDATE SET
            access_hash   = excluded.access_hash,
            path          = excluded.path,
            size          = excluded.size,
            downloaded_at = excluded.downloaded_at
    `, f.FileID, nullableInt64(f.AccessHash), f.Path, nullableInt64(f.Size), at.UTC().Unix())
	if err != nil {
		return fmt.Errorf("save downloaded_file %d: %w", f.FileID, err)
	}
	return nil
}

// GetDownloadedPath returns the on-disk path the file with fileID was last
// downloaded to. ok is false when no record exists. Returning a separate
// ok flag avoids forcing callers to compare against a magic empty string,
// which would be ambiguous with a row whose path was somehow recorded
// empty (defensive — SaveDownloadedFile rejects empty paths).
func (r *Repo) GetDownloadedPath(ctx context.Context, fileID int64) (path string, size int64, ok bool, err error) {
	if fileID == 0 {
		return "", 0, false, errors.New("downloaded_file: file_id is required")
	}
	var (
		p  string
		sz sql.NullInt64
	)
	row := r.db.QueryRowContext(ctx, `
        SELECT path, size FROM downloaded_files WHERE file_id = ?
    `, fileID)
	if err := row.Scan(&p, &sz); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, false, nil
		}
		return "", 0, false, fmt.Errorf("get downloaded_file %d: %w", fileID, err)
	}
	return p, sz.Int64, true, nil
}
