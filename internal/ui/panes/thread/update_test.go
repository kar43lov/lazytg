package thread

import (
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/events"
)

// A reply that arrived live had no parent until the chat was reopened, so the
// quoted line and the "go to what this answers" gesture both went missing on
// exactly the messages a user is most likely to try them on.
func TestApplyIncoming_CarriesTheReplyPointer(t *testing.T) {
	t.Parallel()

	m := New()
	m, _ = m.OpenChat(42)
	m, _ = m.Update(events.MessageReceived{
		ChatID: 42, MessageID: 1, Text: "the question", Date: time.Now(),
	})
	m, _ = m.Update(events.MessageReceived{
		ChatID: 42, MessageID: 2, Text: "the answer", Date: time.Now(), ReplyTo: 1,
	})

	msgs := m.Messages()
	if len(msgs) != 2 {
		t.Fatalf("thread holds %d messages", len(msgs))
	}
	if msgs[1].ReplyTo != 1 {
		t.Fatalf("the live reply points at %d, want 1", msgs[1].ReplyTo)
	}
}
