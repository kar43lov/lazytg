package sqlite_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// The formatting column rides the same upsert as everything else about a
// message. A column the insert lists and the scan forgets comes back nil,
// which for formatting means every message rendered flat.
func TestSaveMessage_CarriesEntities(t *testing.T) {
	t.Parallel()

	repo, ctx := openTestRepo(t)
	if err := repo.SaveChat(ctx, domain.Chat{ID: 42, Type: domain.ChatTypePrivate, Title: "friend"}); err != nil {
		t.Fatalf("SaveChat: %v", err)
	}
	want := []domain.Entity{
		{Kind: domain.EntityBold, Offset: 0, Length: 2},
		{Kind: domain.EntityTextURL, Offset: 3, Length: 4, URL: "https://x.y/z"},
	}
	if err := repo.SaveMessage(ctx, domain.Message{ID: 7, ChatID: 42, Date: time.Now(), Text: "hi there", Entities: want}); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	got, err := repo.Message(ctx, 42, 7)
	if err != nil {
		t.Fatalf("Message: %v", err)
	}
	if !reflect.DeepEqual(got.Entities, want) {
		t.Fatalf("entities = %+v, want %+v", got.Entities, want)
	}
	page, err := repo.GetMessages(ctx, 42, 10, 0)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(page) != 1 || !reflect.DeepEqual(page[0].Entities, want) {
		t.Fatalf("page carries %+v, want %+v", page, want)
	}
}
