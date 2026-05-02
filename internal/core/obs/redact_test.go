package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestRedact_Patterns(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"clean text", "hello world", "hello world"},
		{"phone with plus", "call +79991112233 now", "call +*** now"},
		{"phone without plus", "call 79991112233 now", "call +*** now"},
		{"phone short ignored", "id 12345", "id 12345"},
		{
			"api_hash 32 hex",
			"api_hash=0123456789abcdef0123456789abcdef tail",
			"api_hash=<api_hash> tail",
		},
		{
			"session base64",
			"session: AaBbCcDdEeFfGgHhIiJjKkLlMmNnOoPpQqRrSsTtUuVvWwXxYy",
			"session: <session>",
		},
		{
			"phone and api_hash together",
			"phone +79998887766 hash 0123456789abcdef0123456789abcdef",
			"phone +*** hash <api_hash>",
		},
		{
			"hex shorter than 32 untouched",
			"deadbeef",
			"deadbeef",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Redact(c.in); got != c.want {
				t.Errorf("Redact(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestRedactingHandler_ScrubsMessageAndAttrs(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	h := NewRedactingHandler(inner)
	logger := slog.New(h)

	logger.Info("login for +79991112233",
		slog.String("session", "AaBbCcDdEeFfGgHhIiJjKkLlMmNnOoPpQqRrSsTtUuVv"),
		slog.Int("retry", 3),
	)

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("json: %v (raw=%q)", err, buf.String())
	}

	if msg, _ := got["msg"].(string); !strings.Contains(msg, "+***") || strings.Contains(msg, "+79991112233") {
		t.Errorf("message not redacted: %q", msg)
	}
	if sess, _ := got["session"].(string); sess != "<session>" {
		t.Errorf("session not redacted: %q", sess)
	}
	if retry, _ := got["retry"].(float64); retry != 3 {
		t.Errorf("non-string attr should pass through, got %v", got["retry"])
	}
}

func TestRedactingHandler_WithAttrsRedacts(t *testing.T) {
	var buf bytes.Buffer
	h := NewRedactingHandler(slog.NewJSONHandler(&buf, nil))
	logger := slog.New(h).With(slog.String("phone", "+79991112233"))
	logger.Info("ping")

	if !strings.Contains(buf.String(), `"phone":"+***"`) {
		t.Errorf("With() attribute not redacted: %s", buf.String())
	}
}

func TestRedactingHandler_NestedGroup(t *testing.T) {
	var buf bytes.Buffer
	h := NewRedactingHandler(slog.NewJSONHandler(&buf, nil))
	slog.New(h).LogAttrs(context.Background(), slog.LevelInfo, "x",
		slog.Group("ctx",
			slog.String("phone", "+79991112233"),
			slog.Int("attempt", 1),
		),
	)

	var payload map[string]any
	_ = json.Unmarshal(buf.Bytes(), &payload)
	ctx, _ := payload["ctx"].(map[string]any)
	if ctx == nil || ctx["phone"] != "+***" {
		t.Errorf("nested group phone not redacted: %v", payload)
	}
}
