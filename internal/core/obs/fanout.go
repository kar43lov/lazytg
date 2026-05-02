package obs

import (
	"context"
	"errors"
	"log/slog"
)

// fanout broadcasts a record to every wrapped handler. It is intentionally a
// concrete slice type rather than a struct so an empty fan-out is the zero
// value and slog's default Enabled fast-path stays cheap.
type fanout []slog.Handler

// Enabled returns true when at least one downstream handler is enabled at
// the level — any sink that wants the record forces us to format it.
func (f fanout) Enabled(ctx context.Context, lvl slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, lvl) {
			return true
		}
	}
	return false
}

// Handle dispatches the record to every sink. Errors are joined so a broken
// file sink does not silently drop the stderr copy.
func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range f {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// WithAttrs returns a new fanout where each handler has the attrs bound, so
// downstream sinks share the same prefix.
func (f fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

// WithGroup returns a new fanout where each handler has the group applied.
func (f fanout) WithGroup(name string) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithGroup(name)
	}
	return out
}
