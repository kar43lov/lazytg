package app

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	coresync "github.com/kar43lov/lazytg/internal/core/sync"
	"github.com/kar43lov/lazytg/internal/ui/input"
	"github.com/kar43lov/lazytg/internal/ui/safetext"
)

// Pressing the chosen key of a bot's keyboard. The pane knows which key
// it is; what pressing does depends on the kind: a callback goes to the
// bot, a link to the browser, a reply-keyboard key into the composer to
// be sent by hand, a copy key to the clipboard. The rest is refused with
// a word about why.

// buttonPressedMsg is the bot's answer to a callback press.
type buttonPressedMsg struct {
	label  string
	answer coresync.CallbackAnswer
	err    error
}

// pressTimeout bounds the one request a press costs. Telegram itself
// gives the bot about ten seconds.
const pressTimeout = 20 * time.Second

func (a App) cmdPressButton() (App, tea.Cmd, bool) {
	msg, btn, ok := a.thread.ChosenButton()
	if !ok {
		return a, nil, false
	}
	label := safetext.CleanLine(btn.Text)
	switch btn.Kind {
	case domain.ButtonCallback:
		if a.actions == nil {
			a.status = a.status.SetNotice("cannot press " + quoteLabel(label) + ": not connected")
			return a, nil, true
		}
		a.status = a.status.SetNotice("pressing " + quoteLabel(label) + "…")
		actions := a.actions
		chatID, messageID, data := msg.ChatID, msg.ID, btn.Data
		return a, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), pressTimeout)
			defer cancel()
			answer, err := actions.PressButton(ctx, chatID, messageID, data)
			return buttonPressedMsg{label: label, answer: answer, err: err}
		}, true
	case domain.ButtonURL:
		if link := httpOnly(btn.URL); link != "" {
			return a, a.cmdOpenLink(link), true
		}
		a.status = a.status.SetNotice("not opened: " + quoteLabel(label) + " is not an http or https link")
		return a, nil, true
	case domain.ButtonText:
		// A reply-keyboard key sends its text as a message. It goes into
		// the composer rather than out on the wire: the press is one key,
		// and a message should cost the one everybody knows sends it.
		a = a.setFocus(FocusInput)
		a.status = a.status.SetNotice(quoteLabel(label) + " is in the composer — Enter sends it")
		text := btn.Text
		return a, func() tea.Msg { return input.InsertTextMsg{Text: text} }, true
	case domain.ButtonCopy:
		a.status = a.status.SetNotice("copied what " + quoteLabel(label) + " holds")
		return a, tea.SetClipboard(btn.URL), true
	default:
		a.status = a.status.SetNotice(quoteLabel(label) + " is a kind of button this client cannot press (payment, web app, login or game)")
		return a, nil, true
	}
}

func (a App) applyButtonPressed(msg buttonPressedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		var flood *coresync.FloodWaitError
		if errors.As(msg.err, &flood) {
			a.status = a.status.SetNotice("the server asks to wait " + flood.RetryAfter.String() + " before another press")
			return a, nil
		}
		a.status = a.status.SetNotice("could not press " + quoteLabel(msg.label) + ": " + msg.err.Error())
		return a, nil
	}
	answer := msg.answer
	switch {
	case answer.URL != "":
		if link := httpOnly(answer.URL); link != "" {
			return a, a.cmdOpenLink(link)
		}
		a.status = a.status.SetNotice("the bot answered with a link that is not http or https; not opened")
	case answer.Message != "":
		text := safetext.CleanLine(answer.Message)
		if answer.Alert {
			text = "⚠ " + text
		}
		a.status = a.status.SetNotice(text)
	default:
		a.status = a.status.SetNotice("pressed " + quoteLabel(msg.label))
	}
	return a, nil
}

func quoteLabel(label string) string {
	return "\u201c" + label + "\u201d"
}
