package statusbar

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/pgmac/lazytg/internal/core/events"
)

// updateGolden lets us regenerate the canonical snapshots under testdata/.
// Pass `-update` after `go test` (e.g. `go test ./internal/ui/statusbar/...
// -update`) to rewrite the .txt files. Without the flag the tests assert
// byte-equal output, which catches both content and ANSI-escape regressions.
var updateGolden = flag.Bool("update", false, "rewrite golden snapshots in testdata/")

func TestStatusbarGoldenStates(t *testing.T) {
	t.Parallel()

	const width = 100
	cases := []struct {
		name  string
		model Model
	}{
		{
			name: "connecting",
			model: Model{
				AccountAlias: "@user",
				ChatTitle:    "Saved Messages",
				UnreadTotal:  0,
				ConnState:    StateConnecting,
				StorageMode:  events.StorageModeReadWrite,
			},
		},
		{
			name: "online",
			model: Model{
				AccountAlias: "@user",
				ChatTitle:    "Saved Messages",
				UnreadTotal:  3,
				ConnState:    StateOnline,
				StorageMode:  events.StorageModeReadWrite,
			},
		},
		{
			name: "offline",
			model: Model{
				AccountAlias: "@user",
				ChatTitle:    "Saved Messages",
				UnreadTotal:  3,
				ConnState:    StateOffline,
				StorageMode:  events.StorageModeReadWrite,
			},
		},
		{
			name: "floodwait",
			model: Model{
				AccountAlias: "@user",
				ChatTitle:    "Saved Messages",
				UnreadTotal:  3,
				ConnState:    StateOnline,
				FloodWait:    7 * time.Second,
				StorageMode:  events.StorageModeReadWrite,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.model.View(width)
			path := filepath.Join("testdata", "statusbar_"+tc.name+".txt")

			if *updateGolden {
				if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}

			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden: %v (run with -update to create)", err)
			}
			if string(want) != got {
				t.Fatalf("golden mismatch for %s\nwant: %q\ngot:  %q", tc.name, string(want), got)
			}
			if w := lipgloss.Width(got); w != width {
				t.Fatalf("width mismatch: want %d, got %d for %q", width, w, got)
			}
		})
	}
}

func TestStatusbarViewWidth(t *testing.T) {
	t.Parallel()

	m := New()
	m.AccountAlias = "@user"
	m.ChatTitle = "lazytg general"

	cases := []int{40, 60, 80, 120}
	for _, w := range cases {
		got := m.View(w)
		if rendered := lipgloss.Width(got); rendered != w {
			t.Fatalf("width %d: rendered %d, output %q", w, rendered, got)
		}
	}
}

func TestStatusbarTruncatesChatTitleFirst(t *testing.T) {
	t.Parallel()

	m := New()
	m.AccountAlias = "@a"
	m.ChatTitle = strings.Repeat("X", 200)

	out := m.View(80)
	if lipgloss.Width(out) != 80 {
		t.Fatalf("expected exact width 80, got %d", lipgloss.Width(out))
	}
	if !strings.Contains(out, "@a") {
		t.Fatalf("alias should be preserved; got %q", out)
	}
	if !strings.Contains(out, "…") {
		t.Fatalf("expected ellipsis when chat title is truncated; got %q", out)
	}
}

func TestStatusbarFloodWaitOverridesConn(t *testing.T) {
	t.Parallel()

	m := New()
	m.ConnState = StateOnline
	m.FloodWait = 12 * time.Second

	out := m.View(100)
	if !strings.Contains(out, "floodwait 12s") {
		t.Fatalf("expected floodwait countdown; got %q", out)
	}
	if strings.Contains(out, "online") {
		t.Fatalf("online state should be hidden during floodwait; got %q", out)
	}
}

func TestStatusbarZeroWidthIsEmpty(t *testing.T) {
	t.Parallel()

	if got := New().View(0); got != "" {
		t.Fatalf("width 0 should render empty, got %q", got)
	}
	if got := New().View(-5); got != "" {
		t.Fatalf("negative width should render empty, got %q", got)
	}
}

func TestStatusbarDownloadCell(t *testing.T) {
	t.Parallel()

	m := New()
	m = m.UpsertDownload(NewDownload(7, "report.pdf", 0, 200_000))
	m = m.UpsertDownload(NewDownload(7, "", 100_000, 0))
	out := lipgloss.NewStyle().Render(m.View(120))
	if !strings.Contains(out, "⬇ report.pdf 50%") {
		t.Fatalf("expected download cell at 50%%, got %q", out)
	}
	// Conn cell should be replaced — "online" must not appear.
	if strings.Contains(out, "online") {
		t.Fatalf("conn cell should be hidden during download; got %q", out)
	}
	// Filename preserved across Progress event with empty filename.
	if got := m.ActiveDownloads()[7]; got.Filename() != "report.pdf" {
		t.Fatalf("filename clobbered by progress event: %q", got.Filename())
	}

	m = m.RemoveDownload(7)
	if len(m.ActiveDownloads()) != 0 {
		t.Fatalf("RemoveDownload did not clear: %+v", m.ActiveDownloads())
	}
	out = m.View(120)
	if !strings.Contains(out, StateConnecting) {
		t.Fatalf("conn cell should resume after download removed; got %q", out)
	}
}

func TestStatusbarMultipleDownloadsPickStable(t *testing.T) {
	t.Parallel()

	m := New()
	m = m.UpsertDownload(NewDownload(20, "later.bin", 0, 100))
	m = m.UpsertDownload(NewDownload(10, "earlier.bin", 50, 100))
	out := m.View(120)
	if !strings.Contains(out, "earlier.bin") {
		t.Fatalf("expected smallest fileID to win for stable rendering; got %q", out)
	}
}

func TestStatusbarDefaultsBlankFields(t *testing.T) {
	t.Parallel()

	m := Model{}
	out := m.View(60)
	if !strings.Contains(out, "- | -") {
		t.Fatalf("blank alias and chat should render as dashes; got %q", out)
	}
	if !strings.Contains(out, events.StorageModeReadWrite) {
		t.Fatalf("blank storage mode should default to %s; got %q", events.StorageModeReadWrite, out)
	}
	if !strings.Contains(out, StateConnecting) {
		t.Fatalf("blank conn state should default to %s; got %q", StateConnecting, out)
	}
}
