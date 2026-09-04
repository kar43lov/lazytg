package overlay

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// Confirm is the modal that stands between the user and something they
// cannot undo.
//
// It exists for exactly one action today — deleting messages — and the shape
// is dictated by that action rather than by a wish for a general dialog
// widget. Deleting in Telegram is two different acts wearing one word: remove
// my copy, or remove it from the other person's device too. A yes/no prompt
// would have to pick one silently, and either choice is wrong half the time —
// so the two are separate keys and the prompt says what each one does.
//
// There is no default action and no "press Enter to continue". The keys that
// destroy something are letters the user has to choose, and every other key
// cancels.
type Confirm struct {
	Width  int
	Height int

	visible bool
	prompt  string
	choices []Choice
}

// Choice is one answer the modal offers: the key that picks it, and the
// sentence describing what it does.
type Choice struct {
	Key   string
	Label string
	// Destructive marks the answer that removes something for other
	// people. Painted in red — the one colour a user reads before the
	// words.
	Destructive bool
}

// NewConfirm returns a hidden modal.
func NewConfirm() Confirm { return Confirm{} }

// Show arms the modal with a prompt and the answers it accepts.
func (c Confirm) Show(prompt string, choices []Choice) Confirm {
	c.visible = true
	c.prompt = prompt
	c.choices = choices
	return c
}

// Hide dismisses the modal.
func (c Confirm) Hide() Confirm {
	c.visible = false
	c.prompt = ""
	c.choices = nil
	return c
}

// Visible reports whether the modal is on screen and should be taking keys.
func (c Confirm) Visible() bool { return c.visible }

// Resolve maps a pressed key to one of the offered answers. It returns the
// matched choice and true; an unmatched key returns false, which every caller
// treats as "cancel" — a modal you can only leave through one specific key is
// a trap, and the key people press to escape one is whichever they reach
// first.
func (c Confirm) Resolve(key string) (Choice, bool) {
	if !c.visible {
		return Choice{}, false
	}
	for _, ch := range c.choices {
		if strings.EqualFold(ch.Key, key) {
			return ch, true
		}
	}
	return Choice{}, false
}

var (
	confirmBorder      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 2)
	confirmPromptStyle = lipgloss.NewStyle().Bold(true)
	confirmKeyStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	confirmDangerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
	confirmHintStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// View renders the modal, or the empty string when it is hidden.
func (c Confirm) View() string {
	if !c.visible {
		return ""
	}
	var b strings.Builder
	b.WriteString(confirmPromptStyle.Render(c.prompt))
	for _, ch := range c.choices {
		b.WriteString("\n")
		style := confirmKeyStyle
		if ch.Destructive {
			style = confirmDangerStyle
		}
		fmt.Fprintf(&b, "  %s  %s", style.Render("["+ch.Key+"]"), ch.Label)
	}
	b.WriteString("\n")
	b.WriteString(confirmHintStyle.Render("  any other key cancels"))
	return confirmBorder.Render(b.String())
}
