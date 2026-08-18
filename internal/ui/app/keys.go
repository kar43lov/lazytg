package app

import (
	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/ui/palette"
	uisearch "github.com/kar43lov/lazytg/internal/ui/panes/search"
)

// focusCycledMsg is the tea.Msg emitted by cmdNextFocus / cmdPrevFocus when
// Tab/Shift-Tab moves focus. Update consumes it to apply the focus change.
// Routing through a Cmd (rather than mutating the model in-line) lets tests
// observe the cycle as an externally visible event and matches the rest of
// the bubbletea event-loop convention.
type focusCycledMsg struct{ Direction int }

// helpToggledMsg flips the help overlay visibility. Same rationale as
// focusCycledMsg — go through Cmd so behaviour is observable in tests.
type helpToggledMsg struct{}

// cmdNextFocus advances focus to the next FocusTarget in spatial order.
// chatCycledMsg is emitted by cmdNextChat / cmdPrevChat. Chat switching is a
// global gesture — it works from the composer, so a conversation can be changed
// mid-sentence without a focus dance first — and going through a Cmd keeps it
// observable in tests, the same way focus cycling does.
type chatCycledMsg struct{ Delta int }

func cmdNextChat() tea.Cmd {
	return func() tea.Msg { return chatCycledMsg{Delta: +1} }
}

func cmdPrevChat() tea.Cmd {
	return func() tea.Msg { return chatCycledMsg{Delta: -1} }
}

func cmdNextFocus() tea.Cmd {
	return func() tea.Msg { return focusCycledMsg{Direction: +1} }
}

// cmdPrevFocus moves focus to the previous FocusTarget in spatial order.
func cmdPrevFocus() tea.Cmd {
	return func() tea.Msg { return focusCycledMsg{Direction: -1} }
}

// cmdToggleHelp emits a helpToggledMsg.
func cmdToggleHelp() tea.Cmd {
	return func() tea.Msg { return helpToggledMsg{} }
}

// cmdOpenSearch emits a OpenedMsg the search overlay reacts to
// by becoming Visible and focusing its textinput. Routing through the
// Cmd path keeps the open behaviour observable from tests.
func cmdOpenSearch() tea.Cmd {
	return func() tea.Msg { return uisearch.OpenedMsg{} }
}

// cmdOpenPalette emits a palette.OpenedMsg the command palette
// reacts to by becoming Visible, focusing its textinput, and
// scheduling the candidate-list refresh. Same Cmd-path rationale as
// cmdOpenSearch.
func cmdOpenPalette() tea.Cmd {
	return func() tea.Msg { return palette.OpenedMsg{} }
}

// cmdQuit is tea.Quit — re-exported under our naming convention so call
// sites can stay self-explanatory and so future replacements (e.g. a "are
// you sure?" confirmation) only need to be wired in one place.
func cmdQuit() tea.Cmd {
	return tea.Quit
}
