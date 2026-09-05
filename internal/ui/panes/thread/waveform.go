package thread

import "strings"

// The shape of a voice message.
//
// A voice message is the one attachment whose contents a terminal can
// genuinely show. It cannot play the audio — nothing in a terminal can — but
// Telegram sends the waveform alongside it, the same one every official
// client draws behind the play button, and a row of block characters carries
// what "voice, 0:42" cannot: whether this is somebody saying "ok" or two
// minutes of argument, where the pauses are, whether it is thirty seconds of
// silence somebody sent by accident.

// waveformBars are the eight heights a cell can take, quietest first. The
// space is not one of them: an empty cell would read as the end of the
// waveform rather than as a silent moment inside it.
var waveformBars = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// waveformWidth is how many cells the drawn waveform takes.
//
// Telegram sends roughly a hundred samples whatever the length, so this is a
// choice about the badge rather than about the data: wide enough that a pause
// is visible, narrow enough that the badge still fits beside the duration and
// the size on an eighty-column pane.
const waveformWidth = 24

// renderWaveform draws packed Telegram waveform bytes as block characters, or
// returns "" when there is nothing to draw.
//
// The samples are five bits each and packed without regard for byte
// boundaries, which is why this reads bit by bit rather than byte by byte.
func renderWaveform(packed []byte) string {
	samples := unpackWaveform(packed)
	if len(samples) == 0 {
		return ""
	}

	var b strings.Builder
	b.Grow(waveformWidth * 3)
	for i := 0; i < waveformWidth; i++ {
		// Each cell averages the samples that fall into it. Averaging
		// rather than sampling every nth value: a single loud sample
		// between two quiet ones is noise, and picking it would draw a
		// spike that is not in the recording.
		lo := i * len(samples) / waveformWidth
		hi := (i + 1) * len(samples) / waveformWidth
		if hi <= lo {
			hi = lo + 1
		}
		if hi > len(samples) {
			hi = len(samples)
		}
		if lo >= len(samples) {
			break
		}
		sum := 0
		for _, v := range samples[lo:hi] {
			sum += int(v)
		}
		avg := sum / (hi - lo)
		// Samples run 0..31 and the bars 0..7.
		b.WriteRune(waveformBars[avg*len(waveformBars)/32])
	}
	return b.String()
}

// unpackWaveform expands Telegram's five-bits-per-sample packing.
func unpackWaveform(packed []byte) []byte {
	bits := len(packed) * 8
	count := bits / 5
	if count == 0 {
		return nil
	}
	out := make([]byte, 0, count)
	for i := 0; i < count; i++ {
		start := i * 5
		var v byte
		for bit := 0; bit < 5; bit++ {
			pos := start + bit
			byteIdx := pos / 8
			bitIdx := pos % 8
			// Little-endian within each byte, which is how Telegram
			// packs it: sample 0 occupies the low five bits of byte 0.
			if packed[byteIdx]&(1<<bitIdx) != 0 {
				v |= 1 << bit
			}
		}
		out = append(out, v)
	}
	return out
}
