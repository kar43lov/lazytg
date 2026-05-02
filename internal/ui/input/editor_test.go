package input

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// withCacheDir redirects editorCacheDir to dir for the duration of t.
// Tests use this so prepareEditor does not write into the real user
// cache (~/Library/Caches/lazytg on macOS).
func withCacheDir(t *testing.T, dir string) {
	t.Helper()
	prev := editorCacheDir
	editorCacheDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { editorCacheDir = prev })
}

// withLookPath stubs editorLookPath. found=true makes it return the
// path verbatim (mimicking a hit); found=false makes it return ENOENT.
// Used so the "vi missing" branch can be exercised even on hosts that
// have vi installed.
func withLookPath(t *testing.T, found bool) {
	t.Helper()
	prev := editorLookPath
	editorLookPath = func(name string) (string, error) {
		if !found {
			return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
		}
		return "/usr/bin/" + name, nil
	}
	t.Cleanup(func() { editorLookPath = prev })
}

func TestPrepareEditorCreatesTempWith0600(t *testing.T) {
	withCacheDir(t, t.TempDir())
	t.Setenv("EDITOR", "cat")

	cmd, tmpPath, err := prepareEditor("hello world")
	if err != nil {
		t.Fatalf("prepareEditor: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(tmpPath) })

	if cmd == nil {
		t.Fatal("cmd is nil")
	}

	info, err := os.Stat(tmpPath)
	if err != nil {
		t.Fatalf("stat temp: %v", err)
	}
	if runtime.GOOS != "windows" {
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("temp file perms = %o, want 0600", mode)
		}
	}

	contents, err := os.ReadFile(tmpPath)
	if err != nil {
		t.Fatalf("read temp: %v", err)
	}
	if string(contents) != "hello world" {
		t.Fatalf("temp contents = %q, want %q", string(contents), "hello world")
	}

	// The cmd must end with the temp path so the editor opens our
	// staging file rather than something the user happened to be
	// looking at.
	if got := cmd.Args[len(cmd.Args)-1]; got != tmpPath {
		t.Fatalf("cmd last arg = %q, want %q", got, tmpPath)
	}
	if filepath.Base(cmd.Args[0]) != "cat" {
		t.Fatalf("cmd[0] = %q, want 'cat'", cmd.Args[0])
	}
}

func TestPrepareEditorPropagatesEnvSplitting(t *testing.T) {
	withCacheDir(t, t.TempDir())
	t.Setenv("EDITOR", "code -w --foo")

	cmd, tmpPath, err := prepareEditor("")
	if err != nil {
		t.Fatalf("prepareEditor: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(tmpPath) })

	want := []string{"code", "-w", "--foo", tmpPath}
	if len(cmd.Args) != len(want) {
		t.Fatalf("cmd.Args = %v, want %v", cmd.Args, want)
	}
	for i, a := range cmd.Args {
		if a != want[i] {
			t.Fatalf("cmd.Args[%d] = %q, want %q", i, a, want[i])
		}
	}
}

func TestPrepareEditorErrorsWhenViMissing(t *testing.T) {
	withCacheDir(t, t.TempDir())
	withLookPath(t, false)
	t.Setenv("EDITOR", "")

	_, _, err := prepareEditor("hi")
	if err == nil {
		t.Fatal("want error when EDITOR is unset and vi is not on PATH")
	}
	if !strings.Contains(err.Error(), "vi") {
		t.Fatalf("error = %v, want it to mention 'vi'", err)
	}
}

func TestEditorCallbackReadsContentsAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	tmpPath := filepath.Join(dir, "edit-roundtrip.md")
	if err := os.WriteFile(tmpPath, []byte("edited\n"), 0o600); err != nil {
		t.Fatalf("write temp: %v", err)
	}

	msg := editorCallback(tmpPath)(nil)
	closed, ok := msg.(EditorClosedMsg)
	if !ok {
		t.Fatalf("msg type = %T, want EditorClosedMsg", msg)
	}
	if closed.Err != nil {
		t.Fatalf("Err = %v, want nil", closed.Err)
	}
	if closed.Text != "edited\n" {
		t.Fatalf("Text = %q, want %q", closed.Text, "edited\n")
	}

	if _, err := os.Stat(tmpPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp file should be removed after success; stat err = %v", err)
	}
}

func TestEditorCallbackPropagatesExecErrorAndStillCleansUp(t *testing.T) {
	dir := t.TempDir()
	tmpPath := filepath.Join(dir, "edit-err.md")
	if err := os.WriteFile(tmpPath, []byte("partial"), 0o600); err != nil {
		t.Fatalf("write temp: %v", err)
	}

	sentinel := errors.New("editor crashed")
	msg := editorCallback(tmpPath)(sentinel)
	closed, ok := msg.(EditorClosedMsg)
	if !ok {
		t.Fatalf("msg type = %T, want EditorClosedMsg", msg)
	}
	if closed.Err == nil || !strings.Contains(closed.Err.Error(), "editor crashed") {
		t.Fatalf("Err = %v, want wrap of sentinel 'editor crashed'", closed.Err)
	}

	// Even on exec error the temp file must be cleaned up so the
	// editor staging dir does not accumulate stale drafts.
	if _, err := os.Stat(tmpPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp file should be removed even on exec error; stat err = %v", err)
	}
}

func TestEditorCallbackErrorWhenFileMissing(t *testing.T) {
	tmpPath := filepath.Join(t.TempDir(), "missing.md")

	msg := editorCallback(tmpPath)(nil)
	closed := msg.(EditorClosedMsg)
	if closed.Err == nil {
		t.Fatal("want read error when file is missing")
	}
	if !strings.Contains(closed.Err.Error(), "read editor output") {
		t.Fatalf("error = %v, want it to mention 'read editor output'", closed.Err)
	}
}

func TestResolveEditorFallback(t *testing.T) {
	t.Setenv("EDITOR", "")
	withLookPath(t, true)

	name, args, err := resolveEditor()
	if err != nil {
		t.Fatalf("resolveEditor: %v", err)
	}
	if name != "vi" {
		t.Fatalf("name = %q, want vi", name)
	}
	if len(args) != 0 {
		t.Fatalf("args = %v, want empty", args)
	}
}

func TestResolveEditorWhitespaceOnly(t *testing.T) {
	t.Setenv("EDITOR", "   ")
	withLookPath(t, true)

	name, _, err := resolveEditor()
	if err != nil {
		t.Fatalf("resolveEditor: %v", err)
	}
	if name != "vi" {
		t.Fatalf("name = %q, want fallback to vi", name)
	}
}

func TestOpenEditorReturnsErrCmdWhenEditorMissing(t *testing.T) {
	withCacheDir(t, t.TempDir())
	withLookPath(t, false)
	t.Setenv("EDITOR", "")

	cmd := OpenEditor("hi")
	if cmd == nil {
		t.Fatal("OpenEditor returned nil cmd")
	}
	msg := cmd()
	closed, ok := msg.(EditorClosedMsg)
	if !ok {
		t.Fatalf("msg type = %T, want EditorClosedMsg", msg)
	}
	if closed.Err == nil {
		t.Fatal("want non-nil Err when editor is missing")
	}
	if closed.Text != "" {
		t.Fatalf("Text = %q, want empty on error", closed.Text)
	}
}

// TestOpenEditorWithWrapperScriptRoundTrip drives the full prepare →
// run → callback flow with a fake "editor" that overwrites the temp
// file. It bypasses tea.ExecProcess (which only delivers when a
// tea.Program is running) by invoking exec.Cmd.Run() directly — the
// surface under test is the file lifecycle, not the program loop
// integration.
func TestOpenEditorWithWrapperScriptRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script wrapper is POSIX-only")
	}

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fake-editor.sh")
	const script = "#!/bin/sh\nprintf 'edited\\n' > \"$1\"\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	withCacheDir(t, t.TempDir())
	t.Setenv("EDITOR", scriptPath)

	cmd, tmpPath, err := prepareEditor("draft")
	if err != nil {
		t.Fatalf("prepareEditor: %v", err)
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("cmd.Run: %v", err)
	}

	msg := editorCallback(tmpPath)(nil)
	closed, ok := msg.(EditorClosedMsg)
	if !ok {
		t.Fatalf("msg type = %T, want EditorClosedMsg", msg)
	}
	if closed.Err != nil {
		t.Fatalf("Err = %v, want nil", closed.Err)
	}
	if closed.Text != "edited\n" {
		t.Fatalf("Text = %q, want %q", closed.Text, "edited\n")
	}
	if _, err := os.Stat(tmpPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp file should be removed; stat err = %v", err)
	}
}
