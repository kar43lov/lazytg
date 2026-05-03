//go:build race

package perf

// raceEnabled reports whether the test binary was built with the race
// detector. Memory budget probes skip when true because shadow-memory
// instrumentation inflates the working set unpredictably.
const raceEnabled = true
