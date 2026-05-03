package attach

import (
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func keyChord(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code}
}

// runCmd drains a single tea.Cmd to its tea.Msg. The Cmd is allowed
// to be nil; in that case nil is returned.
func runCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	return cmd()
}

func TestNew_StartsHidden(t *testing.T) {
	t.Parallel()
	m := New(nil)
	if m.Visible {
		t.Fatalf("New should construct hidden overlay")
	}
	if m.PathValue() == "" {
		t.Fatalf("path value should default to home dir, got empty")
	}
}

func TestOpen_PopulatesEntries(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	m := New(nil)
	m.pathInput.SetValue(dir)
	m, openCmd := m.Open(42)
	// Open returns Batch(focusCmd, loadDir). We need to drain the
	// load Cmd so dirLoadedMsg lands on the model. Bubbletea runs Cmds
	// concurrently, but our test runs them inline.
	msg := runCmd(t, openCmd)
	// openCmd is tea.Batch — its return value is a tea.BatchMsg, not
	// a dirLoadedMsg. Bypass the batch by calling loadDir directly:
	_ = msg
	loaded := runCmd(t, m.loadDir())
	m, _ = m.Update(loaded)

	if !m.Visible {
		t.Fatalf("Open should make overlay visible")
	}
	if m.ChatID() != 42 {
		t.Fatalf("ChatID = %d, want 42", m.ChatID())
	}
	entries := m.Entries()
	if len(entries) < 2 {
		t.Fatalf("expected ≥2 entries, got %d", len(entries))
	}
	// Last two should be a.txt + b.txt (sorted alphabetically).
	if entries[len(entries)-2].Name != "a.txt" || entries[len(entries)-1].Name != "b.txt" {
		t.Fatalf("entries order unexpected: %+v", entries)
	}
}

func TestEnter_OnFile_EmitsSubmit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	m := New(nil)
	m.pathInput.SetValue(dir)
	m, _ = m.Open(7)
	loaded := runCmd(t, m.loadDir())
	m, _ = m.Update(loaded)
	// Move cursor to the regular file (skipping ".." and any directories).
	target := -1
	for i, e := range m.Entries() {
		if !e.IsDir && e.Name == "hello.txt" {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatalf("hello.txt not in entries: %+v", m.Entries())
	}
	for m.Cursor() < target {
		m, _ = m.Update(keyChord(tea.KeyDown))
	}

	updated, cmd := m.Update(keyChord(tea.KeyEnter))
	if updated.Visible {
		t.Fatalf("submit should close overlay")
	}
	msg := runCmd(t, cmd)
	submit, ok := msg.(SubmitMsg)
	if !ok {
		t.Fatalf("cmd msg = %T, want SubmitMsg", msg)
	}
	if submit.ChatID != 7 || submit.Path != path {
		t.Fatalf("submit = %+v", submit)
	}
}

func TestEnter_OnDirectory_Descends(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	leaf := filepath.Join(sub, "leaf.txt")
	if err := os.WriteFile(leaf, []byte("y"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	m := New(nil)
	m.pathInput.SetValue(dir)
	m, _ = m.Open(1)
	loaded := runCmd(t, m.loadDir())
	m, _ = m.Update(loaded)

	// Find the "sub" entry index and move to it.
	target := -1
	for i, e := range m.Entries() {
		if e.IsDir && e.Name == "sub" {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatalf("sub directory not in entries: %+v", m.Entries())
	}
	for m.Cursor() < target {
		m, _ = m.Update(keyChord(tea.KeyDown))
	}

	// Enter on directory descends.
	updated, cmd := m.Update(keyChord(tea.KeyEnter))
	if !updated.Visible {
		t.Fatalf("descend should keep overlay visible")
	}
	loaded2 := runCmd(t, cmd)
	updated, _ = updated.Update(loaded2)

	// Listing should now include leaf.txt.
	found := false
	for _, e := range updated.Entries() {
		if e.Name == "leaf.txt" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("descend did not load sub: %+v", updated.Entries())
	}
}

func TestEsc_ClosesAndEmitsClosedMsg(t *testing.T) {
	t.Parallel()
	m := New(nil)
	m.Visible = true
	updated, cmd := m.Update(keyChord(tea.KeyEscape))
	if updated.Visible {
		t.Fatalf("Esc should close overlay")
	}
	msg := runCmd(t, cmd)
	if _, ok := msg.(ClosedMsg); !ok {
		t.Fatalf("cmd msg = %T, want ClosedMsg", msg)
	}
}

func TestUpdate_HiddenIgnoresKeys(t *testing.T) {
	t.Parallel()
	m := New(nil)
	updated, cmd := m.Update(keyChord(tea.KeyEnter))
	if updated.Visible {
		t.Fatalf("hidden overlay should stay hidden")
	}
	if cmd != nil {
		t.Fatalf("hidden overlay should produce no cmd, got %T", cmd)
	}
}

func TestUpdate_TabTogglesMode(t *testing.T) {
	t.Parallel()
	m := New(nil)
	m.Visible = true
	if m.CurrentMode() != ModePath {
		t.Fatalf("initial mode should be ModePath")
	}
	updated, _ := m.Update(keyChord(tea.KeyTab))
	if updated.CurrentMode() != ModeCaption {
		t.Fatalf("Tab should switch to ModeCaption")
	}
	updated, _ = updated.Update(keyChord(tea.KeyTab))
	if updated.CurrentMode() != ModePath {
		t.Fatalf("second Tab should switch back to ModePath")
	}
}

func TestLoadDir_ReportsErrorForBadPath(t *testing.T) {
	t.Parallel()
	m := New(nil)
	m.pathInput.SetValue("/no/such/path/here")
	msg := runCmd(t, m.loadDir())
	updated, _ := m.Update(msg)
	if updated.LoadErr() == nil {
		t.Fatalf("expected LoadErr to be set")
	}
}

func TestEnter_OnEmptyChatID_NoOps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(path, []byte("x"), 0o600)

	m := New(nil)
	m.pathInput.SetValue(dir)
	m, _ = m.Open(0)
	loaded := runCmd(t, m.loadDir())
	m, _ = m.Update(loaded)
	target := -1
	for i, e := range m.Entries() {
		if !e.IsDir {
			target = i
			break
		}
	}
	for m.Cursor() < target {
		m, _ = m.Update(keyChord(tea.KeyDown))
	}
	_, cmd := m.Update(keyChord(tea.KeyEnter))
	// With chatID=0 the submit path returns the model with cmd=nil.
	if cmd != nil {
		msg := runCmd(t, cmd)
		if _, ok := msg.(SubmitMsg); ok {
			t.Fatalf("submit should not fire when chatID=0")
		}
	}
}
