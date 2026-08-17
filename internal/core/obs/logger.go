package obs

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

// LoggerConfig drives logger construction. Zero value is valid: it produces a
// logger that emits info-level JSON to stderr without rotation.
type LoggerConfig struct {
	// Level is the minimum level emitted. Defaults to slog.LevelInfo when
	// the zero value (info=0) is passed.
	Level slog.Level
	// Format is "json" (default, machine-readable) or "text". Applied to
	// both file and stderr handlers.
	Format string
	// FilePath, when non-empty, enables a rotating JSON sink. The TUI runs
	// without a writable stderr, so the file is the primary log target.
	FilePath string
	// Debug enables a human-readable stderr handler in addition to the file
	// sink. When false, stderr is suppressed so the TUI is not garbled.
	Debug bool
}

// New builds a *slog.Logger from cfg. The handler chain is:
//
//	caller-attrs → RedactingHandler → fan-out{file, stderr?}
//
// The fan-out is a tiny custom handler so file and stderr stay in lockstep
// even when one of them is nil.
func New(cfg LoggerConfig) *slog.Logger {
	level := cfg.Level
	opts := &slog.HandlerOptions{Level: level}

	var sinks []slog.Handler

	if cfg.FilePath != "" {
		fileWriter := &lumberjack.Logger{
			Filename:   cfg.FilePath,
			MaxSize:    10,
			MaxBackups: 3,
			MaxAge:     30,
			Compress:   false,
		}
		sinks = append(sinks, slog.NewJSONHandler(fileWriter, opts))
	}

	if cfg.Debug {
		sinks = append(sinks, newConsoleHandler(os.Stderr, cfg.Format, opts))
	}

	if len(sinks) == 0 {
		// Default behaviour for tests / library callers that do not opt
		// in to either sink: a JSON handler aimed at io.Discard so log
		// statements stay cheap but the slog API still works.
		sinks = append(sinks, slog.NewJSONHandler(io.Discard, opts))
	}

	var handler slog.Handler = fanout(sinks)
	handler = NewRedactingHandler(handler)
	return slog.New(handler)
}

// newConsoleHandler picks a text or JSON handler for stderr based on cfg.
// We default to text on stderr because that's what humans read.
func newConsoleHandler(w io.Writer, format string, opts *slog.HandlerOptions) slog.Handler {
	if strings.EqualFold(format, "json") {
		return slog.NewJSONHandler(w, opts)
	}
	return slog.NewTextHandler(w, opts)
}

// ParseLevel maps the strings accepted by --log-level to slog.Level.
// Returns an error for unknown values so the caller can propagate it.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("invalid log level %q", s)
	}
}

// WithAccount returns a logger pre-bound with the account_id field. It is a
// thin convenience over slog.With so call sites read symmetrically.
func WithAccount(l *slog.Logger, id int64) *slog.Logger {
	return l.With(slog.Int64("account_id", id))
}

// WithChat returns a logger pre-bound with the chat_id field.
func WithChat(l *slog.Logger, id int64) *slog.Logger {
	return l.With(slog.Int64("chat_id", id))
}

// WithRequestID returns a logger pre-bound with the request_id field.
func WithRequestID(l *slog.Logger, id string) *slog.Logger {
	return l.With(slog.String("request_id", id))
}
