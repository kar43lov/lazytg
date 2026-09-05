package cmd

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/kar43lov/lazytg/internal/app"
	"github.com/kar43lov/lazytg/internal/core/events"
	"github.com/kar43lov/lazytg/internal/core/files"
	uiapp "github.com/kar43lov/lazytg/internal/ui/app"
	"github.com/kar43lov/lazytg/internal/ui/input"
	"github.com/kar43lov/lazytg/internal/ui/palette"
	"github.com/kar43lov/lazytg/internal/ui/panes/chats"
	uisearch "github.com/kar43lov/lazytg/internal/ui/panes/search"
	"github.com/kar43lov/lazytg/internal/ui/panes/thread"
	"github.com/kar43lov/lazytg/internal/ui/statusbar"
)

// initialConnState maps the attach outcome onto the connection indicator.
//
// statusbar.New starts at "connecting" and events.ConnectionStateChanged is
// published only by ReconnectManager, which reports what the transport does
// from the moment startReconnect launches it — not before. Seeding the state
// from the attach outcome covers that first gap: without it a fully working
// session showed a yellow "connecting" until the connection first changed,
// and an offline one claimed it was still trying. Found on the first live
// smoke, when the manager was never started at all and the seed was the only
// value the indicator ever had.
func initialConnState(attached bool) string {
	if attached {
		return statusbar.StateOnline
	}
	return statusbar.StateOffline
}

// newTuiCmd builds the default "no subcommand" entry point. Running
// `lazytg` without arguments dispatches here so the user gets the TUI
// they expect; named subcommands (login, logout, etc.) keep their own
// RunE bodies.
//
// The command body is intentionally small: it asks app.Build to compose
// every non-MTProto service, constructs the Bubble Tea model on top, and
// kicks off the bus → tea.Program fan-in. Stage 2 stops short of the
// full MTProto session (that needs a logged-in account); the empty-state
// view tells the user what to do next.
func newTuiCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "tui",
		Short:         "Open the lazytg TUI (default)",
		Hidden:        true, // surfaced via root.RunE, not as a separate help entry
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE:          runTUI,
	}
}

// runTUI is the body of both `lazytg` (no args) and `lazytg tui`. It
// composes the runtime via app.Build, attaches a logged-in client when
// available, then runs the Bubble Tea program. The bus → program.Send
// fan-in is started just before tea.Program.Run so events emitted by the
// background services land in Update as plain tea.Msg values.
func runTUI(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	logger := LoggerFromContext(ctx)

	runtime, err := app.Build(ctx, app.Config{
		Phone:   flagAccount,
		Polling: flagPolling,
		Logger:  logger,
	})
	if err != nil {
		return fmt.Errorf("tui: build: %w", err)
	}
	defer func() { _ = runtime.Close() }()

	// Kick off the storage-side background services (live drain +
	// degradation probe). These run for the lifetime of the TUI.
	bgCtx, cancelBG := context.WithCancel(ctx)
	defer cancelBG()
	bgDone := runtime.RunBackground(bgCtx)

	// Connect to Telegram before building the UI, so the panes below receive
	// live services instead of nil. Doing it the other way round would mean
	// handing the composer a nil sender and swapping it in mid-flight, which
	// is a data race on fields the Bubble Tea goroutine already captured.
	//
	// Never fatal: with no session, no network, or a revoked authorisation we
	// fall through to the cached-only view — which is the documented offline
	// behaviour, not a failure.
	stopTelegram := attachTelegram(bgCtx, runtime, logger)
	defer stopTelegram()

	// Build the Bubble Tea model on top of the runtime. Chats and thread read
	// through the sqlite mirror; the sender and history provider are live only
	// when the attach above succeeded, and both panes treat nil as "offline".
	chatsModel := chats.NewWithRepo(runtime.Repo, logger)
	threadModel := thread.NewWithRepo(runtime.Repo, threadHistoryProvider(runtime), logger)
	inputModel := input.NewWithDeps(composerSender(runtime), runtime.Keymap, logger)

	// Stage 3 overlays: search runs on the lazy-indexed FTS pipeline,
	// palette feeds from the frecency store, attach is folded in via
	// the Ctrl-U chord. The download/upload services are nil until
	// AttachClient runs — the UI app treats nil as "chord becomes a
	// quiet no-op" so a logged-out session does not crash on Ctrl-D
	// or Ctrl-U.
	searchModel := uisearch.New(runtime.SearchSvc, 0, logger)
	paletteModel := palette.New(runtime.Frecency, runtime.Repo, logger)

	// runtime.Client is set by AttachClient and nowhere else, so it is the
	// direct answer to "did the attach above succeed".
	status := statusbar.New()
	status.ConnState = initialConnState(runtime.Client != nil)

	deps := uiapp.Deps{
		Status:          &status,
		Bus:             runtime.Bus,
		Log:             logger,
		Keymap:          runtime.Keymap,
		Chats:           &chatsModel,
		Thread:          &threadModel,
		Input:           &inputModel,
		Search:          &searchModel,
		Palette:         &paletteModel,
		PaletteFrecency: runtime.Frecency,
		Jumper:          runtime.SearchSvc,
	}
	if runtime.DownloadSvc != nil {
		deps.Downloader = runtime.DownloadSvc
	}
	// Nil when the launch is cache-only. The edit and delete chords then
	// say so rather than appearing to work: a deletion that happened only
	// on this screen is the one outcome worse than none.
	if runtime.Actions != nil {
		deps.Actions = runtime.Actions
	}
	// A missing viewer is not a reason to refuse to start: the open
	// gesture degrades to a plain download, which is what lazytg did
	// before it could open anything. The reason is logged once, here,
	// rather than on every keypress.
	if opener, err := files.NewOpener(logger); err != nil {
		logger.Info("open: no media viewer configured", "err", err)
	} else {
		deps.Opener = opener
	}
	if runtime.UploadSvc != nil {
		deps.Uploader = runtime.UploadSvc
	}
	if runtime.Client != nil {
		deps.SelfID = runtime.Client.Self().ID
	}
	if runtime.Backfill != nil {
		deps.Backfiller = runtime.Backfill
	}
	uiModel := uiapp.New(deps)

	// Bubble Tea v2 uses declarative view fields for alt-screen / mouse
	// mode; both are set inside the App.View body. The only Program-level
	// option we still need is WithContext for clean shutdown.
	program := tea.NewProgram(
		uiModel,
		tea.WithContext(ctx),
	)

	// Bus → program.Send fan-in. The sub-context is cancelled before
	// program.Run returns so the goroutine exits cleanly even if the
	// outer context outlives the TUI session (e.g. signal-shutdown).
	fanCtx, cancelFan := context.WithCancel(ctx)
	defer cancelFan()
	go forwardBusEvents(fanCtx, runtime.Bus, program)

	if _, runErr := program.Run(); runErr != nil {
		cancelBG()
		<-bgDone
		return fmt.Errorf("tui: program: %w", runErr)
	}

	// Clean shutdown: stop background services then wait for them.
	cancelBG()
	<-bgDone
	return nil
}

// forwardBusEvents subscribes to the in-process event bus and re-emits
// every event as a plain tea.Msg via program.Send. The fan-in keeps the
// UI agnostic of who produced the event (MTProto dispatcher, polling
// fallback, send service, degradation detector) — every consumer sees a
// uniform stream of typed events on the tea.Update path.
//
// The function returns when ctx is cancelled OR the bus subscription
// channel closes (the bus closes the channel when its parent ctx is
// done). Either condition is a clean shutdown signal.
func forwardBusEvents(ctx context.Context, bus *events.Bus, program *tea.Program) {
	if bus == nil || program == nil {
		return
	}
	ch := bus.Subscribe(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			program.Send(ev)
		}
	}
}
