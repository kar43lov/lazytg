package keymap

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"

	"github.com/BurntSushi/toml"
)

// fileFormat mirrors the on-disk keymap.toml structure:
//
//	[bindings]
//	send  = "enter"
//	reply = "ctrl+r"
//
// Unknown keys inside [bindings] are reported as errors so typos don't
// silently fall back to defaults — that was a frequent UX papercut in
// similar projects (mutt, weechat).
type fileFormat struct {
	Bindings map[string]string `toml:"bindings"`
}

// bindingFields enumerates every TOML key the loader recognises and gives
// each one a typed accessor into Keymap. New bindings must be added both
// here and in the Keymap struct.
//
// Help text for overrides is preserved as the canonical key string with no
// description (parseKey decides that). The action label visible in the help
// overlay is set here from the Default() value, so user overrides keep the
// same user-facing description.
func bindingFields(km *Keymap) map[string]*key.Binding {
	return map[string]*key.Binding{
		"send":         &km.Send,
		"newline":      &km.Newline,
		"reply":        &km.Reply,
		"open_editor":  &km.OpenEditor,
		"toggle_help":  &km.ToggleHelp,
		"focus_next":   &km.FocusNext,
		"focus_prev":   &km.FocusPrev,
		"scroll_up":    &km.ScrollUp,
		"scroll_down":  &km.ScrollDown,
		"search":       &km.Search,
		"open_palette": &km.OpenPalette,
		"download":     &km.Download,
		"quit":         &km.Quit,
	}
}

// Load reads a keymap.toml file at path and returns a Keymap merged on top
// of Default().
//
// Behaviour:
//   - empty path or missing file → Default(), no error (fresh installs work)
//   - syntax error in TOML       → error wrapping toml's diagnostic
//   - unknown binding name       → error listing the bad key
//   - parseKey failure           → error pointing at the offending value
//   - resulting conflicts        → error from DetectConflicts
//
// Conflicts are checked AFTER merging; this catches the case where a user
// rebinds one action to a chord already used by another (e.g. send=ctrl+r
// while reply still defaults to ctrl+r).
func Load(path string) (Keymap, error) {
	km := Default()

	if path == "" {
		return km, nil
	}

	// Path is user-supplied configuration (~/.config/lazytg/keymap.toml or
	// the override flag) — gosec G304 is by design here.
	data, err := os.ReadFile(filepath.Clean(path)) //nolint:gosec
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return km, nil
		}
		return Keymap{}, fmt.Errorf("keymap: read %s: %w", path, err)
	}

	var cfg fileFormat
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return Keymap{}, fmt.Errorf("keymap: parse %s: %w", path, err)
	}

	if err := apply(&km, cfg.Bindings); err != nil {
		return Keymap{}, fmt.Errorf("keymap: %s: %w", path, err)
	}

	if conflicts := DetectConflicts(km); len(conflicts) > 0 {
		return Keymap{}, fmt.Errorf("keymap: %s: %w", path, conflictError(conflicts))
	}

	return km, nil
}

// apply merges the parsed [bindings] map into km. It returns an error on the
// first invalid name or unparseable value to keep diagnostics deterministic;
// users iterate quickly when they see one error at a time.
func apply(km *Keymap, raw map[string]string) error {
	if len(raw) == 0 {
		return nil
	}

	fields := bindingFields(km)

	// Sort names for deterministic error reporting.
	names := make([]string, 0, len(raw))
	for n := range raw {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		field, ok := fields[name]
		if !ok {
			return fmt.Errorf("unknown binding %q (allowed: %s)", name, strings.Join(allowedNames(fields), ", "))
		}
		binding, err := parseKey(raw[name])
		if err != nil {
			return fmt.Errorf("binding %q: %w", name, err)
		}
		// Preserve the human-readable description from the existing default
		// while replacing the chord; help overlays render desc next to keys.
		desc := field.Help().Desc
		binding.SetHelp(binding.Keys()[0], desc)
		*field = binding
	}
	return nil
}

func allowedNames(fields map[string]*key.Binding) []string {
	names := make([]string, 0, len(fields))
	for n := range fields {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
