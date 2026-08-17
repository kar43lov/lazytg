package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/kar43lov/lazytg/internal/app"
	"github.com/kar43lov/lazytg/internal/core/config"
	"github.com/kar43lov/lazytg/internal/core/obs"
	"github.com/kar43lov/lazytg/internal/storage/sqlite"
)

// configFileName is the canonical lazytg TOML location inside
// Paths.Config. The file does not yet exist at Stage 3 (config-from-file
// is a v0.2 work item) but the bundle still tries to copy it so the
// pipeline is wired end-to-end.
const configFileName = "lazytg.toml"

// logFileName mirrors what cmd/lazytg/cmd/logger.go writes to. We
// duplicate the constant here rather than export it so the bundle is
// not coupled to the logger configuration's surface area — they could
// drift but the values match by convention.
const logFileName = "lazytg.log"

// configPathSource adapts the resolved XDG config dir into the
// obs.ConfigSource interface. It returns the user's TOML file when one
// exists; the bundle handles the missing-file case internally.
type configPathSource struct{ path string }

func (c configPathSource) ConfigPath() string { return c.path }

// newDebugBundleCmd produces the redacted diagnostics tarball described
// in docs/SECURITY.md. It opens the SQLite repo read-only-style (the
// bundle only needs COUNT(*) queries) and assembles the bundle with
// the build version and the user's log + config paths from XDG.
//
// --out overrides the default path (./lazytg-bundle-<timestamp>.tar.gz
// in the user's cwd). The flag exists primarily for tests but is also
// useful for triages who want to drop the bundle into /tmp.
func newDebugBundleCmd() *cobra.Command {
	var outFlag string
	cmd := &cobra.Command{
		Use:   "debug-bundle",
		Short: "Collect a redacted diagnostics bundle (logs, config, db stats — never session blob)",
		Long: "debug-bundle assembles a small tar.gz with the build version, the\n" +
			"trailing log lines, the redacted user config, basic SQLite stats and a\n" +
			"goroutine dump. Session blobs, api_hash values and message text are\n" +
			"never included — the contents pass through obs.Redact and a grep test\n" +
			"in CI verifies the constraint.\n",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := outFlag
			if out == "" {
				stamp := time.Now().UTC().Format("20060102-150405")
				out = "lazytg-bundle-" + stamp + ".tar.gz"
			}

			paths, err := resolvePathsOnly()
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			// Run the same permissions audit as the TUI startup so a
			// debug bundle generated against a tampered repo is
			// refused at the same point — keeps the CLI subcommands
			// from drifting below the TUI's security floor.
			if err := app.CheckPermissions(paths, LoggerFromContext(ctx)); err != nil {
				return fmt.Errorf("debug-bundle: %w", err)
			}

			repo, err := sqlite.Open(ctx, dbPath(paths))
			if err != nil {
				return fmt.Errorf("open repo: %w", err)
			}
			defer func() { _ = repo.Close() }()

			bundle := &obs.Bundle{
				Logger:  LoggerFromContext(ctx),
				Cfg:     configPathSource{path: filepath.Join(paths.Config, configFileName)},
				Version: obs.VersionInfo{Version: version, Commit: commit, Date: date},
				Store:   repo,
				LogPath: logFilePath(paths),
			}

			abs, err := bundle.Create(ctx, out)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), abs)
			return err
		},
	}
	cmd.Flags().StringVar(&outFlag, "out", "", "output path (default: ./lazytg-bundle-<timestamp>.tar.gz)")
	return cmd
}

// logFilePath mirrors buildLogger's logger.FilePath computation so the
// bundle reads from the same location production logs are written to.
// Returns "" when StateDir is unavailable — the bundle treats that as
// "no log file configured" and writes a placeholder body.
func logFilePath(paths config.Paths) string {
	if paths.State == "" {
		return ""
	}
	return filepath.Join(paths.State, logFileName)
}
