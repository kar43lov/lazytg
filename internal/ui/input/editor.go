package input

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// editorFallback is the editor used when the EDITOR env var is unset.
// Matches the convention shared by git, less, and most POSIX tools.
const editorFallback = "vi"

// editorCacheDir resolves the directory in which lazytg stages
// $EDITOR temp files. Wrapped in a package-level var so tests can
// redirect it to t.TempDir() instead of polluting the real user
// cache (~/Library/Caches/lazytg on macOS, $XDG_CACHE_HOME/lazytg on
// Linux).
var editorCacheDir = os.UserCacheDir

// editorLookPath is exec.LookPath wrapped so tests can pin its
// behaviour without touching $PATH globally. Useful for the
// "vi missing" branch which needs LookPath to fail deterministically
// even when /usr/bin/vi exists on the host.
var editorLookPath = exec.LookPath

// OpenEditor builds a tea.Cmd that pauses the TUI, spawns $EDITOR with
// a temp file pre-populated with currentText, and emits
// EditorClosedMsg once the editor exits. The temp file lives under the
// user cache dir with mode 0600 and is removed after the editor exits
// (even on error) so draft text is never left on disk.
//
// Errors during prepareEditor (cache dir unwritable, $EDITOR resolution
// failure, etc.) short-circuit into an EditorClosedMsg with Err set so
// the input pane can render a status line and refocus the textarea
// without invoking tea.ExecProcess at all — that path would lose the
// error to the program loop.
func OpenEditor(currentText string) tea.Cmd {
	cmd, tmpPath, err := prepareEditor(currentText)
	if err != nil {
		return func() tea.Msg {
			return EditorClosedMsg{Err: err}
		}
	}
	return tea.ExecProcess(cmd, editorCallback(tmpPath))
}

// prepareEditor resolves $EDITOR, creates the staging temp file with
// currentText pre-loaded, and returns the *exec.Cmd ready for
// tea.ExecProcess. The temp file path is returned alongside so the
// caller (or tests) can clean up.
//
// The function is package-private but unexported for two reasons:
//  1. Tests want to exercise the temp-file plumbing without going
//     through tea.ExecProcess (which only delivers its callback when
//     the surrounding tea.Program is running).
//  2. Splitting prepareEditor / editorCallback keeps each piece small
//     enough that the failure modes don't tangle — temp-file errors
//     happen synchronously, exec errors arrive asynchronously.
func prepareEditor(currentText string) (*exec.Cmd, string, error) {
	editor, args, err := resolveEditor()
	if err != nil {
		return nil, "", err
	}

	cacheDir, err := editorCacheDir()
	if err != nil {
		return nil, "", fmt.Errorf("user cache dir: %w", err)
	}
	dir := filepath.Join(cacheDir, "lazytg")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", fmt.Errorf("mkdir %s: %w", dir, err)
	}

	f, err := os.CreateTemp(dir, "edit-*.md")
	if err != nil {
		return nil, "", fmt.Errorf("create temp: %w", err)
	}
	tmpPath := f.Name()

	// os.CreateTemp uses 0600 by default but be explicit so the perm
	// is portable across umask settings and platforms (the unit test
	// asserts 0600 directly).
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return nil, "", fmt.Errorf("chmod temp: %w", err)
	}

	if _, err := f.WriteString(currentText); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return nil, "", fmt.Errorf("write temp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return nil, "", fmt.Errorf("close temp: %w", err)
	}

	// Build a fresh slice so we don't mutate the args returned by
	// resolveEditor (defensive against future callers reusing args).
	fullArgs := make([]string, 0, len(args)+1)
	fullArgs = append(fullArgs, args...)
	fullArgs = append(fullArgs, tmpPath)
	// G204: we deliberately exec the user-configured $EDITOR (vi
	// fallback). The editor program is the user's choice; lazytg is
	// merely delegating to it the way git/less/sudoedit do. The arg
	// vector is fixed (editor + flags + temp path); no shell is
	// involved, so injection through user input cannot happen here.
	cmd := exec.Command(editor, fullArgs...) //nolint:gosec
	return cmd, tmpPath, nil
}

// editorCallback returns the post-exit handler for tea.ExecProcess:
// read the temp file, remove it, and emit EditorClosedMsg. Removal
// happens in a defer so editor errors do not leak the temp file.
//
// Note: file contents are returned verbatim — trailing newlines are
// preserved. The input pane is responsible for normalising the value
// when it writes it back to the textarea (currently passthrough).
func editorCallback(tmpPath string) tea.ExecCallback {
	return func(execErr error) tea.Msg {
		defer func() { _ = os.Remove(tmpPath) }()
		if execErr != nil {
			return EditorClosedMsg{Err: fmt.Errorf("editor exited: %w", execErr)}
		}
		// G304: tmpPath is produced by os.CreateTemp inside our own
		// cache dir on the same goroutine — there is no user-input
		// path here, just our own staging file we created two
		// callbacks ago. False positive.
		contents, readErr := os.ReadFile(tmpPath) //nolint:gosec
		if readErr != nil {
			return EditorClosedMsg{Err: fmt.Errorf("read editor output: %w", readErr)}
		}
		return EditorClosedMsg{Text: string(contents)}
	}
}

// resolveEditor splits the EDITOR env var into command + extra args.
// Falls back to "vi" when EDITOR is unset or whitespace-only. Returns
// an error when neither is on PATH so OpenEditor can surface a usable
// message instead of getting "exec: not found" later.
//
// Whitespace splitting is intentionally naive — it handles the common
// cases ("vi", "code -w", "nvim --noplugin") and leaves quoted paths
// to the user's shell. Stage 2 deliberately keeps this minimal; v0.2
// will add a sandbox env-filter and shell-style splitting if the need
// arises.
func resolveEditor() (string, []string, error) {
	raw := strings.TrimSpace(os.Getenv("EDITOR"))
	if raw == "" {
		if _, err := editorLookPath(editorFallback); err != nil {
			return "", nil, fmt.Errorf("EDITOR is unset and %q is not on PATH: %w", editorFallback, err)
		}
		return editorFallback, nil, nil
	}
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return "", nil, errors.New("EDITOR is whitespace-only")
	}
	return parts[0], parts[1:], nil
}
