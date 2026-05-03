package files

import "sync"

// uploadProgressThrottler is the upload-side twin of progressThrottler.
// The two could share an implementation, but keying by UploadID
// (in-process counter) instead of FileID (Telegram media id) keeps the
// state slots cleanly separated and lets the upload path reset state
// keyed by the same id callers see in events.
type uploadProgressThrottler struct {
	mu     sync.Mutex
	states map[int64]*uploadProgressState
}

type uploadProgressState struct {
	lastEmittedBytes int64
	lastEmittedPct   int
}

func newUploadProgressThrottler() *uploadProgressThrottler {
	return &uploadProgressThrottler{states: make(map[int64]*uploadProgressState)}
}

// reset zeros the state for uploadID so the next callback emits the
// initial 0% event. Called by UploadService at the top of every
// SendFile so a retry after failure does not look like a continuation
// of the previous attempt.
func (t *uploadProgressThrottler) reset(uploadID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.states[uploadID] = &uploadProgressState{}
}

// release drops the per-uploadID state once SendFile has emitted its
// terminal Completed/Failed event. Mirrors progressThrottler.release;
// without it the map grows unbounded across the lifetime of the TUI.
func (t *uploadProgressThrottler) release(uploadID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.states, uploadID)
}

// shouldEmit reports whether the (bytes, total) tick should produce a
// bus event for uploadID. Identical thresholds to the download path
// (1 MiB OR 5%, whichever first).
func (t *uploadProgressThrottler) shouldEmit(uploadID, bytes, total int64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	st, ok := t.states[uploadID]
	if !ok {
		st = &uploadProgressState{}
		t.states[uploadID] = st
	}
	if st.lastEmittedBytes == 0 && bytes > 0 {
		st.lastEmittedBytes = bytes
		if total > 0 {
			st.lastEmittedPct = int(bytes * 100 / total)
		}
		return true
	}
	if bytes-st.lastEmittedBytes >= progressThrottleStep {
		st.lastEmittedBytes = bytes
		if total > 0 {
			st.lastEmittedPct = int(bytes * 100 / total)
		}
		return true
	}
	if total > 0 {
		pct := int(bytes * 100 / total)
		if pct-st.lastEmittedPct >= progressThrottlePercent {
			st.lastEmittedBytes = bytes
			st.lastEmittedPct = pct
			return true
		}
	}
	return false
}
