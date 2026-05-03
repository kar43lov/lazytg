package files

import (
	"sync"
)

// progressThrottleStep is the minimum byte delta between two emitted
// progress events. 1 MiB matches the budget the plan calls out: small
// enough to feel responsive on a 50 Mbps link, large enough that a 2 GiB
// file does not spam ~four hundred thousand events at gotd's 512 KiB
// chunk granularity.
const progressThrottleStep = 1 << 20 // 1 MiB

// progressThrottlePercent is the alternative trigger: emit every 5%
// of the file even when 1 MiB worth of bytes has not yet accumulated.
// The two thresholds are OR'd together so very small files (< 1 MiB)
// still see at least 20 progress events for a smooth UI.
const progressThrottlePercent = 5

// progressThrottler decides whether a progress callback should turn
// into a bus event. State is keyed by FileID so two concurrent
// downloads do not interfere with each other's emit thresholds.
type progressThrottler struct {
	mu     sync.Mutex
	states map[int64]*progressState
}

type progressState struct {
	lastEmittedBytes int64
	lastEmittedPct   int
}

func newProgressThrottler() *progressThrottler {
	return &progressThrottler{states: make(map[int64]*progressState)}
}

// reset zeros the state for fileID so the next callback emits the
// initial 0% event. Called by DownloadService at the top of every
// Download() so a retry after failure does not look like a continuation
// of the previous attempt.
func (t *progressThrottler) reset(fileID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.states[fileID] = &progressState{}
}

// release drops the per-fileID state once the download is finished
// (success or failure). Without it the map grows unbounded across the
// lifetime of the TUI — a long-running session that downloaded
// thousands of files would leak one entry per fileID.
func (t *progressThrottler) release(fileID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.states, fileID)
}

// shouldEmit reports whether the (bytes, total) tick should produce a
// bus event for fileID. The throttler keeps state across calls so a
// monotonic stream of byte counts produces a small, evenly-spaced set
// of events.
func (t *progressThrottler) shouldEmit(fileID, bytes, total int64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	st, ok := t.states[fileID]
	if !ok {
		st = &progressState{}
		t.states[fileID] = st
	}
	// Always emit the very first tick after reset so the UI sees a
	// 0%/N event and can render the progress bar from byte one.
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
