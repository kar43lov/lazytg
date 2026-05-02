package cmd

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/pgmac/lazytg/internal/app"
	"github.com/pgmac/lazytg/internal/core/events"
	uiapp "github.com/pgmac/lazytg/internal/ui/app"
	"github.com/pgmac/lazytg/internal/ui/input"
	"github.com/pgmac/lazytg/internal/ui/panes/chats"
	"github.com/pgmac/lazytg/internal/ui/panes/thread"
)

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

	// Build the Bubble Tea model on top of the runtime. The pane wiring
	// uses the production sqlite repo for chats/thread reads — the TUI
	// is read-mostly until login attaches the MTProto client.
	chatsModel := chats.NewWithRepo(runtime.Repo, logger)
	threadModel := thread.NewWithRepo(runtime.Repo, nil, logger)
	inputModel := input.NewWithDeps(nil, runtime.Keymap, logger)

	uiModel := uiapp.New(uiapp.Deps{
		Bus:    runtime.Bus,
		Log:    logger,
		Keymap: runtime.Keymap,
		Chats:  &chatsModel,
		Thread: &threadModel,
		Input:  &inputModel,
	})

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
