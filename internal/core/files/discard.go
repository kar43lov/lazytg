package files

import (
	"context"
	"log/slog"
)

// discardHandler is a no-op slog.Handler used when the package's nil-log
// fallback fires. We mirror the same pattern as internal/core/sync /
// internal/core/search to avoid pulling in a third-party dependency.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (discardHandler) WithAttrs([]slog.Attr) slog.Handler        { return discardHandler{} }
func (discardHandler) WithGroup(string) slog.Handler             { return discardHandler{} }
