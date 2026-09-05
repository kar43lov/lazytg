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
		if sharedByDesign(owners) {
			continue
		}
		reports = append(reports, ConflictReport{Key: k, Bindings: owners})
	}
	sort.Slice(reports, func(i, j int) bool {
		return reports[i].Key < reports[j].Key
	})
	return reports
}

// coexisting names pairs of actions allowed to answer to the same key
// because the context tells them apart.
//
// There is exactly one such pair, and admitting it is better than the
// alternatives. Tab means "finish what I am typing" in every shell and
// editor, and it means "next pane" in every TUI; both are right, and which
// one applies is decided by whether there is a `:shortcode` under the cursor.
// Giving the completion its own key would make the feature unusable — nobody
// will learn a second key for the gesture they already know — and dropping
// the conflict check entirely would let a user shadow Send with Reply and
// find out at runtime.
var coexisting = [][2]string{
	{"complete_emoji", "focus_next"},
	// "p" pins a chat in the list and follows a reply in the thread. The
	// two panes never hold the focus at once.
	{"jump_to_reply", "pin_chat"},
}

func sharedByDesign(owners []string) bool {
	if len(owners) != 2 {
		return false
	}
	for _, pair := range coexisting {
		if owners[0] == pair[0] && owners[1] == pair[1] {
			return true
		}
	}
	return false
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
