package files

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/kar43lov/lazytg/internal/core/events"
)

// UploadSoftLimit is the size threshold (50 MiB) above which UploadService
// emits a FileUploadWarning event before continuing. Below the limit no
// warning fires. The number is a UX choice — Telegram itself accepts up
// to 2 GiB without complaint.
const UploadSoftLimit int64 = 50 << 20

// UploadHardLimit is the absolute upper bound (2 GiB) above which
// UploadService refuses to upload at all. Telegram itself rejects
// anything larger so failing fast saves the user a round trip.
const UploadHardLimit int64 = 2 << 30

// ErrFileTooLarge is returned when a file exceeds UploadHardLimit. The
// sentinel lets callers branch the UX (toast / status bar) without
// inspecting the error message.
var ErrFileTooLarge = errors.New("upload: file exceeds 2 GiB hard limit")

// TGUploader is the gotd-free contract UploadService relies on for
// streaming bytes to Telegram. The concrete implementation in
// internal/tg/files.go reads the file at path and returns an opaque
// handle the caller can pass into the matching SendMediaSender. The
// any return value is intentionally untyped — UploadService never
// inspects it; only the SendMediaSender adapter on the other side of
// the gotd boundary unwraps it. This keeps the depguard rule (core ⊥
// gotd) trivially satisfied.
type TGUploader interface {
	Upload(ctx context.Context, path string, progress UploadProgressCallback) (handle any, err error)
}

// UploadProgressCallback mirrors tg.uploaderProgressCallback but is
// re-declared here so consumers do not have to import internal/tg. The
// two function shapes are identical, which keeps the wiring layer to a
// single-line type-alias adapter.
type UploadProgressCallback = func(uploaded, total int64)

// SendMediaSender is the gotd-free contract UploadService uses to
// dispatch the just-uploaded file as a chat message. handle is the
// opaque value returned by TGUploader.Upload; the concrete adapter in
// internal/tg type-asserts it back into a gotd payload. caption and
// replyTo follow the same semantics as Sender.SendText.
type SendMediaSender interface {
	SendMedia(ctx context.Context, chatID int64, handle any, caption string, replyTo int) (messageID int64, err error)
}

// SendRateLimiter is the optional throttle UploadService consults
// before each SendMedia attempt. It mirrors the contract used by
// SendService.WithRateLimiter so the wiring layer can reuse the same
// security.SendGuard for both text and media sends — file uploads
// count toward the same ban-risk fingerprint, so the 10 msg/sec
// ceiling must apply to both paths.
//
// Nil is allowed — the service skips the wait so unit tests of
// unrelated behaviour stay free of plumbing.
type SendRateLimiter interface {
	Wait(ctx context.Context) error
}

// UploadService coordinates a single Ctrl-U press end-to-end:
//
//  1. Stat the file. Reject anything > UploadHardLimit (2 GiB) up
//     front so the user gets immediate feedback instead of a chunky
//     gotd error mid-stream.
//  2. Emit Started + (optional) Warning events so the status bar can
//     render an upload row.
//  3. Stream the bytes through TGUploader.Upload, throttling progress
//     events the same way the download path does (every 5% or every 1
//     MiB, whichever first).
//  4. Pass the returned handle to SendMediaSender.SendMedia along
//     with the caption + chat id + replyTo.
//  5. Emit Completed (with the server-assigned message id) on success
//     or Failed (with the error string) on any failure.
//
// The service is safe for concurrent use across distinct uploads —
// every call gets a fresh UploadID via the package-level monotonic
// counter, which doubles as the dedup key in the status-bar map.
type UploadService struct {
	tg       TGUploader
	sender   SendMediaSender
	bus      EventPublisher
	log      *slog.Logger
	progress *uploadProgressThrottler
	limiter  SendRateLimiter
}

// NewUploadService validates non-nil deps and returns a ready service.
// log can be nil; a discarding logger is used in that case so call
// sites remain clean.
func NewUploadService(tg TGUploader, sender SendMediaSender, bus EventPublisher, log *slog.Logger) (*UploadService, error) {
	if tg == nil {
		return nil, errors.New("upload: tg uploader is required")
	}
	if sender == nil {
		return nil, errors.New("upload: sender is required")
	}
	if log == nil {
		log = slog.New(discardHandler{})
	}
	return &UploadService{
		tg:       tg,
		sender:   sender,
		bus:      bus,
		log:      log,
		progress: newUploadProgressThrottler(),
	}, nil
}

// WithRateLimiter installs limiter as the gate consulted before
// SendMedia. Returns the same *UploadService for chaining at the
// wiring site. nil clears any previously installed limiter.
func (s *UploadService) WithRateLimiter(limiter SendRateLimiter) *UploadService {
	s.limiter = limiter
	return s
}

// uploadIDCounter feeds nextUploadID. atomic.Int64 is goroutine-safe
// by construction so concurrent SendFile calls each pick a fresh id
// without locking.
var uploadIDCounter atomic.Int64

func nextUploadID() events.UploadID {
	return uploadIDCounter.Add(1)
}

// SendFile is the public entry point. chatID is where the file should
// land; path is the source on disk; caption is appended to the message
// body (optional); replyTo, when non-zero, attaches the message as a
// reply.
//
// Returns the in-process UploadID and any error. The UploadID is
// useful to callers that want to correlate later bus events without
// inspecting their payloads — the status bar already keys by it
// internally.
func (s *UploadService) SendFile(ctx context.Context, chatID int64, path, caption string, replyTo int) (events.UploadID, error) {
	if chatID == 0 {
		return 0, errors.New("upload: chat_id is required")
	}
	if path == "" {
		return 0, errors.New("upload: path is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("upload: stat %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("upload: %q is not a regular file", path)
	}
	size := info.Size()
	if size > UploadHardLimit {
		return 0, fmt.Errorf("%w: %d bytes", ErrFileTooLarge, size)
	}

	uploadID := nextUploadID()
	filename := filepath.Base(path)

	s.publish(events.FileUploadStarted{
		UploadID: uploadID,
		ChatID:   chatID,
		Path:     path,
		Filename: filename,
		Size:     size,
	})
	if size > UploadSoftLimit {
		s.publish(events.FileUploadWarning{
			UploadID:  uploadID,
			Size:      size,
			Threshold: UploadSoftLimit,
		})
	}

	// reset per-uploadID state for the throttler; release on exit so
	// the map does not grow unbounded across the TUI's lifetime.
	s.progress.reset(uploadID)
	defer s.progress.release(uploadID)
	cb := func(bytes, total int64) {
		if !s.progress.shouldEmit(uploadID, bytes, total) {
			return
		}
		s.publish(events.FileUploadProgress{
			UploadID:      uploadID,
			BytesUploaded: bytes,
			TotalBytes:    total,
		})
	}

	handle, err := s.tg.Upload(ctx, path, cb)
	if err != nil {
		s.uploadFailure(uploadID, fmt.Errorf("stream: %w", err))
		return uploadID, err
	}

	// Rate-limit guard sits between Upload and SendMedia so the bytes
	// are already on the server when we wait — only the user-visible
	// "this counts as a send" RPC is throttled. Mirrors the placement
	// in coresync.SendService.deliver for text sends so both paths
	// share the same SendGuard ceiling.
	if s.limiter != nil {
		if err := s.limiter.Wait(ctx); err != nil {
			s.uploadFailure(uploadID, fmt.Errorf("rate-limit wait: %w", err))
			return uploadID, err
		}
	}

	messageID, err := s.sender.SendMedia(ctx, chatID, handle, caption, replyTo)
	if err != nil {
		s.uploadFailure(uploadID, fmt.Errorf("send media: %w", err))
		return uploadID, err
	}

	s.publish(events.FileUploadCompleted{
		UploadID:  uploadID,
		ChatID:    chatID,
		MessageID: messageID,
	})
	return uploadID, nil
}

// uploadFailure publishes a Failed event and logs the error.
// Centralised because every error path (upload, sendMedia) needs the
// same teardown shape.
func (s *UploadService) uploadFailure(id events.UploadID, err error) {
	if err == nil {
		return
	}
	s.log.Warn("upload: failed", "upload_id", id, "err", err)
	s.publish(events.FileUploadFailed{UploadID: id, Err: err.Error()})
}

func (s *UploadService) publish(ev events.Event) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(ev)
}
