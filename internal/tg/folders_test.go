package tg

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
)

type stubFoldersAPI struct {
	res *tg.MessagesDialogFilters
	err error
}

func (s stubFoldersAPI) MessagesGetDialogFilters(context.Context) (*tg.MessagesDialogFilters, error) {
	return s.res, s.err
}

func TestFoldersFetcher_DecodesAnOrdinaryFolder(t *testing.T) {
	t.Parallel()

	f := &tg.DialogFilter{
		ID:       7,
		Title:    tg.TextWithEntities{Text: "Work"},
		Emoticon: "💼",
		Groups:   true,
		IncludePeers: []tg.InputPeerClass{
			&tg.InputPeerUser{UserID: 11},
			&tg.InputPeerChannel{ChannelID: 12},
		},
		ExcludePeers: []tg.InputPeerClass{&tg.InputPeerChat{ChatID: 13}},
		PinnedPeers:  []tg.InputPeerClass{&tg.InputPeerUser{UserID: 14}},
	}
	fetcher := NewFoldersFetcher(stubFoldersAPI{res: &tg.MessagesDialogFilters{
		Filters: []tg.DialogFilterClass{f},
	}})

	got, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("folders = %d, want 1", len(got))
	}
	folder := got[0]
	if folder.ID != 7 || folder.Title != "Work" || folder.Emoticon != "💼" {
		t.Fatalf("folder = %+v, want the id, title and emoji preserved", folder)
	}
	if !folder.Groups {
		t.Fatal("the category switch was dropped")
	}
	if len(folder.Include) != 2 || folder.Include[0] != 11 || folder.Include[1] != 12 {
		t.Fatalf("include = %v, want the user and the channel ids", folder.Include)
	}
	if len(folder.Exclude) != 1 || folder.Exclude[0] != 13 {
		t.Fatalf("exclude = %v, want the basic-group id", folder.Exclude)
	}
	if len(folder.Pinned) != 1 || folder.Pinned[0] != 14 {
		t.Fatalf("pinned = %v", folder.Pinned)
	}
}

// "All chats" is a pseudo-folder meaning "no filtering", and the pane already
// has a tab for that. Two tabs meaning the same thing is one too many.
func TestFoldersFetcher_DropsTheDefaultFolder(t *testing.T) {
	t.Parallel()

	fetcher := NewFoldersFetcher(stubFoldersAPI{res: &tg.MessagesDialogFilters{
		Filters: []tg.DialogFilterClass{
			&tg.DialogFilterDefault{},
			&tg.DialogFilter{ID: 7, Title: tg.TextWithEntities{Text: "Work"}},
		},
	}})

	got, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 || got[0].ID != 7 {
		t.Fatalf("folders = %+v, want only the real one", got)
	}
}

// A shared folder has no category switches at all, so its membership must be
// its list — inventing categories would add chats the person who shared it
// never put in.
func TestFoldersFetcher_MarksASharedFolderExplicit(t *testing.T) {
	t.Parallel()

	fetcher := NewFoldersFetcher(stubFoldersAPI{res: &tg.MessagesDialogFilters{
		Filters: []tg.DialogFilterClass{
			&tg.DialogFilterChatlist{
				ID:           9,
				Title:        tg.TextWithEntities{Text: "Shared"},
				IncludePeers: []tg.InputPeerClass{&tg.InputPeerUser{UserID: 21}},
			},
		},
	}})

	got, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 || !got[0].ExplicitOnly {
		t.Fatalf("folder = %+v, want ExplicitOnly set", got)
	}
	if len(got[0].Include) != 1 || got[0].Include[0] != 21 {
		t.Fatalf("include = %v", got[0].Include)
	}
}

// InputPeerSelf carries no id in this list, and a folder holding Saved
// Messages must not therefore produce a zero id that would match nothing —
// or, worse, match a chat whose id happens to be zero.
func TestFoldersFetcher_SkipsSelfPeer(t *testing.T) {
	t.Parallel()

	fetcher := NewFoldersFetcher(stubFoldersAPI{res: &tg.MessagesDialogFilters{
		Filters: []tg.DialogFilterClass{
			&tg.DialogFilter{
				ID:           7,
				IncludePeers: []tg.InputPeerClass{&tg.InputPeerSelf{}, &tg.InputPeerUser{UserID: 31}},
			},
		},
	}})

	got, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got[0].Include) != 1 || got[0].Include[0] != 31 {
		t.Fatalf("include = %v, want the self peer skipped", got[0].Include)
	}
}
