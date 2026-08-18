package thread

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestWheel_ScrollsTheThread covers the wheel path added after the first live
// smoke: mouse capture was on, the thread ignored MouseWheelMsg entirely, and
// the wheel did nothing over the one pane where scrolling is the main verb.
func TestWheel_ScrollsTheThread(t *testing.T) {
	t.Parallel()

	repo := newFakeRepo(500)
	m := sized(NewWithRepo(repo, nil, nil))
	m, cmd := m.OpenChat(1)
	m, _ = m.Update(runCmd(t, cmd).(messagesLoadedMsg))

	// A fresh chat opens pinned to the newest message, i.e. at the bottom.
	if !m.viewport.AtBottom() {
		t.Fatal("precondition: a freshly loaded thread should sit at the bottom")
	}
	up, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if up.viewport.AtBottom() {
		t.Error("wheel up did not move the viewport")
	}
	down, _ := up.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if down.viewport.YOffset() <= up.viewport.YOffset() {
		t.Errorf("wheel down did not scroll back: offset %d → %d",
			up.viewport.YOffset(), down.viewport.YOffset())
	}
}

// TestWheel_AtTopPaginatesLikeKeyboard pins that the wheel and the keyboard
// agree about loading older history. If only the keys paginated, scrolling up
// with the wheel would stop dead at the top of the loaded window and read as
// "there is no more history".
func TestWheel_AtTopPaginatesLikeKeyboard(t *testing.T) {
	t.Parallel()

	repo := newFakeRepo(500)
	m := sized(NewWithRepo(repo, nil, nil))
	m, cmd := m.OpenChat(1)
	m, _ = m.Update(runCmd(t, cmd).(messagesLoadedMsg))

	m.viewport.GotoTop()
	scrolled, wheelCmd := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if wheelCmd == nil {
		t.Fatal("wheel up at the top produced no pagination Cmd")
	}
	if !scrolled.Loading() {
		t.Error("pagination started but the loading flag was not set")
	}
	if _, ok := runCmd(t, wheelCmd).(messagesPaginationLoadedMsg); !ok {
		t.Errorf("pagination Cmd produced %T, want messagesPaginationLoadedMsg", runCmd(t, wheelCmd))
	}
}

// TestWheel_HorizontalDoesNotPaginate keeps a sideways trackpad nudge from
// firing a network-shaped page load. The viewport treats it as horizontal
// scrolling; the pane must not read it as "the user reached the top".
func TestWheel_HorizontalDoesNotPaginate(t *testing.T) {
	t.Parallel()

	repo := newFakeRepo(500)
	m := sized(NewWithRepo(repo, nil, nil))
	m, cmd := m.OpenChat(1)
	m, _ = m.Update(runCmd(t, cmd).(messagesLoadedMsg))

	// The app filters horizontal wheels before they reach the pane; this covers
	// the pane's own behaviour if that filter is ever relaxed.
	m.viewport.GotoBottom()
	_, sideCmd := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelRight})
	if sideCmd != nil {
		t.Errorf("horizontal wheel at the bottom produced %T", runCmd(t, sideCmd))
	}
}
