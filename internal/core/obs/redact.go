// Package obs hosts cross-cutting observability primitives: structured
// logging with redaction and (later) the debug-bundle producer.
package obs

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
)

// Patterns match common Telegram-related secrets that must never reach disk
// or stderr verbatim. Order matters: api_hash must be tried before the
// generic session matcher because a 32-hex string also matches the long
// base64-ish pattern.
//
// phoneRe deliberately requires a leading "+" so that bare numeric IDs
// (chat_id, account_id, unix timestamps — all int64 values that frequently
// reach 10+ digits) survive unredacted. Telegram phone numbers are always
// rendered with the "+" country prefix, so this is a safe narrowing.
//
// sessionRe requires the run to contain at least one non-hex character so
// long hex blobs (auth keys, message hashes) fall through to api_hash or
// remain visible. A pure base64 token will always include "+" / "/" / "=" or
// upper/lower case mix, so this barely changes coverage of real secrets.
var (
	phoneRe   = regexp.MustCompile(`\+\d{10,15}\b`)
	apiHashRe = regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`)
	sessionRe = regexp.MustCompile(`[A-Za-z0-9+/=_-]{40,}`)
)

// Redact masks values that look like Telegram secrets: phone numbers,
// MTProto session blobs and api_hash hex strings. The function is best-effort
// — it is meant to keep accidents out of logs, not to defeat a determined
// reader. Order: api_hash → session (longer) → phone (shorter).
//
// The session matcher is broad (40+ base64-ish chars), so we apply it only
// to runs that contain at least one of "+/=_-". A pure-alphanumeric run of
// that length is almost always an identifier (URL slug, hex hash already
// caught by api_hash, repo path) rather than a session blob.
func Redact(s string) string {
	if s == "" {
		return s
	}
	s = apiHashRe.ReplaceAllString(s, "<api_hash>")
	s = sessionRe.ReplaceAllStringFunc(s, func(m string) string {
		if strings.ContainsAny(m, "+/=_-") {
			return "<session>"
		}
		return m
	})
	s = phoneRe.ReplaceAllString(s, "+***")
	return s
}

// RedactingHandler wraps a slog.Handler and applies Redact to every string
// attribute (and the message) before delegating. Group attributes are walked
// recursively so structured fields nested under With() calls are also
// scrubbed.
type RedactingHandler struct {
	inner slog.Handler
}

// NewRedactingHandler returns a handler that redacts string values before
// passing the record to inner. Non-string attributes are forwarded as-is.
func NewRedactingHandler(inner slog.Handler) *RedactingHandler {
	return &RedactingHandler{inner: inner}
}

// Enabled delegates to the wrapped handler.
func (h *RedactingHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	return h.inner.Enabled(ctx, lvl)
}

// Handle scrubs the record's message and attributes, then forwards to the
// wrapped handler. The original record is not mutated — slog records carry a
// pointer-to-array of attrs that we must clone before changing.
func (h *RedactingHandler) Handle(ctx context.Context, r slog.Record) error {
	rc := slog.NewRecord(r.Time, r.Level, Redact(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		rc.AddAttrs(redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, rc)
}

// WithAttrs returns a new handler whose pre-bound attributes have already been
// redacted, so we don't pay the regex cost on every Handle call.
func (h *RedactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cleaned := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		cleaned[i] = redactAttr(a)
	}
	return &RedactingHandler{inner: h.inner.WithAttrs(cleaned)}
}

// WithGroup delegates to the wrapped handler — group names themselves are
// developer-controlled identifiers, not user data.
func (h *RedactingHandler) WithGroup(name string) slog.Handler {
	return &RedactingHandler{inner: h.inner.WithGroup(name)}
}

// redactAttr returns a copy of a with string values scrubbed. Group attrs are
// walked recursively. Non-string scalars are returned unchanged.
func redactAttr(a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, Redact(a.Value.String()))
	case slog.KindGroup:
		src := a.Value.Group()
		dst := make([]slog.Attr, len(src))
		for i, g := range src {
			dst[i] = redactAttr(g)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(dst...)}
	default:
		return a
	}
}
