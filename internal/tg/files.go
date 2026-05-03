package tg

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/pgmac/lazytg/internal/core/domain"
	coresync "github.com/pgmac/lazytg/internal/core/sync"
)

// downloaderClient is the slice of *tg.Client that gotd's downloader needs.
// Pulled out as an interface so unit tests can substitute a stub without a
// full tgtest harness — the downloader.Client interface itself is the
// canonical reference, repeated here so downstream code does not import
// gotd.
type downloaderClient = downloader.Client

// Downloader streams a Telegram media file into an io.Writer using gotd's
// chunked downloader. Progress is reported through an optional callback so
// the UI status bar can render a per-file `⬇ filename 47%` widget without
// the downloader needing to know about events / channels.
//
// All gotd types stay confined to this file: callers in core/files pass a
// gotd-free domain.MediaInfo and receive (bytesDownloaded, error). The
// depguard rule (core ⊥ gotd) holds without further plumbing.
type Downloader struct {
	api downloaderClient
	log *slog.Logger
	// partSize matches gotd's default 512 KiB. Stored here so tests can
	// pin a smaller value without touching the package-level constant.
	partSize int
}

// downloaderDefaultPartSize is the chunk size we ask the gotd downloader to
// use. 512 KiB is the gotd default; matching it explicitly documents the
// trade-off (smaller parts → finer progress granularity but more RPCs).
const downloaderDefaultPartSize = 512 * 1024

// NewDownloader returns a Downloader bound to the given API client.
func NewDownloader(api downloaderClient, log *slog.Logger) *Downloader {
	if log == nil {
		log = slog.New(noopHandler{})
	}
	return &Downloader{api: api, log: log, partSize: downloaderDefaultPartSize}
}

// ProgressCallback is invoked from the download loop after each chunk has
// been written to the output. bytesSoFar is the cumulative byte count;
// totalBytes is the originally-advertised file size (0 when unknown — gotd
// does not always know up-front).
//
// The callback runs on the goroutine driving the download and must not
// block: heavy work (event publish, UI render) should be dispatched
// asynchronously. The wrapper guarantees at least one invocation
// (totalBytes, totalBytes) on a successful download so consumers can
// always treat "callback fired with bytesSoFar==totalBytes" as
// completion.
type ProgressCallback func(bytesSoFar, totalBytes int64)

// Download streams the media described by info into w. Returns the
// number of bytes written and any error from the underlying gotd
// pipeline. progress may be nil.
//
// FILE_REFERENCE_EXPIRED (FloodWaitError-shaped error from gotd, but
// distinct from a flood wait) is wrapped into ErrFileReferenceExpired so
// callers can decide whether to refetch the parent message and retry —
// without that branch the user would see a generic "download failed"
// after their cached reference aged past ~1h.
func (d *Downloader) Download(ctx context.Context, info domain.MediaInfo, w io.Writer, progress ProgressCallback) (int64, error) {
	if d.api == nil {
		return 0, fmt.Errorf("downloader: api client is nil")
	}
	if w == nil {
		return 0, fmt.Errorf("downloader: writer is nil")
	}
	loc, err := buildFileLocation(info)
	if err != nil {
		return 0, err
	}

	pw := &progressWriter{
		w:        w,
		progress: progress,
		total:    info.Size,
	}

	dl := downloader.NewDownloader().WithPartSize(d.partSize)
	if _, err := dl.Download(d.api, loc).Stream(ctx, pw); err != nil {
		if d, ok := tgerr.AsFloodWait(err); ok {
			return pw.written, &coresync.FloodWaitError{RetryAfter: d}
		}
		if isFileReferenceExpired(err) {
			return pw.written, fmt.Errorf("%w: %v", ErrFileReferenceExpired, err)
		}
		return pw.written, fmt.Errorf("downloader: stream: %w", err)
	}
	// Final progress tick: gotd may not call us back exactly at
	// total bytes (the last chunk can be short, and total is sometimes 0
	// when the size is unknown ahead of time). Synthesise a terminal
	// callback so consumers can render "100%" deterministically.
	if progress != nil {
		progress(pw.written, pw.written)
	}
	return pw.written, nil
}

// ErrFileReferenceExpired is returned when the cached file_reference
// failed server-side validation. Telegram rotates file references
// roughly hourly; the fix is to re-fetch the parent message via
// messages.getMessages and retry with the fresh reference. Callers in
// core/files surface this as a friendly "file link expired, retrying…"
// message.
var ErrFileReferenceExpired = fmt.Errorf("file_reference expired")

// isFileReferenceExpired inspects err for the well-known
// FILE_REFERENCE_EXPIRED RPC error type. gotd returns these as wrapped
// *tgerr.Error; we use TypeOf to match without importing the type from
// the test target.
func isFileReferenceExpired(err error) bool {
	if err == nil {
		return false
	}
	e, ok := tgerr.As(err)
	if !ok {
		return false
	}
	return e.Type == "FILE_REFERENCE_EXPIRED"
}

// buildFileLocation maps a domain.MediaInfo into a gotd
// InputFileLocationClass. Document and Photo are the only variants
// supported in v0.1; future variants (encrypted file, secure file)
// would land here as additional cases.
func buildFileLocation(info domain.MediaInfo) (tg.InputFileLocationClass, error) {
	switch info.Kind {
	case domain.MediaKindDocument:
		return &tg.InputDocumentFileLocation{
			ID:            info.FileID,
			AccessHash:    info.AccessHash,
			FileReference: info.FileReference,
			ThumbSize:     info.ThumbSize,
		}, nil
	case domain.MediaKindPhoto:
		return &tg.InputPhotoFileLocation{
			ID:            info.FileID,
			AccessHash:    info.AccessHash,
			FileReference: info.FileReference,
			ThumbSize:     info.ThumbSize,
		}, nil
	default:
		return nil, fmt.Errorf("downloader: unsupported media kind %q", info.Kind)
	}
}

// progressWriter wraps an io.Writer to count bytes and call back the
// progress callback after each write. The writer is *not* concurrency-
// safe; gotd's downloader streams sequentially into a single writer so
// no synchronisation is needed.
type progressWriter struct {
	w        io.Writer
	progress ProgressCallback
	written  int64
	total    int64
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.written += int64(n)
	if p.progress != nil {
		p.progress(p.written, p.total)
	}
	return n, err
}

// MediaFromMessage extracts a domain.MediaInfo from a gotd *tg.Message.
// Returns nil when the message has no media or the media variant is not
// supported in v0.1 (geo, contact, poll, web preview etc — those land
// in v0.2 once the UI can render them).
//
// This is the single chokepoint where gotd MessageMedia* objects turn
// into domain types; it lives in tg/ so internal/core can stay
// gotd-free.
func MediaFromMessage(m *tg.Message) *domain.MediaInfo {
	media, ok := m.GetMedia()
	if !ok {
		return nil
	}
	switch v := media.(type) {
	case *tg.MessageMediaDocument:
		doc, ok := v.Document.(*tg.Document)
		if !ok {
			return nil
		}
		return &domain.MediaInfo{
			Kind:          domain.MediaKindDocument,
			FileID:        doc.ID,
			AccessHash:    doc.AccessHash,
			FileReference: doc.FileReference,
			DC:            doc.DCID,
			Filename:      documentFilename(doc),
			Size:          doc.Size,
			MimeType:      doc.MimeType,
		}
	case *tg.MessageMediaPhoto:
		photo, ok := v.Photo.(*tg.Photo)
		if !ok {
			return nil
		}
		size, thumb := largestPhotoSize(photo)
		return &domain.MediaInfo{
			Kind:          domain.MediaKindPhoto,
			FileID:        photo.ID,
			AccessHash:    photo.AccessHash,
			FileReference: photo.FileReference,
			DC:            photo.DCID,
			Filename:      fmt.Sprintf("photo_%d.jpg", photo.ID),
			Size:          size,
			ThumbSize:     thumb,
		}
	}
	return nil
}

// documentFilename pulls the user-facing filename from a Document's
// attribute list. Documents typically carry a DocumentAttributeFilename
// alongside type-specific attributes (audio, video, sticker). Returns a
// generic "document_<id>.bin" fallback when no attribute provides one
// so the on-disk name is at least recognisable per file.
func documentFilename(doc *tg.Document) string {
	for _, attr := range doc.Attributes {
		if a, ok := attr.(*tg.DocumentAttributeFilename); ok {
			return a.FileName
		}
	}
	return fmt.Sprintf("document_%d.bin", doc.ID)
}

// largestPhotoSize selects the highest-resolution jpeg variant from
// photo.Sizes and returns its size + size selector. Falls back to
// ("", 0) when no usable size is present (very rare: the server
// almost always returns at least one).
func largestPhotoSize(photo *tg.Photo) (int64, string) {
	var bestSize int64
	var bestThumb string
	for _, s := range photo.Sizes {
		switch v := s.(type) {
		case *tg.PhotoSize:
			if int64(v.Size) > bestSize {
				bestSize = int64(v.Size)
				bestThumb = v.Type
			}
		case *tg.PhotoSizeProgressive:
			// Progressive sizes carry multiple byte sizes; the last
			// entry is the full-resolution jpeg the user expects to
			// see when they download.
			if len(v.Sizes) > 0 {
				last := int64(v.Sizes[len(v.Sizes)-1])
				if last > bestSize {
					bestSize = last
					bestThumb = v.Type
				}
			}
		}
	}
	return bestSize, bestThumb
}
