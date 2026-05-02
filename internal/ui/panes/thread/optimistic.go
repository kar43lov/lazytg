package thread

import (
	"charm.land/lipgloss/v2"

	"github.com/pgmac/lazytg/internal/core/events"
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
	switch msg.State {
	case events.OutgoingStatePending:
		return pendingStyle.Render("[⏳] ") + msg.Text
	case events.OutgoingStateFailed:
		out := failedStyle.Render("[✗] ") + msg.Text
		if msg.Error != "" {
			out += "\n" + failedStyle.Render("error: "+msg.Error)
		}
		return out
	case events.OutgoingStateSent:
		return msg.Text
	default:
		return "[?] " + msg.Text
	}
}
