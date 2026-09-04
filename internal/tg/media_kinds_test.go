package tg

import (
	"testing"

	"github.com/gotd/td/tg"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// docWith builds a MessageMediaDocument message carrying the given
// attributes, which is the only thing that distinguishes a voice message
// from a video from a sticker on the wire.
func docWith(id int64, mime string, attrs ...tg.DocumentAttributeClass) *tg.Message {
	doc := &tg.Document{ID: id, AccessHash: 1, MimeType: mime, Attributes: attrs}
	m := &tg.Message{ID: 1, Date: 100}
	m.SetMedia(&tg.MessageMediaDocument{Document: doc})
	return m
}

// Every attachment that was not a photo used to be a "document", which
// is true of the wire and useless to a reader: a voice message, a round
// video message and a PDF all rendered as the same grey badge, and the
// two without a filename attribute rendered as "document_<id>.bin".
//
// The round-message flag is the one that matters most here — it is the
// only thing separating a кружочек from an ordinary video, and both
// arrive as DocumentAttributeVideo.
func TestClassifyDocument(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		msg      *tg.Message
		wantKind domain.MediaKind
		wantDur  int
		wantName string
	}{
		{
			name:     "round video message is a video note",
			msg:      docWith(11, "video/mp4", &tg.DocumentAttributeVideo{RoundMessage: true, Duration: 7}),
			wantKind: domain.MediaKindVideoNote,
			wantDur:  7,
			wantName: "video_note_11.mp4",
		},
		{
			name:     "plain video",
			msg:      docWith(12, "video/mp4", &tg.DocumentAttributeVideo{Duration: 95}),
			wantKind: domain.MediaKindVideo,
			wantDur:  95,
			wantName: "video_12.mp4",
		},
		{
			name:     "voice message",
			msg:      docWith(13, "audio/ogg", &tg.DocumentAttributeAudio{Voice: true, Duration: 42}),
			wantKind: domain.MediaKindVoice,
			wantDur:  42,
			wantName: "voice_13.ogg",
		},
		{
			name:     "music track keeps its filename",
			msg:      docWith(14, "audio/mpeg", &tg.DocumentAttributeAudio{Duration: 195}, &tg.DocumentAttributeFilename{FileName: "track.mp3"}),
			wantKind: domain.MediaKindAudio,
			wantDur:  195,
			wantName: "track.mp3",
		},
		{
			name:     "sticker wins over its filename attribute",
			msg:      docWith(15, "image/webp", &tg.DocumentAttributeSticker{}, &tg.DocumentAttributeFilename{FileName: "sticker.webp"}),
			wantKind: domain.MediaKindSticker,
			wantDur:  0,
			wantName: "sticker.webp",
		},
		{
			name:     "animated video is a GIF, not a video",
			msg:      docWith(16, "video/mp4", &tg.DocumentAttributeVideo{Duration: 3}, &tg.DocumentAttributeAnimated{}),
			wantKind: domain.MediaKindAnimation,
			wantDur:  3,
			wantName: "animation_16.mp4",
		},
		{
			name:     "everything else stays a document",
			msg:      docWith(17, "application/pdf", &tg.DocumentAttributeFilename{FileName: "report.pdf"}),
			wantKind: domain.MediaKindDocument,
			wantDur:  0,
			wantName: "report.pdf",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MediaFromMessage(tc.msg)
			if got == nil {
				t.Fatalf("expected media, got nil")
			}
			if got.Kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", got.Kind, tc.wantKind)
			}
			if got.Duration != tc.wantDur {
				t.Errorf("duration = %d, want %d", got.Duration, tc.wantDur)
			}
			if got.Filename != tc.wantName {
				t.Errorf("filename = %q, want %q", got.Filename, tc.wantName)
			}
		})
	}
}

// An empty filename attribute is not a filename. Telegram does send one
// occasionally, and taking it at face value produced an attachment saved
// as the sanitiser's fallback ("file") with no extension for the system
// viewer to dispatch on.
func TestDocumentFilename_IgnoresAnEmptyAttribute(t *testing.T) {
	t.Parallel()

	got := MediaFromMessage(docWith(18, "video/mp4",
		&tg.DocumentAttributeVideo{RoundMessage: true, Duration: 5},
		&tg.DocumentAttributeFilename{FileName: ""},
	))
	if got == nil || got.Filename != "video_note_18.mp4" {
		t.Fatalf("filename = %+v, want the per-kind fallback", got)
	}
}
