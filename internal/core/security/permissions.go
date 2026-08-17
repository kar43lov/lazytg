package security

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
)

// PathKind discriminates between regular files and directories. The
// distinction matters because the expected mode bits differ (0600 vs
// 0700) and because a directory check that ends up pointing at a file
// (or vice versa) is itself a finding worth surfacing — accidental
// type swaps usually mean some other process clobbered the path.
type PathKind string

const (
	// KindFile expects the path to be a regular file with the matching
	// permission bits.
	KindFile PathKind = "file"
	// KindDir expects the path to be a directory with the matching
	// permission bits.
	KindDir PathKind = "dir"
)

// Severity controls how EnforceFatal reacts to an issue. Fail-class
// findings on critical paths (session, secrets, database) abort
// startup; warn-class findings on parents (config dir, log dir) only
// log so a user that intentionally relaxed parent permissions can
// still launch the app.
type Severity string

const (
	// SeverityFail flags an issue that EnforceFatal turns into a hard
	// error. Reserved for files holding session blobs, the SQLite
	// database, or anything else that would compromise the account if
	// readable to other local users.
	SeverityFail Severity = "fail"
	// SeverityWarn flags an issue worth logging but not aborting on.
	// Used for parent directories where a relaxed mode is annoying but
	// not directly account-compromising.
	SeverityWarn Severity = "warn"
)

// IssueCategory describes why a PathCheck failed. We keep "missing"
// distinct from "permissions wrong" so first-run startups (the file
// will not exist yet) do not look identical to a tampered file with a
// surprising mode.
type IssueCategory string

const (
	// CategoryPermissions means the path exists with unexpected bits.
	CategoryPermissions IssueCategory = "permissions"
	// CategoryWrongType means a file was expected but found a directory
	// or vice versa.
	CategoryWrongType IssueCategory = "wrong_type"
	// CategoryMissing means the path does not exist. Always emitted as
	// "info" — first-run startups will trip this and we do not want to
	// fail them.
	CategoryMissing IssueCategory = "missing"
	// CategoryStatError means stat() returned an error other than
	// ErrNotExist. EnforceFatal treats this as fail to surface
	// permission denials and broken filesystems early.
	CategoryStatError IssueCategory = "stat_error"
)

// PathCheck describes a single audit target. Path may be empty (when
// the wiring layer has not resolved an XDG dir yet, for example) — in
// that case the check is silently skipped, mirroring the behaviour of
// downstream callers that already tolerate "no such path".
type PathCheck struct {
	Path         string
	Type         PathKind
	ExpectedMode os.FileMode
	Severity     Severity
}

// SecurityIssue describes a single audit finding. Cause is non-nil only
// for CategoryStatError — the rest are constructed from os.Stat metadata.
//
//revive:disable-next-line:exported // stutter is acceptable here; "Issue" is too generic in this package
type SecurityIssue struct {
	Path     string
	Category IssueCategory
	Severity Severity
	Expected os.FileMode
	Actual   os.FileMode
	Message  string
	Cause    error
}

// statFunc is the test seam for permissions checks. Production wiring
// uses os.Stat; the test file injects an in-memory map so we can assert
// behaviour without temp directories on every assertion.
type statFunc func(path string) (os.FileInfo, error)

// CheckAtStartup runs every PathCheck and returns the resulting issues
// in input order. Empty Path entries are skipped silently. The
// production caller (internal/app/wire.Build) feeds the result to
// EnforceFatal so fail-class findings short-circuit startup and
// warn-class findings get logged.
func CheckAtStartup(checks []PathCheck) []SecurityIssue {
	return checkWithStat(checks, os.Stat)
}

// checkWithStat is the test-injectable workhorse behind CheckAtStartup.
// Kept package-private — callers that want to swap stat() should
// declare their own fixtures and call this directly from a _test.go
// file in the same package.
func checkWithStat(checks []PathCheck, stat statFunc) []SecurityIssue {
	issues := make([]SecurityIssue, 0, len(checks))
	for _, c := range checks {
		if c.Path == "" {
			continue
		}
		issue, ok := evaluateOne(c, stat)
		if ok {
			issues = append(issues, issue)
		}
	}
	return issues
}

// evaluateOne runs a single PathCheck. The boolean return is true when
// an issue was produced; false means the check passed cleanly.
func evaluateOne(c PathCheck, stat statFunc) (SecurityIssue, bool) {
	info, err := stat(c.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return SecurityIssue{
				Path:     c.Path,
				Category: CategoryMissing,
				Severity: SeverityWarn,
				Expected: c.ExpectedMode,
				Message:  fmt.Sprintf("%s does not exist (will be created on first use)", c.Path),
			}, true
		}
		return SecurityIssue{
			Path:     c.Path,
			Category: CategoryStatError,
			Severity: SeverityFail,
			Expected: c.ExpectedMode,
			Message:  fmt.Sprintf("stat %s: %v", c.Path, err),
			Cause:    err,
		}, true
	}

	if c.Type == KindFile && !info.Mode().IsRegular() {
		return SecurityIssue{
			Path:     c.Path,
			Category: CategoryWrongType,
			Severity: c.Severity,
			Expected: c.ExpectedMode,
			Actual:   info.Mode(),
			Message:  fmt.Sprintf("%s: expected regular file, got %s", c.Path, info.Mode().String()),
		}, true
	}
	if c.Type == KindDir && !info.IsDir() {
		return SecurityIssue{
			Path:     c.Path,
			Category: CategoryWrongType,
			Severity: c.Severity,
			Expected: c.ExpectedMode,
			Actual:   info.Mode(),
			Message:  fmt.Sprintf("%s: expected directory, got %s", c.Path, info.Mode().String()),
		}, true
	}

	actual := info.Mode().Perm()
	if actual != c.ExpectedMode {
		return SecurityIssue{
			Path:     c.Path,
			Category: CategoryPermissions,
			Severity: c.Severity,
			Expected: c.ExpectedMode,
			Actual:   actual,
			Message:  fmt.Sprintf("%s: insecure permissions %#o (want %#o)", c.Path, actual, c.ExpectedMode),
		}, true
	}
	return SecurityIssue{}, false
}

// EnforceFatal returns a non-nil error iff at least one issue has
// Severity == SeverityFail OR Category == CategoryStatError. The error
// message lists every offending path so a single boot can surface
// every problem at once instead of one-by-one.
//
// Missing paths (CategoryMissing) never fail — a fresh install legally
// has no secrets.age yet, no lazytg.db yet, etc. The wiring layer is
// expected to log warn-class issues separately.
func EnforceFatal(issues []SecurityIssue) error {
	fatals := make([]SecurityIssue, 0, len(issues))
	for _, issue := range issues {
		if issue.Severity == SeverityFail {
			fatals = append(fatals, issue)
		}
	}
	if len(fatals) == 0 {
		return nil
	}
	sort.SliceStable(fatals, func(i, j int) bool { return fatals[i].Path < fatals[j].Path })
	parts := make([]string, len(fatals))
	for i, issue := range fatals {
		parts[i] = issue.Message
	}
	return fmt.Errorf("security: refusing to start (%d issue(s)): %s", len(fatals), strings.Join(parts, "; "))
}

// FilterBySeverity returns the subset of issues matching s. Used by
// the wiring layer to log warn-class findings without breaking
// startup. Returned slice may be empty but never nil so callers can
// range over it without a nil-check.
func FilterBySeverity(issues []SecurityIssue, s Severity) []SecurityIssue {
	out := make([]SecurityIssue, 0, len(issues))
	for _, issue := range issues {
		if issue.Severity == s {
			out = append(out, issue)
		}
	}
	return out
}
