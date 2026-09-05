package domain

import "encoding/json"

// The keyboard a bot puts under a message.
//
// Half of what a developer uses Telegram for is bots, and a bot's message
// with buttons that cannot be pressed is a form with the submit button
// torn off. The keyboard rides on the message (reply_markup) and changes
// when the bot edits the message, which is how most bots answer a press.
//
// Two kinds of keyboard exist on the wire and both are kept as rows of
// buttons: the inline keyboard, drawn under the message, whose buttons
// call the bot back or open a link; and the reply keyboard, which every
// official client draws in place of the composer, whose buttons send
// their text as an ordinary message. Here both are drawn under the
// message — one place, one gesture — and the kind of each button decides
// what pressing it does.

// ButtonKind is what pressing a button does.
type ButtonKind string

const (
	// ButtonCallback calls the bot back with the button's data.
	ButtonCallback ButtonKind = "callback"
	// ButtonURL opens a web address.
	ButtonURL ButtonKind = "url"
	// ButtonText sends the button's text as a message, the reply-keyboard
	// kind.
	ButtonText ButtonKind = "text"
	// ButtonCopy puts a text on the clipboard.
	ButtonCopy ButtonKind = "copy"
	// ButtonOther is a kind this client does not act on — a payment, a
	// web app, a login, a game. Drawn so the message reads as the bot
	// meant it, refused on press with a word about why.
	ButtonOther ButtonKind = "other"
)

// Button is one key of a keyboard.
type Button struct {
	Text string
	Kind ButtonKind
	// Data is the callback payload, for ButtonCallback.
	Data []byte
	// URL is the address for ButtonURL, and the text for ButtonCopy.
	URL string
}

// storedButton is the on-disk shape; short keys, the column is written on
// every upsert.
type storedButton struct {
	T string `json:"t"`
	K string `json:"k"`
	D []byte `json:"d,omitempty"`
	U string `json:"u,omitempty"`
}

// EncodeButtons renders a keyboard for storage; no keyboard is the empty
// string, the column default.
func EncodeButtons(rows [][]Button) string {
	if len(rows) == 0 {
		return ""
	}
	out := make([][]storedButton, 0, len(rows))
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		stored := make([]storedButton, 0, len(row))
		for _, b := range row {
			stored = append(stored, storedButton{T: b.Text, K: string(b.Kind), D: b.Data, U: b.URL})
		}
		out = append(out, stored)
	}
	if len(out) == 0 {
		return ""
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(raw)
}

// DecodeButtons is the inverse; malformed input reads as no keyboard.
func DecodeButtons(s string) [][]Button {
	if s == "" {
		return nil
	}
	var stored [][]storedButton
	if err := json.Unmarshal([]byte(s), &stored); err != nil {
		return nil
	}
	rows := make([][]Button, 0, len(stored))
	for _, row := range stored {
		if len(row) == 0 {
			continue
		}
		buttons := make([]Button, 0, len(row))
		for _, b := range row {
			buttons = append(buttons, Button{Text: b.T, Kind: ButtonKind(b.K), Data: b.D, URL: b.U})
		}
		rows = append(rows, buttons)
	}
	if len(rows) == 0 {
		return nil
	}
	return rows
}
