package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// resetFlags clears package-level persistent flag state so tests can be run
// in any order. cobra reuses the underlying pflag.FlagSet between calls when
// the variables are package-level — we own the variables, so we reset them.
func resetFlags() {
	flagAccount = ""
	flagDebug = false
	flagLogLevel = "info"
}

func TestRoot_PersistentFlagsRegistered(t *testing.T) {
	resetFlags()
	root := newRootCmd()

	for _, name := range []string{"account", "debug", "log-level"} {
		if root.PersistentFlags().Lookup(name) == nil {
			t.Errorf("persistent flag %q is not registered", name)
		}
	}
}

func TestRoot_ParsesAccountFlag(t *testing.T) {
	resetFlags()
	root := newRootCmd()
	root.SetArgs([]string{"--account", "+79991112233", "debug-bundle"})
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
	resetFlags()
	root := newRootCmd()
	root.SetArgs([]string{"--log-level", "loud", "debug-bundle"})
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

func TestDebugBundle_StubPrintsMarker(t *testing.T) {
	resetFlags()
	root := newRootCmd()
	out := &bytes.Buffer{}
	root.SetArgs([]string{"debug-bundle"})
	root.SetOut(out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.String(), "debug-bundle stub") {
		t.Fatalf("expected stub marker in stdout, got %q", out.String())
	}
}

func TestVersion_PrintsBuildInfo(t *testing.T) {
	resetFlags()
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
	resetFlags()
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
	resetFlags()
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
