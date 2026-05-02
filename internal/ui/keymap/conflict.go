package keymap

import (
	"fmt"
	"sort"
	"strings"
)

// ConflictReport describes a single chord that's bound to two or more
// actions in the merged keymap.
//
// Bindings is sorted alphabetically by action name to keep error messages
// deterministic across runs (map iteration order in Go is randomised).
type ConflictReport struct {
	Key      string
	Bindings []string
}

// String returns a human-readable description like
// "ctrl+r: reply and send" suitable for surfacing to the user via the CLI
// or status bar.
func (c ConflictReport) String() string {
	return fmt.Sprintf("%s: %s", c.Key, strings.Join(c.Bindings, " and "))
}

// DetectConflicts finds chords assigned to more than one action in km.
//
// We compare against the merged map (post-Load) rather than the user's TOML
// alone — that's the only way to catch the common case where someone
// rebinds action A to a chord that another action still holds by default.
//
// The result is ordered by chord string for stable test snapshots.
func DetectConflicts(km Keymap) []ConflictReport {
	fields := bindingFields(&km)

	keyOwners := map[string][]string{}
	for name, field := range fields {
		for _, k := range field.Keys() {
			keyOwners[k] = append(keyOwners[k], name)
		}
	}

	var reports []ConflictReport
	for k, owners := range keyOwners {
		if len(owners) < 2 {
			continue
		}
		sort.Strings(owners)
		reports = append(reports, ConflictReport{Key: k, Bindings: owners})
	}
	sort.Slice(reports, func(i, j int) bool {
		return reports[i].Key < reports[j].Key
	})
	return reports
}

// conflictError wraps a slice of conflicts so it can be returned as a single
// error from Load. The Error() output lists every conflict on its own line
// for easy reading in terminals.
func conflictError(reports []ConflictReport) error {
	parts := make([]string, len(reports))
	for i, r := range reports {
		parts[i] = r.String()
	}
	return &ConflictError{Reports: reports, msg: "conflicting bindings: " + strings.Join(parts, "; ")}
}

// ConflictError is the error type returned by Load when conflicts are
// detected. It implements error and exposes the structured Reports slice
// for callers that want to render conflicts themselves.
type ConflictError struct {
	Reports []ConflictReport
	msg     string
}

func (e *ConflictError) Error() string { return e.msg }
