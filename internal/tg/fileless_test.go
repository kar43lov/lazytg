package tg

import (
	"strings"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

func withMedia(media tg.MessageMediaClass) *tg.Message {
	m := &tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: 1}, Date: 1}
	m.SetMedia(media)
	return m
}

// A place, a card, a poll and a dice are attachments with no file behind
// them. Each becomes a media row that names itself and cannot be
// downloaded — FileID stays zero, which is what the download path refuses.
func TestMediaFromMessage_FilelessKinds(t *testing.T) {
	t.Parallel()

	cases := []struct {
		media tg.MessageMediaClass
		kind  domain.MediaKind
		label string
	}{
		{&tg.MessageMediaGeo{Geo: &tg.GeoPoint{Lat: 55.7558, Long: 37.6173}}, domain.MediaKindLocation, "55.755800,37.617300"},
		{&tg.MessageMediaVenue{Geo: &tg.GeoPoint{Lat: 1, Long: 2}, Title: "Cafe", Address: "Main st"}, domain.MediaKindLocation, "1.000000,2.000000"},
		{&tg.MessageMediaContact{FirstName: "Ann", LastName: "Lee", PhoneNumber: "+1555"}, domain.MediaKindContact, "Ann Lee +1555"},
		{&tg.MessageMediaDice{Emoticon: "🎲", Value: 4}, domain.MediaKindDice, "🎲 4"},
	}
	for _, tc := range cases {
		got := MediaFromMessage(withMedia(tc.media))
		if got == nil || got.Kind != tc.kind || got.Filename != tc.label || got.FileID != 0 {
			t.Errorf("%T: got %+v, want kind %s label %q", tc.media, got, tc.kind, tc.label)
		}
	}
	venue := MediaFromMessage(withMedia(&tg.MessageMediaVenue{Geo: &tg.GeoPoint{Lat: 1, Long: 2}, Title: "Cafe", Address: "Main st"}))
	if venue.MimeType != "Cafe — Main st" {
		t.Fatalf("venue title = %q", venue.MimeType)
	}
}

// A poll carries no text of its own; the row gets the question and the
// options with their tallies, so there is something to read and to find.
func TestMessageText_DescribesAPoll(t *testing.T) {
	t.Parallel()

	poll := &tg.MessageMediaPoll{
		Poll: tg.Poll{
			Question: tg.TextWithEntities{Text: "Lunch?"},
			Answers: []tg.PollAnswerClass{
				&tg.PollAnswer{Text: tg.TextWithEntities{Text: "Pizza"}, Option: []byte{0}},
				&tg.PollAnswer{Text: tg.TextWithEntities{Text: "Sushi"}, Option: []byte{1}},
			},
		},
		Results: tg.PollResults{
			Results:     []tg.PollAnswerVoters{{Option: []byte{0}, Voters: 3, Chosen: true}, {Option: []byte{1}, Voters: 1}},
			TotalVoters: 4,
		},
	}
	got := messageText(withMedia(poll))
	want := "Lunch?\n● Pizza — 75%\n○ Sushi — 25%\n4 votes"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	media := MediaFromMessage(withMedia(poll))
	if media == nil || media.Kind != domain.MediaKindPoll || media.Filename != "Lunch?" {
		t.Fatalf("media = %+v", media)
	}
	plain := &tg.Message{Message: "hello"}
	if messageText(plain) != "hello" {
		t.Fatal("text with a body was replaced")
	}
}

func TestSendsAsPhoto(t *testing.T) {
	t.Parallel()

	small := &tg.InputFile{ID: 1}
	big := &tg.InputFileBig{ID: 2}
	if !sendsAsPhoto(small, "image/jpeg") || !sendsAsPhoto(small, "image/PNG") {
		t.Fatal("a small jpeg or png is a photo")
	}
	if sendsAsPhoto(small, "image/gif") || sendsAsPhoto(small, "application/pdf") || sendsAsPhoto(big, "image/jpeg") {
		t.Fatal("a gif, a pdf, or a picture past the photo cap is a document")
	}
	if !strings.HasPrefix("image/jpeg", "image/") {
		t.Fatal("sanity")
	}
}
