package overlay

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/kar43lov/lazytg/internal/ui/emoji"
	"github.com/kar43lov/lazytg/internal/ui/keymap"
)

// EmojiPicker is the browse-and-pick half of emoji entry.
//
// The `:shortcode` completion in the composer is the fast path and covers
// somebody who knows the name. This covers the rest: you know roughly what
// you want and would recognise it on sight, which is what a picker is for and
// what a completion cannot do.
//
// Typing filters. That is the whole navigation model — there is no separate
// search field to focus and nothing to press before you can type, because in
// a picker of a thousand characters the arrow keys are a way to get lost. The
// category tabs are for browsing without a word in mind.
type EmojiPicker struct {
	Width  int
	Height int

	visible  bool
	query    string
	catIndex int
	cursor   int
	// recent is the characters picked in this session, newest first. Kept
	// in memory rather than on disk: it is worth having within a sitting,
	// and a file of what somebody sent is not worth writing down.
	recent []string
	keymap keymap.Keymap
}

// EmojiPickedMsg is emitted when the user chooses one.
type EmojiPickedMsg struct{ Char string }

// EmojiClosedMsg is emitted when the picker is dismissed with nothing picked.
type EmojiClosedMsg struct{}

// recentCategory is the pseudo-category holding what was picked this session.
const recentCategory = "Recent"

// recentLimit caps how many characters the pseudo-category remembers.
const recentLimit = 24

// NewEmojiPicker returns a hidden picker.
func NewEmojiPicker(km keymap.Keymap) EmojiPicker {
	return EmojiPicker{keymap: km}
}

// Visible reports whether the picker is on screen.
func (p EmojiPicker) Visible() bool { return p.visible }

// Open shows the picker, starting from whatever it was last showing.
func (p EmojiPicker) Open() EmojiPicker {
	p.visible = true
	p.query = ""
	p.cursor = 0
	return p
}

// Close hides it.
func (p EmojiPicker) Close() EmojiPicker {
	p.visible = false
	p.query = ""
	p.cursor = 0
	return p
}

// SetSize records the terminal size.
func (p EmojiPicker) SetSize(w, h int) EmojiPicker {
	p.Width, p.Height = w, h
	return p
}

// Update handles a key while the picker is open.
func (p EmojiPicker) Update(msg tea.Msg) (EmojiPicker, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok || !p.visible {
		return p, nil
	}

	entries := p.entries()
	cols := p.cols()

	switch {
	case k.String() == "esc":
		return p.Close(), func() tea.Msg { return EmojiClosedMsg{} }
	case k.String() == "enter":
		if p.cursor < len(entries) {
			char := entries[p.cursor].Char
			p = p.remember(char)
			p = p.Close()
			return p, func() tea.Msg { return EmojiPickedMsg{Char: char} }
		}
		return p, nil
	case k.String() == "backspace":
		if p.query != "" {
			p.query = trimLastRune(p.query)
			p.cursor = 0
		}
		return p, nil
	case k.String() == "left":
		p.cursor = clampIndex(p.cursor-1, len(entries))
		return p, nil
	case k.String() == "right":
		p.cursor = clampIndex(p.cursor+1, len(entries))
		return p, nil
	case k.String() == "up":
		p.cursor = clampIndex(p.cursor-cols, len(entries))
		return p, nil
	case k.String() == "down":
		p.cursor = clampIndex(p.cursor+cols, len(entries))
		return p, nil
	case key.Matches(k, p.keymap.NextFolder), k.String() == "tab":
		p = p.moveCategory(1)
		return p, nil
	case key.Matches(k, p.keymap.PrevFolder), k.String() == "shift+tab":
		p = p.moveCategory(-1)
		return p, nil
	}

	// Anything printable extends the filter. Checked last so the keys
	// above keep their meaning — "[" is a category step, not a search for
	// a bracket.
	if r := printableRune(k); r != 0 {
		p.query += string(r)
		p.cursor = 0
	}
	return p, nil
}

// categories is the tab strip: Recent first when there is one, then the
// table's own groups.
func (p EmojiPicker) categories() []string {
	cats := emoji.Categories()
	if len(p.recent) > 0 {
		return append([]string{recentCategory}, cats...)
	}
	return cats
}

// entries is what the grid is currently showing: the search result when
// something is typed, the active category otherwise.
func (p EmojiPicker) entries() []emoji.Entry {
	if p.query != "" {
		return emoji.Search(p.query)
	}
	cats := p.categories()
	if len(cats) == 0 {
		return nil
	}
	name := cats[clampIndex(p.catIndex, len(cats))]
	if name == recentCategory {
		out := make([]emoji.Entry, 0, len(p.recent))
		for _, char := range p.recent {
			out = append(out, emoji.Entry{Char: char, Name: char, Code: char, Category: recentCategory})
		}
		return out
	}
	return emoji.InCategory(name)
}

func (p EmojiPicker) moveCategory(delta int) EmojiPicker {
	cats := p.categories()
	if len(cats) == 0 {
		return p
	}
	p.query = ""
	p.cursor = 0
	p.catIndex = (p.catIndex + delta + len(cats)) % len(cats)
	return p
}

// remember pushes a character to the front of the recent list.
func (p EmojiPicker) remember(char string) EmojiPicker {
	out := make([]string, 0, recentLimit)
	out = append(out, char)
	for _, c := range p.recent {
		if c == char {
			continue
		}
		if len(out) == recentLimit {
			break
		}
		out = append(out, c)
	}
	p.recent = out
	return p
}

// cols is how many emoji fit across the box. Three cells each: emoji are
// double-width in every terminal font, and the third column is the gap that
// keeps two of them from reading as one.
func (p EmojiPicker) cols() int {
	w := p.innerWidth()
	if w < 3 {
		return 1
	}
	return w / 3
}

// innerWidth is the room a line of content actually has. boxWidth is the
// outer measure — lipgloss counts the border and the padding inside it — so
// text laid out against boxWidth minus the border alone overflows by the
// padding, and lipgloss wraps the overflow onto a line of its own: a grid
// sized for twenty emoji drew nineteen and orphaned the twentieth on the
// next row, every row. Seen live on 05.09.2026.
func (p EmojiPicker) innerWidth() int {
	return p.boxWidth() - 4
}

func (p EmojiPicker) boxWidth() int {
	w := p.Width - 8
	if w > 62 {
		w = 62
	}
	if w < 20 {
		w = 20
	}
	return w
}

// rows is how many rows of emoji fit, leaving room for the tab strip, the
// filter line, the name of the selection and the border.
func (p EmojiPicker) rows() int {
	h := p.Height - 10
	if h > 10 {
		h = 10
	}
	if h < 2 {
		h = 2
	}
	return h
}

// View renders the picker, or "" when it is hidden.
func (p EmojiPicker) View() string {
	if !p.visible {
		return ""
	}
	entries := p.entries()
	cols, rows := p.cols(), p.rows()
	cursor := clampIndex(p.cursor, len(entries))

	// Scroll by whole rows so the grid does not jump under the cursor.
	firstRow := 0
	if cols > 0 {
		if r := cursor / cols; r >= rows {
			firstRow = r - rows + 1
		}
	}

	var b strings.Builder
	b.WriteString(p.header())
	b.WriteString("\n\n")

	sel := lipgloss.NewStyle().Reverse(true)
	for r := firstRow; r < firstRow+rows; r++ {
		for c := 0; c < cols; c++ {
			i := r*cols + c
			if i >= len(entries) {
				break
			}
			cell := entries[i].Char + " "
			if i == cursor {
				cell = sel.Render(entries[i].Char) + " "
			}
			b.WriteString(cell)
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(p.footer(entries, cursor))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("5")).
		Padding(0, 1).
		Width(p.boxWidth())
	return box.Render(b.String())
}

func (p EmojiPicker) header() string {
	if p.query != "" {
		return lipgloss.NewStyle().Bold(true).Render("search: " + p.query)
	}
	cats := p.categories()
	active := clampIndex(p.catIndex, len(cats))
	parts := make([]string, 0, len(cats))
	for i, c := range cats {
		if i == active {
			parts = append(parts, "["+c+"]")
			continue
		}
		parts = append(parts, " "+c+" ")
	}
	line := strings.Join(parts, "")
	if lipgloss.Width(line) > p.innerWidth() {
		line = "[" + cats[active] + "]"
	}
	return line
}

// pickerHint is the key legend under the grid. Kept under the narrowest box
// this picker draws so it never wraps onto a second line.
const pickerHint = "type to search · ←↑↓→ · tab category · enter · esc"

func (p EmojiPicker) footer(entries []emoji.Entry, cursor int) string {
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	if len(entries) == 0 {
		return dim.Render("nothing matches — backspace to widen it")
	}
	e := entries[cursor]
	name := e.Name
	if e.Category != recentCategory {
		name = fmt.Sprintf("%s  :%s:", e.Name, e.Code)
	}
	return dim.Render(truncate(name, p.innerWidth())) + "\n" +
		dim.Render(truncate(pickerHint, p.innerWidth()))
}

func clampIndex(i, n int) int {
	if n == 0 {
		return 0
	}
	if i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}

func trimLastRune(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return string(r[:len(r)-1])
}

// printableRune returns the character a key press carries, or 0 when the key
// is not one. Modified keys are excluded: Alt+E opened this thing, and it
// must not be typed into the filter on the way in.
func printableRune(k tea.KeyPressMsg) rune {
	if k.Mod != 0 {
		return 0
	}
	runes := []rune(k.Text)
	if len(runes) != 1 {
		return 0
	}
	if runes[0] < ' ' {
		return 0
	}
	return runes[0]
}

func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	return string(r[:w-1]) + "…"
}
