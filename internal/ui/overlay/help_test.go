package overlay

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/pgmac/lazytg/internal/ui/keymap"
)

func TestHelpHiddenByDefault(t *testing.T) {
	t.Parallel()

	h := New(keymap.Default())
	if h.Visible {
		t.Fatalf("Help should start hidden")
	}
	if got := h.View(80, 24); got != "" {
		t.Fatalf("hidden help must render empty; got %q", got)
	}
}

func TestHelpVisibleListsBindings(t *testing.T) {
	t.Parallel()

	h := New(keymap.Default())
	h.Visible = true

	out := h.View(80, 24)
	for _, want := range []string{"send", "enter", "reply", "ctrl+r", "toggle help"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered help should contain %q; got %q", want, out)
		}
	}
}

func TestHelpEscDismisses(t *testing.T) {
	t.Parallel()

	h := New(keymap.Default())
	h.Visible = true

	updated, _ := h.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if updated.Visible {
		t.Fatalf("Esc must dismiss the overlay")
	}
}

func TestHelpQDismisses(t *testing.T) {
	t.Parallel()

	h := New(keymap.Default())
	h.Visible = true

	updated, _ := h.Update(tea.KeyPressMsg{Text: "q", Code: 'q'})
	if updated.Visible {
		t.Fatalf("'q' must dismiss the overlay")
	}
}

func TestHelpToggleKeyDismisses(t *testing.T) {
	t.Parallel()

	h := New(keymap.Default())
	h.Visible = true

	updated, _ := h.Update(tea.KeyPressMsg{Text: "?", Code: '?'})
	if updated.Visible {
		t.Fatalf("'?' must dismiss the overlay when already shown")
	}
}

func TestHelpIgnoresNonDismissKey(t *testing.T) {
	t.Parallel()

	h := New(keymap.Default())
	h.Visible = true

	updated, _ := h.Update(tea.KeyPressMsg{Text: "a", Code: 'a'})
	if !updated.Visible {
		t.Fatalf("Help should remain visible after unrelated key")
	}
}
