package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// newRootCmd builds the lazytg root command with persistent flags. A factory
// (rather than a package-level var) lets tests build a fresh tree per case
// without leaking flag state.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "lazytg",
		Short: "Local-first Telegram TUI client",
		Long: "lazytg is a Telegram TUI client written in Go.\n\n" +
			"It keeps a local SQLite mirror of your message history with FTS5 search,\n" +
			"so you can grep your chats instantly and offline. Stage 1 only ships the\n" +
			"foundation: auth, storage, CLI scaffolding. The TUI lands in stage 2.\n",
		SilenceUsage: true,
	}

	pf := root.PersistentFlags()
	pf.StringVar(&flagAccount, "account", "", "phone number of the account to operate on (e.g. +79998887766)")
	pf.StringVar(&flagConfig, "config", "", "path to config file (default: $XDG_CONFIG_HOME/lazytg/config.toml)")
	pf.BoolVar(&flagDebug, "debug", false, "enable verbose logging to stderr")
	pf.StringVar(&flagLogLevel, "log-level", "info", "logging level: debug|info|warn|error")

	root.PersistentPreRunE = func(_ *cobra.Command, _ []string) error {
		switch strings.ToLower(flagLogLevel) {
		case "debug", "info", "warn", "error":
			return nil
		default:
			return fmt.Errorf("invalid --log-level %q (want debug|info|warn|error)", flagLogLevel)
		}
	}

	root.AddCommand(newLoginCmd())
	root.AddCommand(newLogoutCmd())
	root.AddCommand(newAccountsCmd())
	root.AddCommand(newVersionCmd())
	root.AddCommand(newDebugBundleCmd())
	root.AddCommand(newDebugCmd())

	return root
}
