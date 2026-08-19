package app

import (
	"context"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/core/events"
	"github.com/kar43lov/lazytg/internal/core/search"
	"github.com/kar43lov/lazytg/internal/ui/input"
	"github.com/kar43lov/lazytg/internal/ui/palette"
	"github.com/kar43lov/lazytg/internal/ui/panes/attach"
	"github.com/kar43lov/lazytg/internal/ui/panes/chats"
	uisearch "github.com/kar43lov/lazytg/internal/ui/panes/search"
	"github.com/kar43lov/lazytg/internal/ui/panes/thread"
	"github.com/kar43lov/lazytg/internal/ui/statusbar"
)

// downloadTimeout caps how long a Ctrl-D triggered fetch can run
// before the cmd's context fires. Large media uploads (the user just
// received a 1.5 GiB video) can take minutes legitimately, so the cap
// is generous; a stuck gotd handshake should not, however, block the
// command goroutine forever.
const downloadTimeout = 30 * time.Minute

// uploadTimeout caps how long a single Ctrl-U triggered upload can
// run. Same generous 30-minute cap as the download path — a 2 GiB
// file on a 50 Mbps link still finishes in ~5 minutes, so the cap
// only protects against a wedged gotd handshake.
const uploadTimeout = 30 * time.Minute

// recordVisitTimeout caps the post-select RecordVisit RPC. The
// chat switch already happened by the time this fires; the timeout
// only protects against a stuck SQLite VFS lock leaking a goroutine
// indefinitely.
const recordVisitTimeout = 2 * time.Second

// jumpContextTimeout caps the JumpContext window query that fires
// when the user presses Enter on a search hit. The query is two
// SELECTs against the local SQLite — micros in steady state — but a
// stuck VFS lock should not strand the chat switch.
const jumpContextTimeout = 5 * time.Second

// jumpAround is the ±N context window used by the search-jump path.
// Mirrors search.DefaultJumpContext (= 5) so the user sees five
// messages before and after the matched one, matching the Stage 3
// plan ("scroll к контексту ±5 сообщений").
const jumpAround = 5

// Init is the initial Cmd batch. Each sub-pane gets its own Init so they can
// kick off their own loaders (Task 7+: chats reads from repo, etc.). Stage 2
// Task 6 returns no-ops from the placeholders — that is fine, tea.Batch on
// a list of nils is itself nil.
func (a App) Init() tea.Cmd {
	return tea.Batch(
		a.chats.Init(),
		a.thread.Init(),
		a.input.Init(),
	)
}

// Update is the central event router. Order matters:
//  1. Resize is handled before anything else so the small-terminal fallback
//     can short-circuit further work.
//  2. The help overlay swallows every key while visible (modal contract).
//  3. Global key bindings (toggle help, focus cycling, quit) take priority
//     over per-pane key handling so the user can always dismiss/quit even
//     when a sub-pane has consumed its own key set.
//  4. Anything left over is delegated to the focused sub-pane.
//  5. Internal Cmd-driven messages (focusCycledMsg, helpToggledMsg) are
//     applied last — they always arrive as a plain tea.Msg so the route
//     order doesn't matter.
func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		return a.handleResize(m), nil
	case tea.MouseClickMsg:
		return a.handleMouseClick(m)
	case tea.MouseWheelMsg:
		return a.handleMouseWheel(m)
	case tea.MouseMotionMsg:
		return a.handleMouseMotion(m)
	case tea.MouseReleaseMsg:
		return a.handleMouseRelease(m)
	case focusCycledMsg:
		return a.applyFocusChange(m.Direction), nil
	case chatCycledMsg:
		return a.handleChatCycled(m)
	case helpToggledMsg:
		a.help.Visible = !a.help.Visible
		return a, nil
	case chats.ChatSelectedMsg:
		return a.handleChatSelected(m)
	case input.RequestReplyMsg:
		return a.handleReplyRequest()
	case input.SendDispatchedMsg:
		// The input pane reports a successful queue-up. Insert the
		// optimistic row in the thread immediately so the user sees
		// "[⏳] hello" before the SendService bus event lands. Pass
		// the same message back through input.Update so the pane can
		// register the localID in its inFlight tracker — needed for
		// async draft restoration on a later Failed bus event (the
		// MTProto-level failure surface that synchronous SendFailedMsg
		// does not reach).
		a.thread = a.thread.ApplyDispatched(m.LocalID, m.ChatID, m.Text)
		updated, cmd := a.input.Update(m)
		a.input = updated
		return a, cmd
	case events.MessageReceived,
		events.DialogUpdated,
		events.OutgoingMessageStateChanged,
		events.ConnectionStateChanged,
		events.StorageStateChanged,
		events.FileDownloadStarted,
		events.FileDownloadProgress,
		events.FileDownloadCompleted,
		events.FileDownloadFailed,
		events.FileUploadStarted,
		events.FileUploadProgress,
		events.FileUploadCompleted,
		events.FileUploadFailed,
		events.FileUploadWarning:
		return a.broadcastBusEvent(msg)
	case events.ReindexProgress, events.SearchJumpRequested:
		// Bus events that have no UI consumer in v0.1 — declared
		// here so they don't fall through into broadcastToPanes
		// (which would forward them to every pane's Update) and so
		// future stages can plug a status-bar / overlay handler in
		// without a routing change.
		return a, nil
	case searchJumpLoadedMsg:
		return a.applySearchJumpLoaded(m)
	case thread.DownloadRequestedMsg:
		return a.handleDownloadRequest(m)
	case uisearch.OpenedMsg:
		return a.openSearch()
	case uisearch.ClosedMsg:
		return a.closeSearch(), nil
	case uisearch.JumpMsg:
		return a.handleSearchJump(m)
	case uisearch.ResultsMsg, uisearch.QueryChangedMsg:
		updated, cmd := a.search.Update(msg)
		a.search = updated
		return a, cmd
	case palette.OpenedMsg:
		return a.openPalette()
	case palette.ClosedMsg:
		return a.closePalette(), nil
	case palette.SelectedMsg:
		return a.handlePaletteSelected(m)
	case palette.LoadedMsg, palette.QueryChangedMsg:
		updated, cmd := a.palette.Update(msg)
		a.palette = updated
		return a, cmd
	case attach.OpenedMsg:
		return a.openAttach(m)
	case attach.ClosedMsg:
		return a.closeAttach(), nil
	case attach.SubmitMsg:
		return a.handleAttachSubmit(m)
	}

	if a.help.Visible {
		updatedHelp, cmd := a.help.Update(msg)
		a.help = updatedHelp
		return a, cmd
	}

	if a.palette.Visible {
		updated, cmd := a.palette.Update(msg)
		a.palette = updated
		return a, cmd
	}

	if a.attach.Visible {
		updated, cmd := a.attach.Update(msg)
		a.attach = updated
		return a, cmd
	}

	if a.search.Visible {
		updated, cmd := a.search.Update(msg)
		a.search = updated
		return a, cmd
	}

	if k, ok := msg.(tea.KeyPressMsg); ok {
		if cmd, handled := a.handleGlobalKey(k); handled {
			return a, cmd
		}
		if updated, scrollCmd, handled := a.applyScrollKey(k); handled {
			return updated, scrollCmd
		}
		return a.delegateToFocused(msg)
	}

	// Non-key messages are typically pane-internal results from a
	// previously-spawned tea.Cmd (e.g. chatsLoadedMsg from chats.Init or
	// messagesLoadedMsg from thread.OpenChat). They must reach the pane
	// that produced them regardless of which pane currently has focus —
	// so we broadcast and let each pane's Update filter on its own
	// payload type. Keypresses keep the focus-only routing because that
	// is what a single-active-input UX requires.
	return a.withPendingScroll(a.broadcastToPanes(msg))
}

// broadcastToPanes forwards msg to every sub-pane and merges the
// returned cmds via tea.Batch. Each pane's Update is responsible for
// ignoring messages that aren't its own payload type — the cost of one
// type-switch per pane per non-key message is dwarfed by the cost of an
// in-flight async load that would otherwise be routed by focus into the
// wrong pane.
func (a App) broadcastToPanes(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	updatedChats, cmd := a.chats.Update(msg)
	a.chats = updatedChats
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	updatedThread, cmd := a.thread.Update(msg)
	a.thread = updatedThread
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	updatedInput, cmd := a.input.Update(msg)
	a.input = updatedInput
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	if len(cmds) == 0 {
		return a, nil
	}
	return a, tea.Batch(cmds...)
}

// onChatOpened is the single funnel every path that opens a chat passes
// through — row pick, palette pick, search jump. It does two things: announce
// the open on the bus, and ask for the chat's history.
//
// The announcement drives read receipts. It is published here rather than at
// the three call sites so a fourth way to open a chat cannot quietly stop
// marking chats read; and it is published before the backfill guard below,
// because a cache-only launch still opens chats — there is simply no
// ReadService listening in that case.
//
// The history request exists because the thread pane reads the local mirror
// only, and a chat that just arrived from the dialog sync holds no messages:
// without this it opens empty and stays empty.
//
// The call runs off the update loop because BackfillService.Enqueue blocks when
// its queue is full, and blocking here would freeze the Bubble Tea loop — the
// one goroutine that must never wait on I/O. Ordering does not matter: the
// service de-duplicates in-flight requests by chat id and rate-limits itself
// globally.
//
// historyGate bounds how many of those goroutines can exist. Enqueue takes no
// context, so once its consumer stops (session torn down) a send into a full
// queue blocks forever — one goroutine per chat opened, none of them ever
// returning. Dropping the request when the gate is full is the right trade:
// history is re-requested the next time the chat is opened.
//
// Re-opening a chat deliberately asks again rather than being suppressed by an
// "already fetched" set. Live updates arrive without gap recovery, so anything
// that landed while the process was elsewhere is missing from the mirror; the
// repeat fetch on open is what closes that hole.
func (a App) onChatOpened(chatID int64) {
	if chatID == 0 {
		return
	}
	if a.bus != nil {
		a.bus.Publish(events.ChatOpened{ChatID: chatID})
	}
	if a.backfiller == nil {
		return
	}
	backfiller := a.backfiller
	gate := a.historyGate
	select {
	case gate <- struct{}{}:
	default:
		// Gate full: too many requests are already stuck. Skipping keeps the
		// goroutine count bounded instead of piling up on a dead consumer.
		a.log.Warn("history request dropped, backfill is not draining", "chat_id", chatID)
		return
	}
	go func() {
		defer func() { <-gate }()
		backfiller.Enqueue(chatID)
	}()
}

// handleChatSelected fans the user's chat-pick out to every pane that
// cares: the thread loads the chat, the input binds itself to the new
// chat id, and the status bar updates its title. Routing here (rather
// than in the chats pane) keeps the panes decoupled — chats only knows
// "user pressed Enter on row X", everyone else listens.
func (a App) handleChatSelected(msg chats.ChatSelectedMsg) (tea.Model, tea.Cmd) {
	// Any chat switch invalidates a pending search-jump: the user
	// just pointed at a different chat, so a stale JumpContext
	// completion must not yank them back. Bump the generation
	// counter (drops the async result) and clear pendingScroll
	// (drops the legacy fallback's deferred ScrollTo target).
	a.jumpGen++
	a.pendingScroll = nil

	a.onChatOpened(msg.ChatID)

	var cmds []tea.Cmd

	updatedThread, cmd := a.thread.OpenChat(msg.ChatID)
	a.thread = updatedThread
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	updatedInput, cmd := a.input.Update(input.SetChatMsg{ChatID: msg.ChatID})
	a.input = updatedInput
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	if title, ok := a.chatTitle(msg.ChatID); ok {
		a.status.ChatTitle = title
	}

	a = a.applyDirectory(msg.ChatID)

	// Picking a chat is the start of writing in it, so the composer takes
	// focus and the next keystroke is text rather than a list navigation key.
	// This is what every chat client does and what the pane cycle made
	// awkward: pick a chat, then Tab twice before you can type.
	a = a.setFocus(FocusInput)

	return a, tea.Batch(cmds...)
}

// handleReplyRequest is the app-level resolver for the input pane's
// RequestReplyMsg → SetReplyMsg dance. We inspect the thread for its
// most recent message and arm it as the reply target. When the thread
// is empty (e.g. user pressed Reply before any history loaded) the
// request is silently dropped.
func (a App) handleReplyRequest() (tea.Model, tea.Cmd) {
	msgs := a.thread.Messages()
	if len(msgs) == 0 {
		return a, nil
	}
	target := msgs[len(msgs)-1]
	updatedInput, cmd := a.input.Update(input.SetReplyMsg{Msg: &target})
	a.input = updatedInput
	return a, cmd
}

// broadcastBusEvent fans a bus event out to every interested pane. The
// status bar only reacts to connection / storage transitions; the
// thread pane filters by chat id internally; the chats pane
// debounce-reloads on DialogUpdated *and* MessageReceived (the latter
// drives the dialog reorder when a new message lands — LiveService
// persists the row but does not republish a DialogUpdated, see
// internal/core/sync/live.go). Routing here means the program.Send →
// tea.Msg fan-in (cmd/lazytg/cmd/tui.go) does not need to know which
// pane owns which event.
func (a App) broadcastBusEvent(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch ev := msg.(type) {
	case events.ConnectionStateChanged:
		a.status.ConnState = ev.State
		return a, nil
	case events.StorageStateChanged:
		// DBSizeMonitor and DegradationDetector both produce this
		// event, but they touch independent state in the status bar:
		// the size monitor flips the "⚠ DB N GB" chip via DBSizeMB;
		// the degradation detector flips StorageMode (rw / read-only).
		// Routing by Reason keeps a size-warning event from clobbering
		// a current read-only state and vice versa.
		if ev.Reason == events.ReasonDBSizeWarning {
			a.status.DBSizeMB = ev.DBSizeMB
		} else {
			a.status.StorageMode = ev.Mode
		}
		return a, nil
	case events.DialogUpdated:
		updated, cmd := a.chats.Update(ev)
		a.chats = updated
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		return a, tea.Batch(cmds...)
	case events.MessageReceived:
		// Both panes care: thread filters by current chat id internally
		// and appends the message to the visible list; chats reloads
		// the dialog list so the chat hosting this message bubbles to
		// the top.
		updatedThread, cmdT := a.thread.Update(ev)
		a.thread = updatedThread
		if cmdT != nil {
			cmds = append(cmds, cmdT)
		}
		updatedChats, cmdC := a.chats.Update(ev)
		a.chats = updatedChats
		if cmdC != nil {
			cmds = append(cmds, cmdC)
		}
		return a, tea.Batch(cmds...)
	case events.OutgoingMessageStateChanged:
		// Both the thread (renders the [⏳]/[✗] pill) and the input
		// pane (restores the draft on Failed) need this event. Routing
		// here keeps each pane's concerns separate while a single
		// program.Send fan-in stays the upstream entry point.
		updatedThread, cmdT := a.thread.Update(ev)
		a.thread = updatedThread
		if cmdT != nil {
			cmds = append(cmds, cmdT)
		}
		updatedInput, cmdI := a.input.Update(ev)
		a.input = updatedInput
		if cmdI != nil {
			cmds = append(cmds, cmdI)
		}
		return a, tea.Batch(cmds...)
	case events.FileDownloadStarted:
		a.status = a.status.UpsertDownload(statusbarDownloadStarted(ev))
		return a, nil
	case events.FileDownloadProgress:
		a.status = a.status.UpsertDownload(statusbarDownloadProgress(ev))
		return a, nil
	case events.FileDownloadCompleted:
		a.status = a.status.RemoveDownload(ev.FileID)
		return a, nil
	case events.FileDownloadFailed:
		a.status = a.status.RemoveDownload(ev.FileID)
		return a, nil
	case events.FileUploadStarted:
		a.status = a.status.UpsertUpload(statusbarUploadStarted(ev))
		return a, nil
	case events.FileUploadProgress:
		a.status = a.status.UpsertUpload(statusbarUploadProgress(ev))
		return a, nil
	case events.FileUploadCompleted:
		a.status = a.status.RemoveUpload(ev.UploadID)
		return a, nil
	case events.FileUploadFailed:
		a.status = a.status.RemoveUpload(ev.UploadID)
		return a, nil
	case events.FileUploadWarning:
		// Informational — the status bar already shows the upload row;
		// the warning event is consumed mostly so future toast widgets
		// can latch on without us having to add another routing arm.
		return a, nil
	}
	return a, nil
}

// statusbarUploadStarted builds a statusbar.Upload row from a
// FileUploadStarted event.
func statusbarUploadStarted(ev events.FileUploadStarted) statusbar.Upload {
	return statusbar.NewUpload(ev.UploadID, ev.Filename, 0, ev.Size)
}

// statusbarUploadProgress builds a statusbar.Upload row from a
// FileUploadProgress event, preserving the previously-recorded
// filename when the event itself does not carry one.
func statusbarUploadProgress(ev events.FileUploadProgress) statusbar.Upload {
	return statusbar.NewUpload(ev.UploadID, "", ev.BytesUploaded, ev.TotalBytes)
}

// statusbarDownloadStarted converts a FileDownloadStarted event into a
// statusbar.Download row — initialising progress at 0 of total. Lives
// here (next to broadcastBusEvent) so the package boundary stays:
// statusbar accepts a domain-shaped value object and never imports
// internal/core/events.
func statusbarDownloadStarted(ev events.FileDownloadStarted) statusbar.Download {
	return statusbar.NewDownload(ev.FileID, ev.Filename, 0, ev.Size)
}

// statusbarDownloadProgress converts a FileDownloadProgress event into
// a statusbar.Download row, preserving the previously-recorded
// filename when the event itself does not carry one.
func statusbarDownloadProgress(ev events.FileDownloadProgress) statusbar.Download {
	return statusbar.NewDownload(ev.FileID, "", ev.BytesDownloaded, ev.TotalBytes)
}

// openSearch flips the overlay visible, remembers the previous focus
// target so closeSearch can restore it, and forwards Open() to the
// overlay so its textinput is focused (cursor blink scheduling).
func (a App) openSearch() (tea.Model, tea.Cmd) {
	if a.preSearchFocus < 0 {
		a.preSearchFocus = a.focus
	}
	updated, cmd := a.search.Open()
	a.search = updated
	return a, cmd
}

// closeSearch hides the overlay and restores the focus target the
// user was on when they opened it. Idempotent — calling on an
// already-hidden overlay is a no-op.
func (a App) closeSearch() App {
	a.search = a.search.Close()
	if a.preSearchFocus >= 0 {
		a.preSearchFocus = -1
	}
	return a
}

// handleSearchJump consumes a JumpMsg from the overlay: switches
// focus to the target chat, loads the surrounding ±jumpAround context
// window via the wired JumpContextProvider, and scrolls the thread
// pane to the matched message. When no jumper is wired (tests, early
// boot) it falls back to the legacy "OpenChat + deferred ScrollTo"
// path — that locates the target only when it still fits inside the
// initial 200-message page, but the chat switch itself still works.
// The overlay is closed as part of the jump because the user
// signalled they want to read the chat now.
func (a App) handleSearchJump(msg uisearch.JumpMsg) (tea.Model, tea.Cmd) {
	chatID := msg.Hit.ChatID
	messageID := msg.Hit.Message.ID

	a = a.closeSearch()
	// Bump the jump generation so any in-flight JumpContext from a
	// prior jump is treated as stale by applySearchJumpLoaded — the
	// user signalled a new target and the older async result must not
	// yank the thread back.
	a.jumpGen++
	// Drop any pendingScroll left over from a prior jump for the same
	// reason: the legacy fallback path uses pendingScroll to scroll
	// once the messagesLoadedMsg lands, but that target now belongs to
	// a superseded jump.
	a.pendingScroll = nil
	a.onChatOpened(chatID)

	if title, ok := a.chatTitle(chatID); ok {
		a.status.ChatTitle = title
	}

	if a.bus != nil {
		a.bus.Publish(events.SearchJumpRequested{
			ChatID:    chatID,
			MessageID: messageID,
		})
	}

	updatedInput, inputCmd := a.input.Update(input.SetChatMsg{ChatID: chatID})
	a.input = updatedInput

	cmds := []tea.Cmd{}
	if inputCmd != nil {
		cmds = append(cmds, inputCmd)
	}

	if a.jumper != nil {
		cmds = append(cmds, jumpContextCmd(a.jumper, msg.Hit, jumpAround, a.jumpGen))
		// Pre-position the thread on the target chat so any
		// concurrent live event lands in the right place. SwitchTo
		// also clears the existing message slice — required even
		// for same-chat jumps so LoadJumpWindow's tail-preserve
		// merge only retains genuine live appends that arrived
		// after kickoff (rather than the entire prior tail, which
		// would render as a discontinuous window+tail).
		a.thread = a.thread.SwitchTo(chatID)
	} else {
		a.pendingScroll = &pendingThreadScroll{
			ChatID:    chatID,
			MessageID: messageID,
			Around:    jumpAround,
		}
		updatedThread, openCmd := a.thread.OpenChat(chatID)
		a.thread = updatedThread
		if openCmd != nil {
			cmds = append(cmds, openCmd)
		}
	}

	if len(cmds) == 0 {
		return a, nil
	}
	return a, tea.Batch(cmds...)
}

// searchJumpLoadedMsg carries the result of the async JumpContext
// lookup back into Update. Err non-nil indicates a JumpContext
// failure — the handler falls back to the legacy OpenChat + deferred
// ScrollTo path so the user still lands in the chat without the
// surrounding context window.
//
// Gen is the jump-generation snapshot captured when jumpContextCmd
// was created. applySearchJumpLoaded compares it against the current
// App.jumpGen and discards the message when they differ — the user
// triggered a newer jump (or switched chats) before this completion
// landed, and applying the stale window would yank the thread back.
type searchJumpLoadedMsg struct {
	Gen       uint64
	ChatID    int64
	MessageID int64
	Messages  []domain.Message
	Err       error
}

// jumpContextCmd returns a tea.Cmd that calls JumpContext on the
// supplied jumper and emits a searchJumpLoadedMsg with the loaded
// window (or the error, which the caller treats as a fallback
// trigger). gen is the App.jumpGen value at kickoff so the handler
// can drop stale results — see searchJumpLoadedMsg.Gen.
func jumpContextCmd(jumper JumpContextProvider, hit search.Hit, around int, gen uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), jumpContextTimeout)
		defer cancel()
		msgs, _, err := jumper.JumpContext(ctx, hit, around)
		return searchJumpLoadedMsg{
			Gen:       gen,
			ChatID:    hit.ChatID,
			MessageID: hit.Message.ID,
			Messages:  msgs,
			Err:       err,
		}
	}
}

// applySearchJumpLoaded routes the loaded ±N window into the thread
// pane. On error the handler falls through to the legacy OpenChat +
// deferred ScrollTo path so the user still lands in the chat — the
// only thing they lose is the surrounding context. Returns the load
// cmd from OpenChat so the deferred ScrollTo path actually receives
// a messagesLoadedMsg; without it the thread would stay in
// loading=true state and applyPendingScroll would never fire.
//
// Stale completions (msg.Gen != a.jumpGen) are dropped silently. The
// generation counter is bumped by every jump kickoff and every chat
// switch (chats / palette), so by the time the JumpContext goroutine
// returns the world may already have moved on — applying the result
// then would yank the thread back to the earlier hit (or, on the
// error path, OpenChat the wrong chat).
func (a App) applySearchJumpLoaded(msg searchJumpLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.Gen != a.jumpGen {
		if a.log != nil {
			a.log.Debug("search: dropping stale jump completion",
				"got_gen", msg.Gen, "current_gen", a.jumpGen,
				"chat_id", msg.ChatID, "message_id", msg.MessageID)
		}
		return a, nil
	}
	if msg.Err != nil || len(msg.Messages) == 0 {
		if a.log != nil && msg.Err != nil {
			a.log.Warn("search: jump context load failed",
				"chat_id", msg.ChatID, "message_id", msg.MessageID, "err", msg.Err)
		}
		a.pendingScroll = &pendingThreadScroll{
			ChatID:    msg.ChatID,
			MessageID: msg.MessageID,
			Around:    jumpAround,
		}
		// ReloadAfterJumpFailure (not OpenChat) so any optimistic-UI
		// rows the user added during the async JumpContext window
		// (input pane was already rebound to chatID by handleSearchJump)
		// survive into the recovered tail. Mirrors the success path —
		// LoadJumpWindow preserves outgoing too.
		updatedThread, loadCmd := a.thread.ReloadAfterJumpFailure(msg.ChatID)
		a.thread = updatedThread
		return a, loadCmd
	}
	a.thread = a.thread.LoadJumpWindow(msg.ChatID, msg.Messages, msg.MessageID, jumpAround)
	a.pendingScroll = nil
	// Names, but not focus: a jump lands the user on a specific message to
	// read, so the thread keeps the cursor. Only the chat-picking paths move
	// it to the composer.
	a = a.applyDirectory(msg.ChatID)
	return a, nil
}

// openAttach flips the attach overlay visible (capturing the chat
// id from the message), remembers the previous focus target so
// closeAttach can restore it, and forwards Open() so the path input
// is focused and the directory listing starts loading.
//
// When the message arrives with ChatID == 0 (defensive — the
// keymap-driven path always supplies a chat id), the overlay opens
// anyway but Submit will be a no-op until the user types a chat id.
func (a App) openAttach(msg attach.OpenedMsg) (tea.Model, tea.Cmd) {
	if a.preAttachFocus < 0 {
		a.preAttachFocus = a.focus
	}
	updated, cmd := a.attach.Open(msg.ChatID)
	a.attach = updated
	return a, cmd
}

// closeAttach hides the attach overlay and restores the focus
// target the user was on when they opened it. Idempotent.
func (a App) closeAttach() App {
	a.attach = a.attach.Close()
	if a.preAttachFocus >= 0 {
		a.preAttachFocus = -1
	}
	return a
}

// handleAttachSubmit consumes a SubmitMsg from the attach overlay:
// closes the overlay and dispatches the upload via the wired
// FileUploader. The upload runs in a goroutine bounded by
// uploadTimeout — progress / completion events arrive on the bus and
// route into the status bar through broadcastBusEvent. Returns no
// command — the result is observed entirely through bus events.
//
// When no uploader is wired (test harnesses, unauth sessions) the
// submit is a quiet no-op so the user does not see a misleading error
// for an action that genuinely cannot be performed.
func (a App) handleAttachSubmit(msg attach.SubmitMsg) (tea.Model, tea.Cmd) {
	a = a.closeAttach()
	if a.uploader == nil {
		if a.log != nil {
			a.log.Debug("upload: no uploader wired, ignoring submit")
		}
		return a, nil
	}
	up := a.uploader
	log := a.log
	chatID := msg.ChatID
	path := msg.Path
	caption := msg.Caption
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), uploadTimeout)
		defer cancel()
		if _, err := up.SendFile(ctx, chatID, path, caption, 0); err != nil && log != nil {
			log.Warn("upload: submit failed",
				"chat_id", chatID, "path", path, "err", err)
		}
	}()
	return a, nil
}

// openPalette flips the palette overlay visible, remembers the
// previous focus target so closePalette can restore it, and
// forwards Open() to the palette so its textinput is focused and
// the candidate-list refresh starts.
func (a App) openPalette() (tea.Model, tea.Cmd) {
	if a.prePaletteFocus < 0 {
		a.prePaletteFocus = a.focus
	}
	updated, cmd := a.palette.Open()
	a.palette = updated
	return a, cmd
}

// closePalette hides the palette overlay and restores the focus
// target the user was on when they opened it. Idempotent — calling
// on an already-hidden palette is a no-op.
func (a App) closePalette() App {
	a.palette = a.palette.Close()
	if a.prePaletteFocus >= 0 {
		a.prePaletteFocus = -1
	}
	return a
}

// handlePaletteSelected consumes a SelectedMsg from the palette:
// closes the palette, switches the chat (chats pane Update + thread
// pane OpenChat + input pane SetChatMsg), records the visit so the
// next palette open ranks the just-visited chat higher, and
// optionally publishes a DialogUpdated bus event so future
// subscribers can react.
//
// The RecordVisit call is fire-and-forget in a goroutine because the
// chat switch already happened — a transient SQLite error must not
// block the UI. The error is logged but not surfaced; the next visit
// will retry and the palette will fall back to the chats list order
// in the meantime.
func (a App) handlePaletteSelected(msg palette.SelectedMsg) (tea.Model, tea.Cmd) {
	chatID := msg.ChatID
	a = a.closePalette()
	// Same rationale as handleChatSelected: a palette pick is a
	// chat switch and must invalidate any in-flight search-jump
	// (stale window load) and the legacy fallback's pendingScroll.
	a.jumpGen++
	a.pendingScroll = nil
	a.onChatOpened(chatID)

	updatedThread, cmd := a.thread.OpenChat(chatID)
	a.thread = updatedThread
	cmds := []tea.Cmd{}
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	updatedInput, inputCmd := a.input.Update(input.SetChatMsg{ChatID: chatID})
	a.input = updatedInput
	if inputCmd != nil {
		cmds = append(cmds, inputCmd)
	}

	if title, ok := a.chatTitle(chatID); ok {
		a.status.ChatTitle = title
	}

	// A palette pick is a chat pick: same directory refresh and same landing
	// spot for the cursor. Diverging here is how one entry point ends up
	// showing "user-8385473863" while the other shows names.
	a = a.applyDirectory(chatID)
	a = a.setFocus(FocusInput)

	if a.paletteFrecency != nil {
		fr := a.paletteFrecency
		log := a.log
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), recordVisitTimeout)
			defer cancel()
			if err := fr.RecordVisit(ctx, chatID); err != nil && log != nil {
				log.Warn("palette: record visit failed", "chat_id", chatID, "err", err)
			}
		}()
	}

	if len(cmds) == 0 {
		return a, nil
	}
	return a, tea.Batch(cmds...)
}

// applyPendingScroll runs after broadcastToPanes / broadcastBusEvent
// so any deferred ScrollTo issued by handleSearchJump can land now
// that the thread pane has had a chance to apply its
// messagesLoadedMsg. The scroll is dropped silently if the target
// message is not in the now-loaded slice (the message is older than
// the initialPageSize cap and the user will need to paginate or load
// the full context — Stage 3 leaves that interaction as a follow-up).
func (a App) applyPendingScroll() App {
	if a.pendingScroll == nil {
		return a
	}
	if a.thread.ChatID() != a.pendingScroll.ChatID {
		return a
	}
	if a.thread.Loading() {
		return a
	}
	a.thread = a.thread.ScrollTo(a.pendingScroll.MessageID, a.pendingScroll.Around)
	a.pendingScroll = nil
	return a
}

// withPendingScroll wraps the (App, Cmd) tuple a routing helper
// returned and invokes applyPendingScroll on the App. Used by the
// Update arms that route through broadcastToPanes / broadcastBusEvent
// so a deferred SearchJump scroll lands the moment its preconditions
// are met.
func (a App) withPendingScroll(model tea.Model, cmd tea.Cmd) (tea.Model, tea.Cmd) {
	app, ok := model.(App)
	if !ok {
		return model, cmd
	}
	return app.applyPendingScroll(), cmd
}

// handleDownloadRequest takes a thread-pane DownloadRequestedMsg and
// fires off a goroutine that runs the actual fetch via the wired
// FileDownloader. Progress / completion events arrive on the bus and
// route into the status bar through broadcastBusEvent. Returns no
// command — the result is observed entirely through bus events.
//
// When no downloader is wired (test harnesses, unauth sessions) the
// chord is a quiet no-op so the user does not see a misleading error
// toast for an action that genuinely cannot be performed.
func (a App) handleDownloadRequest(req thread.DownloadRequestedMsg) (tea.Model, tea.Cmd) {
	if a.downloader == nil {
		if a.log != nil {
			a.log.Debug("download: no downloader wired, ignoring chord")
		}
		return a, nil
	}
	// In-flight guard: a repeated Ctrl-D on the same media before the
	// previous goroutine finishes would write to the same .partial
	// path concurrently and corrupt the output. Reserve the FileID
	// for the duration of this download; release on goroutine exit.
	if a.inFlightDownloads != nil && !a.inFlightDownloads.reserve(req.Media.FileID) {
		if a.log != nil {
			a.log.Debug("download: already in flight, ignoring chord",
				"file_id", req.Media.FileID)
		}
		return a, nil
	}
	dl := a.downloader
	log := a.log
	registry := a.inFlightDownloads
	fileID := req.Media.FileID
	go func() {
		if registry != nil {
			defer registry.release(fileID)
		}
		ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
		defer cancel()
		if _, err := dl.Download(ctx, req.ChatID, req.ChatTitle, req.Media); err != nil && log != nil {
			log.Warn("download: request failed",
				"chat_id", req.ChatID, "message_id", req.MessageID, "err", err)
		}
	}()
	return a, nil
}

// chatTitle looks up the current title for the given chat id by
// scanning the chats pane's loaded items. Returns ok=false when the
// list is empty or the id isn't present (the latter happens when the
// user opens a chat ahead of the chats pane finishing its initial
// load — the title catches up on the next reload).
func (a App) chatTitle(id int64) (string, bool) {
	for _, it := range a.chats.Items() {
		if it.ID() == id {
			return it.Name(), true
		}
	}
	return "", false
}

// handleChatCycled moves the chat-list selection by one and opens what it lands
// on. The pane's own order is authoritative — pinned first, then by last
// message, the same order the list renders — so "next" is what the user sees,
// and the selection wraps rather than stopping at the ends.
func (a App) handleChatCycled(msg chatCycledMsg) (tea.Model, tea.Cmd) {
	updated, cmd, ok := a.chats.CycleSelection(msg.Delta)
	if !ok {
		return a, nil
	}
	a.chats = updated
	return a, cmd
}

// applyDirectory hands the thread pane the names it needs to label senders and
// tells it whether the open chat is 1:1.
//
// The chat list is the only sender directory v0.1 has: storage keeps peer ids
// and access hashes but no names, so a title is available exactly for peers the
// user has a dialog with. That covers the other party of every private chat,
// which is where an unnamed "user-8385473863" was most jarring.
func (a App) applyDirectory(chatID int64) App {
	items := a.chats.Items()
	names := make(map[int64]string, len(items))
	var kind domain.ChatType
	for _, it := range items {
		if name := it.Name(); name != "" {
			names[it.ID()] = name
		}
		if it.ID() == chatID {
			kind = it.Type()
		}
	}
	a.thread = a.thread.SetDirectory(names, kind)
	return a
}

// handleResize updates the cached dimensions, recomputes per-pane sizes, and
// flips the small-terminal flag. Called from Update on tea.WindowSizeMsg.
func (a App) handleResize(msg tea.WindowSizeMsg) App {
	a.width, a.height = msg.Width, msg.Height
	a.tooSmall = msg.Width < MinWidth || msg.Height < MinHeight
	if a.tooSmall {
		return a
	}
	// Re-clamp the dragged split against the new width: a split that was
	// comfortable in a wide window can leave the thread one column wide after
	// a shrink, and 0 puts the layout back on the automatic ratio.
	a.chatsWidth = clampChatsWidth(a.chatsWidth, a.width)
	return a.applySizes()
}

// applySizes pushes the current geometry into every pane. Split out of
// handleResize because dragging the separator changes the geometry without a
// terminal resize, and both paths must size the panes identically.
func (a App) applySizes() App {
	l := computeLayout(a.width, a.height, a.chatsWidth)

	a.chats = a.chats.SetSize(l.chatsW, l.paneH)
	// Re-wrapping moves every character in the thread, so a highlight recorded
	// in line/column cells now covers different text. Dragging the separator
	// with a selection on screen made it crawl across unrelated lines.
	a.thread = a.thread.ClearSelection().SetSize(l.threadW, l.paneH)
	a.input = a.input.SetWidth(a.width)
	a.search = a.search.SetSize(a.width, a.height)
	a.palette = a.palette.SetSize(a.width, a.height)
	a.attach = a.attach.SetSize(a.width, a.height)
	return a
}

// handleGlobalKey applies app-level shortcuts (toggle help, focus cycling,
// quit). Returns (cmd, true) when the key was consumed; otherwise the key
// falls through to the focused sub-pane.
//
// ToggleHelp is suppressed when the input pane has focus or the chats
// pane is in filter-input mode. The default chord is "?" — a printable
// character — so swallowing it globally would prevent the user from
// typing "?" into the message body or the chats filter. Quit/FocusNext/
// FocusPrev are non-printable chords and remain global.
func (a App) handleGlobalKey(k tea.KeyPressMsg) (tea.Cmd, bool) {
	helpAllowed := a.focus != FocusInput && !a.chats.IsFilterActive()
	// Search activation has the same suppression rules as ToggleHelp:
	// the default chord ("/") is a printable character that the input
	// pane composer and the chats filter both legitimately consume.
	// When the chats pane is focused the bubbles/list "/" filter is the
	// expected behaviour, so /-search is only available from Thread (or
	// any non-filter Chats moment via the global chord on a future
	// vim-style "ctrl+s" alias).
	searchAllowed := a.focus != FocusInput && !a.chats.IsFilterActive() && a.focus != FocusChats
	// The palette chord (default "ctrl+space") is non-printable so it
	// is safe from input/chats consumption — but the chats filter is
	// modal, and stealing keys from it mid-typed-filter would feel
	// broken to the user.
	paletteAllowed := !a.chats.IsFilterActive()
	// Download chord (Ctrl-D) only fires when the focus is *not* in
	// the input pane (where the same chord deletes the next char in
	// emacs muscle memory) and the chats filter is not active. Stage
	// 3 v0.1: the chord operates on the latest media-bearing message
	// in the visible thread; per-message cursor lands in v0.2.
	downloadAllowed := a.focus != FocusInput && !a.chats.IsFilterActive()
	// Attach chord (Ctrl-U) is allowed from any non-filter focus —
	// even the input pane, because Ctrl-U "delete to start of line"
	// in emacs is uncommon enough that hijacking it for "attach
	// file" matches Telegram Desktop's muscle memory. The Attach
	// overlay needs a chat to upload into; we bail when the input
	// pane has no chat bound yet.
	attachAllowed := !a.chats.IsFilterActive()
	switch {
	case helpAllowed && key.Matches(k, a.keymap.ToggleHelp):
		return cmdToggleHelp(), true
	case searchAllowed && key.Matches(k, a.keymap.Search):
		return cmdOpenSearch(), true
	case paletteAllowed && key.Matches(k, a.keymap.OpenPalette):
		return cmdOpenPalette(), true
	case downloadAllowed && key.Matches(k, a.keymap.Download):
		if cmd, ok := a.cmdDownloadLatestMedia(); ok {
			return cmd, true
		}
		return nil, true
	case attachAllowed && key.Matches(k, a.keymap.Attach):
		if cmd, ok := a.cmdOpenAttach(); ok {
			return cmd, true
		}
		return nil, true
	case key.Matches(k, a.keymap.NextChat):
		return cmdNextChat(), true
	case key.Matches(k, a.keymap.PrevChat):
		return cmdPrevChat(), true
	case key.Matches(k, a.keymap.FocusNext):
		return cmdNextFocus(), true
	case key.Matches(k, a.keymap.FocusPrev):
		return cmdPrevFocus(), true
	case key.Matches(k, a.keymap.Quit):
		return cmdQuit(), true
	}
	return nil, false
}

// cmdOpenAttach picks the chat id from the thread pane (the chat
// the user is currently looking at — same chat the input pane is
// bound to) and emits a OpenedMsg the app routes into the
// attach overlay. Returns (nil, false) when no chat is open so the
// chord becomes a quiet no-op rather than opening an attach overlay
// the user could never submit from.
func (a App) cmdOpenAttach() (tea.Cmd, bool) {
	chatID := a.thread.ChatID()
	if chatID == 0 {
		return nil, false
	}
	return func() tea.Msg {
		return attach.OpenedMsg{ChatID: chatID}
	}, true
}

// cmdDownloadLatestMedia inspects the thread for its most recent
// message with Media. If found, it returns a tea.Cmd that emits a
// thread.DownloadRequestedMsg the app routes into the wired
// FileDownloader. If no media-bearing message exists, returns
// (nil, false) so the caller can treat the chord as consumed-but-noop
// instead of falling through and beeping.
func (a App) cmdDownloadLatestMedia() (tea.Cmd, bool) {
	target := a.thread.LatestMediaMessage()
	if target == nil || target.Media == nil {
		return nil, false
	}
	chatID := target.ChatID
	title, _ := a.chatTitle(chatID)
	media := *target.Media
	messageID := target.ID
	return func() tea.Msg {
		return thread.DownloadRequestedMsg{
			ChatID:    chatID,
			MessageID: messageID,
			ChatTitle: title,
			Media:     media,
		}
	}, true
}

// applyScrollKey checks whether the key matches the configurable
// ScrollUp/ScrollDown chords and, if so, scrolls the thread viewport.
// Scroll keys are intentionally a focus-aware override that only fires
// when the thread pane (or chats pane) is focused — when the input pane
// has focus the same chord (ctrl+b / ctrl+f by default) is the emacs
// character motion the textarea expects, and stealing it would break
// every emacs muscle memory.
//
// After the scroll lands, drives backward / forward pagination via
// the thread model's PaginateOlderIfAtTop / PaginateNewerIfAtBottom
// helpers. Without this the configured chord would page the viewport
// but never trigger the in-Update pagination check (which only runs
// from handleKey on keys that reach delegateToFocused — and the
// chord is intercepted before that).
//
// Direction-aware fallback mirrors handleKey: in the small-window
// case (AtTop && AtBottom both true) the primary direction can be a
// no-op, so fall through to the opposite direction. ScrollDown
// prefers forward (walk toward the live tail) but loads older
// history when there is nothing newer; ScrollUp prefers backward
// but falls forward in the symmetric post-jump case where only
// forward pagination is reachable. Without this fallback pgdown /
// ctrl+f on a chat that fits entirely in the viewport (default
// keymap binds both to ScrollDown) would do nothing — handleKey
// would have paged older, but the chord interception bypasses it.
//
// Returns (updated app, cmd, true) when the chord was consumed,
// otherwise the original app, nil cmd, and false so the caller can
// fall through to the regular delegate path.
func (a App) applyScrollKey(k tea.KeyPressMsg) (App, tea.Cmd, bool) {
	if a.focus == FocusInput {
		return a, nil, false
	}
	switch {
	case key.Matches(k, a.keymap.ScrollUp):
		a.thread = a.thread.ScrollUp()
		updated, cmd := a.thread.PaginateOlderIfAtTop()
		a.thread = updated
		if cmd == nil {
			updated, cmd = a.thread.PaginateNewerIfAtBottom()
			a.thread = updated
		}
		return a, cmd, true
	case key.Matches(k, a.keymap.ScrollDown):
		a.thread = a.thread.ScrollDown()
		updated, cmd := a.thread.PaginateNewerIfAtBottom()
		a.thread = updated
		if cmd == nil {
			updated, cmd = a.thread.PaginateOlderIfAtTop()
			a.thread = updated
		}
		return a, cmd, true
	}
	return a, nil, false
}

// applyFocusChange shifts focus by +/-1 (mod 3) and propagates the change
// into the affected sub-models. Calling SetFocus on every pane keeps the
// "single focused element" invariant trivial — no need to remember which one
// was previously focused.
func (a App) applyFocusChange(dir int) App {
	const total = 3 // FocusChats / FocusInput / FocusThread.
	next := (int(a.focus) + dir + total) % total
	return a.setFocus(FocusTarget(next))
}

// setFocus moves focus to an explicit target and syncs every pane's focus flag.
// Cycling (Tab) and pointing (a click) both land here, so the two cannot drift
// into different notions of "focused".
func (a App) setFocus(target FocusTarget) App {
	a.focus = target
	a.chats = a.chats.SetFocus(a.focus == FocusChats)
	a.input = a.input.SetFocus(a.focus == FocusInput)
	a.thread = a.thread.SetFocus(a.focus == FocusThread)
	return a
}

// delegateToFocused dispatches msg to the currently focused sub-model.
// Returns the updated app and any Cmd produced by the sub-model.
func (a App) delegateToFocused(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch a.focus {
	case FocusChats:
		a.chats, cmd = a.chats.Update(msg)
	case FocusInput:
		a.input, cmd = a.input.Update(msg)
	case FocusThread:
		a.thread, cmd = a.thread.Update(msg)
	}
	return a, cmd
}
