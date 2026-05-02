package keymap

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTOML(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "keymap.toml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write keymap.toml: %v", err)
	}
	return path
}

func TestLoad_EmptyPath_ReturnsDefaults(t *testing.T) {
	t.Parallel()
	got, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\") error = %v", err)
	}
	want := Default()
	if got.Send.Keys()[0] != want.Send.Keys()[0] {
		t.Errorf("send key = %q, want %q", got.Send.Keys()[0], want.Send.Keys()[0])
	}
}

func TestLoad_MissingFile_ReturnsDefaults(t *testing.T) {
	t.Parallel()
	got, err := Load(filepath.Join(t.TempDir(), "does-not-exist.toml"))
	if err != nil {
		t.Fatalf("Load(missing) error = %v", err)
	}
	if got.Send.Keys()[0] != "enter" {
		t.Errorf("send key = %q, want %q", got.Send.Keys()[0], "enter")
	}
}

func TestLoad_EmptyTOML_ReturnsDefaults(t *testing.T) {
	t.Parallel()
	path := writeTOML(t, "")
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load(empty) error = %v", err)
	}
	if got.Reply.Keys()[0] != "ctrl+r" {
		t.Errorf("reply default lost: got %q", got.Reply.Keys()[0])
	}
}

func TestLoad_OverrideSend(t *testing.T) {
	t.Parallel()
	// Override send to ctrl+s and avoid colliding with any existing default;
	// other bindings should remain at their defaults.
	path := writeTOML(t, `
[bindings]
send = "ctrl+s"
`)
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Send.Keys()[0] != "ctrl+s" {
		t.Errorf("send = %q, want ctrl+s", got.Send.Keys()[0])
	}
	if got.Reply.Keys()[0] != "ctrl+r" {
		t.Errorf("reply changed unexpectedly: %q", got.Reply.Keys()[0])
	}
	if got.ToggleHelp.Keys()[0] != "?" {
		t.Errorf("toggle_help changed unexpectedly: %q", got.ToggleHelp.Keys()[0])
	}
	// Help text from defaults should survive overrides.
	if got.Send.Help().Desc != "send" {
		t.Errorf("send help desc lost: %q", got.Send.Help().Desc)
	}
}

func TestLoad_ConflictBetweenSendAndReply(t *testing.T) {
	t.Parallel()
	path := writeTOML(t, `
[bindings]
send  = "ctrl+r"
reply = "ctrl+r"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load: expected conflict error, got nil")
	}
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("Load error = %T (%v), want *ConflictError wrapped", err, err)
	}
	if len(ce.Reports) != 1 {
		t.Fatalf("got %d reports, want 1: %#v", len(ce.Reports), ce.Reports)
	}
	r := ce.Reports[0]
	if r.Key != "ctrl+r" {
		t.Errorf("conflict key = %q, want ctrl+r", r.Key)
	}
	// Both action names must appear in the report so the user can fix the
	// right pair.
	msg := err.Error()
	if !strings.Contains(msg, "send") || !strings.Contains(msg, "reply") {
		t.Errorf("error message %q must mention both send and reply", msg)
	}
}

func TestLoad_ConflictAgainstDefault(t *testing.T) {
	t.Parallel()
	// User overrides only send, but the new chord clashes with the default
	// reply binding. Load must catch this — silently overriding would mean
	// reply stops working without any feedback.
	path := writeTOML(t, `
[bindings]
send = "ctrl+r"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load: expected conflict against default reply, got nil")
	}
	if !strings.Contains(err.Error(), "ctrl+r") {
		t.Errorf("error = %q, want to mention ctrl+r", err.Error())
	}
}

func TestLoad_InvalidValue(t *testing.T) {
	t.Parallel()
	path := writeTOML(t, `
[bindings]
send = "frobnicate"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load: expected parse error, got nil")
	}
	if !strings.Contains(err.Error(), "frobnicate") {
		t.Errorf("error = %q, want it to mention the bad value", err.Error())
	}
	if !strings.Contains(err.Error(), "send") {
		t.Errorf("error = %q, want it to mention the binding name", err.Error())
	}
}

func TestLoad_UnknownBindingName(t *testing.T) {
	t.Parallel()
	path := writeTOML(t, `
[bindings]
explode = "ctrl+x"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load: expected unknown-binding error, got nil")
	}
	if !strings.Contains(err.Error(), "explode") {
		t.Errorf("error = %q, want it to mention the bad name", err.Error())
	}
}

func TestLoad_MalformedTOML(t *testing.T) {
	t.Parallel()
	path := writeTOML(t, "this is not valid toml = =")
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load: expected parse error on malformed TOML")
	}
}

func TestDetectConflicts_Default_None(t *testing.T) {
	t.Parallel()
	if c := DetectConflicts(Default()); len(c) != 0 {
		t.Errorf("default keymap has conflicts: %v", c)
	}
}

func TestConflictReport_String(t *testing.T) {
	t.Parallel()
	r := ConflictReport{Key: "ctrl+r", Bindings: []string{"reply", "send"}}
	got := r.String()
	if got != "ctrl+r: reply and send" {
		t.Errorf("String() = %q, want %q", got, "ctrl+r: reply and send")
	}
}
