package tg

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	coresearch "github.com/kar43lov/lazytg/internal/core/search"
	coresync "github.com/kar43lov/lazytg/internal/core/sync"
)

// SearchAPI is the slice of tg.Client the searcher needs.
type SearchAPI interface {
	MessagesSearchGlobal(ctx context.Context, request *tg.MessagesSearchGlobalRequest) (tg.MessagesMessagesClass, error)
}

// Searcher asks the server for messages the local mirror does not have.
// One messages.searchGlobal per call, and the caller makes a call only
// when the user pressed the key for it — the local index answers
// everything else.
type Searcher struct {
	api  SearchAPI
	self *Self
}

// NewSearcher builds a Searcher over api. self tells outgoing messages
// in Saved Messages from incoming ones, the same way history does.
func NewSearcher(api SearchAPI, self *Self) *Searcher {
	return &Searcher{api: api, self: self}
}

// SearchGlobal runs one messages.searchGlobal for q.Text across every
// chat of the account and converts what came back. Chats and Peers
// describe the conversations the hits belong to, so the caller can
// list a chat the dialog sync never reached.
func (s *Searcher) SearchGlobal(ctx context.Context, q coresearch.RemoteQuery) (coresearch.RemoteResult, error) {
	req := &tg.MessagesSearchGlobalRequest{
		Q:          q.Text,
		Filter:     &tg.InputMessagesFilterEmpty{},
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      q.Limit,
	}
	if !q.After.IsZero() {
		req.MinDate = int(q.After.Unix())
	}
	if !q.Before.IsZero() {
		req.MaxDate = int(q.Before.Unix())
	}
	res, err := s.api.MessagesSearchGlobal(ctx, req)
	if err != nil {
		if d, ok := tgerr.AsFloodWait(err); ok {
			return coresearch.RemoteResult{}, &coresync.FloodWaitError{RetryAfter: d}
		}
		return coresearch.RemoteResult{}, fmt.Errorf("messages.searchGlobal: %w", err)
	}
	mod, ok := res.AsModified()
	if !ok {
		return coresearch.RemoteResult{}, nil
	}
	users := make(map[int64]*tg.User, len(mod.GetUsers()))
	for _, u := range mod.GetUsers() {
		if user, ok := u.(*tg.User); ok {
			users[user.ID] = user
		}
	}
	chats := make(map[int64]tg.ChatClass, len(mod.GetChats()))
	for _, c := range mod.GetChats() {
		chats[c.GetID()] = c
	}
	dir := directoryOf(mod.GetUsers(), mod.GetChats())

	var out coresearch.RemoteResult
	seen := make(map[int64]bool)
	for _, mc := range mod.GetMessages() {
		m, ok := mc.(*tg.Message)
		if !ok {
			continue
		}
		chatID := chatIDFromPeer(m.PeerID)
		if chatID == 0 {
			continue
		}
		out.Messages = append(out.Messages, convertMessage(m, chatID, s.self, dir))
		if seen[chatID] {
			continue
		}
		seen[chatID] = true
		if chat, peer, ok := resolvePeer(m.PeerID, users, chats); ok {
			out.Chats = append(out.Chats, chat)
			out.Peers = append(out.Peers, peer)
		}
	}
	return out, nil
}
