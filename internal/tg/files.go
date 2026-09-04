package tg

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/kar43lov/lazytg/internal/core/domain"
	coresync "github.com/kar43lov/lazytg/internal/core/sync"
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
//
// Declared as a type alias (not a named type) so files.ProgressCallback
// — which has the same shape — is exchangeable at the type level. That
// keeps the wiring layer (internal/app/wire.go) free of an adapter
// shim when constructing files.DownloadService against tg.Downloader.
type ProgressCallback = func(bytesSoFar, totalBytes int64)

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
// InputFileLocationClass.
//
// The split is photo versus everything else, because that is the split
// MTProto itself makes: a photo is addressed by InputPhotoFileLocation,
// and a video, voice message, sticker or plain file is a document. It is
// deliberately not a switch over every MediaKind — the kinds exist to
// tell the reader what an attachment is, and a new one added there would
// otherwise fall into a default branch and stop downloading.
func buildFileLocation(info domain.MediaInfo) (tg.InputFileLocationClass, error) {
	if info.Kind == "" {
		return nil, fmt.Errorf("downloader: media kind is empty")
	}
	if info.Kind.IsPhoto() {
		return &tg.InputPhotoFileLocation{
			ID:            info.FileID,
			AccessHash:    info.AccessHash,
			FileReference: info.FileReference,
			ThumbSize:     info.ThumbSize,
		}, nil
	}
	return &tg.InputDocumentFileLocation{
		ID:            info.FileID,
		AccessHash:    info.AccessHash,
		FileReference: info.FileReference,
		ThumbSize:     info.ThumbSize,
	}, nil
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
		kind, duration := classifyDocument(doc)
		return &domain.MediaInfo{
			Kind:          kind,
			FileID:        doc.ID,
			AccessHash:    doc.AccessHash,
			FileReference: doc.FileReference,
			DC:            doc.DCID,
			Filename:      documentFilename(doc, kind),
			Size:          doc.Size,
			MimeType:      doc.MimeType,
			Duration:      duration,
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

// classifyDocument reads a Document's attribute list and reports what
// the attachment actually is, plus its duration in whole seconds (zero
// for kinds that have none).
//
// The attributes are a set, not a tag: a round video message carries
// DocumentAttributeVideo with RoundMessage set, a voice message carries
// DocumentAttributeAudio with Voice set, and a GIF carries
// DocumentAttributeAnimated *alongside* the video attribute. So the
// scan collects what it finds and decides afterwards, most specific
// first — animated-plus-video is an animation, not a video, and a
// sticker is a sticker even though it also carries a filename.
func classifyDocument(doc *tg.Document) (domain.MediaKind, int) {
	var (
		video     *tg.DocumentAttributeVideo
		audio     *tg.DocumentAttributeAudio
		sticker   bool
		animation bool
	)
	for _, attr := range doc.Attributes {
		switch a := attr.(type) {
		case *tg.DocumentAttributeVideo:
			video = a
		case *tg.DocumentAttributeAudio:
			audio = a
		case *tg.DocumentAttributeSticker:
			sticker = true
		case *tg.DocumentAttributeAnimated:
			animation = true
		}
	}
	switch {
	case sticker:
		return domain.MediaKindSticker, 0
	case video != nil && video.RoundMessage:
		return domain.MediaKindVideoNote, int(video.Duration)
	case animation:
		duration := 0
		if video != nil {
			duration = int(video.Duration)
		}
		return domain.MediaKindAnimation, duration
	case video != nil:
		return domain.MediaKindVideo, int(video.Duration)
	case audio != nil && audio.Voice:
		return domain.MediaKindVoice, audio.Duration
	case audio != nil:
		return domain.MediaKindAudio, audio.Duration
	default:
		return domain.MediaKindDocument, 0
	}
}

// documentFilename pulls the user-facing filename from a Document's
// attribute list.
//
// The fallback is per kind rather than one generic name, because the
// kinds without a filename attribute are exactly the ones a user meets
// most often: Telegram sends no name for a voice message, a video note
// or a sticker, and "document_5123.bin" tells the reader nothing and
// gives the system opener no extension to dispatch on. A name built
// from the kind carries both.
func documentFilename(doc *tg.Document, kind domain.MediaKind) string {
	for _, attr := range doc.Attributes {
		if a, ok := attr.(*tg.DocumentAttributeFilename); ok && a.FileName != "" {
			return a.FileName
		}
	}
	switch kind {
	case domain.MediaKindVideoNote:
		return fmt.Sprintf("video_note_%d.mp4", doc.ID)
	case domain.MediaKindVideo:
		return fmt.Sprintf("video_%d.mp4", doc.ID)
	case domain.MediaKindAnimation:
		return fmt.Sprintf("animation_%d.mp4", doc.ID)
	case domain.MediaKindVoice:
		return fmt.Sprintf("voice_%d.ogg", doc.ID)
	case domain.MediaKindAudio:
		return fmt.Sprintf("audio_%d.mp3", doc.ID)
	case domain.MediaKindSticker:
		return fmt.Sprintf("sticker_%d.webp", doc.ID)
	}
	return fmt.Sprintf("document_%d.bin", doc.ID)
}

// uploaderClient is the slice of *tg.Client that gotd's uploader needs.
// Pulled out as an interface so unit tests can substitute a stub without
// a full tgtest harness — the uploader.Client interface itself is the
// canonical reference, repeated here so downstream code does not import
// gotd.
type uploaderClient = uploader.Client

// uploaderDefaultPartSize is the chunk size we ask the gotd uploader to
// use. 512 KiB matches the gotd default and balances per-RPC overhead
// against finer progress granularity (every part triggers a callback).
const uploaderDefaultPartSize = 512 * 1024

// uploaderProgressCallback is the per-chunk callback the uploader fires
// after every confirmed part. uploaded is cumulative bytes; total is
// the originally-advertised size (may be -1 when streaming an unknown
// length, but this code path always passes a known length so the field
// is always >= 0).
//
// The callback runs on the goroutine driving the upload and must not
// block — heavy work (event publish, UI render) should be dispatched
// asynchronously by the caller.
type uploaderProgressCallback func(uploaded, total int64)

// Uploader streams a local file into Telegram via gotd's chunked
// uploader and returns the resulting tg.InputFileClass that the caller
// can pass into messages.sendMedia.
//
// All gotd types stay confined to this file: callers in core/files pass
// a path + progress callback and receive a domain-shaped InputFile
// reference (wrapped in a small interface so internal/core stays
// gotd-free). The depguard rule (core ⊥ gotd) holds without further
// plumbing.
type Uploader struct {
	api      uploaderClient
	log      *slog.Logger
	partSize int
}

// NewUploader returns an Uploader bound to the given API client. log
// may be nil; a noop handler is wired so call sites stay clean.
func NewUploader(api uploaderClient, log *slog.Logger) *Uploader {
	if log == nil {
		log = slog.New(noopHandler{})
	}
	return &Uploader{api: api, log: log, partSize: uploaderDefaultPartSize}
}

// UploadResult carries the data Sender.SendMedia needs to wrap a
// freshly-uploaded file in an InputMedia* envelope. InputFile is the
// gotd-generated handle (small file: *tg.InputFile, big file:
// *tg.InputFileBig — both satisfy tg.InputFileClass). Filename is the
// basename of the source file; MimeType is the content type the
// uploader / caller wants attached.
//
// Exported fields make this struct serve double duty: callers in
// internal/tg use it directly, while core/files keeps the value as an
// opaque any-handle (UploadResult is never imported from core/files).
type UploadResult struct {
	InputFile tg.InputFileClass
	Filename  string
	MimeType  string
}

// Upload streams the file at path into Telegram using gotd's chunked
// uploader. progress may be nil. The caller must keep path stable
// until Upload returns — gotd reads from disk lazily.
//
// The function returns a UploadResult with the Telegram-side handle
// the caller can pass into Sender.SendMedia. FLOOD_WAIT errors are
// translated to *coresync.FloodWaitError so the retry policy in
// SendService stays gotd-free.
func (u *Uploader) Upload(ctx context.Context, path string, progress uploaderProgressCallback) (*UploadResult, error) {
	if u.api == nil {
		return nil, fmt.Errorf("uploader: api client is nil")
	}
	if path == "" {
		return nil, fmt.Errorf("uploader: path is empty")
	}
	// Resolve absolute path here so a relative cwd-dependent caller does
	// not surprise gotd with a "no such file" deep inside the chunk loop.
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("uploader: abs %q: %w", path, err)
	}
	// path is supplied by the user-side file picker; gosec G304 is by
	// design here — the picker is the trust boundary, not this RPC layer.
	f, err := os.Open(filepath.Clean(abs)) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("uploader: open %q: %w", abs, err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("uploader: stat %q: %w", abs, err)
	}
	totalBytes := info.Size()

	up := uploader.NewUploader(u.api).WithPartSize(u.partSize)
	if progress != nil {
		up = up.WithProgress(&uploaderProgressAdapter{cb: progress, total: totalBytes})
	}

	inputFile, err := up.FromFile(ctx, f)
	if err != nil {
		if d, ok := tgerr.AsFloodWait(err); ok {
			return nil, &coresync.FloodWaitError{RetryAfter: d}
		}
		return nil, fmt.Errorf("uploader: from file %q: %w", abs, err)
	}

	// Final progress tick: gotd's per-chunk callback may not land
	// exactly at total bytes (the last chunk can be short). Synthesise a
	// terminal callback so consumers can render "100%" deterministically.
	if progress != nil {
		progress(totalBytes, totalBytes)
	}

	return &UploadResult{
		InputFile: inputFile,
		Filename:  filepath.Base(abs),
		MimeType:  detectMimeType(abs),
	}, nil
}

// FilesAdapter is the gotd-aware bridge that lets core/files.UploadService
// drive the upload + sendMedia round-trip without importing gotd. The
// adapter satisfies both files.TGUploader (Upload returns an opaque
// any-handle) and files.SendMediaSender (SendMedia accepts that same
// handle) because keeping them on one struct lets the wiring layer
// inject a single dependency rather than two.
//
// Up is the *Uploader that streams bytes server-side; Snd is the
// *Sender that issues messages.sendMedia. Both must be non-nil at
// construction time (NewFilesAdapter validates).
type FilesAdapter struct {
	Up  *Uploader
	Snd *Sender
}

// NewFilesAdapter validates the dependencies and returns a ready
// adapter. Returns an error rather than panicking so the wiring layer
// can fail loudly at startup if either dependency is missing.
func NewFilesAdapter(up *Uploader, snd *Sender) (*FilesAdapter, error) {
	if up == nil {
		return nil, fmt.Errorf("filesadapter: uploader is required")
	}
	if snd == nil {
		return nil, fmt.Errorf("filesadapter: sender is required")
	}
	return &FilesAdapter{Up: up, Snd: snd}, nil
}

// Upload satisfies files.TGUploader. The returned handle is the
// concrete *UploadResult; core/files treats it as opaque any.
func (a *FilesAdapter) Upload(ctx context.Context, path string, progress func(uploaded, total int64)) (any, error) {
	res, err := a.Up.Upload(ctx, path, uploaderProgressCallback(progress))
	if err != nil {
		return nil, err
	}
	return res, nil
}

// SendMedia satisfies files.SendMediaSender. handle must be the
// *UploadResult produced by Upload (or matching test fake) — the type
// assertion is the trust boundary between core/files's any-handle and
// the gotd-typed InputFile that messages.sendMedia needs.
func (a *FilesAdapter) SendMedia(ctx context.Context, chatID int64, handle any, caption string, replyTo int) (int64, error) {
	res, ok := handle.(*UploadResult)
	if !ok || res == nil {
		return 0, fmt.Errorf("filesadapter: invalid upload handle %T", handle)
	}
	return a.Snd.SendMedia(ctx, chatID, res.InputFile, res.Filename, res.MimeType, caption, replyTo)
}

// uploaderProgressAdapter bridges gotd's uploader.Progress interface
// (Chunk(ctx, ProgressState) error) onto our minimal func-based
// uploaderProgressCallback. Stored on the heap because gotd holds a
// reference to it for the duration of the upload.
type uploaderProgressAdapter struct {
	cb    uploaderProgressCallback
	total int64
}

func (a *uploaderProgressAdapter) Chunk(_ context.Context, st uploader.ProgressState) error {
	if a.cb == nil {
		return nil
	}
	total := st.Total
	if total < 0 {
		total = a.total
	}
	a.cb(st.Uploaded, total)
	return nil
}

// detectMimeType returns a best-effort MIME based on the file's
// extension. We deliberately avoid sniffing the magic bytes — that
// would require a second read pass and the server already accepts a
// generic application/octet-stream when we cannot guess a better type.
// Image/* extensions are mapped explicitly so sendMedia can later opt
// into InputMediaUploadedPhoto when the caller wants — Stage 3 routes
// every upload through InputMediaUploadedDocument with ForceFile, so
// the field is currently informational.
func detectMimeType(path string) string {
	ext := filepath.Ext(path)
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".pdf":
		return "application/pdf"
	case ".txt", ".md", ".log":
		return "text/plain"
	case ".mp4":
		return "video/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".zip":
		return "application/zip"
	}
	return "application/octet-stream"
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
