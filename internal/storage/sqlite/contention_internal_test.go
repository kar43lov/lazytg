package sqlite

import (
	"errors"
	"fmt"
	"testing"
)

// codeErr is a stand-in for the driver's error type: the classifier matches on
// the Code() method rather than a concrete type, so a fake with that method is
// exactly what production code sees.
type codeErr struct {
	code int
}

func (e codeErr) Error() string { return fmt.Sprintf("sqlite error %d", e.code) }
func (e codeErr) Code() int     { return e.code }

// TestIsContention_ClassifiesResultCodes pins the code table.
//
// The end-to-end probe tests can only produce contention and connection-level
// failures: BEGIN IMMEDIATE cannot be made to return SQLITE_READONLY or
// SQLITE_FULL on demand. This test covers the other half — that no genuine
// failure code is mistaken for contention, which is what stops ProbeWrite's
// tolerance from silently disabling degradation detection.
//
// Values come from modernc.org/sqlite/lib (sqlite3.h): extended codes carry
// the primary code in the low byte, and the driver enables extended codes on
// every connection, so both forms reach the classifier in practice.
func TestIsContention_ClassifiesResultCodes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		code int
		want bool
	}{
		{"SQLITE_BUSY", 5, true},
		{"SQLITE_LOCKED", 6, true},
		{"SQLITE_BUSY_RECOVERY", 261, true},
		{"SQLITE_BUSY_SNAPSHOT", 517, true},
		{"SQLITE_BUSY_TIMEOUT", 773, true},
		{"SQLITE_LOCKED_SHAREDCACHE", 262, true},
		{"SQLITE_LOCKED_VTAB", 518, true},
		{"SQLITE_PERM", 3, false},
		{"SQLITE_READONLY", 8, false},
		{"SQLITE_READONLY_DIRECTORY", 1544, false},
		{"SQLITE_IOERR", 10, false},
		{"SQLITE_IOERR_WRITE", 778, false},
		{"SQLITE_CORRUPT", 11, false},
		{"SQLITE_FULL", 13, false},
		{"SQLITE_CANTOPEN", 14, false},
		{"SQLITE_NOTADB", 26, false},
		{"SQLITE_OK", 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isContention(codeErr{code: tc.code}); got != tc.want {
				t.Errorf("isContention(code %d) = %v, want %v", tc.code, got, tc.want)
			}
			// Wrapped errors must classify identically — the probe wraps the
			// driver error before it reaches any caller.
			wrapped := fmt.Errorf("probe begin immediate: %w", codeErr{code: tc.code})
			if got := isContention(wrapped); got != tc.want {
				t.Errorf("isContention(wrapped code %d) = %v, want %v", tc.code, got, tc.want)
			}
		})
	}
}

// TestIsContention_NonSQLiteErrors covers the inputs that carry no result code
// at all: a plain error, a nil error, and the sentinel this package returns
// when the soft read-only flag is set. None of them may read as contention.
func TestIsContention_NonSQLiteErrors(t *testing.T) {
	t.Parallel()

	for _, err := range []error{nil, errors.New("boom"), ErrReadOnly} {
		if isContention(err) {
			t.Errorf("isContention(%v) = true, want false", err)
		}
	}
}
