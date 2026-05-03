package tg

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/pgmac/lazytg/internal/core/domain"
)

func TestMediaFromMessage_Document(t *testing.T) {
	doc := &tg.Document{
		ID:            123,
		AccessHash:    -7,
		FileReference: []byte{0xaa, 0xbb},
		MimeType:      "application/pdf",
		Size:          234567,
		DCID:          2,
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeFilename{FileName: "report.pdf"},
		},
	}
	m := &tg.Message{ID: 1, Date: 100}
	m.SetMedia(&tg.MessageMediaDocument{Document: doc})
	got := MediaFromMessage(m)
	if got == nil {
		t.Fatalf("expected media, got nil")
	}
	if got.Kind != domain.MediaKindDocument || got.FileID != 123 ||
		got.AccessHash != -7 || got.MimeType != "application/pdf" ||
		got.Size != 234567 || got.DC != 2 || got.Filename != "report.pdf" {
		t.Fatalf("doc parse mismatch: %+v", got)
	}
	if string(got.FileReference) != string([]byte{0xaa, 0xbb}) {
		t.Fatalf("file_reference mismatch: %x", got.FileReference)
	}
}

func TestMediaFromMessage_DocumentNoFilename(t *testing.T) {
	doc := &tg.Document{ID: 999, AccessHash: 1}
	m := &tg.Message{ID: 2}
	m.SetMedia(&tg.MessageMediaDocument{Document: doc})
	got := MediaFromMessage(m)
	if got == nil || got.Filename != "document_999.bin" {
		t.Fatalf("missing fallback filename: %+v", got)
	}
}

func TestMediaFromMessage_Photo(t *testing.T) {
	photo := &tg.Photo{
		ID:            555,
		AccessHash:    77,
		FileReference: []byte{0x01},
		DCID:          5,
		Sizes: []tg.PhotoSizeClass{
			&tg.PhotoSize{Type: "x", Size: 5000},
			&tg.PhotoSize{Type: "y", Size: 50000},
			&tg.PhotoSize{Type: "w", Size: 30000},
		},
	}
	m := &tg.Message{ID: 3}
	m.SetMedia(&tg.MessageMediaPhoto{Photo: photo})
	got := MediaFromMessage(m)
	if got == nil {
		t.Fatalf("expected photo media, got nil")
	}
	if got.Kind != domain.MediaKindPhoto || got.FileID != 555 || got.AccessHash != 77 ||
		got.DC != 5 || got.Size != 50000 || got.ThumbSize != "y" {
		t.Fatalf("photo parse mismatch: %+v", got)
	}
	if got.Filename != "photo_555.jpg" {
		t.Fatalf("photo filename: %q", got.Filename)
	}
}

func TestMediaFromMessage_PlainText(t *testing.T) {
	m := &tg.Message{ID: 4, Message: "hello"}
	if got := MediaFromMessage(m); got != nil {
		t.Fatalf("plain message should not produce media: %+v", got)
	}
}

func TestMediaFromMessage_UnsupportedKind(t *testing.T) {
	m := &tg.Message{ID: 5}
	m.SetMedia(&tg.MessageMediaContact{PhoneNumber: "+x"})
	if got := MediaFromMessage(m); got != nil {
		t.Fatalf("contact media should fall through to nil: %+v", got)
	}
}

func TestBuildFileLocation(t *testing.T) {
	doc := domain.MediaInfo{
		Kind: domain.MediaKindDocument, FileID: 1, AccessHash: 2, FileReference: []byte{3},
	}
	loc, err := buildFileLocation(doc)
	if err != nil {
		t.Fatalf("doc: %v", err)
	}
	if _, ok := loc.(*tg.InputDocumentFileLocation); !ok {
		t.Fatalf("doc -> %T", loc)
	}

	photo := domain.MediaInfo{
		Kind: domain.MediaKindPhoto, FileID: 1, AccessHash: 2, ThumbSize: "y",
	}
	loc, err = buildFileLocation(photo)
	if err != nil {
		t.Fatalf("photo: %v", err)
	}
	if got, ok := loc.(*tg.InputPhotoFileLocation); !ok || got.ThumbSize != "y" {
		t.Fatalf("photo -> %T (%+v)", loc, got)
	}

	if _, err := buildFileLocation(domain.MediaInfo{Kind: "weird"}); err == nil {
		t.Fatalf("expected error for unknown kind")
	}
}

func TestProgressWriter_TracksBytesAndCallsCallback(t *testing.T) {
	var buf bytes.Buffer
	var ticks []int64
	pw := &progressWriter{w: &buf, total: 6, progress: func(b, total int64) {
		ticks = append(ticks, b)
		if total != 6 {
			t.Errorf("total mismatch: %d", total)
		}
	}}
	if _, err := pw.Write([]byte("abc")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := pw.Write([]byte("def")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if pw.written != 6 {
		t.Fatalf("written = %d", pw.written)
	}
	if len(ticks) != 2 || ticks[0] != 3 || ticks[1] != 6 {
		t.Fatalf("ticks: %v", ticks)
	}
	if buf.String() != "abcdef" {
		t.Fatalf("buf: %q", buf.String())
	}
}

// fakeAPI implements downloader.Client minimally; gotd's Stream goes
// through UploadGetFile which we satisfy with a static byte payload.
// Returning a real-shape UploadFile from a single call short-circuits
// the chunked retry loop — Stream stops as soon as it sees a chunk
// shorter than the requested limit.
type fakeAPI struct {
	payload []byte
	calls   int
}

func (f *fakeAPI) UploadGetFile(_ context.Context, req *tg.UploadGetFileRequest) (tg.UploadFileClass, error) {
	f.calls++
	off := req.Offset
	end := off + int64(req.Limit)
	if end > int64(len(f.payload)) {
		end = int64(len(f.payload))
	}
	if off >= int64(len(f.payload)) {
		return &tg.UploadFile{Bytes: nil}, nil
	}
	return &tg.UploadFile{Bytes: f.payload[off:end]}, nil
}

func (f *fakeAPI) UploadGetFileHashes(context.Context, *tg.UploadGetFileHashesRequest) ([]tg.FileHash, error) {
	return nil, nil
}

func (f *fakeAPI) UploadReuploadCDNFile(context.Context, *tg.UploadReuploadCDNFileRequest) ([]tg.FileHash, error) {
	return nil, nil
}

func (f *fakeAPI) UploadGetCDNFileHashes(context.Context, *tg.UploadGetCDNFileHashesRequest) ([]tg.FileHash, error) {
	return nil, nil
}

func (f *fakeAPI) UploadGetCDNFile(context.Context, *tg.UploadGetCDNFileRequest) (tg.UploadCDNFileClass, error) {
	return nil, errors.New("CDN not used in fake")
}

func (f *fakeAPI) UploadGetWebFile(context.Context, *tg.UploadGetWebFileRequest) (*tg.UploadWebFile, error) {
	return nil, errors.New("web file not used in fake")
}

func TestDownloader_Download_StreamsBytes(t *testing.T) {
	payload := bytes.Repeat([]byte("AB"), 256) // 512 bytes
	api := &fakeAPI{payload: payload}
	dl := NewDownloader(api, nil)
	dl.partSize = 128 // override default 512KB so we exercise the chunk loop

	var got bytes.Buffer
	var lastTotal int64
	progressTicks := 0
	n, err := dl.Download(t.Context(), domain.MediaInfo{
		Kind: domain.MediaKindDocument, FileID: 1, AccessHash: 1, Size: int64(len(payload)),
	}, &got, func(b, total int64) {
		progressTicks++
		lastTotal = total
	})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if n != int64(len(payload)) || !bytes.Equal(got.Bytes(), payload) {
		t.Fatalf("payload mismatch: n=%d want=%d", n, len(payload))
	}
	if progressTicks == 0 {
		t.Fatalf("progress callback never fired")
	}
	if lastTotal != int64(len(payload)) {
		t.Fatalf("final total mismatch: %d", lastTotal)
	}
	if api.calls < 1 {
		t.Fatalf("UploadGetFile never called")
	}
}

func TestDownloader_RejectsNilWriter(t *testing.T) {
	dl := NewDownloader(&fakeAPI{}, nil)
	if _, err := dl.Download(t.Context(), domain.MediaInfo{
		Kind: domain.MediaKindDocument, FileID: 1,
	}, nil, nil); err == nil {
		t.Fatalf("expected error on nil writer")
	}
}

// Ensure the wrapper itself implements io.Writer (compile-time
// assurance that progressWriter wires correctly).
var _ io.Writer = (*progressWriter)(nil)
