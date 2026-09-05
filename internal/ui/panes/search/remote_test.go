package search

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/search"
	coresync "github.com/kar43lov/lazytg/internal/core/sync"
)

func typed(t *testing.T, m Model, text string) Model {
	t.Helper()
	for _, r := range text {
		m, _ = m.Update(keyText(string(r)))
	}
	return m
}

func TestTab_AsksTheServerAndLabelsTheAnswer(t *testing.T) {
	t.Parallel()

	local := &fakeService{}
	remote := &fakeService{hits: []search.Hit{{Message: sampleHit(7, "budget").Message, ChatID: 500, Snippet: "the <b>budget</b>", Remote: true}}}
	m := New(local, time.Hour, nil).WithRemote(remote)
	m, _ = m.Open()
	m = typed(t, m, "budget")
	if !strings.Contains(m.View(80, 24), "Tab asks the server") {
		t.Fatalf("the hint must name the key:\n%s", m.View(80, 24))
	}

	m, cmd := m.Update(keyChord(tea.KeyTab, 0))
	if cmd == nil || !m.RemoteLoading() {
		t.Fatal("Tab must run the server search")
	}
	if !strings.Contains(m.View(80, 24), "Asking the server") {
		t.Fatalf("the wait must be visible:\n%s", m.View(80, 24))
	}
	msg := runCmd(t, cmd)
	if remote.callCount() != 1 || remote.lastQuery() != "budget" || local.callCount() != 0 {
		t.Fatalf("remote calls=%d query=%q local calls=%d", remote.callCount(), remote.lastQuery(), local.callCount())
	}
	res, ok := msg.(ResultsMsg)
	if !ok || !res.Remote || res.Generation != m.QueryGeneration() {
		t.Fatalf("answer = %#v", msg)
	}
	m, _ = m.Update(res)
	if !m.RemoteShown() || m.RemoteLoading() || len(m.Hits()) != 1 {
		t.Fatalf("answer not applied: shown=%v loading=%v hits=%d", m.RemoteShown(), m.RemoteLoading(), len(m.Hits()))
	}
	view := m.View(80, 24)
	if !strings.Contains(view, "server  chat=500") || !strings.Contains(view, "from the server") {
		t.Fatalf("the row and the hint must say where it came from:\n%s", view)
	}

	// Enter jumps to a server hit like any other.
	_, jump := m.Update(keyChord(tea.KeyEnter, 0))
	if j, ok := runCmd(t, jump).(JumpMsg); !ok || j.Hit.ChatID != 500 || !j.Hit.Remote {
		t.Fatalf("jump = %#v", runCmd(t, jump))
	}
}

func TestTab_StaleAnswerAndNoRemote(t *testing.T) {
	t.Parallel()

	remote := &fakeService{hits: []search.Hit{sampleHit(7, "old")}}
	m := New(&fakeService{}, time.Hour, nil).WithRemote(remote)
	m, _ = m.Open()
	m = typed(t, m, "old")
	m, cmd := m.Update(keyChord(tea.KeyTab, 0))
	answer := runCmd(t, cmd).(ResultsMsg)

	// The user typed on while the server was thinking.
	m = typed(t, m, "er")
	m, _ = m.Update(answer)
	if m.RemoteShown() || len(m.Hits()) != 0 {
		t.Fatalf("a stale answer must be dropped: shown=%v hits=%d", m.RemoteShown(), len(m.Hits()))
	}

	// A fresh local answer takes the screen back from a server one.
	m, _ = m.Update(keyChord(tea.KeyTab, 0))
	fresh := ResultsMsg{Hits: remote.hits, Remote: true, Generation: m.QueryGeneration()}
	m, _ = m.Update(fresh)
	if !m.RemoteShown() {
		t.Fatal("a fresh answer must show")
	}
	m, _ = m.Update(ResultsMsg{Hits: nil})
	if m.RemoteShown() {
		t.Fatal("local results must clear the server label")
	}

	// Without a remote service the key does nothing and the hint is silent.
	plain := New(&fakeService{}, time.Hour, nil)
	plain, _ = plain.Open()
	plain = typed(t, plain, "x")
	plain, cmd = plain.Update(keyChord(tea.KeyTab, 0))
	if cmd != nil || plain.RemoteLoading() {
		t.Fatal("Tab without a remote must be a no-op")
	}
	if strings.Contains(plain.View(80, 24), "server") {
		t.Fatalf("no remote, no hint:\n%s", plain.View(80, 24))
	}
}

func TestTab_FloodWaitIsSaidInWords(t *testing.T) {
	t.Parallel()

	remote := &fakeService{err: &coresync.FloodWaitError{RetryAfter: 30 * time.Second}}
	m := New(&fakeService{}, time.Hour, nil).WithRemote(remote)
	m, _ = m.Open()
	m = typed(t, m, "x")
	m, cmd := m.Update(keyChord(tea.KeyTab, 0))
	m, _ = m.Update(runCmd(t, cmd))
	if view := m.View(80, 24); !strings.Contains(view, "the server asks to wait 30s") {
		t.Fatalf("flood wait must be readable:\n%s", view)
	}
	if m.RemoteLoading() {
		t.Fatal("an error ends the wait")
	}
}
