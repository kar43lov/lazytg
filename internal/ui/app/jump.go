package app

import (
	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/search"
)

// Following a reply back to what it answers, and coming back again.
//
// A reply quotes one line of what it answers, which is enough to recognise
// the message and never enough to read it. Every graphical client makes the
// quote clickable for that reason; here it is a key, and the way back is
// another — the pair behaves like a jumplist, which is the model the audience
// for this program already has.
//
// The fast path is local: a reply usually sits a few messages below what it
// answers, so the parent is already rendered and the jump is a cursor move
// with no I/O at all. When it is not, the same window loader the search jump
// uses fetches it from the mirror.

// jumpOrigin is a place to come back to.
type jumpOrigin struct {
	chatID    int64
	messageID int64
}

// jumpStackLimit caps how far back the trail remembers.
//
// Deep enough for the chain of replies people actually follow, shallow enough
// that it cannot grow without bound in a long session. The oldest entry is
// dropped rather than refusing the jump: losing the far end of a trail is
// invisible, refusing to move is not.
const jumpStackLimit = 32

// cmdJumpToParent follows the message under the cursor back to the one it
// replies to.
func (a App) cmdJumpToParent() (App, tea.Cmd, bool) {
	msg, ok := a.thread.CursorMessage()
	if !ok {
		return a, nil, false
	}
	if msg.ReplyTo == 0 {
		a.status = a.status.SetNotice("this message is not a reply")
		return a, nil, true
	}
	a = a.pushJumpOrigin(jumpOrigin{chatID: a.thread.ChatID(), messageID: msg.ID})
	return a.goToMessage(msg.ReplyTo)
}

// cmdJumpBack returns to where the last jump started.
func (a App) cmdJumpBack() (App, tea.Cmd, bool) {
	if len(a.jumpStack) == 0 {
		a.status = a.status.SetNotice("nowhere to go back to")
		return a, nil, true
	}
	origin := a.jumpStack[len(a.jumpStack)-1]
	a.jumpStack = a.jumpStack[:len(a.jumpStack)-1]
	if origin.chatID != a.thread.ChatID() {
		// The trail belongs to a conversation the user has left. Sending
		// them back to another chat on a key meant for "undo that jump"
		// would be a surprise.
		a.status = a.status.SetNotice("that trail belongs to another chat")
		return a, nil, true
	}
	return a.goToMessage(origin.messageID)
}

// goToMessage puts the cursor on a message, loading a window around it when
// it is not on this page.
func (a App) goToMessage(id int64) (App, tea.Cmd, bool) {
	if updated, ok := a.thread.FocusMessage(id); ok {
		a.thread = updated
		a.status = a.status.SetNotice("")
		return a, nil, true
	}
	if a.jumper == nil {
		a.status = a.status.SetNotice("that message is not loaded — scroll up to fetch more")
		return a, nil, true
	}
	// Same loader the search jump uses. The hit is synthetic: the window
	// query needs a chat and a message id and nothing else, and inventing a
	// second path to the same SQL is how the two drift apart.
	a.jumpGen++
	chatID := a.thread.ChatID()
	a.status = a.status.SetNotice("looking it up…")
	return a, jumpContextCmd(a.jumper, search.Hit{
		ChatID:  chatID,
		Message: domain.Message{ID: id, ChatID: chatID},
	}, jumpAround, a.jumpGen), true
}

// pushJumpOrigin records where a jump started, dropping the oldest entry when
// the trail is full.
func (a App) pushJumpOrigin(origin jumpOrigin) App {
	stack := make([]jumpOrigin, 0, len(a.jumpStack)+1)
	stack = append(stack, a.jumpStack...)
	stack = append(stack, origin)
	if len(stack) > jumpStackLimit {
		stack = stack[len(stack)-jumpStackLimit:]
	}
	a.jumpStack = stack
	return a
}

// clearJumpTrail drops the trail. Called on a chat switch: every entry names
// a message in the conversation being left.
func (a App) clearJumpTrail() App {
	a.jumpStack = nil
	return a
}
