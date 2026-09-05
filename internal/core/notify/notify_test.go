package notify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The text is arguments, never a shell line, and on macOS the AppleScript
// string it becomes cannot be broken out of by a quote in the message.
func TestArgv(t *testing.T) {
	t.Parallel()

	n := &Notifier{}
	program, args := n.argv("Ann", `she said "hi" \ bye`)
	if program != "osascript" || len(args) != 2 || args[0] != "-e" {
		t.Fatalf("osascript argv = %s %v", program, args)
	}
	if args[1] != `display notification "she said \"hi\" \\ bye" with title "Ann"` {
		t.Fatalf("script = %s", args[1])
	}
	tn := &Notifier{command: []string{"terminal-notifier", "-title"}}
	if p, a := tn.argv("Ann", "hi"); p != "terminal-notifier" || strings.Join(a, " ") != "-title Ann -message hi" {
		t.Fatalf("terminal-notifier argv = %s %v", p, a)
	}
	custom := &Notifier{command: []string{"my-notify", "--urgency", "low"}}
	if p, a := custom.argv("Ann", "hi; rm -rf /"); p != "my-notify" || len(a) != 4 || a[3] != "hi; rm -rf /" {
		t.Fatalf("override argv = %s %v", p, a)
	}
}

// The override runs with the title and body as its last two arguments.
func TestNotify_RunsTheOverride(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "got")
	script := filepath.Join(dir, "notifier.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s|%s' \"$1\" \"$2\" > "+out+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(CommandEnv, script)
	n, err := New(nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := n.Notify(context.Background(), "Ann", "see you at six"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	got, _ := os.ReadFile(out)
	if string(got) != "Ann|see you at six" {
		t.Fatalf("notifier got %q", got)
	}
	t.Setenv(CommandEnv, "no-such-program-xyz")
	if _, err := New(nil); err == nil {
		t.Fatal("an override that is not executable was accepted")
	}
	var none *Notifier
	if err := none.Notify(context.Background(), "a", "b"); err == nil {
		t.Fatal("a nil notifier notified")
	}
}
