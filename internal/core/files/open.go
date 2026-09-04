package files

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// OpenCommandEnv overrides the program lazytg hands a downloaded file to.
// Whitespace-separated, program first: LAZYTG_OPEN_CMD="mpv --loop" plays
// a video note on a loop the way Telegram does, and
// LAZYTG_OPEN_CMD="feh" keeps photos out of a heavier viewer.
const OpenCommandEnv = "LAZYTG_OPEN_CMD"

// ErrNoOpener is returned when the platform has no known default and the
// user has set no override. Reported rather than guessed: a wrong guess
// would execute an arbitrary program name found on PATH.
var ErrNoOpener = errors.New("open: no system opener for this platform — set " + OpenCommandEnv)

// Opener hands a file on disk to the viewer the operating system, or the
// user, has chosen for it.
//
// This is how lazytg looks at media without pretending to be a media
// viewer. A photo can be drawn in a terminal that speaks a graphics
// protocol, but a round video message cannot be drawn in any terminal at
// all — it is video, and the only honest answer is to hand it to
// something that plays video. Making that the single path for every kind
// keeps one behaviour to learn instead of two.
//
// The program is executed directly, never through a shell: the argument
// is a filename chosen by whoever sent the message, and a shell would
// make its punctuation executable.
type Opener struct {
	command []string
	log     *slog.Logger
}

// NewOpener builds an Opener from the environment override if there is
// one, and from the platform default otherwise. An unusable override —
// empty, or naming a program that is not on PATH — is a hard error at
// construction rather than a surprise at the first keypress.
func NewOpener(log *slog.Logger) (*Opener, error) {
	if log == nil {
		log = slog.New(discardHandler{})
	}
	if raw := strings.TrimSpace(os.Getenv(OpenCommandEnv)); raw != "" {
		parts := strings.Fields(raw)
		if _, err := exec.LookPath(parts[0]); err != nil {
			return nil, fmt.Errorf("open: %s names %q, which is not executable: %w", OpenCommandEnv, parts[0], err)
		}
		return &Opener{command: parts, log: log}, nil
	}
	switch runtime.GOOS {
	case "darwin":
		return &Opener{command: []string{"open"}, log: log}, nil
	case "linux", "freebsd", "openbsd", "netbsd":
		return &Opener{command: []string{"xdg-open"}, log: log}, nil
	default:
		return nil, ErrNoOpener
	}
}

// Command reports the program and arguments this Opener will run, for
// the status line and for tests.
func (o *Opener) Command() []string { return append([]string(nil), o.command...) }

// Open launches the viewer for path and returns as soon as it has
// started.
//
// It does not wait for the viewer to exit. Waiting would hold the caller
// for as long as the user looks at the photo, and on macOS `open` exits
// immediately anyway, having handed the file to another application. The
// process is reaped on its own goroutine so a viewer that does stay in
// the foreground does not become a zombie.
//
// path must be absolute and must exist. Both are checked rather than
// assumed: a relative path would resolve against lazytg's working
// directory rather than the download root, and a path that begins with a
// dash would be read by the viewer as a flag.
//
// What is *not* checked is that the path lies inside the download root,
// because today it always does: DownloadService is the only caller, and
// it builds every path through FileStore. A second caller would change
// that — if one appears, the check belongs here rather than in it, since
// the argument would then be reachable from message content and this is
// the function that hands it to a program.
func (o *Opener) Open(ctx context.Context, path string) error {
	if o == nil || len(o.command) == 0 {
		return ErrNoOpener
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("open: %q is not an absolute path", path)
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("open: %w", err)
	}

	args := append(append([]string(nil), o.command[1:]...), path)
	cmd := exec.CommandContext(ctx, o.command[0], args...) //nolint:gosec // program comes from the platform default or an explicit env override, never from message content
	// The viewer must not inherit the TUI's terminal: a program that
	// writes to stdout would paint over the rendered thread, and one that
	// reads stdin would steal the user's keystrokes.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open: start %s: %w", o.command[0], err)
	}
	go func() {
		if err := cmd.Wait(); err != nil {
			o.log.Debug("open: viewer exited with an error", "path", path, "err", err)
		}
	}()
	return nil
}
