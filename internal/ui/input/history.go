package input

// historyDefaultMax is the upper bound of the in-memory ring buffer. 100
// is generous enough that "last week's recurring reply" is still
// reachable but small enough that a forgotten daemon does not bloat the
// process for users who paste large messages.
const historyDefaultMax = 100

// History is a circular buffer of previously-sent message bodies plus a
// browse cursor. The browse semantics mirror readline: Prev walks toward
// older entries, Next walks toward newer ones, and stepping past the
// newest entry returns the empty "draft slot" so the user can resume
// typing a fresh message.
//
// The implementation is single-goroutine (the UI Update path is the only
// caller) so no locking is required. State is intentionally kept in
// memory; persistence to ~/.local/state/lazytg/input_history.txt is a
// follow-up the plan calls "optional, can be deferred".
type History struct {
	entries []string
	max     int
	// cursor walks the entries slice during Prev/Next. -1 means "draft
	// slot": Next from the newest entry returns "" and parks here. Add
	// also resets cursor to -1 so a fresh send starts a new browse
	// session.
	cursor int
}

// NewHistory returns a History with the supplied capacity. Non-positive
// values fall back to historyDefaultMax so callers can pass 0 to mean
// "use the default" without a separate constructor.
func NewHistory(capacity int) *History {
	if capacity <= 0 {
		capacity = historyDefaultMax
	}
	return &History{
		entries: make([]string, 0, capacity),
		max:     capacity,
		cursor:  -1,
	}
}

// Add appends text to the buffer, evicting the oldest entry when the
// buffer is full. Empty strings are ignored so a stray Enter on a blank
// composer does not pollute the ring. Add also resets the browse
// cursor so the next Prev returns the just-added entry.
func (h *History) Add(text string) {
	if h == nil || text == "" {
		return
	}
	if len(h.entries) >= h.max {
		copy(h.entries, h.entries[1:])
		h.entries = h.entries[:h.max-1]
	}
	h.entries = append(h.entries, text)
	h.cursor = -1
}

// Len returns the number of entries currently held. Exposed for tests
// that want to assert capacity behaviour without inspecting the slice.
func (h *History) Len() int {
	if h == nil {
		return 0
	}
	return len(h.entries)
}

// Prev returns the previous (older) entry and the "did we move" flag.
// When already at the oldest entry Prev returns the same string with
// ok=false so callers can leave the textarea contents untouched.
func (h *History) Prev() (string, bool) {
	if h == nil || len(h.entries) == 0 {
		return "", false
	}
	switch h.cursor {
	case -1:
		h.cursor = len(h.entries) - 1
	case 0:
		return h.entries[0], false
	default:
		h.cursor--
	}
	return h.entries[h.cursor], true
}

// Next returns the next (newer) entry. When already at the newest entry
// Next walks one step further, parks at -1 (the draft slot) and returns
// "" with ok=true so the caller knows to clear the textarea.
func (h *History) Next() (string, bool) {
	if h == nil || len(h.entries) == 0 {
		return "", false
	}
	if h.cursor == -1 {
		return "", false
	}
	h.cursor++
	if h.cursor >= len(h.entries) {
		h.cursor = -1
		return "", true
	}
	return h.entries[h.cursor], true
}

// Reset rewinds the browse cursor to the draft slot without touching
// the stored entries. Used after the user manually types into the
// textarea so a subsequent Prev starts from the newest entry again.
func (h *History) Reset() {
	if h == nil {
		return
	}
	h.cursor = -1
}
