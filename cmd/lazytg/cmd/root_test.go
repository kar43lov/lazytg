package cmd

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/storage/sqlite"
)

// resetFlags clears package-level persistent flag state so tests can be run
// in any order. cobra reuses the underlying pflag.FlagSet between calls when
// the variables are package-level — we own the variables, so we reset them.
func resetFlags() {
	flagAccount = ""
	flagConfig = ""
	flagDebug = false
	flagLogLevel = "info"
}

// setupCmdTest yields a hermetic environment for cobra command tests:
// fresh flag state and HOME/XDG paths re-rooted to a t.TempDir() so the
// PersistentPreRunE buildLogger call does not mkdir the developer's real
// ~/Library/Application Support/lazytg or ~/.local/state/lazytg.
//
// We override HOME (used by os.UserHomeDir on linux/darwin) and clear
// XDG_*_HOME so linux falls through to HOME-based defaults too.
func setupCmdTest(t *testing.T) {
	t.Helper()
	resetFlags()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
}

func TestRoot_PersistentFlagsRegistered(t *testing.T) {
	setupCmdTest(t)
	root := newRootCmd()

	for _, name := range []string{"account", "config", "debug", "log-level"} {
		if root.PersistentFlags().Lookup(name) == nil {
			t.Errorf("persistent flag %q is not registered", name)
		}
	}
}

func TestRoot_ParsesAccountFlag(t *testing.T) {
	setupCmdTest(t)
	root := newRootCmd()
	// --out routes the bundle into a tmp dir so this flag-parsing
	// test does not leave a tarball in the package's cwd.
	out := t.TempDir() + "/bundle.tar.gz"
	root.SetArgs([]string{"--account", "+79991112233", "debug-bundle", "--out", out})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if flagAccount != "+79991112233" {
		t.Fatalf("--account not parsed, got %q", flagAccount)
	}
}

func TestRoot_RejectsInvalidLogLevel(t *testing.T) {
	setupCmdTest(t)
	root := newRootCmd()
	// --out keeps the test hermetic even though the command should
	// fail before the bundle is written.
	out := t.TempDir() + "/bundle.tar.gz"
	root.SetArgs([]string{"--log-level", "loud", "debug-bundle", "--out", out})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for invalid log level, got nil")
	}
	if !strings.Contains(err.Error(), "invalid log level") {
		t.Fatalf("error %q does not mention invalid log level", err)
	}
}

func TestDebugBundle_WritesBundleToOutPath(t *testing.T) {
	setupCmdTest(t)
	dir := t.TempDir()
	outPath := dir + "/bundle.tar.gz"

	root := newRootCmd()
	out := &bytes.Buffer{}
	root.SetArgs([]string{"debug-bundle", "--out", outPath})
	root.SetOut(out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.String(), outPath) {
		t.Fatalf("expected bundle path in stdout, got %q", out.String())
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("bundle file not written: %v", err)
	}
}

func TestVersion_PrintsBuildInfo(t *testing.T) {
	setupCmdTest(t)
	root := newRootCmd()
	out := &bytes.Buffer{}
	root.SetArgs([]string{"version"})
	root.SetOut(out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.String()
	for _, want := range []string{"lazytg", "commit:", "built:", "go:"} {
		if !strings.Contains(got, want) {
			t.Errorf("version output missing %q: %s", want, got)
		}
	}
}

func TestRoot_HelpListsAllSubcommands(t *testing.T) {
	setupCmdTest(t)
	root := newRootCmd()
	out := &bytes.Buffer{}
	root.SetArgs([]string{"--help"})
	root.SetOut(out)
	root.SetErr(out)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute --help: %v", err)
	}
	got := out.String()
	for _, want := range []string{"login", "logout", "accounts", "version", "debug-bundle"} {
		if !strings.Contains(got, want) {
			t.Errorf("help output missing subcommand %q", want)
		}
	}
}

func TestLogout_RequiresAccount(t *testing.T) {
	setupCmdTest(t)
	root := newRootCmd()
	out := &bytes.Buffer{}
	root.SetArgs([]string{"logout"})
	root.SetOut(out)
	root.SetErr(out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error when --account is missing")
	}
	if !strings.Contains(err.Error(), "--account") {
		t.Fatalf("error %q does not mention --account", err)
	}
}

func TestLogout_RejectsInvalidPhone(t *testing.T) {
	setupCmdTest(t)
	root := newRootCmd()
	out := &bytes.Buffer{}
	root.SetArgs([]string{"--account", "not-a-phone", "logout"})
	root.SetOut(out)
	root.SetErr(out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for invalid phone, got nil")
	}
	if !strings.Contains(err.Error(), "invalid phone") {
		t.Fatalf("error %q does not mention invalid phone", err)
	}
}

// TestAccounts_ActiveMarker_NormalisesFlag asserts that --account marks the
// stored canonical phone even when the user passes a non-canonical form
// ("+7 999 ..."). Without normalisation the marker silently disappears.
func TestAccounts_ActiveMarker_NormalisesFlag(t *testing.T) {
	setupCmdTest(t)

	// Pre-populate the DB with a canonical phone the way `login` would.
	paths, err := resolvePathsOnly()
	if err != nil {
		t.Fatalf("resolvePathsOnly: %v", err)
	}
	if mkErr := os.MkdirAll(paths.Data, 0o700); mkErr != nil {
		t.Fatalf("mkdir data: %v", mkErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	repo, err := sqlite.Open(ctx, dbPath(paths))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	if saveErr := repo.SaveAccount(ctx, domain.Account{Phone: "+79991112233"}); saveErr != nil {
		t.Fatalf("save account: %v", saveErr)
	}
	if closeErr := repo.Close(); closeErr != nil {
		t.Fatalf("close repo: %v", closeErr)
	}

	root := newRootCmd()
	out := &bytes.Buffer{}
	root.SetArgs([]string{"--account", "+7 999 111 22 33", "accounts"})
	root.SetOut(out)
	root.SetErr(&bytes.Buffer{})

	if execErr := root.Execute(); execErr != nil {
		t.Fatalf("Execute: %v", execErr)
	}
	got := out.String()
	if !strings.Contains(got, "*") {
		t.Fatalf("active marker missing for non-canonical --account: %q", got)
	}
	if !strings.Contains(got, "+79991112233") {
		t.Fatalf("canonical phone missing in output: %q", got)
	}
}

// TestAccounts_NoDatabase verifies that `lazytg accounts` does not silently
// create the SQLite file on a fresh machine — the command is documented as
// read-only.
func TestAccounts_NoDatabase(t *testing.T) {
	setupCmdTest(t)
	root := newRootCmd()
	out := &bytes.Buffer{}
	root.SetArgs([]string{"accounts"})
	root.SetOut(out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.String(), "no accounts logged in") {
		t.Fatalf("expected friendly empty message, got %q", out.String())
	}

	// Verify the SQLite database was NOT created as a side effect.
	paths, err := resolvePathsOnly()
	if err != nil {
		t.Fatalf("resolvePathsOnly: %v", err)
	}
	if _, statErr := os.Stat(dbPath(paths)); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("accounts unexpectedly created %s (statErr=%v)", dbPath(paths), statErr)
	}
}
