package chats

import "testing"

// "The nth chat as shown" is the whole contract: Alt+2 means the second row
// the user can see.
func TestSelectNth_PicksTheRowByPosition(t *testing.T) {
	t.Parallel()

	m := loaded(t, 5)
	m, cmd, ok := m.SelectNth(3)
	if !ok {
		t.Fatal("SelectNth(3) found no chat in a list of five")
	}
	if cmd == nil {
		t.Fatal("nothing was opened")
	}
	if got := m.list.Index(); got != 2 {
		t.Fatalf("selection is on row %d, want the third (index 2)", got)
	}
	selected, ok := m.SelectedItem()
	if !ok {
		t.Fatal("no item selected")
	}
	if selected.Name() != chatName(2) {
		t.Fatalf("selected %q, want %q", selected.Name(), chatName(2))
	}
}

// A number past the end does nothing rather than clamping: landing on the
// last row would open a different chat every time the list changed length.
func TestSelectNth_PastTheEndDoesNothing(t *testing.T) {
	t.Parallel()

	m := loaded(t, 3)
	before := m.list.Index()
	_, cmd, ok := m.SelectNth(9)
	if ok {
		t.Fatal("SelectNth(9) claimed to find a ninth chat in a list of three")
	}
	if cmd != nil {
		t.Fatal("something was opened anyway")
	}
	if m.list.Index() != before {
		t.Fatal("the selection moved")
	}
}

func TestSelectNth_CountsFromOne(t *testing.T) {
	t.Parallel()

	m := loaded(t, 3)
	m, _, ok := m.SelectNth(1)
	if !ok {
		t.Fatal("SelectNth(1) found nothing")
	}
	if got := m.list.Index(); got != 0 {
		t.Fatalf("the first chat is at index %d", got)
	}
	if _, _, ok := m.SelectNth(0); ok {
		t.Fatal("SelectNth(0) selected something")
	}
}

// The folder tabs filter the list, and the shortcut has to mean what is on
// screen rather than what the account has.
func TestSelectNth_CountsWhatTheFolderShows(t *testing.T) {
	t.Parallel()

	m := loaded(t, 5)
	visible := len(m.list.VisibleItems())
	if visible != 5 {
		t.Fatalf("setup: %d visible items", visible)
	}
	if _, _, ok := m.SelectNth(5); !ok {
		t.Fatal("the fifth of five was not found")
	}
	if _, _, ok := m.SelectNth(6); ok {
		t.Fatal("a sixth was found in a list of five")
	}
}
