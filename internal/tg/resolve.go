package tg

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/kar43lov/lazytg/internal/core/domain"
	coresync "github.com/kar43lov/lazytg/internal/core/sync"
)

// A conversation that is not in the list yet — a person, a channel, a
// group — is reached the way every client reaches it: by the public
// handle. contacts.resolveUsername answers with the object and its access
// hash, which is everything the client needs to address it afterwards.
// One request per explicit ask, never for a name the user did not type.

// UsernameAPI is the one call the resolver needs.
type UsernameAPI interface {
	ContactsResolveUsername(ctx context.Context, request *tg.ContactsResolveUsernameRequest) (*tg.ContactsResolvedPeer, error)
}

// UsernameResolver turns a public handle into a chat and its peer.
type UsernameResolver struct {
	api UsernameAPI
}

// NewUsernameResolver wraps the API.
func NewUsernameResolver(api UsernameAPI) *UsernameResolver {
	return &UsernameResolver{api: api}
}

// ResolveUsername asks the server who holds the handle. The leading "@" is
// accepted and ignored.
func (r *UsernameResolver) ResolveUsername(ctx context.Context, name string) (domain.Chat, domain.Peer, error) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "@")
	if name == "" {
		return domain.Chat{}, domain.Peer{}, errors.New("resolve: empty username")
	}
	res, err := r.api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: name})
	if err != nil {
		if d, ok := tgerr.AsFloodWait(err); ok {
			return domain.Chat{}, domain.Peer{}, &coresync.FloodWaitError{RetryAfter: d}
		}
		if tgerr.Is(err, "USERNAME_NOT_OCCUPIED", "USERNAME_INVALID") {
			return domain.Chat{}, domain.Peer{}, fmt.Errorf("@%s: %w", name, coresync.ErrNoSuchUsername)
		}
		return domain.Chat{}, domain.Peer{}, fmt.Errorf("contacts.resolveUsername @%s: %w", name, err)
	}
	users := make(map[int64]*tg.User, len(res.Users))
	for _, u := range res.Users {
		if user, ok := u.(*tg.User); ok {
			users[user.ID] = user
		}
	}
	chats := make(map[int64]tg.ChatClass, len(res.Chats))
	for _, c := range res.Chats {
		chats[c.GetID()] = c
	}
	chat, peer, ok := resolvePeer(res.Peer, users, chats)
	if !ok {
		return domain.Chat{}, domain.Peer{}, fmt.Errorf("resolve @%s: the server named a peer it did not describe", name)
	}
	return chat, peer, nil
}
