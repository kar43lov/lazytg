package thread

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/kar43lov/lazytg/internal/core/domain"
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

// Same shape as the reply pointer: a message that arrived live must keep
// its formatting, or it renders flat until the chat is reopened.
func TestApplyIncoming_CarriesEntities(t *testing.T) {
	t.Parallel()

	m := New()
	m, _ = m.OpenChat(42)
	want := domain.Entity{Kind: domain.EntityBold, Offset: 0, Length: 3}
	m, _ = m.Update(events.MessageReceived{
		ChatID: 42, MessageID: 1, Text: "big news", Date: time.Now(),
		Entities: []domain.Entity{want},
	})
	msgs := m.Messages()
	if len(msgs) != 1 || len(msgs[0].Entities) != 1 || msgs[0].Entities[0] != want {
		t.Fatalf("live message carries %+v", msgs)
	}
}

// An edit arriving live replaces the row where it stands — text, spans and
// the "edited" mark — and neither appends nor moves the reader. A message the
// pane does not hold is left alone rather than inserted where it does not
// belong.
func TestApplyIncoming_EditReplacesTheRowInPlace(t *testing.T) {
	t.Parallel()

	m := sized(New())
	m, _ = m.OpenChat(42)
	when := time.Now()
	m, _ = m.Update(events.MessageReceived{ChatID: 42, MessageID: 1, Text: "first", Date: when})
	m, _ = m.Update(events.MessageReceived{ChatID: 42, MessageID: 2, Text: "second", Date: when.Add(time.Second)})

	bold := domain.Entity{Kind: domain.EntityBold, Offset: 0, Length: 5}
	m, _ = m.Update(events.MessageReceived{
		ChatID: 42, MessageID: 1, Text: "fixed", Date: when, Edited: true,
		Entities: []domain.Entity{bold}, EditDate: when.Add(time.Minute),
	})
	m, _ = m.Update(events.MessageReceived{ChatID: 42, MessageID: 9, Text: "not loaded", Date: when, Edited: true})

	msgs := m.Messages()
	if len(msgs) != 2 {
		t.Fatalf("thread holds %d messages, want 2 (an edit appends nothing)", len(msgs))
	}
	if msgs[0].ID != 1 || msgs[0].Text != "fixed" || len(msgs[0].Entities) != 1 || msgs[0].EditDate.IsZero() {
		t.Fatalf("edited row is %+v", msgs[0])
	}
	if !strings.Contains(ansi.Strip(m.View()), "edited") {
		t.Fatalf("the header does not say edited:\n%s", m.View())
	}
}
