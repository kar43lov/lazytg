package cmd

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// Build-time variables. GoReleaser fills them via -ldflags; the defaults make
// `go run` and local `go build` print something useful too.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build version information",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(),
				"lazytg %s\ncommit: %s\nbuilt:  %s\ngo:     %s/%s %s\n",
				version, commit, date, runtime.GOOS, runtime.GOARCH, runtime.Version())
			return err
		},
	}
}
