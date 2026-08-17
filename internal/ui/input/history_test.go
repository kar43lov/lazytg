package input

import "testing"

func TestHistoryAddIgnoresEmpty(t *testing.T) {
	t.Parallel()

	h := NewHistory(0)
	h.Add("")
	if h.Len() != 0 {
		t.Fatalf("empty Add should be ignored, got len %d", h.Len())
	}
}

func TestHistoryPrevWalksOldest(t *testing.T) {
	t.Parallel()

	h := NewHistory(0)
	for _, msg := range []string{"a", "b", "c", "d", "e"} {
		h.Add(msg)
	}

	got, ok := h.Prev()
	if !ok || got != "e" {
		t.Fatalf("Prev #1: want (e,true), got (%q,%v)", got, ok)
	}
	got, ok = h.Prev()
	if !ok || got != "d" {
		t.Fatalf("Prev #2: want (d,true), got (%q,%v)", got, ok)
	}
	got, ok = h.Prev()
	if !ok || got != "c" {
		t.Fatalf("Prev #3: want (c,true), got (%q,%v)", got, ok)
	}
}

func TestHistoryNextWalksDraftSlot(t *testing.T) {
	t.Parallel()

	h := NewHistory(0)
	h.Add("a")
	h.Add("b")
	h.Add("c")

	if _, ok := h.Prev(); !ok {
		t.Fatalf("seed Prev failed")
	}
	if _, ok := h.Prev(); !ok {
		t.Fatalf("second Prev failed")
	}
	got, ok := h.Next()
	if !ok || got != "c" {
		t.Fatalf("Next #1: want (c,true), got (%q,%v)", got, ok)
	}
	got, ok = h.Next()
	if !ok || got != "" {
		t.Fatalf("Next #2 (draft): want ('',true), got (%q,%v)", got, ok)
	}
}

func TestHistoryPrevAtOldestStays(t *testing.T) {
	t.Parallel()

	h := NewHistory(0)
	h.Add("a")

	got, _ := h.Prev() // walks to "a"
	if got != "a" {
		t.Fatalf("seed Prev: want a, got %q", got)
	}
	got, ok := h.Prev() // already at oldest
	if got != "a" || ok {
		t.Fatalf("Prev at oldest: want (a,false), got (%q,%v)", got, ok)
	}
}

func TestHistoryNextWithoutPrevIsNoop(t *testing.T) {
	t.Parallel()

	h := NewHistory(0)
	h.Add("a")
	h.Add("b")

	got, ok := h.Next()
	if ok || got != "" {
		t.Fatalf("Next from draft: want ('',false), got (%q,%v)", got, ok)
	}
}

func TestHistoryCircularEvictsOldest(t *testing.T) {
	t.Parallel()

	h := NewHistory(3)
	h.Add("a")
	h.Add("b")
	h.Add("c")
	h.Add("d") // evicts "a"

	if h.Len() != 3 {
		t.Fatalf("expected len 3 after overflow, got %d", h.Len())
	}
	got, _ := h.Prev() // d
	if got != "d" {
		t.Fatalf("Prev #1: want d, got %q", got)
	}
	got, _ = h.Prev() // c
	if got != "c" {
		t.Fatalf("Prev #2: want c, got %q", got)
	}
	got, _ = h.Prev() // b
	if got != "b" {
		t.Fatalf("Prev #3: want b, got %q", got)
	}
	got, ok := h.Prev() // already oldest, want false
	if got != "b" || ok {
		t.Fatalf("Prev #4: want (b,false), got (%q,%v)", got, ok)
	}
}

func TestHistoryAddResetsCursor(t *testing.T) {
	t.Parallel()

	h := NewHistory(0)
	h.Add("a")
	h.Add("b")
	if _, ok := h.Prev(); !ok {
		t.Fatalf("seed Prev failed")
	}
	if _, ok := h.Prev(); !ok {
		t.Fatalf("second Prev failed")
	}
	h.Add("c") // resets cursor; next Prev should be "c"
	got, _ := h.Prev()
	if got != "c" {
		t.Fatalf("Prev after Add: want c, got %q", got)
	}
}

func TestHistoryNilSafe(t *testing.T) {
	t.Parallel()

	var h *History
	h.Add("x") // must not panic
	if h.Len() != 0 {
		t.Fatalf("nil Len should be 0, got %d", h.Len())
	}
	if got, ok := h.Prev(); ok || got != "" {
		t.Fatalf("nil Prev should be ('',false), got (%q,%v)", got, ok)
	}
	if got, ok := h.Next(); ok || got != "" {
		t.Fatalf("nil Next should be ('',false), got (%q,%v)", got, ok)
	}
	h.Reset() // must not panic
}
