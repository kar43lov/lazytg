package tg

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/kar43lov/lazytg/internal/core/domain"
	coresync "github.com/kar43lov/lazytg/internal/core/sync"
)

// MessagesGetDialogsClient is the subset of *tg.Client that DialogsFetcher
// needs, declared here so unit tests can substitute a stub instead of standing
// up tgtest.
type MessagesGetDialogsClient interface {
	MessagesGetDialogs(ctx context.Context, request *tg.MessagesGetDialogsRequest) (tg.MessagesDialogsClass, error)
}

// DialogsFetcher loads the chat list from Telegram. It satisfies
// coresync.DialogsProvider so internal/core stays free of gotd types.
type DialogsFetcher struct {
	api MessagesGetDialogsClient
}

// NewDialogsFetcher returns a DialogsFetcher talking to api — Client.API() in
// production, a stub in tests.
func NewDialogsFetcher(api MessagesGetDialogsClient) *DialogsFetcher {
	return &DialogsFetcher{api: api}
}

// FetchDialogs returns one page of the dialog list.
//
// The response carries dialogs, their top messages, and the chat/user objects
// referenced by both. Titles and access hashes live only in those side lists,
// so decoding means joining three collections rather than reading dialogs
// alone — a dialog on its own is just a peer id and a counter.
func (d *DialogsFetcher) FetchDialogs(ctx context.Context, limit int, cursor coresync.DialogCursor) (coresync.DialogPage, error) {
	offsetPeer, err := cursorPeer(cursor)
	if err != nil {
		return coresync.DialogPage{}, err
	}

	res, err := d.api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
		Limit:      limit,
		OffsetDate: cursor.Date,
		OffsetID:   cursor.ID,
		OffsetPeer: offsetPeer,
	})
	if err != nil {
		if wait, ok := tgerr.AsFloodWait(err); ok {
			return coresync.DialogPage{}, &coresync.FloodWaitError{RetryAfter: wait}
		}
		return coresync.DialogPage{}, fmt.Errorf("messages.getDialogs: %w", err)
	}

	mod, ok := res.AsModified()
	if !ok {
		// DialogsNotModified answers a hash-based cache request. lazytg never
		// sends a hash, so this is unreachable in practice; treat it as "no
		// new data" rather than an error.
		return coresync.DialogPage{}, nil
	}
	return decodeDialogs(mod, limit), nil
}

// cursorPeer turns a paging cursor into the InputPeer that getDialogs expects.
// A zero cursor starts at the top of the list.
func cursorPeer(cursor coresync.DialogCursor) (tg.InputPeerClass, error) {
	if cursor.IsZero() {
		return &tg.InputPeerEmpty{}, nil
	}
	peer, err := buildInputPeer(cursor.PeerID, cursor.PeerAccessHash, cursor.PeerType)
	if err != nil {
		return nil, fmt.Errorf("dialog cursor: %w", err)
	}
	return peer, nil
}

// decodeDialogs joins dialogs with their chat/user metadata and top-message
// dates into domain values.
func decodeDialogs(mod tg.ModifiedMessagesDialogs, limit int) coresync.DialogPage {
	var (
		users = indexUsers(mod.GetUsers())
		chats = indexChats(mod.GetChats())
		dates = indexTopMessageDates(mod.GetMessages())
		raw   = mod.GetDialogs()
	)

	page := coresync.DialogPage{
		Chats: make([]domain.Chat, 0, len(raw)),
		Peers: make([]domain.Peer, 0, len(raw)),
	}

	// The cursor must describe one single dialog — the last one we actually
	// decoded — so it is captured inside the loop. Assembling it afterwards
	// from separate sources mismatched the parts: the peer came from the last
	// decoded chat while the message id came from the last raw dialog, which
	// differ whenever the final entry was skipped, and Telegram answers an
	// inconsistent position with skipped or repeated pages.
	var last struct {
		peer       domain.Peer
		topMessage int
		date       time.Time
		valid      bool
	}

	for _, dc := range raw {
		dlg, ok := dc.(*tg.Dialog)
		if !ok {
			// DialogFolder — the archive pseudo-dialog. Its contents are not
			// reachable through this call, and rendering a folder as a chat
			// would be a dead row in the list.
			continue
		}
		chat, peer, ok := resolveDialog(dlg, users, chats, dates)
		if !ok {
			continue
		}
		page.Chats = append(page.Chats, chat)
		page.Peers = append(page.Peers, peer)

		last.peer = peer
		last.topMessage = dlg.TopMessage
		last.date = chat.LastMessageDate
		last.valid = true
	}

	// Paging needs all three parts of the position. A dialog whose top message
	// is missing from the response has a zero date, and int(time.Time{}.Unix())
	// is -62135596800 — Telegram treats that as "from the beginning", so the
	// walk would fetch page one forever. Stopping here costs the tail of a very
	// large account; continuing would spin.
	if dialogsHaveMore(mod, limit) && last.valid && !last.date.IsZero() {
		page.HasMore = true
		page.Next = coresync.DialogCursor{
			Date:           int(last.date.Unix()),
			ID:             last.topMessage,
			PeerID:         last.peer.ID,
			PeerAccessHash: last.peer.AccessHash,
			PeerType:       string(last.peer.Type),
		}
	}
	return page
}

// resolveDialog maps one dialog onto a domain Chat plus the Peer needed to
// address it later. Returns false when the referenced user/chat object is
// absent from the response — a malformed page rather than something to guess
// at, since inventing an access hash would produce a chat that errors on open.
func resolveDialog(
	dlg *tg.Dialog,
	users map[int64]*tg.User,
	chats map[int64]tg.ChatClass,
	dates map[peerMessageKey]time.Time,
) (domain.Chat, domain.Peer, bool) {
	chat := domain.Chat{
		UnreadCount: dlg.UnreadCount,
		Pinned:      dlg.Pinned,
		UnreadMark:  dlg.UnreadMark,
		MutedUntil:  muteUntilOf(dlg.NotifySettings),
	}
	var peer domain.Peer

	switch p := dlg.Peer.(type) {
	case *tg.PeerUser:
		u, ok := users[p.UserID]
		if !ok {
			return domain.Chat{}, domain.Peer{}, false
		}
		chat.ID = p.UserID
		chat.Type = domain.ChatTypePrivate
		chat.Title = userTitle(u)
		chat.Online, chat.LastSeen = presenceOf(u.Status)
		if u.Self {
			// The dialog with yourself. Every official client names it
			// rather than showing the account's own name twice, and none
			// tells you when you were last seen.
			chat.Title = SavedMessagesTitle
			chat.Online, chat.LastSeen = false, time.Time{}
		}
		chat.Username = u.Username
		peer = domain.Peer{ID: p.UserID, Type: domain.ChatTypePrivate, AccessHash: u.AccessHash}

	case *tg.PeerChat:
		c, ok := chats[p.ChatID]
		if !ok {
			return domain.Chat{}, domain.Peer{}, false
		}
		basic, ok := c.(*tg.Chat)
		if !ok {
			return domain.Chat{}, domain.Peer{}, false
		}
		chat.ID = p.ChatID
		chat.Type = domain.ChatTypeGroup
		chat.Title = basic.Title
		// Basic groups have no access hash — InputPeerChat takes the id alone.
		peer = domain.Peer{ID: p.ChatID, Type: domain.ChatTypeGroup}

	case *tg.PeerChannel:
		c, ok := chats[p.ChannelID]
		if !ok {
			return domain.Chat{}, domain.Peer{}, false
		}
		ch, ok := c.(*tg.Channel)
		if !ok {
			return domain.Chat{}, domain.Peer{}, false
		}
		kind := domain.ChatTypeChannel
		if ch.Megagroup {
			kind = domain.ChatTypeSupergroup
		}
		chat.ID = p.ChannelID
		chat.Type = kind
		chat.Title = ch.Title
		chat.Username = ch.Username
		peer = domain.Peer{ID: p.ChannelID, Type: kind, AccessHash: ch.AccessHash}

	default:
		return domain.Chat{}, domain.Peer{}, false
	}

	if ts, ok := dates[peerMessageKey{peerID: chat.ID, msgID: dlg.TopMessage}]; ok {
		chat.LastMessageDate = ts
	}
	return chat, peer, true
}

// userTitle builds a display name for a private dialog. Telegram allows both
// name fields to be empty (deleted accounts, some bots), so the fallback chain
// ends at the numeric id — an empty row in the chat list is worse than an ugly
// one.
func userTitle(u *tg.User) string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name != "" {
		return name
	}
	if u.Username != "" {
		return "@" + u.Username
	}
	if u.Deleted {
		return "Deleted account"
	}
	return "user " + strconv.FormatInt(u.ID, 10)
}

func indexUsers(raw []tg.UserClass) map[int64]*tg.User {
	out := make(map[int64]*tg.User, len(raw))
	for _, uc := range raw {
		if u, ok := uc.(*tg.User); ok {
			out[u.ID] = u
		}
	}
	return out
}

func indexChats(raw []tg.ChatClass) map[int64]tg.ChatClass {
	out := make(map[int64]tg.ChatClass, len(raw))
	for _, cc := range raw {
		switch c := cc.(type) {
		case *tg.Chat:
			out[c.ID] = c
		case *tg.Channel:
			out[c.ID] = c
		case *tg.ChatForbidden:
			out[c.ID] = c
		case *tg.ChannelForbidden:
			out[c.ID] = c
		}
	}
	return out
}

// peerMessageKey scopes a message id to its peer. Message ids are unique per
// chat, not globally, so a bare id would collide across dialogs and hand the
// wrong timestamp to the wrong chat.
type peerMessageKey struct {
	peerID int64
	msgID  int
}

func indexTopMessageDates(raw []tg.MessageClass) map[peerMessageKey]time.Time {
	out := make(map[peerMessageKey]time.Time, len(raw))
	for _, mc := range raw {
		switch m := mc.(type) {
		case *tg.Message:
			out[peerMessageKey{peerIDToInt64(m.PeerID), m.ID}] = time.Unix(int64(m.Date), 0).UTC()
		case *tg.MessageService:
			// A service message ("X joined the group") can legitimately be a
			// dialog's top message; its date still orders the chat list.
			out[peerMessageKey{peerIDToInt64(m.PeerID), m.ID}] = time.Unix(int64(m.Date), 0).UTC()
		}
	}
	return out
}

// dialogsHaveMore reports whether another page may exist. MessagesDialogs is
// the complete list by definition; for a slice we fall back to "the page came
// back full", which is safer than trusting the declared Count.
func dialogsHaveMore(mod tg.ModifiedMessagesDialogs, limit int) bool {
	if _, full := mod.(*tg.MessagesDialogs); full {
		return false
	}
	return limit > 0 && len(mod.GetDialogs()) >= limit
}

// SavedMessagesTitle is what the dialog with the account itself is called.
// The name every official client uses, so a user looking for it finds it
// under the words they already know.
const SavedMessagesTitle = "Saved Messages"

// UsersGetUsersClient is the one call SelfDialog needs beyond the dialog
// list. Optional on the fetcher's API: the tests' dialog stubs do not
// implement it, and the production client does.
type UsersGetUsersClient interface {
	UsersGetUsers(ctx context.Context, id []tg.InputUserClass) ([]tg.UserClass, error)
}

// SelfDialog describes the account's own chat — Saved Messages — whether or
// not the server lists it. Telegram returns that dialog only once something
// has been written there, and every official client shows it regardless,
// because it is where people keep the things they send themselves. One
// request, at sync, against the account's own record.
func (d *DialogsFetcher) SelfDialog(ctx context.Context) (domain.Chat, domain.Peer, error) {
	api, ok := d.api.(UsersGetUsersClient)
	if !ok {
		return domain.Chat{}, domain.Peer{}, errors.New("self dialog: users.getUsers is not available on this client")
	}
	users, err := api.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUserSelf{}})
	if err != nil {
		if wait, ok := tgerr.AsFloodWait(err); ok {
			return domain.Chat{}, domain.Peer{}, &coresync.FloodWaitError{RetryAfter: wait}
		}
		return domain.Chat{}, domain.Peer{}, fmt.Errorf("users.getUsers(self): %w", err)
	}
	if len(users) == 0 {
		return domain.Chat{}, domain.Peer{}, errors.New("self dialog: the server returned no user")
	}
	u, ok := users[0].(*tg.User)
	if !ok {
		return domain.Chat{}, domain.Peer{}, fmt.Errorf("self dialog: unexpected %T", users[0])
	}
	chat := domain.Chat{ID: u.ID, Type: domain.ChatTypePrivate, Title: SavedMessagesTitle, Username: u.Username}
	peer := domain.Peer{ID: u.ID, Type: domain.ChatTypePrivate, AccessHash: u.AccessHash}
	return chat, peer, nil
}

// muteUntilOf reads when notifications resume off a dialog's settings. Zero
// when the dialog is not muted, and a date in 2038 for "forever", stored as
// it comes so a later unmute from the server and a local one agree.
func muteUntilOf(ns tg.PeerNotifySettings) time.Time {
	until, ok := ns.GetMuteUntil()
	if !ok || until <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(until), 0).UTC()
}

// presenceOf reads a user's status: online now, or when they were last —
// zero when Telegram says only "recently" or nothing, which is what it says
// for people who hide it, and the list shows nothing rather than guessing.
func presenceOf(status tg.UserStatusClass) (online bool, lastSeen time.Time) {
	switch s := status.(type) {
	case *tg.UserStatusOnline:
		return true, time.Time{}
	case *tg.UserStatusOffline:
		if s.WasOnline > 0 {
			return false, time.Unix(int64(s.WasOnline), 0).UTC()
		}
	}
	return false, time.Time{}
}
