package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/pgmac/lazytg/internal/core/domain"
	"github.com/pgmac/lazytg/internal/core/events"
)

// Downloader is the gotd-free contract DownloadService relies on for
// streaming bytes. The concrete implementation in internal/tg/files.go
// builds the right gotd InputFileLocation from info and writes the
// bytes into w.
type Downloader interface {
	Download(ctx context.Context, info domain.MediaInfo, w io.Writer, progress ProgressCallback) (int64, error)
}

// ProgressCallback mirrors tg.ProgressCallback but is re-declared in
// the core package so consumers do not have to import internal/tg.
// The two types are identical, so the wiring layer can pass a
// core/files.ProgressCallback wherever a tg.ProgressCallback is
// expected.
type ProgressCallback = func(bytesSoFar, totalBytes int64)

// EventPublisher is the slice of events.Bus DownloadService calls.
// Defining a narrow interface here lets unit tests collect emitted
// events without spinning up the bus.
type EventPublisher interface {
	Publish(ev events.Event)
}

// DownloadService coordinates a single Ctrl-D press end-to-end:
//
//  1. Consult DedupCache; if hit, emit Started + Completed with the
//     cached path so the UI can show "[已 photo.jpg] cached" without
//     re-streaming.
//  2. Resolve final on-disk path via FileStore + sanitised chat title.
//  3. Mkdir parent (0700), open <path>.partial for writing.
//  4. Stream via Downloader; throttled progress events on the bus.
//  5. On success: rename .partial → final, chmod 0600, persist dedup.
//  6. On error: delete .partial, emit Failed, return wrapped error.
//
// The service is safe for concurrent use across distinct fileIDs; two
// simultaneous downloads of the same fileID are not racey because the
// .partial filename is keyed by fileID, but the second writer will
// overwrite the first's bytes mid-flight. The UI prevents that by
// disabling Ctrl-D on a message whose download is already in-flight.
type DownloadService struct {
	tg       Downloader
	store    *FileStore
	dedup    *DedupCache
	bus      EventPublisher
	log      *slog.Logger
	progress *progressThrottler
}

// NewDownloadService validates non-nil deps and returns a ready service.
// log can be nil; a discarding logger is used in that case so call
// sites remain clean.
func NewDownloadService(tg Downloader, store *FileStore, dedup *DedupCache, bus EventPublisher, log *slog.Logger) (*DownloadService, error) {
	if tg == nil {
		return nil, errors.New("download: tg downloader is required")
	}
	if store == nil {
		return nil, errors.New("download: file store is required")
	}
	if dedup == nil {
		return nil, errors.New("download: dedup cache is required")
	}
	if log == nil {
		log = slog.New(discardHandler{})
	}
	return &DownloadService{
		tg:       tg,
		store:    store,
		dedup:    dedup,
		bus:      bus,
		log:      log,
		progress: newProgressThrottler(),
	}, nil
}

// Download is the public entry point. chatID + chatTitle drive the
// on-disk layout; info carries the MTProto coordinates. Returns the
// final on-disk path on success.
//
// info.FileID == 0 returns an error rather than producing a path: a
// missing FileID would defeat dedup and the gotd downloader cannot
// build an InputFileLocation without one.
func (s *DownloadService) Download(ctx context.Context, chatID int64, chatTitle string, info domain.MediaInfo) (string, error) {
	if info.FileID == 0 {
		return "", errors.New("download: info.FileID is required")
	}

	// Dedup: existing on-disk file shortcircuits the whole pipeline.
	if cached, ok, err := s.dedup.Lookup(ctx, info.FileID, s.store.Exists); err != nil {
		s.log.Warn("download: dedup lookup failed",
			"file_id", info.FileID, "err", err)
	} else if ok {
		s.publish(events.FileDownloadStarted{
			FileID: info.FileID, ChatID: chatID, Path: cached,
			Filename: info.Filename, Size: info.Size,
		})
		s.publish(events.FileDownloadCompleted{
			FileID: info.FileID, Path: cached, Size: info.Size,
		})
		return cached, nil
	}

	finalPath := s.store.Path(chatTitle, info.Filename)
	tmpPath := finalPath + ".partial"
	if err := s.store.EnsureDir(finalPath); err != nil {
		s.failure(info.FileID, fmt.Errorf("ensure dir: %w", err))
		return "", err
	}
	// O_TRUNC because a stale .partial from a previous failed run would
	// otherwise be appended to and produce a corrupt file.
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, s.store.FilePerm()) //nolint:gosec
	if err != nil {
		s.failure(info.FileID, fmt.Errorf("open tmp: %w", err))
		return "", err
	}

	s.publish(events.FileDownloadStarted{
		FileID: info.FileID, ChatID: chatID, Path: finalPath,
		Filename: info.Filename, Size: info.Size,
	})

	// progressThrottler is single-flight per FileID; reset state so a
	// second Download call after a previous failure starts from 0%.
	s.progress.reset(info.FileID)

	cb := func(bytes, total int64) {
		if !s.progress.shouldEmit(info.FileID, bytes, total) {
			return
		}
		s.publish(events.FileDownloadProgress{
			FileID: info.FileID, BytesDownloaded: bytes, TotalBytes: total,
		})
	}

	written, dlErr := s.tg.Download(ctx, info, f, cb)
	if cerr := f.Close(); cerr != nil && dlErr == nil {
		dlErr = fmt.Errorf("close tmp: %w", cerr)
	}
	if dlErr != nil {
		_ = os.Remove(tmpPath)
		s.failure(info.FileID, dlErr)
		return "", dlErr
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		s.failure(info.FileID, fmt.Errorf("rename: %w", err))
		return "", err
	}
	// Chmod after rename to enforce the SECURITY.md baseline regardless
	// of the user's umask: open(0o600) honours umask & is therefore
	// best-effort, while os.Chmod sets the bits unconditionally.
	if err := os.Chmod(finalPath, s.store.FilePerm()); err != nil {
		s.log.Warn("download: chmod final file failed",
			"path", finalPath, "err", err)
	}

	if err := s.dedup.Record(ctx, DedupRecord{
		FileID:     info.FileID,
		AccessHash: info.AccessHash,
		Path:       finalPath,
		Size:       written,
	}); err != nil {
		// Persist failure is non-fatal: the file is on disk and usable.
		// The next Download call will simply re-stream the bytes.
		s.log.Warn("download: dedup record failed",
			"file_id", info.FileID, "err", err)
	}

	s.publish(events.FileDownloadCompleted{
		FileID: info.FileID, Path: finalPath, Size: written,
	})
	return finalPath, nil
}

// failure publishes a Failed event and logs the error. Centralised
// because every error path (mkdir, open, stream, rename) needs the
// same teardown shape.
func (s *DownloadService) failure(fileID int64, err error) {
	if err == nil {
		return
	}
	s.log.Warn("download: failed", "file_id", fileID, "err", err)
	s.publish(events.FileDownloadFailed{FileID: fileID, Err: err.Error()})
}

// publish forwards ev to the bus when one is configured. Tests pass a
// nil bus to drop events on the floor; production always wires the
// real bus.
func (s *DownloadService) publish(ev events.Event) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(ev)
}
