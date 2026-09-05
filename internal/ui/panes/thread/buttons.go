package thread

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/ui/safetext"
)

// The keyboard a bot put under a message, drawn as rows of keys below the
// body. One of them is the chosen key when the cursor is on the message;
// ← and → walk the keys, Enter presses the chosen one (the app owns the
// press — it needs the network — and asks the pane which key that is).

// buttonStyle draws a key; chosenButtonStyle the one Enter would press.
var (
	buttonStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	chosenButtonStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("6")).Bold(true)
	buttonHintStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// maxButtonRunes caps a label: the text is the bot's, and a line is not.
const maxButtonRunes = 40

// flatButtons lists a keyboard's keys in reading order, which is the
// order ← and → walk them.
func flatButtons(rows [][]domain.Button) []domain.Button {
	var out []domain.Button
	for _, row := range rows {
		out = append(out, row...)
	}
	return out
}

// renderButtons draws the keyboard. chosen is the index in reading order
// of the key to highlight, or -1 when the cursor is elsewhere.
func renderButtons(rows [][]domain.Button, chosen int, width int) string {
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	idx := 0
	for r, row := range rows {
		if r > 0 {
			b.WriteByte('\n')
		}
		line := make([]string, 0, len(row))
		for _, btn := range row {
			label := "[ " + truncRunes(safetext.CleanLine(btn.Text), maxButtonRunes) + " ]"
			if idx == chosen {
				line = append(line, chosenButtonStyle.Render(label))
			} else {
				line = append(line, buttonStyle.Render(label))
			}
			idx++
		}
		b.WriteString(wrapText(strings.Join(line, " "), width-2))
	}
	if chosen >= 0 {
		b.WriteByte('\n')
		b.WriteString(buttonHintStyle.Render("← → pick a key, Enter to press it"))
	}
	return b.String()
}

// ChosenButton is the key Enter would press: the chosen one on the
// message under the cursor. False when that message has no keyboard.
func (m Model) ChosenButton() (domain.Message, domain.Button, bool) {
	msg, ok := m.CursorMessage()
	if !ok {
		return domain.Message{}, domain.Button{}, false
	}
	keys := flatButtons(msg.Buttons)
	if len(keys) == 0 {
		return domain.Message{}, domain.Button{}, false
	}
	idx := m.buttonCursor
	if idx < 0 || idx >= len(keys) {
		idx = 0
	}
	return msg, keys[idx], true
}

// moveButton walks the keys of the message under the cursor by delta,
// wrapping at the ends. A message without keys is left alone, and the
// caller lets the key fall through to whatever it did before.
func (m Model) moveButton(delta int) (Model, bool) {
	msg, ok := m.CursorMessage()
	if !ok {
		return m, false
	}
	n := len(flatButtons(msg.Buttons))
	if n == 0 {
		return m, false
	}
	m.buttonCursor = ((m.buttonCursor+delta)%n + n) % n
	m.viewport.SetContent(m.renderAll())
	return m, true
}

// chosenIndexFor is the highlight to draw on a message: the chosen index
// when the cursor is on it, none otherwise.
func (m Model) chosenIndexFor(msg domain.Message, cursor bool) int {
	if !cursor || len(msg.Buttons) == 0 {
		return -1
	}
	if n := len(flatButtons(msg.Buttons)); m.buttonCursor >= n {
		return 0
	}
	return m.buttonCursor
}
