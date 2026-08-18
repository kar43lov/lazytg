package cmd

import (
	"github.com/spf13/cobra"

	"github.com/kar43lov/lazytg/internal/core/obs"
	tgclient "github.com/kar43lov/lazytg/internal/tg"
)

// newRootCmd builds the lazytg root command with persistent flags. A factory
// (rather than a package-level var) lets tests build a fresh tree per case
// without leaking flag state.
//
// Running the binary with no subcommand opens the TUI (Stage 2 default).
// Subcommands (login, logout, accounts, version, debug-bundle) keep their
// own RunE bodies; the TUI body is implemented in tui.go::runTUI.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "lazytg",
		Short: "Local-first Telegram TUI client",
		Long: "lazytg is a Telegram TUI client written in Go.\n\n" +
			"Run without arguments to open the TUI; named subcommands (login,\n" +
			"accounts, …) handle one-shot tasks. Stage 2 ships the TUI on top of\n" +
			"the cached SQLite mirror — cached chats and history render even when\n" +
			"no MTProto session is active.\n",
		SilenceUsage: true,
		RunE:         runTUI,
	}

	pf := root.PersistentFlags()
	pf.StringVar(&flagAccount, "account", "", "phone number of the account to operate on (e.g. +79998887766)")
	pf.StringVar(&flagConfig, "config", "", "path to config file (reserved; no-op until stage 2)")
	pf.BoolVar(&flagDebug, "debug", false, "enable verbose logging to stderr")
	pf.StringVar(&flagLogLevel, "log-level", "info", "logging level: debug|info|warn|error")
	pf.BoolVar(&flagPolling, "polling", false, "poll recent chats for messages the live update path may have dropped (gap-prone connections)")
	pf.StringVar(&flagAPIID, "api-id", "", "Telegram api_id, overrides "+tgclient.EnvAPIID+" and the embedded value")
	// Passing a secret on the command line puts it in `ps` output and shell
	// history. The flag exists for scripted one-offs; the help text steers
	// people to the env var, which is why the wording is a warning rather
	// than a description. No backquotes in the usage string: cobra reads a
	// backquoted word as the flag's value placeholder, which printed
	// "--api-hash ps" instead of "--api-hash string" in the help output.
	pf.StringVar(&flagAPIHash, "api-hash", "", "Telegram api_hash (visible in process list — prefer "+tgclient.EnvAPIHash+")")

	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		level, err := obs.ParseLevel(flagLogLevel)
		if err != nil {
			return err
		}
		logger := buildLogger(level, flagDebug)
		ctx := cmd.Context()
		if ctx == nil {
			ctx = cmd.Root().Context()
		}
		cmd.SetContext(withLogger(ctx, logger))
		return nil
	}

	root.AddCommand(newLoginCmd())
	root.AddCommand(newLogoutCmd())
	root.AddCommand(newAccountsCmd())
	root.AddCommand(newVersionCmd())
	root.AddCommand(newDebugBundleCmd())
	root.AddCommand(newReindexCmd())
	root.AddCommand(newTuiCmd())

	return root
}
