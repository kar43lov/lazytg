package obs

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in      string
		want    slog.Level
		wantErr bool
	}{
		{"", slog.LevelInfo, false},
		{"info", slog.LevelInfo, false},
		{"DEBUG", slog.LevelDebug, false},
		{"warn", slog.LevelWarn, false},
		{"warning", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"loud", slog.LevelInfo, true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := ParseLevel(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("ParseLevel(%q) err=%v wantErr=%v", c.in, err, c.wantErr)
			}
			if got != c.want {
				t.Errorf("ParseLevel(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestNew_NoSinks_DoesNotPanic(t *testing.T) {
	// Zero config: no file, no debug. Logger should still accept calls and
	// drop them silently — useful for libraries that don't want output.
	logger := New(LoggerConfig{})
	logger.Info("hello", slog.String("k", "v"))
}

func TestNew_FileSink_WritesJSONWithRedaction(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "lazytg.log")

	logger := New(LoggerConfig{
		Level:    slog.LevelDebug,
		Format:   "json",
		FilePath: logFile,
		Debug:    false,
	})
	logger.Info("auth ok for +79991112233",
		slog.String("phone", "+79991112233"),
		slog.Int64("account_id", 42),
	)

	// lumberjack flushes on Write, but we still close-over by re-reading.
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("log file empty")
	}

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(data), &entry); err != nil {
		t.Fatalf("json: %v (raw=%s)", err, data)
	}
	for _, k := range []string{"time", "level", "msg"} {
		if _, ok := entry[k]; !ok {
			t.Errorf("missing field %q in %v", k, entry)
		}
	}
	if msg, _ := entry["msg"].(string); strings.Contains(msg, "+79991112233") {
		t.Errorf("phone not redacted in msg: %v", msg)
	}
	if phone, _ := entry["phone"].(string); phone != "+***" {
		t.Errorf("phone attr not redacted: %v", entry["phone"])
	}
}

func TestNew_DebugStderrCaptured(t *testing.T) {
	// Capture stderr by redirecting os.Stderr to an os.Pipe before creating
	// the logger. The logger snapshots os.Stderr at construction time, so
	// any later writes go to the pipe we control.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	logger := New(LoggerConfig{Level: slog.LevelDebug, Debug: true})
	logger.Info("debug-on", slog.String("k", "v"))

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(got), "debug-on") {
		t.Errorf("expected stderr output, got %q", got)
	}
}

func TestNew_NoDebug_StderrSilent(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	dir := t.TempDir()
	logger := New(LoggerConfig{
		Level:    slog.LevelDebug,
		FilePath: filepath.Join(dir, "log.json"),
		Debug:    false,
	})
	logger.Info("file-only")

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	got, _ := io.ReadAll(r)
	if len(got) != 0 {
		t.Errorf("stderr should be silent without --debug, got %q", got)
	}
}

func TestWithHelpers(t *testing.T) {
	var buf bytes.Buffer
	h := NewRedactingHandler(slog.NewJSONHandler(&buf, nil))
	base := slog.New(h)

	WithAccount(base, 7).Info("a")
	WithChat(base, 11).Info("b")
	WithRequestID(base, "req-1").Info("c")

	out := buf.String()
	for _, want := range []string{`"account_id":7`, `"chat_id":11`, `"request_id":"req-1"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
}
