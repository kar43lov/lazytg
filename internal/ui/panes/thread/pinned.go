package thread

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
	"github.com/kar43lov/lazytg/internal/ui/safetext"
)

// The pinned message, in a bar above the conversation.
//
// Every client shows the newest pinned message where the reader cannot
// miss it: it is the one message the group decided everyone should see.
// The bar takes a row from the viewport only while there is something to
// show, and it shows the newest pinned message the pane holds — a pinned
// message older than the loaded window is not fetched for it, which is
// the honest limit of a bar drawn from the mirror rather than from a
// search request per chat opened.

var pinnedBarStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))

// pinnedMessage is the newest pinned message in the window.
func (m Model) pinnedMessage() (domain.Message, bool) {
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].Pinned {
			return m.messages[i], true
		}
	}
	return domain.Message{}, false
}

// barRows is how many rows the bar takes: one, or none.
func (m Model) barRows() int {
	if _, ok := m.pinnedMessage(); ok {
		return 1
	}
	return 0
}

// pinnedBar renders the bar, empty when there is nothing pinned in view.
func (m Model) pinnedBar() string {
	msg, ok := m.pinnedMessage()
	if !ok {
		return ""
	}
	text := safetext.CleanLine(msg.Text)
	if text == "" && msg.Media != nil {
		text = mediaLabel(msg.Media)
	}
	width := m.Width - paneHPadding
	if width < minViewportWidth {
		width = minViewportWidth
	}
	return pinnedBarStyle.Render(truncRunes(pinnedMark+" "+resolveAuthor(msg, m.chatID, m.private, m.authorNames)+": "+text, width))
}

// PinnedMessageID is the id of the message the bar shows, zero for none.
func (m Model) PinnedMessageID() int64 {
	if msg, ok := m.pinnedMessage(); ok {
		return msg.ID
	}
	return 0
}

// applyPinned flags or unflags the named messages and relays out: the
// bar appears or goes, and the viewport gives up or takes back its row.
func (m Model) applyPinned(ev events.MessagesPinned) (Model, tea.Cmd) {
	if ev.ChatID != m.chatID || len(ev.IDs) == 0 {
		return m, nil
	}
	wanted := make(map[int64]bool, len(ev.IDs))
	for _, id := range ev.IDs {
		wanted[id] = true
	}
	changed := false
	msgs := make([]domain.Message, len(m.messages))
	copy(msgs, m.messages)
	for i := range msgs {
		if wanted[msgs[i].ID] && msgs[i].Pinned != ev.Pinned {
			msgs[i].Pinned = ev.Pinned
			changed = true
		}
	}
	if !changed {
		return m, nil
	}
	m.messages = msgs
	return m.relayout(), nil
}

// relayout re-applies the size so the viewport's height follows the bar.
func (m Model) relayout() Model {
	if m.Width == 0 && m.Height == 0 {
		m.viewport.SetContent(m.renderAll())
		return m
	}
	return m.SetSize(m.Width, m.Height)
}
