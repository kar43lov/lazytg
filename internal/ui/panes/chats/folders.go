package chats

import (
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/ui/safetext"
)

// Chat folders.
//
// Telegram stores the folder definitions and every client decides what to do
// with them. Here they narrow the list and nothing else: there is no editing,
// no creating, no reordering. Those belong in a settings screen, and getting
// them wrong changes what the user sees on their phone.
//
// The definitions are rules rather than lists of chats — see domain.Folder —
// so membership is recomputed on every reload. A chat that arrives after the
// folder was defined lands in it immediately, which is the behaviour every
// other client has and the reason folders are worth having at all.

// SetFoldersMsg installs the folders read from Telegram. Sent once after the
// session attaches; folders change rarely enough that re-reading them on a
// schedule would be traffic for nothing.
type SetFoldersMsg struct {
	Folders []domain.Folder
}

// folderAll is the index of the pseudo-tab that applies no filter. It is not
// a Telegram folder — the API has a DialogFilterDefault for "All chats" and
// this pane drops it, because two tabs meaning the same thing is one too
// many.
const folderAll = -1

// SetFolders installs the folder list, keeping the active tab if the folder
// it names still exists. A folder deleted on another device therefore drops
// the user back to the unfiltered list rather than leaving them looking at an
// empty pane with no way to tell why.
func (m Model) SetFolders(folders []domain.Folder) (Model, tea.Cmd) {
	prev := m.activeFolderID()
	had := len(m.folders) > 0
	m.folders = folders
	m.folderIdx = folderAll
	for i, f := range folders {
		if f.ID == prev {
			m.folderIdx = i
			break
		}
	}
	// The strip costs a row, so the list has to be resized when it appears
	// or goes away — otherwise the pane draws a row the list does not know
	// about and the bottom chat falls off the end.
	if had != (len(folders) > 0) {
		m = m.SetSize(m.Width, m.Height)
	}
	return m.applyLoaded(m.chats)
}

// Folders returns the installed folders. Test helper.
func (m Model) Folders() []domain.Folder { return m.folders }

// ActiveFolder returns the folder currently narrowing the list, and false when
// the list is unfiltered.
func (m Model) ActiveFolder() (domain.Folder, bool) {
	if m.folderIdx < 0 || m.folderIdx >= len(m.folders) {
		return domain.Folder{}, false
	}
	return m.folders[m.folderIdx], true
}

func (m Model) activeFolderID() int64 {
	if f, ok := m.ActiveFolder(); ok {
		return f.ID
	}
	return 0
}

// NextFolder and PrevFolder cycle the tabs, wrapping through the unfiltered
// list. Wrapping rather than stopping at the ends: the tab strip is short and
// circular movement is what a person expects from a row of tabs they are
// stepping through with one key.
func (m Model) NextFolder() (Model, tea.Cmd) { return m.moveFolder(1) }

// PrevFolder steps one tab to the left.
func (m Model) PrevFolder() (Model, tea.Cmd) { return m.moveFolder(-1) }

func (m Model) moveFolder(delta int) (Model, tea.Cmd) {
	if len(m.folders) == 0 {
		return m, nil
	}
	// Index space is [-1, len): -1 is the unfiltered tab.
	span := len(m.folders) + 1
	pos := m.folderIdx + 1 + delta
	pos = ((pos % span) + span) % span
	m.folderIdx = pos - 1
	return m.applyLoaded(m.chats)
}

// SelectFolder jumps to a tab by position, one-based as the user sees it,
// where 1 is the unfiltered list. Out-of-range positions are ignored rather
// than clamped: Alt+7 in an account with three folders means the user
// mis-pressed, and moving them somewhere they did not ask for is worse than
// doing nothing.
func (m Model) SelectFolder(oneBased int) (Model, tea.Cmd) {
	if oneBased < 1 || oneBased > len(m.folders)+1 {
		return m, nil
	}
	m.folderIdx = oneBased - 2
	return m.applyLoaded(m.chats)
}

// visibleChats narrows the master slice to the active folder.
func (m Model) visibleChats(items []ChatItem) []ChatItem {
	folder, ok := m.ActiveFolder()
	if !ok {
		return items
	}
	out := make([]ChatItem, 0, len(items))
	for _, it := range items {
		if folder.Matches(it.Chat()) {
			out = append(out, it)
		}
	}
	return out
}

// The active tab is bracketed as well as coloured. Colour alone fails two
// ways that matter here: a monochrome-ish theme flattens the difference, and
// lipgloss underlining emits a full escape pair per character, which triples
// the size of a row that is redrawn on every frame.
var (
	folderTabStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	folderActiveTabStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
)

// folderStrip renders the tab row, or an empty string when the account has no
// folders — an account with none should look exactly as it did before folders
// existed, rather than gaining a row that says "All".
func (m Model) folderStrip(width int) string {
	if len(m.folders) == 0 {
		return ""
	}
	labels := make([]string, 0, len(m.folders)+1)
	labels = append(labels, m.styleTab("All", m.folderIdx == folderAll))
	for i, f := range m.folders {
		labels = append(labels, m.styleTab(safetext.CleanLine(f.Label()), m.folderIdx == i))
	}
	strip := strings.Join(labels, " ")
	if width > 0 && lipgloss.Width(strip) > width {
		strip = truncateTabs(labels, width)
	}
	return strip
}

func (m Model) styleTab(label string, active bool) string {
	if active {
		return folderActiveTabStyle.Render("[" + label + "]")
	}
	return folderTabStyle.Render(" " + label + " ")
}

// truncateTabs drops tabs from the right until the strip fits, marking the
// loss with an ellipsis. The active tab is kept even when it would be cut:
// a strip that hides the tab you are on tells you nothing about where you
// are.
func truncateTabs(labels []string, width int) string {
	const ellipsis = "…"
	out := make([]string, 0, len(labels))
	used := 0
	for _, l := range labels {
		w := lipgloss.Width(l) + 1
		if used+w > width-lipgloss.Width(ellipsis) {
			out = append(out, ellipsis)
			break
		}
		out = append(out, l)
		used += w
	}
	return strings.Join(out, " ")
}

// listItems converts the visible slice into what bubbles/list consumes.
//
// width is what the delegate will give each row: the list's width less the
// two columns of padding the default delegate puts in front of a title.
func listItems(items []ChatItem, width int) []list.Item {
	out := make([]list.Item, len(items))
	for i, it := range items {
		out[i] = it.withWidth(width)
	}
	return out
}

// rowWidth is the room a row has inside the list.
func (m Model) rowWidth() int {
	w := m.list.Width() - rowPadding
	if w < 0 {
		return 0
	}
	return w
}

// rowPadding is the default delegate's left padding on a title, which it
// takes out of the width before truncating.
const rowPadding = 2
