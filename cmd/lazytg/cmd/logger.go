package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/pgmac/lazytg/internal/core/config"
	"github.com/pgmac/lazytg/internal/core/obs"
)

// loggerKey is the context key used to attach the per-invocation slog.Logger.
// A dedicated unexported type satisfies revive's context-keys-type rule and
// prevents collisions with arbitrary string-keyed values.
type loggerKey struct{}

// withLogger returns ctx augmented with l. Pure pass-through helper so the
// PersistentPreRunE body stays readable.
func withLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, l)
}

// LoggerFromContext returns the slog.Logger attached by the root command. If
// none is present (e.g. unit tests that bypass cobra) it returns
// slog.Default so call sites never get a nil pointer.
func LoggerFromContext(ctx context.Context) *slog.Logger {
	if ctx == nil {
		return slog.Default()
	}
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}

// buildLogger materialises a slog.Logger from the persistent CLI flags.
// We point the file sink at $XDG_STATE_HOME/lazytg/lazytg.log when the state
// directory resolves; if it doesn't (e.g. sandboxed test env), we degrade to
// a stderr-only or discard logger rather than failing the command.
//
// stderr is the warning channel: we surface a one-line note when StateDir()
// fails so the operator sees that file logging is off (otherwise logs
// silently vanish into io.Discard with --debug=false).
func buildLogger(level slog.Level, debug bool) *slog.Logger {
	cfg := obs.LoggerConfig{
		Level:  level,
		Format: "json",
		Debug:  debug,
	}
	stateDir, err := config.StateDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "lazytg: file logging disabled: %v\n", err)
	} else {
		cfg.FilePath = filepath.Join(stateDir, "lazytg.log")
	}
	return obs.New(cfg)
}
