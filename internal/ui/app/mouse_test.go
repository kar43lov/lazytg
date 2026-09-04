package app

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/ui/keymap"
	"github.com/kar43lov/lazytg/internal/ui/panes/chats"
	"github.com/kar43lov/lazytg/internal/ui/panes/thread"
)

// fakeChatsRepo drives the chats pane without touching SQLite.
type fakeChatsRepo struct{ chats []domain.Chat }

func (r fakeChatsRepo) GetChats(context.Context) ([]domain.Chat, error) { return r.chats, nil }

// appWithChats returns a sized App whose chats pane already holds the given
// chats, loaded the way production loads them (Init's Cmd fed back through
// Update) so the pane's internal list state is real rather than constructed.
func appWithChats(t *testing.T, ids ...int64) App {
	t.Helper()
	rows := make([]domain.Chat, len(ids))
	for i, id := range ids {
		rows[i] = domain.Chat{ID: id, Title: "chat", Type: domain.ChatTypePrivate}
	}
	pane := chats.NewWithRepo(fakeChatsRepo{chats: rows}, nil)
	loadCmd := pane.Init()
	if loadCmd == nil {
		t.Fatal("chats pane Init returned no load Cmd")
	}
	pane, _ = pane.Update(loadCmd())

	a := New(Deps{Keymap: keymap.Default(), Chats: &pane})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return model.(App)
}

// appWithLongChats is appWithChats with titles and previews long enough to
// reach the pane edge at any split.
func appWithLongChats(t *testing.T) App {
	t.Helper()
	rows := []domain.Chat{
		{
			ID: 11, Type: domain.ChatTypePrivate,
			Title:              "Иван Егошин с очень длинным именем которое не влезает",
			LastMessagePreview: "а тебе в одно место сообщения приходят от меня, и это довольно длинный текст",
		},
		{
			ID: 22, Type: domain.ChatTypePrivate,
			Title:              "Telegram",
			LastMessagePreview: "Login code: 94532. Do not give this code to anyone, even if they say they are from Telegram!",
		},
	}
	pane := chats.NewWithRepo(fakeChatsRepo{chats: rows}, nil)
	loadCmd := pane.Init()
	if loadCmd == nil {
		t.Fatal("chats pane Init returned no load Cmd")
	}
	pane, _ = pane.Update(loadCmd())

	threadPane := thread.NewWithRepo(wideThreadRepo{}, nil, nil)
	a := New(Deps{Keymap: keymap.Default(), Chats: &pane, Thread: &threadPane})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := model.(App)

	// Load the thread through its own OpenChat path so the pane holds real
	// rendered content rather than an empty viewport.
	opened, cmd := app.thread.OpenChat(11)
	app.thread = opened
	if cmd != nil {
		app.thread, _ = app.thread.Update(cmd())
	}
	return app
}

// wideThreadRepo serves one long message so the thread pane has content wide
// enough to reach its pane edge.
type wideThreadRepo struct{}

func (wideThreadRepo) GetMessages(_ context.Context, chatID int64, _, _ int) ([]domain.Message, error) {
	return []domain.Message{{
		ID: 1, ChatID: chatID, FromID: 11,
		Date: time.Unix(1_700_000_000, 0).UTC(),
		Text: "а тебе в одно место сообщения приходят от меня, и этот текст заведомо шире любой панели",
	}}, nil
}

func (wideThreadRepo) GetMessagesBefore(context.Context, int64, int64, int) ([]domain.Message, error) {
	return nil, nil
}

func (wideThreadRepo) GetMessagesAfter(context.Context, int64, int64, int) ([]domain.Message, error) {
	return nil, nil
}

// click builds a left-button press at (x, y).
func click(x, y int) tea.MouseClickMsg {
	return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
}

// wheel builds a wheel event at (x, y).
func wheel(x, y int, button tea.MouseButton) tea.MouseWheelMsg {
	return tea.MouseWheelMsg{X: x, Y: y, Button: button}
}

// TestMouseClick_FocusesPaneUnderPointer covers the routing the first live
// smoke asked for: mouse capture was already on, so clicks reached the app and
// were dropped — the interface looked frozen to anyone who reached for the
// pointer instead of Tab.
func TestMouseClick_FocusesPaneUnderPointer(t *testing.T) {
	t.Parallel()

	// 120 wide → chats pane is columns 0..35, separator at 36, thread from 37.
	// 40 high → panes are rows 0..35, composer 36..38, status 39.
	cases := []struct {
		name string
		msg  tea.MouseClickMsg
		want FocusTarget
	}{
		{"chats pane", click(5, 4), FocusChats},
		{"thread pane", click(80, 10), FocusThread},
		{"composer", click(20, 37), FocusInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newApp(t)
			// Start from a focus that is never the expected answer, so a test
			// cannot pass on the default.
			a = a.setFocus(FocusThread)
			if tc.want == FocusThread {
				a = a.setFocus(FocusChats)
			}
			model, _ := a.Update(tc.msg)
			if got := model.(App).Focus(); got != tc.want {
				t.Errorf("focus = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMouseClick_SeparatorAndStatusRowAreInert pins that the columns and rows
// belonging to no pane do nothing. Snapping them to a neighbour would move
// focus on a click the user reads as landing on a border.
func TestMouseClick_SeparatorAndStatusRowAreInert(t *testing.T) {
	t.Parallel()

	for _, msg := range []tea.MouseClickMsg{click(36, 10), click(50, 39)} {
		a := newApp(t)
		a = a.setFocus(FocusChats)
		model, cmd := a.Update(msg)
		if got := model.(App).Focus(); got != FocusChats {
			t.Errorf("click at (%d,%d) moved focus to %v", msg.X, msg.Y, got)
		}
		if cmd != nil {
			t.Errorf("click at (%d,%d) produced a Cmd", msg.X, msg.Y)
		}
	}
}

// TestMouseClick_NonLeftButtonIgnored keeps the right button free for the
// terminal's own context menu, which is what a user expects there.
func TestMouseClick_NonLeftButtonIgnored(t *testing.T) {
	t.Parallel()

	a := newApp(t)
	a = a.setFocus(FocusInput)
	model, cmd := a.Update(tea.MouseClickMsg{X: 5, Y: 4, Button: tea.MouseRight})
	if got := model.(App).Focus(); got != FocusInput {
		t.Errorf("right click moved focus to %v", got)
	}
	if cmd != nil {
		t.Error("right click produced a Cmd")
	}
}

// TestMouseClick_OnChatRowSelectsIt is the end-to-end of clicking a chat: the
// pane must both take focus and emit the selection the keyboard path emits.
func TestMouseClick_OnChatRowSelectsIt(t *testing.T) {
	t.Parallel()

	// Two chats: rows 2..4 hold the first, 5..7 the second.
	a := appWithChats(t, 11, 22)

	model, cmd := a.Update(click(3, 5))
	got := model.(App)
	if got.Focus() != FocusChats {
		t.Errorf("focus = %v, want FocusChats", got.Focus())
	}
	if cmd == nil {
		t.Fatal("clicking a chat row produced no Cmd — the chat would not open")
	}
	sel, ok := cmd().(chats.ChatSelectedMsg)
	if !ok {
		t.Fatalf("Cmd produced %T, want chats.ChatSelectedMsg", cmd())
	}
	if sel.ChatID != 22 {
		t.Errorf("selected chat = %d, want 22 (the clicked row)", sel.ChatID)
	}
}

// TestMouseWheel_DoesNotMoveFocus separates reading from typing: scrolling the
// thread while the composer holds focus must not redirect the next keystroke.
func TestMouseWheel_DoesNotMoveFocus(t *testing.T) {
	t.Parallel()

	a := newApp(t)
	a = a.setFocus(FocusInput)
	model, _ := a.Update(wheel(80, 10, tea.MouseWheelDown))
	if got := model.(App).Focus(); got != FocusInput {
		t.Errorf("wheel over the thread moved focus to %v", got)
	}
}

// TestMouseWheel_OverChatsMovesSelection covers the chats-pane wheel: the list
// has no independent scroll offset, so the wheel moves the highlight.
func TestMouseWheel_OverChatsMovesSelection(t *testing.T) {
	t.Parallel()

	a := appWithChats(t, 11, 22)

	model, _ := a.Update(wheel(3, 4, tea.MouseWheelDown))
	sel, ok := model.(App).chats.SelectedItem()
	if !ok {
		t.Fatal("no selection after the wheel")
	}
	if sel.ID() != 22 {
		t.Errorf("selected = %d, want 22 after one notch down", sel.ID())
	}
}

// TestMouseWheel_HorizontalIgnored keeps a trackpad's sideways nudge from
// scrolling the thread vertically.
func TestMouseWheel_HorizontalIgnored(t *testing.T) {
	t.Parallel()

	a := appWithChats(t, 11, 22)

	model, _ := a.Update(wheel(3, 4, tea.MouseWheelRight))
	sel, _ := model.(App).chats.SelectedItem()
	if sel.ID() != 11 {
		t.Errorf("horizontal wheel moved the selection to %d", sel.ID())
	}
}

// TestMouse_IgnoredWhileOverlayVisible pins the modal contract: overlays own
// the screen, so a click behind one must not move focus invisibly.
func TestMouse_IgnoredWhileOverlayVisible(t *testing.T) {
	t.Parallel()

	a := New(Deps{Keymap: keymap.Default()})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	withHelp := model.(App)
	withHelp.help.Visible = true
	withHelp = withHelp.setFocus(FocusChats)

	after, cmd := withHelp.Update(click(80, 10))
	if got := after.(App).Focus(); got != FocusChats {
		t.Errorf("click behind the help overlay moved focus to %v", got)
	}
	if cmd != nil {
		t.Error("click behind the help overlay produced a Cmd")
	}
}

// TestMouse_IgnoredWhenTerminalTooSmall matches the view: the 2-pane frame is
// not drawn at all below 80x24, so its coordinates mean nothing.
func TestMouse_IgnoredWhenTerminalTooSmall(t *testing.T) {
	t.Parallel()

	a := New(Deps{Keymap: keymap.Default()})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	small := model.(App).setFocus(FocusChats)

	after, cmd := small.Update(click(5, 4))
	if got := after.(App).Focus(); got != FocusChats {
		t.Errorf("click in a too-small terminal moved focus to %v", got)
	}
	if cmd != nil {
		t.Error("click in a too-small terminal produced a Cmd")
	}
}

// TestComputeLayout_MatchesResize keeps the geometry used for hit-testing tied
// to the geometry the panes are actually sized with. They were one inline
// calculation before; two copies that agree today would drift.
func TestComputeLayout_MatchesResize(t *testing.T) {
	t.Parallel()

	for _, size := range [][2]int{{120, 40}, {80, 24}, {200, 60}, {81, 25}} {
		a := New(Deps{Keymap: keymap.Default()})
		model, _ := a.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		app := model.(App)
		l := computeLayout(size[0], size[1], 0)

		if app.chats.Width != l.chatsW || app.chats.Height != l.paneH {
			t.Errorf("%dx%d: chats pane is %dx%d, layout says %dx%d",
				size[0], size[1], app.chats.Width, app.chats.Height, l.chatsW, l.paneH)
		}
		if app.thread.Width != l.threadW || app.thread.Height != l.paneH {
			t.Errorf("%dx%d: thread pane is %dx%d, layout says %dx%d",
				size[0], size[1], app.thread.Width, app.thread.Height, l.threadW, l.paneH)
		}
		if l.chatsW+1+l.threadW != size[0] {
			t.Errorf("%dx%d: panes plus separator cover %d columns", size[0], size[1], l.chatsW+1+l.threadW)
		}
	}
}

// press/drag/release build the three events of a pointer drag.
func drag(x, y int) tea.MouseMotionMsg {
	return tea.MouseMotionMsg{X: x, Y: y, Button: tea.MouseLeft}
}

func release(x, y int) tea.MouseReleaseMsg {
	return tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft}
}

// TestSeparatorDrag_ResizesPanes covers the split the user asked to be
// movable. The panes must resize while the pointer moves, not only on release,
// or the drag gives no feedback about where it will land.
func TestSeparatorDrag_ResizesPanes(t *testing.T) {
	t.Parallel()

	a := newApp(t) // 120x40 → chats 36 wide, separator at column 36
	if a.chats.Width != 36 {
		t.Fatalf("precondition: chats width %d, want the 30%% default of 36", a.chats.Width)
	}

	model, _ := a.Update(click(36, 10))
	grabbed := model.(App)
	if !grabbed.draggingSep {
		t.Fatal("clicking the separator did not start a drag")
	}

	model, _ = grabbed.Update(drag(50, 10))
	widened := model.(App)
	if widened.chats.Width != 50 {
		t.Errorf("mid-drag chats width = %d, want 50", widened.chats.Width)
	}
	if widened.thread.Width != 120-50-1 {
		t.Errorf("thread width = %d, want %d (the rest minus the separator)", widened.thread.Width, 120-50-1)
	}

	model, _ = widened.Update(release(60, 10))
	done := model.(App)
	if done.draggingSep {
		t.Error("release did not end the drag")
	}
	if done.chats.Width != 60 {
		t.Errorf("release position ignored: width %d, want 60", done.chats.Width)
	}
}

// TestSeparatorDrag_ClampsToUsableWidths keeps either pane from being dragged
// into a column of syllables, which the user would then have to undo blind.
func TestSeparatorDrag_ClampsToUsableWidths(t *testing.T) {
	t.Parallel()

	a := newApp(t)
	model, _ := a.Update(click(36, 10))

	model, _ = model.(App).Update(drag(1, 10))
	if got := model.(App).chats.Width; got != minPaneWidth {
		t.Errorf("dragged to the far left: chats width %d, want the %d minimum", got, minPaneWidth)
	}

	model, _ = model.(App).Update(drag(119, 10))
	if got := model.(App).chats.Width; got != 120-1-minPaneWidth {
		t.Errorf("dragged to the far right: chats width %d, want %d", got, 120-1-minPaneWidth)
	}
	if got := model.(App).thread.Width; got < minPaneWidth {
		t.Errorf("thread squeezed to %d columns", got)
	}
}

// TestMotionWithoutGrabIsIgnored: motion arrives whenever a button is held, so
// a drag that started inside a pane (a text drag, a mis-click) must not resize
// anything.
func TestMotionWithoutGrabIsIgnored(t *testing.T) {
	t.Parallel()

	a := newApp(t)
	before := a.chats.Width
	model, _ := a.Update(drag(70, 10))
	if got := model.(App).chats.Width; got != before {
		t.Errorf("motion without grabbing the separator resized the panes: %d → %d", before, got)
	}
}

// TestSeparatorSplitSurvivesResize checks the split is kept across terminal
// resizes and re-clamped when the window can no longer honour it — a split that
// stayed put would leave the thread one column wide after a shrink.
func TestSeparatorSplitSurvivesResize(t *testing.T) {
	t.Parallel()

	a := newApp(t)
	model, _ := a.Update(click(36, 10))
	model, _ = model.(App).Update(release(70, 10))
	wide := model.(App)
	if wide.chats.Width != 70 {
		t.Fatalf("precondition: chats width %d, want 70", wide.chats.Width)
	}

	model, _ = wide.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	if got := model.(App).chats.Width; got != 70 {
		t.Errorf("after growing the terminal: chats width %d, want the dragged 70 kept", got)
	}

	model, _ = model.(App).Update(tea.WindowSizeMsg{Width: 82, Height: 30})
	shrunk := model.(App)
	if got := shrunk.chats.Width; got != 82-1-minPaneWidth {
		t.Errorf("after shrinking: chats width %d, want it re-clamped to %d", got, 82-1-minPaneWidth)
	}
	if got := shrunk.thread.Width; got < minPaneWidth {
		t.Errorf("thread squeezed to %d columns after a shrink", got)
	}
}

// TestChatSelection_FocusesComposer pins the "pick a chat, start typing"
// behaviour: both the click and the Enter path go through ChatSelectedMsg, so
// covering the message covers both.
func TestChatSelection_FocusesComposer(t *testing.T) {
	t.Parallel()

	a := appWithChats(t, 11, 22)
	model, _ := a.Update(chats.ChatSelectedMsg{ChatID: 22})
	if got := model.(App).Focus(); got != FocusInput {
		t.Errorf("focus after picking a chat = %v, want FocusInput", got)
	}
}

// TestFrameNeverOverflowsItsWidth is the regression for what dragging the
// separator exposed: both panes were sized to the full box width while the box
// itself spends two columns on padding, so any full-width row was two columns
// too wide. lipgloss wrapped the overflow onto a new line, every row below
// shifted down, and the last chat disappeared out of the pane — text vanishing
// as the split moved.
//
// Asserting on the composed frame (rather than a pane in isolation) is the
// point: the defect only exists once the panes are placed in their boxes.
func TestFrameNeverOverflowsItsWidth(t *testing.T) {
	t.Parallel()

	for _, split := range []int{0, minPaneWidth, 40, 80, 99} {
		// Content long enough to reach the pane edge in both panes — a fixture
		// of short titles cannot overflow anything and would make this test
		// pass against the very defect it exists for.
		a := appWithLongChats(t)
		if split > 0 {
			model, _ := a.Update(click(a.layout().sepX, 5))
			model, _ = model.(App).Update(release(split, 5))
			a = model.(App)
		}
		lines := strings.Split(a.View().Content, "\n")
		if len(lines) > a.height {
			t.Errorf("split=%d: frame is %d rows tall, terminal is %d — the extra rows "+
				"push the composer and the last chats off screen",
				split, len(lines), a.height)
		}
		for i, line := range lines {
			if w := lipgloss.Width(line); w > a.width {
				t.Errorf("split=%d line %d is %d columns wide, terminal is %d: %q",
					split, i, w, a.width, line)
			}
		}
	}
}

// TestChatCycling_WrapsInBothDirections covers the ctrl+tab / ctrl+shift+tab
// gesture: next and previous conversation in the order the list shows (pinned
// first, then by last message — the same order Telegram uses), wrapping at both
// ends, from wherever focus happens to be.
func TestChatCycling_WrapsInBothDirections(t *testing.T) {
	t.Parallel()

	a := appWithChats(t, 11, 22, 33)

	next := func(app App) (App, int64) {
		t.Helper()
		model, cmd := app.Update(chatCycledMsg{Delta: +1})
		if cmd == nil {
			t.Fatal("cycling produced no Cmd — no chat would open")
		}
		sel, ok := cmd().(chats.ChatSelectedMsg)
		if !ok {
			t.Fatalf("Cmd produced %T, want chats.ChatSelectedMsg", cmd())
		}
		return model.(App), sel.ChatID
	}

	app, id := next(a)
	if id != 22 {
		t.Errorf("first next: opened %d, want 22", id)
	}
	app, id = next(app)
	if id != 33 {
		t.Errorf("second next: opened %d, want 33", id)
	}
	app, id = next(app)
	if id != 11 {
		t.Errorf("third next: opened %d, want 11 — the list must wrap", id)
	}

	model, cmd := app.Update(chatCycledMsg{Delta: -1})
	if cmd == nil {
		t.Fatal("cycling back produced no Cmd")
	}
	if sel := cmd().(chats.ChatSelectedMsg); sel.ChatID != 33 {
		t.Errorf("previous from the first chat: opened %d, want 33 (wrap backwards)", sel.ChatID)
	}
	_ = model
}

// TestChatCycling_WorksWhileTyping is the "где бы ты ни находился" half: the
// binding is consumed globally, so a chat switch does not require leaving the
// composer first.
func TestChatCycling_WorksWhileTyping(t *testing.T) {
	t.Parallel()

	a := appWithChats(t, 11, 22)
	a = a.setFocus(FocusInput)

	cmd, handled := a.handleGlobalKey(keyChord('n', tea.ModAlt))
	if !handled {
		t.Fatal("alt+n was not handled globally — it would have gone into the message text")
	}
	if _, ok := cmd().(chatCycledMsg); !ok {
		t.Fatalf("alt+n produced %T, want chatCycledMsg", cmd())
	}

	cmd, handled = a.handleGlobalKey(keyChord('p', tea.ModAlt))
	if !handled {
		t.Fatal("alt+p was not handled globally")
	}
	if msg, ok := cmd().(chatCycledMsg); !ok || msg.Delta != -1 {
		t.Fatalf("alt+p produced %#v, want chatCycledMsg{Delta:-1}", cmd())
	}
}

// TestChatCycling_EmptyListIsNoop guards the pre-load window: the pane has no
// items yet and the gesture must do nothing rather than select index -1.
func TestChatCycling_EmptyListIsNoop(t *testing.T) {
	t.Parallel()

	a := newApp(t)
	model, cmd := a.Update(chatCycledMsg{Delta: +1})
	if cmd != nil {
		t.Errorf("cycling an empty list produced %T", cmd())
	}
	if model.(App).Focus() != a.Focus() {
		t.Error("cycling an empty list changed focus")
	}
}

// TestTextSelection_DragCopiesToClipboard is the end-to-end of the gesture the
// user asked for: press inside the thread, drag, release — and the selected
// text is on the clipboard. The clipboard leg goes through OSC 52, so it works
// over ssh and inside tmux without lazytg shelling out to pbcopy/xclip.
func TestTextSelection_DragCopiesToClipboard(t *testing.T) {
	t.Parallel()

	a := appWithLongChats(t)
	l := a.layout()
	x := l.threadX + 3
	y := threadTextRow(t, a, "сообщения приходят")

	model, _ := a.Update(click(x, y))
	dragging := model.(App)
	if !dragging.draggingText {
		t.Fatal("pressing inside the thread did not start a selection")
	}
	model, _ = dragging.Update(drag(x+20, y))
	model, cmd := model.(App).Update(release(x+20, y))

	after := model.(App)
	if after.draggingText {
		t.Error("release did not end the selection drag")
	}
	if !after.thread.HasSelection() {
		t.Error("nothing stayed highlighted after the drag")
	}
	if cmd == nil {
		t.Fatal("release produced no Cmd — nothing would reach the clipboard")
	}
	// tea.SetClipboard's message type is unexported; asserting it is non-nil and
	// distinct from the no-op path is as far as the public API allows.
	if cmd() == nil {
		t.Error("the clipboard Cmd produced no message")
	}
}

// TestTextSelection_ClickWithoutDragCopiesNothing keeps a plain click from
// leaving an empty highlight sitting in the pane and firing a clipboard write
// with no content.
func TestTextSelection_ClickWithoutDragCopiesNothing(t *testing.T) {
	t.Parallel()

	a := appWithLongChats(t)
	l := a.layout()
	x := l.threadX + 3
	y := threadTextRow(t, a, "сообщения приходят")

	model, _ := a.Update(click(x, y))
	model, cmd := model.(App).Update(release(x, y))
	after := model.(App)

	if cmd != nil {
		t.Errorf("a click with no drag produced %T", cmd())
	}
	if after.thread.HasSelection() {
		t.Error("a click with no drag left a highlight")
	}
}

// TestTextSelection_DragFromChatsDoesNotSelect pins the guard: motion events
// arrive while any button is held, so a drag begun in the chats pane must not
// start selecting text when it crosses the separator.
func TestTextSelection_DragFromChatsDoesNotSelect(t *testing.T) {
	t.Parallel()

	a := appWithLongChats(t)
	l := a.layout()

	model, _ := a.Update(click(3, 3)) // inside the chats pane
	model, _ = model.(App).Update(drag(l.threadX+5, 4))
	if model.(App).thread.HasSelection() {
		t.Error("a drag that started in the chats pane selected thread text")
	}
}

// TestTextSelection_ClearedByClickingElsewhere: the highlight belongs to the
// last gesture, so choosing a chat or the composer drops it.
func TestTextSelection_ClearedByClickingElsewhere(t *testing.T) {
	t.Parallel()

	a := appWithLongChats(t)
	l := a.layout()
	x := l.threadX + 3
	y := threadTextRow(t, a, "сообщения приходят")

	model, _ := a.Update(click(x, y))
	model, _ = model.(App).Update(drag(x+20, y))
	model, _ = model.(App).Update(release(x+20, y))
	if !model.(App).thread.HasSelection() {
		t.Fatal("precondition: expected a selection after the drag")
	}

	model, _ = model.(App).Update(click(3, 3)) // a chat row
	if model.(App).thread.HasSelection() {
		t.Error("clicking a chat left the old thread selection highlighted")
	}
}

// TestTextSelection_DoubleClickTakesTheMessage covers the shortcut for the
// common case — copy this message, whole — without a precise drag.
func TestTextSelection_DoubleClickTakesTheMessage(t *testing.T) {
	t.Parallel()

	a := appWithLongChats(t)
	l := a.layout()
	x := l.threadX + 3
	y := threadTextRow(t, a, "сообщения приходят")

	model, _ := a.Update(click(x, y))
	model, _ = model.(App).Update(release(x, y))
	second, cmd := model.(App).Update(click(x, y))

	if !second.(App).thread.HasSelection() {
		t.Error("double click did not highlight the message")
	}
	if cmd == nil {
		t.Fatal("double click produced no clipboard Cmd")
	}
}

// TestDragThenPressIsNotADoubleClick covers the gesture collision review found:
// a drag ends where it began often enough (a short selection, then a second
// attempt at it), and the two presses land in the same cell inside the 400ms
// window. Reading that as a double click replaces the user's fresh selection
// with the whole message.
func TestDragThenPressIsNotADoubleClick(t *testing.T) {
	t.Parallel()

	a := appWithLongChats(t)
	model, _ := a.Update(click(a.layout().sepX+4, 3))
	app := model.(App)

	// Drag a few cells and release: a real selection, copied to the clipboard.
	model, _ = app.Update(drag(app.layout().sepX+18, 3))
	model, cmd := model.(App).Update(release(app.layout().sepX+18, 3))
	if cmd == nil {
		t.Fatal("precondition: the drag copied nothing, so there was no gesture to break")
	}
	app = model.(App)

	// Press again on the cell the drag started from, well inside the window.
	model, _ = app.Update(click(app.layout().sepX+4, 3))
	app = model.(App)
	if !app.draggingText {
		t.Error("the press after a drag was swallowed as a double click instead of starting a new selection")
	}
}

// TestResizeDropsTheSelection: re-wrapping moves every character, so a
// highlight recorded in line/column cells would end up covering different text
// — visible as a selection crawling across unrelated lines while the separator
// is dragged.
func TestResizeDropsTheSelection(t *testing.T) {
	t.Parallel()

	a := appWithLongChats(t)
	model, _ := a.Update(click(a.layout().sepX+4, 3))
	model, _ = model.(App).Update(drag(a.layout().sepX+20, 5))
	app := model.(App)
	if !app.thread.HasSelection() {
		t.Fatal("precondition: nothing was selected before the resize")
	}

	model, _ = app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if model.(App).thread.HasSelection() {
		t.Error("the highlight survived a resize and now covers re-wrapped text")
	}
}

// threadTextRow returns the pane row carrying the fixture's message body.
//
// Hard-coded row numbers used to do this job, and they broke the moment the
// thread grew a date separator above its first message — three tests failed
// while the gesture they cover was working perfectly. Locating the row by its
// content keeps them measuring the gesture rather than the layout.
func threadTextRow(t *testing.T, a App, want string) int {
	t.Helper()
	for i, line := range strings.Split(a.thread.View(), "\n") {
		if strings.Contains(line, want) {
			return i
		}
	}
	t.Fatalf("no row of the thread pane contains %q:\n%s", want, a.thread.View())
	return -1
}
