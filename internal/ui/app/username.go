package app

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	coresync "github.com/kar43lov/lazytg/internal/core/sync"
	"github.com/kar43lov/lazytg/internal/ui/palette"
)

// A conversation that is not in the list yet is reached by its handle,
// from the palette: "@name" or a t.me address with no local match hands
// the name to the server, and the answer opens like any other chat.

// usernameResolvedMsg is the server's answer to an "@name" from the
// palette.
type usernameResolvedMsg struct {
	name   string
	chatID int64
	err    error
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
		id, err := actions.OpenByUsername(ctx, name)
		return usernameResolvedMsg{name: name, chatID: id, err: err}
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
	return a.handlePaletteSelected(palette.SelectedMsg{ChatID: msg.chatID})
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
