package tg

import (
	"reflect"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// "😀 bold" — the emoji is one rune and two UTF-16 units, so Telegram says
// the bold span starts at unit 3 while it starts at rune 2. A converter that
// copied the offset would embolden " bol" and leave the d.
func TestEntitiesFromMessage_CountsRunesNotUnits(t *testing.T) {
	t.Parallel()

	m := &tg.Message{Message: "😀 bold"}
	m.SetEntities([]tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 3, Length: 4}})

	got := EntitiesFromMessage(m)
	want := []domain.Entity{{Kind: domain.EntityBold, Offset: 2, Length: 4}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestEntitiesToWire_CountsUnitsNotRunes(t *testing.T) {
	t.Parallel()

	got := entitiesToWire("😀 bold", []domain.Entity{{Kind: domain.EntityBold, Offset: 2, Length: 4}})
	want := []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 3, Length: 4}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestEntities_WireRoundTripKeepsPayloads(t *testing.T) {
	t.Parallel()

	text := "a 🚀 b c d e f g h"
	in := []tg.MessageEntityClass{
		&tg.MessageEntityTextURL{Offset: 5, Length: 1, URL: "https://x.y"},
		&tg.MessageEntityPre{Offset: 7, Length: 3, Language: "go"},
		&tg.MessageEntitySpoiler{Offset: 11, Length: 1},
		&tg.MessageEntityCustomEmoji{Offset: 13, Length: 1, DocumentID: 9},
	}
	back := entitiesToWire(text, entitiesFromWire(text, in))
	if !reflect.DeepEqual(back, in) {
		t.Fatalf("round trip changed the spans:\n got %+v\nwant %+v", back, in)
	}
}

// A mention by user id needs an access hash this client does not carry,
// and the server derives urls, hashtags and commands from the text itself.
// None of those go back on the wire; the text they cover does.
func TestEntitiesToWire_DropsWhatTheServerDerives(t *testing.T) {
	t.Parallel()

	got := entitiesToWire("@a #b /c x", []domain.Entity{
		{Kind: domain.EntityMention, Offset: 0, Length: 2},
		{Kind: domain.EntityHashtag, Offset: 3, Length: 2},
		{Kind: domain.EntityBotCommand, Offset: 6, Length: 2},
		{Kind: domain.EntityMentionName, Offset: 9, Length: 1, UserID: 5},
		{Kind: domain.EntityBold, Offset: 9, Length: 1},
	})
	want := []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 9, Length: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// A span whose end runs past the text shrinks to the text; one that ends
// before it starts is dropped. Both are server-side mistakes this client
// has to survive rather than panic on.
func TestEntitiesFromWire_ClampsToTheText(t *testing.T) {
	t.Parallel()

	got := entitiesFromWire("abc", []tg.MessageEntityClass{
		&tg.MessageEntityBold{Offset: 1, Length: 10},
		&tg.MessageEntityItalic{Offset: 5, Length: 2},
	})
	want := []domain.Entity{{Kind: domain.EntityBold, Offset: 1, Length: 2}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestEntitiesFromWire_KeepsAnUnknownKindByName(t *testing.T) {
	t.Parallel()

	got := entitiesFromWire("1234 5678", []tg.MessageEntityClass{&tg.MessageEntityBankCard{Offset: 0, Length: 9}})
	if len(got) != 1 || got[0].Kind != "messageEntityBankCard" {
		t.Fatalf("got %+v, want the constructor name as the kind", got)
	}
}
