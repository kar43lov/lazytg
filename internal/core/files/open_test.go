package files

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// touch writes an empty file and returns its absolute path.
func touch(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// The override is what makes the feature usable for the kinds a system
// viewer handles badly — a round video message wants a looping player,
// not whatever the OS opens .mp4 with.
func TestNewOpener_UsesTheEnvironmentOverride(t *testing.T) {
	t.Setenv(OpenCommandEnv, "/bin/echo played")

	o, err := NewOpener(nil)
	if err != nil {
		t.Fatalf("new opener: %v", err)
	}
	if got := o.Command(); len(got) != 2 || got[0] != "/bin/echo" || got[1] != "played" {
		t.Fatalf("command = %v, want the override split into program and argument", got)
	}
}

// A misspelled override is caught once, at startup, rather than failing
// silently on every keypress with nothing on screen to explain it.
func TestNewOpener_RejectsAnOverrideThatIsNotExecutable(t *testing.T) {
	t.Setenv(OpenCommandEnv, "definitely-not-a-real-program-9f3c")

	if _, err := NewOpener(nil); err == nil {
		t.Fatalf("expected an error for an override that is not on PATH")
	}
}

func TestOpener_LaunchesTheViewer(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "opened")
	// A tiny script stands in for the system viewer: it records that it
	// ran, and with which argument.
	script := filepath.Join(dir, "viewer.sh")
	body := "#!/bin/sh\nprintf '%s' \"$1\" > " + marker + "\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil { //nolint:gosec
		t.Fatalf("write script: %v", err)
	}
	t.Setenv(OpenCommandEnv, script)

	o, err := NewOpener(nil)
	if err != nil {
		t.Fatalf("new opener: %v", err)
	}
	target := touch(t, dir, "photo.jpg")
	if err := o.Open(t.Context(), target); err != nil {
		t.Fatalf("open: %v", err)
	}

	// Open returns as soon as the viewer starts, so the marker appears
	// shortly after rather than immediately.
	//
	// The wait is deliberately long. Opener discards the viewer's streams
	// by design, so a viewer that starts and then fails leaves nothing to
	// observe but the missing marker — and on a machine running the whole
	// suite at once, "not yet" and "never" look identical for the first
	// second or two. A two-second deadline failed exactly that way inside
	// the pre-commit hook while passing on its own. Thirty seconds costs
	// nothing when the viewer works (the marker lands in milliseconds) and
	// only delays a run that is failing anyway.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if got, err := os.ReadFile(marker); err == nil { //nolint:gosec
			if string(got) != target {
				t.Fatalf("viewer received %q, want %q", got, target)
			}
			return
		}
		if time.Now().After(deadline) {
			// Say which half broke, so the next failure names itself:
			// a missing script is a fixture problem, a present one that
			// wrote nothing is the launch path.
			info, statErr := os.Stat(script)
			t.Fatalf("viewer never wrote the marker; script stat: %v (err %v)", info, statErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A relative path would resolve against lazytg's working directory
// rather than the download root, and a path starting with a dash would
// be read by the viewer as a flag. Neither reaches exec.
func TestOpener_RefusesAPathThatIsNotAbsolute(t *testing.T) {
	t.Setenv(OpenCommandEnv, "/bin/echo")

	o, err := NewOpener(nil)
	if err != nil {
		t.Fatalf("new opener: %v", err)
	}
	if err := o.Open(t.Context(), "photo.jpg"); err == nil {
		t.Fatalf("expected a relative path to be refused")
	} else if !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("error = %v, want it to name the problem", err)
	}
}

func TestOpener_RefusesAMissingFile(t *testing.T) {
	t.Setenv(OpenCommandEnv, "/bin/echo")

	o, err := NewOpener(nil)
	if err != nil {
		t.Fatalf("new opener: %v", err)
	}
	if err := o.Open(t.Context(), filepath.Join(t.TempDir(), "gone.jpg")); err == nil {
		t.Fatalf("expected a missing file to be refused")
	}
}
