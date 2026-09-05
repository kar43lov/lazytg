package thread

import (
	"strings"
	"testing"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// packWaveform is the encoder Telegram's decoder is the mirror of: five bits
// per sample, little-endian within each byte, packed without regard for byte
// boundaries. Written here so the test drives the real format rather than a
// convenient one.
func packWaveform(samples []byte) []byte {
	bits := len(samples) * 5
	out := make([]byte, (bits+7)/8)
	for i, v := range samples {
		for bit := 0; bit < 5; bit++ {
			if v&(1<<bit) == 0 {
				continue
			}
			pos := i*5 + bit
			out[pos/8] |= 1 << (pos % 8)
		}
	}
	return out
}

func TestUnpackWaveform_RoundTripsTelegramsPacking(t *testing.T) {
	t.Parallel()

	samples := []byte{0, 31, 1, 30, 15, 16, 7, 24, 3, 28}
	got := unpackWaveform(packWaveform(samples))
	if len(got) < len(samples) {
		t.Fatalf("unpacked %d samples, want at least %d", len(got), len(samples))
	}
	for i, want := range samples {
		if got[i] != want {
			t.Fatalf("sample %d = %d, want %d (full: %v)", i, got[i], want, got[:len(samples)])
		}
	}
}

func TestRenderWaveform_LoudIsTallAndQuietIsShort(t *testing.T) {
	t.Parallel()

	quiet := renderWaveform(packWaveform(repeatSample(0, 100)))
	loud := renderWaveform(packWaveform(repeatSample(31, 100)))

	if quiet == "" || loud == "" {
		t.Fatalf("nothing drawn: quiet=%q loud=%q", quiet, loud)
	}
	if !strings.ContainsRune(quiet, waveformBars[0]) {
		t.Fatalf("silence drew %q", quiet)
	}
	if !strings.ContainsRune(loud, waveformBars[len(waveformBars)-1]) {
		t.Fatalf("a loud recording drew %q", loud)
	}
}

// A pause has to be visible — it is half of what the shape is for.
func TestRenderWaveform_ShowsAPause(t *testing.T) {
	t.Parallel()

	samples := append(repeatSample(31, 40), repeatSample(0, 20)...)
	samples = append(samples, repeatSample(31, 40)...)
	got := renderWaveform(packWaveform(samples))

	runes := []rune(got)
	if len(runes) != waveformWidth {
		t.Fatalf("drew %d cells, want %d", len(runes), waveformWidth)
	}
	middle := runes[len(runes)/2]
	if middle != waveformBars[0] {
		t.Fatalf("the pause in the middle drew %q, not the quietest bar", string(middle))
	}
}

// An empty cell would read as the end of the waveform rather than as a silent
// moment inside it.
func TestRenderWaveform_NeverDrawsASpace(t *testing.T) {
	t.Parallel()

	got := renderWaveform(packWaveform(repeatSample(0, 100)))
	if strings.ContainsRune(got, ' ') {
		t.Fatalf("silence drew a gap: %q", got)
	}
}

func TestRenderWaveform_NothingToDraw(t *testing.T) {
	t.Parallel()

	if got := renderWaveform(nil); got != "" {
		t.Fatalf("renderWaveform(nil) = %q", got)
	}
	if got := renderWaveform([]byte{}); got != "" {
		t.Fatalf("renderWaveform(empty) = %q", got)
	}
}

// A single loud sample between two quiet ones is noise; picking it rather
// than averaging would draw a spike that is not in the recording.
func TestRenderWaveform_AveragesRatherThanSamples(t *testing.T) {
	t.Parallel()

	samples := repeatSample(0, 100)
	samples[50] = 31
	got := renderWaveform(packWaveform(samples))

	for _, r := range got {
		if r == waveformBars[len(waveformBars)-1] {
			t.Fatalf("one loud sample drew a full-height spike: %q", got)
		}
	}
}

func repeatSample(v byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// The shape belongs on the badge, where the reader is deciding whether to
// spend thirty seconds listening to it.
func TestMediaBadge_DrawsTheVoiceShape(t *testing.T) {
	t.Parallel()

	samples := append(repeatSample(31, 50), repeatSample(0, 50)...)
	badge := mediaBadge(&domain.MediaInfo{
		Kind:     domain.MediaKindVoice,
		Duration: 42,
		Size:     12345,
		Waveform: packWaveform(samples),
	})
	if !strings.Contains(badge, "voice") {
		t.Fatalf("badge = %q", badge)
	}
	if !strings.ContainsRune(badge, waveformBars[len(waveformBars)-1]) {
		t.Fatalf("no waveform in the badge: %q", badge)
	}
}

// A voice message stored before the waveform column keeps the badge it had.
func TestMediaBadge_WithoutAWaveform(t *testing.T) {
	t.Parallel()

	badge := mediaBadge(&domain.MediaInfo{Kind: domain.MediaKindVoice, Duration: 42, Size: 100})
	for _, bar := range waveformBars {
		if strings.ContainsRune(badge, bar) {
			t.Fatalf("a waveform was invented: %q", badge)
		}
	}
}
