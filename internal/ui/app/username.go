package app

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	coresync "github.com/kar43lov/lazytg/internal/core/sync"
	"github.com/kar43lov/lazytg/internal/ui/palette"
)

// A conversation that is not in the list yet is reached by its handle,
// from the palette: "@name" or a t.me address with no local match hands
// the name to the server, and the answer opens like any other chat.

// usernameResolvedMsg is the server's answer to an "@name" from the
// palette.
type usernameResolvedMsg struct {
	name string
	chat domain.Chat
	err  error
}

// resolveTimeout bounds the one request a typed handle costs.
const resolveTimeout = 15 * time.Second

func (a App) handleOpenUsername(msg palette.OpenUsernameMsg) (tea.Model, tea.Cmd) {
	a = a.closePalette()
	if a.actions == nil {
		a.status = a.status.SetNotice("cannot open @" + msg.Username + ": not connected")
		return a, nil
	}
	a.status = a.status.SetNotice("looking up @" + msg.Username + "…")
	actions := a.actions
	name := msg.Username
	return a, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
		defer cancel()
		chat, err := actions.OpenByUsername(ctx, name)
		return usernameResolvedMsg{name: name, chat: chat, err: err}
	}
}

func (a App) applyUsernameResolved(msg usernameResolvedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		a.status = a.status.SetNotice("@" + msg.name + ": " + resolveFailure(msg.err))
		return a, nil
	}
	// From here it is a palette pick: the same path opens the thread,
	// moves the list highlight and, with a forward pending, sends there.
	a.status = a.status.SetNotice("opened @" + msg.name)
	model, cmd := a.handlePaletteSelected(palette.SelectedMsg{ChatID: msg.chat.ID})
	a = model.(App)
	if _, listed := a.chats.ItemByID(msg.chat.ID); !listed {
		// The list reloads on its own a moment later; until then the
		// resolved row is the only place the sender's name lives, and
		// a bot answering as "user-983000232" is what the wait looks
		// like without this.
		a = a.applyDirectory(msg.chat.ID)
		names := map[int64]string{msg.chat.ID: msg.chat.Title}
		for id, name := range a.directoryNames() {
			names[id] = name
		}
		a.thread = a.thread.SetDirectory(names, msg.chat.Type)
	}
	return a, cmd
}

// resolveFailure words the refusal.
func resolveFailure(err error) string {
	if errors.Is(err, coresync.ErrNoSuchUsername) {
		return "no such username"
	}
	var flood *coresync.FloodWaitError
	if errors.As(err, &flood) {
		return "the server asks to wait " + flood.RetryAfter.String() + " before another lookup"
	}
	return err.Error()
}
