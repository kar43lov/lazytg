package thread

import (
	"time"

	"charm.land/lipgloss/v2"

	"github.com/kar43lov/lazytg/internal/core/events"
)

// OutgoingMessage is the projection of an in-flight outgoing message
// that the thread pane needs to render. Mirrors core/sync.OutgoingRecord
// but kept here so the UI does not depend on the storage layout — the
// fields it cares about are a subset.
type OutgoingMessage struct {
	LocalID string
	ChatID  int64
	Text    string
	State   string
	Error   string

	// SentAt is when the composer dispatched the message, used for the row's
	// timestamp until the server echo replaces it with the real one. A zero
	// value renders without a header — the state transitions that rebuild this
	// struct must carry it over.
	SentAt time.Time
}

// pendingStyle / failedStyle decorate the optimistic prefix glyphs.
// Pending uses no colour (the glyph alone reads as "in flight");
// failed is bright red (ANSI 9) so the user notices a stuck message
// without having to scroll the whole thread.
var (
	pendingStyle = lipgloss.NewStyle()
	failedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

// RenderOptimistic renders an in-flight outgoing message. The output
// is a single styled line ready to be appended to the viewport content
// alongside regular messages.
//
// State semantics:
//   - pending → "[⏳] <text>"
//   - failed  → "[✗] <text>" in red, with the failure reason on a
//     trailing line if present (so the user can see why and decide
//     whether to retry from the composer)
//   - sent    → just <text>, no glyph (a sent message is
//     indistinguishable from a regular incoming one)
//
// Unknown states render bare text plus a bug-bait "[?]" prefix so a
// future state we forgot to handle is visually obvious during
// development.
func RenderOptimistic(msg OutgoingMessage) string {
	var glyph, body string
	switch msg.State {
	case events.OutgoingStatePending:
		glyph = pendingStyle.Render("⏳")
		body = msg.Text
	case events.OutgoingStateFailed:
		glyph = failedStyle.Render("✗")
		body = msg.Text
		if msg.Error != "" {
			body += "\n" + failedStyle.Render("error: "+msg.Error)
		}
	case events.OutgoingStateSent:
		body = msg.Text
	default:
		glyph = "?"
		body = msg.Text
	}
	if msg.SentAt.IsZero() {
		// No dispatch time recorded (a state event that arrived before the
		// composer's own message). A header with a fabricated time would be
		// worse than none: it would read as a real timestamp.
		if glyph != "" {
			return "[" + glyph + "] " + body
		}
		return body
	}
	return renderHeader(msg.SentAt, selfAuthorLabel, glyph) + "\n" + body
}

// selfAuthorLabel names the account's own messages while they are in flight.
// "you" rather than the numeric id: the id is meaningless to the reader, and
// the row is by definition theirs.
const selfAuthorLabel = "you"
