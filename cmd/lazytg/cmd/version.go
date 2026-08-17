package cmd

import (
	"errors"
	"fmt"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	tgclient "github.com/kar43lov/lazytg/internal/tg"
)

// Build-time variables. GoReleaser fills them via -ldflags; the defaults make
// `go run` and local `go build` print something useful too.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// credentialStatus describes where the api_id/api_hash in effect come from,
// never what they are. It is the first thing to ask for when someone reports
// an api_id-level ban: "embedded" means the shipped release key, anything
// else means the user's own.
func credentialStatus() string {
	_, _, src, err := tgclient.ResolveCredentials(flagAPIID, flagAPIHash)
	if err != nil {
		if errors.Is(err, tgclient.ErrNoCredentials) {
			return string(tgclient.SourceNone) + " (no credentials — see docs/INSTALL.md)"
		}
		// A misconfiguration (half-filled layer, unparseable id) is a
		// different problem from "none configured" and needs its own
		// wording — the multi-line error is trimmed to its first line so
		// `version` stays a five-line output.
		first, _, _ := strings.Cut(err.Error(), "\n")
		return "misconfigured: " + first
	}
	embedded := "no"
	if tgclient.HasEmbeddedCredentials() {
		embedded = "yes"
	}
	return fmt.Sprintf("%s (build embeds credentials: %s)", src, embedded)
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build version information",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(),
				"lazytg %s\ncommit: %s\nbuilt:  %s\ngo:     %s/%s %s\napi:    %s\n",
				version, commit, date, runtime.GOOS, runtime.GOARCH, runtime.Version(),
				credentialStatus())
			return err
		},
	}
}
