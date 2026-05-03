package security

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCheckAtStartup_TightFile verifies that a regular file at exactly
// the expected permission bits produces no issues. This is the boot-up
// "everything is fine" baseline — if it ever starts emitting findings
// we know a regression slipped into the perm-comparison logic.
func TestCheckAtStartup_TightFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission checks are POSIX-specific")
	}
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.age")
	if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// WriteFile honours umask so explicit chmod guarantees the bits
	// regardless of CI environment.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	issues := CheckAtStartup([]PathCheck{{
		Path:         path,
		Type:         KindFile,
		ExpectedMode: 0o600,
		Severity:     SeverityFail,
	}})
	if len(issues) != 0 {
		t.Fatalf("expected no issues, got %+v", issues)
	}
}

// TestCheckAtStartup_LooseFileEmitsFail covers the headline boot guard:
// secrets.age (or any fail-class check) at 0644 must surface as a
// SeverityFail issue so EnforceFatal can refuse to start.
func TestCheckAtStartup_LooseFileEmitsFail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission checks are POSIX-specific")
	}
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.age")
	if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	issues := CheckAtStartup([]PathCheck{{
		Path:         path,
		Type:         KindFile,
		ExpectedMode: 0o600,
		Severity:     SeverityFail,
	}})
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %+v", len(issues), issues)
	}
	got := issues[0]
	if got.Category != CategoryPermissions {
		t.Fatalf("category: want %q, got %q", CategoryPermissions, got.Category)
	}
	if got.Severity != SeverityFail {
		t.Fatalf("severity: want %q, got %q", SeverityFail, got.Severity)
	}
	if got.Actual != 0o644 || got.Expected != 0o600 {
		t.Fatalf("expected/actual mismatch: actual=%#o expected=%#o", got.Actual, got.Expected)
	}
	if err := EnforceFatal(issues); err == nil {
		t.Fatalf("EnforceFatal must reject fail-class issues")
	}
}

// TestCheckAtStartup_LooseDirEmitsWarn covers parent-directory mode
// drift (XDG dirs are commonly umask 0755). Severity stays warn so
// EnforceFatal returns nil and the boot proceeds with a logged
// warning instead of an abort.
func TestCheckAtStartup_LooseDirEmitsWarn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission checks are POSIX-specific")
	}
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "lazytg")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	issues := CheckAtStartup([]PathCheck{{
		Path:         target,
		Type:         KindDir,
		ExpectedMode: 0o700,
		Severity:     SeverityWarn,
	}})
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %+v", len(issues), issues)
	}
	got := issues[0]
	if got.Category != CategoryPermissions {
		t.Fatalf("category: want %q, got %q", CategoryPermissions, got.Category)
	}
	if got.Severity != SeverityWarn {
		t.Fatalf("severity: want %q, got %q", SeverityWarn, got.Severity)
	}
	if err := EnforceFatal(issues); err != nil {
		t.Fatalf("EnforceFatal must not promote warn-class issues, got %v", err)
	}
}

// TestCheckAtStartup_MissingPathReturnsMissing asserts that a brand-new
// install (no secrets.age yet, no lazytg.db yet) does not turn into a
// boot failure. The check still surfaces an info-level issue so the
// wiring layer can log "first run, will create" if it wants — that
// observability hook is more useful than silent acceptance.
func TestCheckAtStartup_MissingPathReturnsMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	missing := filepath.Join(dir, "no-such-file.age")

	issues := CheckAtStartup([]PathCheck{{
		Path:         missing,
		Type:         KindFile,
		ExpectedMode: 0o600,
		Severity:     SeverityFail, // declared severity does not matter for missing
	}})
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %+v", len(issues), issues)
	}
	got := issues[0]
	if got.Category != CategoryMissing {
		t.Fatalf("category: want %q, got %q", CategoryMissing, got.Category)
	}
	if got.Severity != SeverityWarn {
		t.Fatalf("missing must always be warn (first-run friendly), got %q", got.Severity)
	}
	if err := EnforceFatal(issues); err != nil {
		t.Fatalf("EnforceFatal must not block on missing paths, got %v", err)
	}
}

// TestCheckAtStartup_EmptyPathSkipped guards the wiring contract: an
// XDG dir that has not been resolved yet (zero string) is skipped
// silently so callers do not have to filter their inputs.
func TestCheckAtStartup_EmptyPathSkipped(t *testing.T) {
	t.Parallel()

	issues := CheckAtStartup([]PathCheck{
		{Path: "", Type: KindFile, ExpectedMode: 0o600, Severity: SeverityFail},
		{Path: "", Type: KindDir, ExpectedMode: 0o700, Severity: SeverityWarn},
	})
	if len(issues) != 0 {
		t.Fatalf("expected empty-path checks to be skipped, got %+v", issues)
	}
}

// TestCheckAtStartup_TypeMismatch covers the case where a directory
// shows up where a file was expected (or vice versa). Severity is
// inherited from the declared check — these are usually fail-class.
func TestCheckAtStartup_TypeMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission checks are POSIX-specific")
	}
	t.Parallel()

	dir := t.TempDir()
	dirAsFile := filepath.Join(dir, "is-actually-a-dir")
	if err := os.MkdirAll(dirAsFile, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	issues := CheckAtStartup([]PathCheck{{
		Path:         dirAsFile,
		Type:         KindFile,
		ExpectedMode: 0o600,
		Severity:     SeverityFail,
	}})
	if len(issues) != 1 {
		t.Fatalf("expected type-mismatch issue, got %+v", issues)
	}
	if issues[0].Category != CategoryWrongType {
		t.Fatalf("category: want %q, got %q", CategoryWrongType, issues[0].Category)
	}
}

// TestCheckAtStartup_StatErrorIsFatal feeds a synthetic stat error
// (anything other than ErrNotExist) and confirms it bubbles up as a
// CategoryStatError + SeverityFail finding. We use the test seam so
// we don't have to fabricate a permission-denied filesystem on CI.
func TestCheckAtStartup_StatErrorIsFatal(t *testing.T) {
	t.Parallel()

	bad := errors.New("synthetic IO failure")
	stat := func(string) (os.FileInfo, error) { return nil, bad }
	issues := checkWithStat([]PathCheck{{
		Path:         "/whatever",
		Type:         KindFile,
		ExpectedMode: 0o600,
		Severity:     SeverityWarn, // promoted to fail by category
	}}, stat)
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %+v", issues)
	}
	if issues[0].Category != CategoryStatError {
		t.Fatalf("category: want %q, got %q", CategoryStatError, issues[0].Category)
	}
	if issues[0].Severity != SeverityFail {
		t.Fatalf("severity: want fail (stat errors are critical), got %q", issues[0].Severity)
	}
	if !errors.Is(issues[0].Cause, bad) {
		t.Fatalf("Cause should wrap the underlying error, got %v", issues[0].Cause)
	}
	if err := EnforceFatal(issues); err == nil {
		t.Fatalf("stat error must be fatal")
	}
}

// TestEnforceFatal_AggregatesAllFails verifies the multi-issue path:
// every fail-class finding ends up in the resulting error string so a
// single boot logs every problem instead of forcing chmod-restart-rinse-repeat.
func TestEnforceFatal_AggregatesAllFails(t *testing.T) {
	t.Parallel()

	issues := []SecurityIssue{
		{Path: "/b/secrets.age", Category: CategoryPermissions, Severity: SeverityFail, Message: "/b/secrets.age: insecure"},
		{Path: "/a/lazytg.db", Category: CategoryPermissions, Severity: SeverityFail, Message: "/a/lazytg.db: insecure"},
		{Path: "/c/cfg", Category: CategoryPermissions, Severity: SeverityWarn, Message: "/c/cfg: relaxed"},
	}
	err := EnforceFatal(issues)
	if err == nil {
		t.Fatalf("expected aggregate error")
	}
	msg := err.Error()
	for _, want := range []string{"/a/lazytg.db", "/b/secrets.age"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error message %q missing path %q", msg, want)
		}
	}
	if strings.Contains(msg, "/c/cfg") {
		t.Fatalf("warn-class issue must not appear in fatal message, got %q", msg)
	}
}

// TestFilterBySeverity slices warn vs fail from the mixed bag the
// wiring layer feeds into the logger.
func TestFilterBySeverity(t *testing.T) {
	t.Parallel()

	all := []SecurityIssue{
		{Severity: SeverityFail, Path: "a"},
		{Severity: SeverityWarn, Path: "b"},
		{Severity: SeverityWarn, Path: "c"},
	}
	if got := FilterBySeverity(all, SeverityFail); len(got) != 1 || got[0].Path != "a" {
		t.Fatalf("FilterBySeverity(fail) = %+v", got)
	}
	if got := FilterBySeverity(all, SeverityWarn); len(got) != 2 {
		t.Fatalf("FilterBySeverity(warn) = %+v", got)
	}
}

// TestCheckAtStartup_NotExistFromCustomFS rounds out coverage: the
// missing-path branch must trigger when the injected stat returns
// fs.ErrNotExist regardless of the underlying type.
func TestCheckAtStartup_NotExistFromCustomFS(t *testing.T) {
	t.Parallel()

	stat := func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist }
	issues := checkWithStat([]PathCheck{{
		Path:         "/nope",
		Type:         KindFile,
		ExpectedMode: 0o600,
		Severity:     SeverityFail,
	}}, stat)
	if len(issues) != 1 || issues[0].Category != CategoryMissing {
		t.Fatalf("expected missing, got %+v", issues)
	}
}
