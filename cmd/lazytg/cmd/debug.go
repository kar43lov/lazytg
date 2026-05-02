package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newDebugCmd is a parent for future `lazytg debug *` subcommands (stats,
// trace, dump). Stage 1 only ships an empty parent so the help text is
// discoverable; concrete subcommands land in stages 2 and 3.
func newDebugCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "debug",
		Short: "Diagnostics commands",
		Long:  "Container for diagnostic subcommands. See `lazytg debug-bundle`.",
	}
}

// newDebugBundleCmd is the stub for stage 1. Stage 3 wires it to the
// real bundle producer (logs + redacted config + DB stats — never the
// session blob or message text). For now it just prints a stub line so
// the command is discoverable in --help.
func newDebugBundleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "debug-bundle",
		Short: "Collect a redacted diagnostics bundle (stub)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "debug-bundle stub: implementation in stage 3")
			return err
		},
	}
}
