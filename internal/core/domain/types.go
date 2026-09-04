// Package domain holds the core value types shared across the storage, sync
// and UI layers. Types here MUST be free of external infrastructure imports
// (no gotd, no bubbletea, no database/sql) so that any layer can depend on
// them without dragging in transitive heavy dependencies.
package domain

import (
	"errors"
	"strings"
	"time"
)

// ErrInvalidPhone is returned by NormalizePhone when the input cannot be
// reduced to an E.164-shaped string (leading '+' followed by 10–15 digits).
var ErrInvalidPhone = errors.New("invalid phone number")

// NormalizePhone strips whitespace, dashes, parentheses and dots from s and
// returns the canonical E.164 representation ("+" + 10..15 digits). Used as
// the secret-store key and accounts.phone primary key so that "+7 999 111 22
// 33" and "+79991112233" map to the same logical account.
func NormalizePhone(s string) (string, error) {
	var b strings.Builder
	b.Grow(len(s))
	for i, r := range strings.TrimSpace(s) {
		switch {
		case i == 0 && r == '+':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '(' || r == ')' || r == '.' || r == '\t':
			// stripped
		default:
			return "", ErrInvalidPhone
		}
	}
	out := b.String()
	if !strings.HasPrefix(out, "+") {
		return "", ErrInvalidPhone
	}
	digits := out[1:]
	if len(digits) < 10 || len(digits) > 15 {
		return "", ErrInvalidPhone
	}
	return out, nil
}

// ChatType enumerates the kinds of Telegram peers lazytg cares about.
// Stored in the database as a short string so that values remain readable
// when inspecting the DB with sqlite3 CLI.
type ChatType string

const (
	// ChatTypePrivate is a one-to-one user dialog.
	ChatTypePrivate ChatType = "private"
	// ChatTypeGroup is a small (basic) group chat.
	ChatTypeGroup ChatType = "group"
	// ChatTypeSupergroup is a supergroup or megagroup.
	ChatTypeSupergroup ChatType = "supergroup"
	// ChatTypeChannel is a broadcast channel.
	ChatTypeChannel ChatType = "channel"
)

// Account represents a single Telegram account that lazytg has logged in to.
type Account struct {
	ID        int64
	Phone     string
	Alias     string
	CreatedAt time.Time
}

// Chat is the local view of a Telegram dialog (user, group, channel) used by
// the UI and search index.
type Chat struct {
	ID              int64
	Type            ChatType
	Title           string
	Username        string
	LastMessageDate time.Time
	UnreadCount     int
	Pinned          bool

	// LastMessagePreview is the text of the chat's newest cached message,
	// filled by the read path only (GetChats) and ignored on write. It is a
	// display convenience, not part of the stored chat row.
	LastMessagePreview string
}

// Message is a stored message belonging to a Chat. RawBlob holds the
// serialised gotd payload so the UI can re-render rich content without a
// round-trip to Telegram. Media is optional; a nil pointer means the
// message is plain text.
type Message struct {
	ID      int64
	ChatID  int64
	FromID  int64
	Date    time.Time
	Text    string
	ReplyTo int64
	RawBlob []byte
	Media   *MediaInfo
	// Outgoing marks a message the account itself sent. It is stored rather
	// than derived because Telegram omits from_id in a 1:1 dialog — the
	// sender is implied by this flag and the peer — and without it the
	// thread pane cannot tell the reader's own messages from the other
	// party's. See migration 0010.
	Outgoing bool
}

// MediaKind discriminates the variant of a Telegram media object so the
// download pipeline can build the right gotd InputFileLocation. Stored as
// a short string in the database so sqlite3 CLI inspection stays
// human-readable.
type MediaKind string

// MediaKind values name what the attachment is to a reader, not how it
// travels. Everything except a photo is a document on the wire, so the
// download path keys off IsPhoto rather than off a specific kind — but a
// thread that renders "video note, 0:07" where it used to render
// "document_5123.bin" is the difference between a legible conversation
// and a list of opaque blobs, and a client cannot tell a round video
// message from an ordinary video without reading the attributes.
//
// Rows written before migration 0011 carry "document" for all of these;
// they keep working and simply render as a generic attachment until the
// message is fetched again.
const (
	// MediaKindDocument is an attachment with no more specific kind —
	// a file the sender attached, an archive, a PDF.
	MediaKindDocument MediaKind = "document"
	// MediaKindPhoto is a still photo attachment.
	MediaKindPhoto MediaKind = "photo"
	// MediaKindVideo is an ordinary video file.
	MediaKindVideo MediaKind = "video"
	// MediaKindVideoNote is the round video message Telegram clients
	// call a "video message" and Russian-speaking users call кружочек:
	// DocumentAttributeVideo with RoundMessage set.
	MediaKindVideoNote MediaKind = "video_note"
	// MediaKindVoice is a voice message: DocumentAttributeAudio with
	// Voice set.
	MediaKindVoice MediaKind = "voice"
	// MediaKindAudio is a music track — audio that is not a voice
	// message.
	MediaKindAudio MediaKind = "audio"
	// MediaKindSticker is a sticker (static or animated).
	MediaKindSticker MediaKind = "sticker"
	// MediaKindAnimation is a GIF — MP4 without sound, marked animated.
	MediaKindAnimation MediaKind = "animation"
)

// IsPhoto reports whether the kind is transported as a photo rather than
// as a document. It is the only distinction the download path needs: an
// InputPhotoFileLocation for a photo, an InputDocumentFileLocation for
// everything else. Callers must not switch on the specific kind for
// this — a new kind added above would then silently download as nothing.
func (k MediaKind) IsPhoto() bool { return k == MediaKindPhoto }

// MediaInfo is the persisted metadata required to render a media message
// in the thread pane and to download the underlying file via gotd.
//
// FileReference is opaque and refreshed by getDifference; it expires
// after roughly one hour. Callers that hit a "FILE_REFERENCE_EXPIRED"
// error must re-fetch the parent message before retrying the download.
//
// DC is informational only — gotd's downloader handles cross-DC routing
// transparently. The field is kept so future migrations can switch to
// explicit DC pinning without losing the existing cache.
//
// ThumbSize is the optional photo size selector ("x", "y", …) passed
// into InputPhotoFileLocation; empty for documents (always full file).
//
// Duration is the playing time in whole seconds for the kinds that have
// one (video, video note, voice, audio) and zero everywhere else. It is
// what lets the badge say how long a voice message is before the user
// spends a download finding out. See migration 0011.
type MediaInfo struct {
	Kind          MediaKind
	FileID        int64
	AccessHash    int64
	FileReference []byte
	DC            int
	Filename      string
	Size          int64
	MimeType      string
	ThumbSize     string
	Duration      int
}

// Peer is the resolved MTProto access metadata for a chat. AccessHash is
// required for users and channels and ignored for plain groups; Type
// mirrors ChatType so the storage layer can pick the right InputPeer
// variant without re-parsing the chats table.
type Peer struct {
	ID         int64
	Type       ChatType
	AccessHash int64
}
