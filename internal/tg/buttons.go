package tg

import (
	"github.com/gotd/td/tg"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// ButtonsFromMessage reads the keyboard under a message: the inline kind,
// whose buttons call the bot back or open a link, and the reply kind,
// whose buttons send their text. Both become rows of domain buttons;
// "hide keyboard" and "force reply" carry no buttons and read as none.
func ButtonsFromMessage(m *tg.Message) [][]domain.Button {
	markup, ok := m.GetReplyMarkup()
	if !ok {
		return nil
	}
	return buttonsFromMarkup(markup)
}

func buttonsFromMarkup(markup tg.ReplyMarkupClass) [][]domain.Button {
	switch mk := markup.(type) {
	case *tg.ReplyInlineMarkup:
		return convertRows(mk.Rows, false)
	case *tg.ReplyKeyboardMarkup:
		return convertRows(mk.Rows, true)
	}
	return nil
}

// convertRows keeps the shape the bot chose. replyKeyboard says the rows
// come from a reply keyboard, where a plain button sends its text; on an
// inline keyboard the same wire type is a label the client cannot act on.
func convertRows(rows []tg.KeyboardButtonRow, replyKeyboard bool) [][]domain.Button {
	out := make([][]domain.Button, 0, len(rows))
	for _, row := range rows {
		buttons := make([]domain.Button, 0, len(row.Buttons))
		for _, b := range row.Buttons {
			buttons = append(buttons, convertButton(b, replyKeyboard))
		}
		if len(buttons) > 0 {
			out = append(out, buttons)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func convertButton(b tg.KeyboardButtonClass, replyKeyboard bool) domain.Button {
	switch btn := b.(type) {
	case *tg.KeyboardButtonCallback:
		return domain.Button{Text: btn.Text, Kind: domain.ButtonCallback, Data: btn.Data}
	case *tg.KeyboardButtonURL:
		return domain.Button{Text: btn.Text, Kind: domain.ButtonURL, URL: btn.URL}
	case *tg.KeyboardButtonCopy:
		return domain.Button{Text: btn.Text, Kind: domain.ButtonCopy, URL: btn.CopyText}
	case *tg.KeyboardButton:
		if replyKeyboard {
			return domain.Button{Text: btn.Text, Kind: domain.ButtonText}
		}
		return domain.Button{Text: btn.Text, Kind: domain.ButtonOther}
	}
	// Every other kind — buy, game, login, web app, switch-inline, the
	// request-* family — is drawn with its label and refused on press.
	return domain.Button{Text: b.GetText(), Kind: domain.ButtonOther}
}
