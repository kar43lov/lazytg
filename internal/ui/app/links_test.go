package app

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/ui/input"
	"github.com/kar43lov/lazytg/internal/ui/keymap"
	"github.com/kar43lov/lazytg/internal/ui/panes/thread"
)

func TestOpenableLink(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		msg  domain.Message
		want string
	}{
		{"text_url first", domain.Message{Text: "see docs and https://b.example", Entities: []domain.Entity{
			{Kind: domain.EntityTextURL, Offset: 4, Length: 4, URL: "https://a.example/d"},
		}}, "https://a.example/d"},
		{"marked url", domain.Message{Text: "go https://x.example/p?q=1 now", Entities: []domain.Entity{
			{Kind: domain.EntityURL, Offset: 3, Length: 23},
		}}, "https://x.example/p?q=1"},
		{"bare url, trailing punctuation dropped", domain.Message{Text: "read https://y.example/a)."}, "https://y.example/a"},
		{"not a web scheme", domain.Message{Text: "call", Entities: []domain.Entity{{Kind: domain.EntityTextURL, Offset: 0, Length: 4, URL: "tel:+123"}}}, ""},
		{"a scheme with a host but no browser", domain.Message{Text: "get", Entities: []domain.Entity{{Kind: domain.EntityTextURL, Offset: 0, Length: 3, URL: "ftp://files.example/a"}}}, ""},
		{"escape inside the link is stripped", domain.Message{Text: "x", Entities: []domain.Entity{{Kind: domain.EntityTextURL, Offset: 0, Length: 1, URL: "https://z.example/\x1b]52;c;x\x07p"}}}, "https://z.example/]52;c;xp"},
		{"attachment wins over a caption link", domain.Message{Text: "https://c.example", Media: &domain.MediaInfo{Kind: domain.MediaKindPhoto, FileID: 5}}, ""},
		{"location opens a map", domain.Message{Media: &domain.MediaInfo{Kind: domain.MediaKindLocation, Filename: "55.755800,37.617300"}},
			"https://www.openstreetmap.org/?mlat=55.755800&mlon=37.617300#map=16/55.755800/37.617300"},
		{"nothing", domain.Message{Text: "just words"}, ""},
	}
	for _, tc := range cases {
		if got := openableLink(tc.msg); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// "o" on a text message with a link hands the link to the browser; the
// attachment above it is not what was asked for.
func TestOpenKey_FollowsTheLinkUnderTheCursor(t *testing.T) {
	t.Parallel()

	threadModel := thread.New()
	now := time.Now()
	threadModel = injectMessages(threadModel, []domain.Message{
		{ID: 1, ChatID: 42, Date: now, Media: &domain.MediaInfo{Kind: domain.MediaKindPhoto, FileID: 9, Filename: "a.jpg"}},
		{ID: 2, ChatID: 42, Date: now.Add(time.Second), Text: "see https://example.com/page"},
	})
	inputModel := input.NewWithDeps(nil, keymap.Default(), nil)
	opener := &fakeOpener{}
	a := New(Deps{Keymap: keymap.Default(), Thread: &threadModel, Input: &inputModel, Opener: opener})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a = tabTo(t, model.(App), FocusThread)

	cmd, ok := a.cmdOpenCursorMedia()
	if !ok || cmd == nil {
		t.Fatal("o on a message with a link did nothing")
	}
	model, _ = a.Update(cmd())
	a = model.(App)
	if got := opener.snapshot(); len(got) != 1 || got[0] != "url:https://example.com/page" {
		t.Fatalf("opener got %v", got)
	}
	if !strings.Contains(a.statusText(), "example.com") {
		t.Fatalf("status reads %q", a.statusText())
	}
}

// A chord that did nothing says why. Silence reads as a broken key.
func TestOpenAndDownload_SayWhyNothingHappened(t *testing.T) {
	t.Parallel()

	threadModel := thread.New()
	now := time.Now()
	threadModel = injectMessages(threadModel, []domain.Message{
		{ID: 1, ChatID: 42, Date: now, Media: &domain.MediaInfo{Kind: domain.MediaKindPoll, Filename: "Lunch?"}},
		{ID: 2, ChatID: 42, Date: now.Add(time.Second), Text: "call", Entities: []domain.Entity{{Kind: domain.EntityTextURL, Offset: 0, Length: 4, URL: "tel:+123"}}},
	})
	inputModel := input.NewWithDeps(nil, keymap.Default(), nil)
	opener := &fakeOpener{}
	a := New(Deps{Keymap: keymap.Default(), Thread: &threadModel, Input: &inputModel, Opener: opener})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a = tabTo(t, model.(App), FocusThread)

	run := func(a App, key string) App {
		t.Helper()
		next, cmd := press(t, a, key)
		if cmd == nil {
			t.Fatalf("%q produced no command", key)
		}
		model, _ := next.Update(cmd())
		return model.(App)
	}
	a = run(a, "o")
	if !strings.Contains(a.statusText(), "only http and https") {
		t.Fatalf("o on a tel: link: status %q", a.statusText())
	}
	a = cursorUp(t, a, 1)
	a = run(a, "o")
	if !strings.Contains(a.statusText(), "poll has no file") {
		t.Fatalf("o on a poll: status %q", a.statusText())
	}
	next, cmd := a.Update(keyChord('d', tea.ModCtrl))
	a = next.(App)
	if cmd == nil {
		t.Fatal("ctrl+d on a poll produced no command")
	}
	model, _ = a.Update(cmd())
	a = model.(App)
	if !strings.Contains(a.statusText(), "nothing to download: a poll") {
		t.Fatalf("ctrl+d on a poll: status %q", a.statusText())
	}
	if got := opener.snapshot(); len(got) != 0 {
		t.Fatalf("the opener was called: %v", got)
	}
}
