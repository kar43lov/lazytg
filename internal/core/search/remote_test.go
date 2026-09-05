package search

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

type fakeRemote struct {
	got RemoteQuery
	res RemoteResult
	err error
}

func (f *fakeRemote) SearchGlobal(_ context.Context, q RemoteQuery) (RemoteResult, error) {
	f.got = q
	return f.res, f.err
}

type fakeRemoteStore struct {
	chats    []domain.Chat
	messages []domain.Message
	peers    []domain.Peer
	saveErr  error
}

func (f *fakeRemoteStore) SaveChatIfMissing(_ context.Context, c domain.Chat) error {
	f.chats = append(f.chats, c)
	return nil
}

func (f *fakeRemoteStore) SaveMessage(_ context.Context, m domain.Message) error {
	f.messages = append(f.messages, m)
	return f.saveErr
}

func (f *fakeRemoteStore) Save(_ context.Context, p domain.Peer) error {
	f.peers = append(f.peers, p)
	return nil
}

func TestRemoteService_RefusesWhatTheServerCannotHonour(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{}
	svc := NewRemoteService(remote, &fakeRemoteStore{}, nil, nil)
	for _, raw := range []string{"from:@alice", "in:#work budget", "has:file", "budget -late", "   "} {
		_, err := svc.Search(context.Background(), raw, 0)
		if err == nil {
			t.Fatalf("%q must be refused", raw)
		}
		if remote.got.Text != "" {
			t.Fatalf("%q reached the server as %+v", raw, remote.got)
		}
	}
	if _, err := svc.Search(context.Background(), "in:#work budget", 0); !errors.Is(err, ErrRemoteLocalFilters) {
		t.Fatalf("filters-only: %v", err)
	}
	if _, err := svc.Search(context.Background(), "after:2024-01-01", 0); err == nil {
		t.Fatal("dates-only must be refused")
	}
}

func TestRemoteService_AsksMirrorsAndMarks(t *testing.T) {
	t.Parallel()

	when := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	remote := &fakeRemote{res: RemoteResult{
		Messages: []domain.Message{
			{ID: 7, ChatID: 500, FromID: 42, Date: when, Text: "the Budget is late again, sorry"},
			{ID: 9, ChatID: 42, Date: when, Text: "no match here"},
		},
		Chats: []domain.Chat{{ID: 500, Type: domain.ChatTypeSupergroup, Title: "Team"}},
		Peers: []domain.Peer{{ID: 500, Type: domain.ChatTypeSupergroup, AccessHash: 9}},
	}}
	store := &fakeRemoteStore{}
	svc := NewRemoteService(remote, store, store, nil)

	hits, err := svc.Search(context.Background(), `budget after:2024-01-01 "late again"`, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if remote.got.Text != "budget late again" || remote.got.Limit != DefaultRemoteLimit || remote.got.After.IsZero() || !remote.got.Before.IsZero() {
		t.Fatalf("server asked %+v", remote.got)
	}
	if len(store.peers) != 1 || len(store.chats) != 1 || store.chats[0].Title != "Team" || len(store.messages) != 2 {
		t.Fatalf("mirror: peers=%d chats=%+v messages=%d", len(store.peers), store.chats, len(store.messages))
	}
	if len(hits) != 2 || !hits[0].Remote || hits[0].ChatID != 500 || hits[0].Message.ID != 7 {
		t.Fatalf("hits = %+v", hits)
	}
	if !strings.Contains(hits[0].Snippet, "<b>Budget</b>") {
		t.Fatalf("snippet = %q", hits[0].Snippet)
	}
	if hits[1].Snippet != "no match here" {
		t.Fatalf("unmarked snippet = %q", hits[1].Snippet)
	}
}

func TestRemoteService_KeepsTheHitWhenTheMirrorRefuses(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{res: RemoteResult{Messages: []domain.Message{{ID: 7, ChatID: 500, Text: "x"}}}}
	store := &fakeRemoteStore{saveErr: errors.New("FOREIGN KEY constraint failed")}
	hits, err := NewRemoteService(remote, store, nil, nil).Search(context.Background(), "x", 3)
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits = %+v, err = %v", hits, err)
	}
	if remote.got.Limit != 3 {
		t.Fatalf("limit = %d", remote.got.Limit)
	}

	remote = &fakeRemote{err: errors.New("network")}
	if _, err := NewRemoteService(remote, store, nil, nil).Search(context.Background(), "x", 0); err == nil {
		t.Fatal("a failed request must surface")
	}
	if _, err := (*RemoteService)(nil).Search(context.Background(), "x", 0); err == nil {
		t.Fatal("a nil service must say it is not connected")
	}
}

func TestRemoteSnippet(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a ", 60) + "needle" + strings.Repeat(" b", 60)
	cases := []struct{ text, needle, want string }{
		{"Привет, коллега", "привет", "<b>Привет</b>, коллега"},
		{"only the second word matches", "zzz second", "only the <b>second</b> word matches"},
		{"nothing here", "zzz", "nothing here"},
		{long, "needle", "...a a a <b>needle</b> b b b..."},
	}
	for _, c := range cases {
		got := remoteSnippet(c.text, c.needle)
		if c.text == long {
			if !strings.HasPrefix(got, "...") || !strings.HasSuffix(got, "...") || !strings.Contains(got, "<b>needle</b>") {
				t.Fatalf("window = %q", got)
			}
			continue
		}
		if got != c.want {
			t.Fatalf("remoteSnippet(%q, %q) = %q, want %q", c.text, c.needle, got, c.want)
		}
	}
}
