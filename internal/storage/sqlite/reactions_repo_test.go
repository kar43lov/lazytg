package sqlite_test

import (
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

func TestSetReactions_TouchesNothingElseAboutTheRow(t *testing.T) {
	t.Parallel()

	repo, ctx := openTestRepo(t)

	if err := repo.SaveChat(ctx, domain.Chat{ID: 42, Type: domain.ChatTypePrivate, Title: "friend"}); err != nil {
		t.Fatalf("SaveChat: %v", err)
	}
	msg := domain.Message{ID: 7, ChatID: 42, Date: time.Now(), Text: "the body must survive"}
	if err := repo.SaveMessage(ctx, msg); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}

	rs := []domain.Reaction{{Emoticon: "🔥", Count: 2, Chosen: true}}
	if err := repo.SetReactions(ctx, 42, 7, rs); err != nil {
		t.Fatalf("SetReactions: %v", err)
	}

	got, err := repo.Message(ctx, 42, 7)
	if err != nil {
		t.Fatalf("Message: %v", err)
	}
	// The point of the separate statement: a reaction update carries no
	// message body, so writing it through the upsert would blank the text.
	if got.Text != "the body must survive" {
		t.Fatalf("text is now %q", got.Text)
	}
	if len(got.Reactions) != 1 || got.Reactions[0] != rs[0] {
		t.Fatalf("reactions = %v", got.Reactions)
	}
	if got.ChosenReaction() != "🔥" {
		t.Fatalf("ChosenReaction = %q", got.ChosenReaction())
	}
}

// Reactions arrive for the whole account, including chats whose history was
// never fetched. That is the ordinary case, not an error.
func TestSetReactions_OnAMessageThatIsNotMirrored(t *testing.T) {
	t.Parallel()

	repo, ctx := openTestRepo(t)

	if err := repo.SetReactions(ctx, 999, 1, []domain.Reaction{{Emoticon: "👍", Count: 1}}); err != nil {
		t.Fatalf("SetReactions on an unknown message: %v", err)
	}
}

func TestSaveMessage_CarriesReactions(t *testing.T) {
	t.Parallel()

	repo, ctx := openTestRepo(t)

	if err := repo.SaveChat(ctx, domain.Chat{ID: 42, Type: domain.ChatTypePrivate, Title: "friend"}); err != nil {
		t.Fatalf("SaveChat: %v", err)
	}
	msg := domain.Message{
		ID: 7, ChatID: 42, Date: time.Now(), Text: "hi",
		Reactions: []domain.Reaction{{Emoticon: "👀", Count: 5}},
	}
	if err := repo.SaveMessage(ctx, msg); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	got, err := repo.Message(ctx, 42, 7)
	if err != nil {
		t.Fatalf("Message: %v", err)
	}
	if len(got.Reactions) != 1 || got.Reactions[0].Count != 5 {
		t.Fatalf("reactions = %v", got.Reactions)
	}
}
