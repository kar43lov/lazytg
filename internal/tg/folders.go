package tg

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// FoldersFetcher reads the user's chat folders — what the API calls dialog
// filters — so the chat list can be narrowed the way it is on every other
// client the user has.
//
// Folders are a client-side arrangement that Telegram stores server-side: the
// server keeps the definition, and each client decides what to do with it.
// That is why this reads them and nothing writes them here. Editing folders
// is a settings screen, not a chat client's job, and getting it wrong changes
// what the user sees on their phone.
type FoldersFetcher struct {
	api MessagesGetDialogFiltersClient
}

// MessagesGetDialogFiltersClient is the slice of *tg.Client this needs.
type MessagesGetDialogFiltersClient interface {
	MessagesGetDialogFilters(ctx context.Context) (*tg.MessagesDialogFilters, error)
}

// NewFoldersFetcher wires a fetcher over the MTProto client.
func NewFoldersFetcher(api MessagesGetDialogFiltersClient) *FoldersFetcher {
	return &FoldersFetcher{api: api}
}

// Fetch returns the folders the account has defined, in the order Telegram
// lists them — which is the order the user arranged them in.
//
// Three variants come back and only one of them carries rules:
//
//   - DialogFilter is an ordinary folder: named, with peers to include and
//     exclude and a set of category switches.
//   - DialogFilterChatlist is a shared folder somebody sent a link to. It has
//     no category switches at all — membership is the explicit list — which
//     is why its peers are read but its flags are not.
//   - DialogFilterDefault is "All chats", the pseudo-folder that means no
//     filtering. It is dropped here rather than represented: the UI already
//     has a tab for the unfiltered list and two of them would be one too
//     many.
func (f *FoldersFetcher) Fetch(ctx context.Context) ([]domain.Folder, error) {
	if f == nil || f.api == nil {
		return nil, fmt.Errorf("folders: no MTProto client")
	}
	res, err := f.api.MessagesGetDialogFilters(ctx)
	if err != nil {
		return nil, fmt.Errorf("folders: messages.getDialogFilters: %w", err)
	}

	out := make([]domain.Folder, 0, len(res.Filters))
	for _, raw := range res.Filters {
		switch fl := raw.(type) {
		case *tg.DialogFilter:
			out = append(out, domain.Folder{
				ID:              int64(fl.ID),
				Title:           fl.Title.Text,
				Emoticon:        fl.Emoticon,
				Pinned:          peerIDs(fl.PinnedPeers),
				Include:         peerIDs(fl.IncludePeers),
				Exclude:         peerIDs(fl.ExcludePeers),
				Contacts:        fl.Contacts,
				NonContacts:     fl.NonContacts,
				Groups:          fl.Groups,
				Broadcasts:      fl.Broadcasts,
				Bots:            fl.Bots,
				ExcludeMuted:    fl.ExcludeMuted,
				ExcludeRead:     fl.ExcludeRead,
				ExcludeArchived: fl.ExcludeArchived,
			})
		case *tg.DialogFilterChatlist:
			out = append(out, domain.Folder{
				ID:       int64(fl.ID),
				Title:    fl.Title.Text,
				Emoticon: fl.Emoticon,
				Pinned:   peerIDs(fl.PinnedPeers),
				Include:  peerIDs(fl.IncludePeers),
				// A shared folder is exactly its list. Leaving the category
				// switches false is not a gap: they do not exist on this
				// variant, and inventing them would add chats the person who
				// shared the folder did not put in it.
				ExplicitOnly: true,
			})
		}
	}
	return out, nil
}

// peerIDs flattens gotd's InputPeer variants into the local chat ids the rest
// of lazytg uses.
//
// The ids are taken bare, without the access hash, because that is how the
// chats table is keyed — the hash lives in the peers table and is looked up
// when a request needs one. A folder only ever answers "is this chat in it",
// which needs the id and nothing else.
func peerIDs(peers []tg.InputPeerClass) []int64 {
	if len(peers) == 0 {
		return nil
	}
	out := make([]int64, 0, len(peers))
	for _, p := range peers {
		switch typed := p.(type) {
		case *tg.InputPeerUser:
			out = append(out, typed.UserID)
		case *tg.InputPeerChat:
			out = append(out, typed.ChatID)
		case *tg.InputPeerChannel:
			out = append(out, typed.ChannelID)
		case *tg.InputPeerSelf:
			// Saved Messages. It has no id of its own in this list; the
			// caller resolves it from the account, and a folder that
			// contains it is rare enough not to warrant plumbing the
			// self id through for it.
			continue
		}
	}
	return out
}
