// Package notify puts a message in the desktop's notification centre.
//
// The terminal bell is the notification a terminal has, and it is not
// enough for the session this client is built for: a tmux window on a
// server, detached for the afternoon, rings a bell nobody is in the room
// to hear. The desktop has a notification centre for exactly that, and
// every platform ships a command that posts to it. Off by default: a
// notification is the client leaving the terminal, and the person at the
// keyboard is the one to allow that (LAZYTG_NOTIFY=desktop).
//
// Two things are deliberate. The text goes out as arguments, never through
// a shell: the body is a message somebody else wrote. And on macOS the
// AppleScript string it becomes has its two escapes closed, because that
// is the one place the text is interpreted before it is shown.
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// CommandEnv names a notifier to run instead of the platform default:
// the program and its fixed arguments, with the title and body appended
// as two more arguments. Split on whitespace, never run through a shell.
const CommandEnv = "LAZYTG_NOTIFY_CMD"

// ErrNoNotifier says the platform has no notifier this package knows.
var ErrNoNotifier = errors.New("notify: no desktop notifier on this platform")

// timeout bounds one notification; a notifier that hangs must not hold
// the client's goroutine for good.
const timeout = 5 * time.Second

// Notifier posts to the desktop.
type Notifier struct {
	// command is the program and fixed arguments; nil means osascript.
	command []string
	log     *slog.Logger
}

// New picks the notifier: the override when set, terminal-notifier or
// osascript on macOS, notify-send elsewhere.
func New(log *slog.Logger) (*Notifier, error) {
	if log == nil {
		log = slog.New(discardHandler{})
	}
	if raw := strings.TrimSpace(os.Getenv(CommandEnv)); raw != "" {
		parts := strings.Fields(raw)
		if _, err := exec.LookPath(parts[0]); err != nil {
			return nil, fmt.Errorf("notify: %s names %q, which is not executable: %w", CommandEnv, parts[0], err)
		}
		return &Notifier{command: parts, log: log}, nil
	}
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("terminal-notifier"); err == nil {
			return &Notifier{command: []string{"terminal-notifier", "-title"}, log: log}, nil
		}
		return &Notifier{log: log}, nil
	case "linux", "freebsd", "openbsd", "netbsd":
		if _, err := exec.LookPath("notify-send"); err != nil {
			return nil, fmt.Errorf("notify: notify-send is not installed: %w", err)
		}
		return &Notifier{command: []string{"notify-send"}, log: log}, nil
	default:
		return nil, ErrNoNotifier
	}
}

// Notify posts one notification and waits for the notifier to hand it
// over, no longer than the timeout.
func (n *Notifier) Notify(ctx context.Context, title, body string) error {
	if n == nil {
		return ErrNoNotifier
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	program, args := n.argv(title, body)
	cmd := exec.CommandContext(ctx, program, args...) //nolint:gosec // program is the platform notifier or the user's override; the text is passed as arguments, not through a shell
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("notify: %s: %w", program, err)
	}
	return nil
}

// argv is the command line for one notification.
func (n *Notifier) argv(title, body string) (string, []string) {
	if len(n.command) == 0 {
		return "osascript", []string{"-e", appleScript(title, body)}
	}
	if n.command[0] == "terminal-notifier" {
		return n.command[0], append(append([]string(nil), n.command[1:]...), title, "-message", body)
	}
	return n.command[0], append(append([]string(nil), n.command[1:]...), title, body)
}

// appleScript is the one-line script that posts a notification. The
// text is interpreted once, as an AppleScript string, whose only escapes
// are the backslash and the double quote; both are closed here.
func appleScript(title, body string) string {
	return fmt.Sprintf(`display notification "%s" with title "%s"`, appleQuote(body), appleQuote(title))
}

func appleQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (h discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return h }
func (h discardHandler) WithGroup(string) slog.Handler           { return h }
