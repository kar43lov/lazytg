package thread

import (
	"testing"
)

// TestScrollTo_PositionsViewportAroundTarget verifies that calling
// ScrollTo on a loaded thread shifts the viewport so the target row
// is visible with around=N rows of context above the hit.
func TestScrollTo_PositionsViewportAroundTarget(t *testing.T) {
	t.Parallel()

	repo := newFakeRepo(100)
	m := sized(NewWithRepo(repo, nil, nil))
	m, cmd := m.OpenChat(1)
	loaded := runCmd(t, cmd).(messagesLoadedMsg)
	m, _ = m.Update(loaded)

	// applyLoaded ran GotoBottom — confirm the precondition holds so
	// the assertion below ("ScrollTo moved away from bottom") is
	// meaningful.
	bottomOffset := m.YOffset()
	if bottomOffset == 0 {
		t.Fatalf("precondition: expected non-zero YOffset at bottom, got 0")
	}

	// loaded gives us ids 301..500 (the newest 200 of 500 — but we
	// constructed the repo with 100 messages so all 100 land here).
	// Pick a target near the middle.
	msgs := m.Messages()
	if len(msgs) < 60 {
		t.Fatalf("expected at least 60 visible messages, got %d", len(msgs))
	}
	target := msgs[50]

	scrolled := m.ScrollTo(target.ID, 5)
	if scrolled.YOffset() == bottomOffset {
		t.Fatalf("ScrollTo should move the viewport away from the bottom; both offsets = %d", bottomOffset)
	}
	// The target's row should sit at or below YOffset (i.e. visible)
	// with around=5 rows above. Asserting the exact offset is brittle
	// because it depends on countRenderedLines's wrap math, so instead
	// we verify the offset is in the right *region*: less than the
	// bottom (we scrolled up) and greater than 0 (we are not at the
	// top).
	if got := scrolled.YOffset(); got >= bottomOffset {
		t.Fatalf("ScrollTo offset must be less than bottom; got %d, bottom %d", got, bottomOffset)
	}
	if got := scrolled.YOffset(); got <= 0 {
		t.Fatalf("ScrollTo to a middle target must not land at the top; got %d", got)
	}
}

// TestScrollTo_UnknownIDIsNoop confirms the model is unchanged when
// the target message is not in m.messages.
func TestScrollTo_UnknownIDIsNoop(t *testing.T) {
	t.Parallel()

	repo := newFakeRepo(50)
	m := sized(NewWithRepo(repo, nil, nil))
	m, cmd := m.OpenChat(1)
	loaded := runCmd(t, cmd).(messagesLoadedMsg)
	m, _ = m.Update(loaded)

	before := m.YOffset()
	scrolled := m.ScrollTo(999_999, 5)
	if scrolled.YOffset() != before {
		t.Fatalf("ScrollTo on unknown id must not move viewport; before=%d after=%d", before, scrolled.YOffset())
	}
}

// TestScrollTo_TargetAtStartClampsToZero verifies that a ScrollTo to
// the very first message (with around > 0) lands at YOffset 0 instead
// of going negative.
func TestScrollTo_TargetAtStartClampsToZero(t *testing.T) {
	t.Parallel()

	repo := newFakeRepo(50)
	m := sized(NewWithRepo(repo, nil, nil))
	m, cmd := m.OpenChat(1)
	loaded := runCmd(t, cmd).(messagesLoadedMsg)
	m, _ = m.Update(loaded)

	target := m.Messages()[0]
	scrolled := m.ScrollTo(target.ID, 5)
	if got := scrolled.YOffset(); got != 0 {
		t.Fatalf("ScrollTo to first message should clamp YOffset to 0; got %d", got)
	}
}
