// Package cmd assembles the cobra command tree exposed by the lazytg binary.
// It is intentionally thin: each subcommand wires the relevant core/tg/storage
// packages together and contains as little business logic as possible.
package cmd

// Persistent flag values shared across subcommands. They are populated by
// cobra during PersistentPreRunE and consumed by the subcommand RunE bodies.
var (
	flagAccount  string
	flagConfig   string
	flagDebug    bool
	flagLogLevel string
	flagPolling  bool
	flagAPIID    string
	flagAPIHash  string
)

// Execute runs the root command. Invoked from cmd/lazytg/main.go and returns
// the process exit code; cobra has already printed any error to stderr.
func Execute() int {
	if err := newRootCmd().Execute(); err != nil {
		return 1
	}
	return 0
}
