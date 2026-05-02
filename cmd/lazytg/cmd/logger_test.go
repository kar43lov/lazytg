package cmd

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/spf13/cobra"
)

func TestLoggerFromContext_NoLogger_ReturnsDefault(t *testing.T) {
	if got := LoggerFromContext(context.Background()); got != slog.Default() {
		t.Errorf("expected slog.Default fallback, got %p", got)
	}
}

func TestLoggerFromContext_NilCtx_ReturnsDefault(t *testing.T) {
	//nolint:staticcheck // intentionally testing nil-ctx safety net
	var ctx context.Context
	if got := LoggerFromContext(ctx); got != slog.Default() {
		t.Errorf("expected slog.Default fallback for nil ctx, got %p", got)
	}
}

func TestLoggerFromContext_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	want := slog.New(slog.NewJSONHandler(&buf, nil))

	ctx := withLogger(context.Background(), want)
	if got := LoggerFromContext(ctx); got != want {
		t.Errorf("LoggerFromContext returned %p, want %p", got, want)
	}
}

func TestRoot_AttachesLoggerToContext(t *testing.T) {
	resetFlags()
	root := newRootCmd()

	var captured *slog.Logger
	probe := &cobra.Command{
		Use: "capture-logger",
		RunE: func(cmd *cobra.Command, _ []string) error {
			captured = LoggerFromContext(cmd.Context())
			return nil
		},
	}
	root.AddCommand(probe)
	root.SetArgs([]string{"capture-logger"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if captured == nil {
		t.Fatal("subcommand never ran or logger not attached")
	}
	if captured == slog.Default() {
		t.Error("PersistentPreRunE did not install a custom logger")
	}
}
