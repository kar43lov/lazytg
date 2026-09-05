package tg

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/kar43lov/lazytg/internal/core/domain"
	coresync "github.com/kar43lov/lazytg/internal/core/sync"
)

// stubGetDialogs replays a scripted sequence of getDialogs responses, one per
// call, and records the requests so pagination can be asserted. Same rationale
// as stubGetHistory: mocking the high-level client is faster and more focused
// than standing up gotd/td/tgtest.
type stubGetDialogs struct {
	responses []tg.MessagesDialogsClass
	err       error
	calls     []*tg.MessagesGetDialogsRequest
}

func (s *stubGetDialogs) MessagesGetDialogs(_ context.Context, req *tg.MessagesGetDialogsRequest) (tg.MessagesDialogsClass, error) {
	s.calls = append(s.calls, req)
	if s.err != nil {
		return nil, s.err
	}
	idx := len(s.calls) - 1
	if idx < len(s.responses) {
		return s.responses[idx], nil
	}
	return &tg.MessagesDialogs{}, nil
}

func dialogAt(peer tg.PeerClass, topMessage, unread int, pinned bool) tg.DialogClass {
	return &tg.Dialog{
		Peer:        peer,
		TopMessage:  topMessage,
		UnreadCount: unread,
		Pinned:      pinned,
	}
}

func topMessage(id int, peer tg.PeerClass, at time.Time) tg.MessageClass {
	return &tg.Message{ID: id, PeerID: peer, Date: int(at.Unix())}
}

func TestDialogsFetcher_DecodesEveryPeerKind(t *testing.T) {
	t.Parallel()

	when := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	api := &stubGetDialogs{responses: []tg.MessagesDialogsClass{
		&tg.MessagesDialogs{
			Dialogs: []tg.DialogClass{
				dialogAt(&tg.PeerUser{UserID: 11}, 100, 3, true),
				dialogAt(&tg.PeerChat{ChatID: 22}, 200, 0, false),
				dialogAt(&tg.PeerChannel{ChannelID: 33}, 300, 7, false),
				dialogAt(&tg.PeerChannel{ChannelID: 44}, 400, 0, false),
			},
			Messages: []tg.MessageClass{
				topMessage(100, &tg.PeerUser{UserID: 11}, when),
				topMessage(200, &tg.PeerChat{ChatID: 22}, when.Add(-time.Hour)),
				topMessage(300, &tg.PeerChannel{ChannelID: 33}, when.Add(-2*time.Hour)),
				topMessage(400, &tg.PeerChannel{ChannelID: 44}, when.Add(-3*time.Hour)),
			},
			Users: []tg.UserClass{
				&tg.User{ID: 11, AccessHash: 1111, FirstName: "Алёна", LastName: "Петрова", Username: "alena"},
			},
			Chats: []tg.ChatClass{
				&tg.Chat{ID: 22, Title: "Family"},
				&tg.Channel{ID: 33, AccessHash: 3333, Title: "Dev talk", Username: "devtalk", Megagroup: true},
				&tg.Channel{ID: 44, AccessHash: 4444, Title: "News", Broadcast: true},
			},
		},
	}}

	page, err := NewDialogsFetcher(api).FetchDialogs(context.Background(), 100, coresync.DialogCursor{})
	if err != nil {
		t.Fatalf("FetchDialogs: %v", err)
	}
	if len(page.Chats) != 4 || len(page.Peers) != 4 {
		t.Fatalf("want 4 chats and 4 peers, got %d/%d", len(page.Chats), len(page.Peers))
	}

	want := []domain.Chat{
		{ID: 11, Type: domain.ChatTypePrivate, Title: "Алёна Петрова", Username: "alena", LastMessageDate: when, UnreadCount: 3, Pinned: true},
		{ID: 22, Type: domain.ChatTypeGroup, Title: "Family", LastMessageDate: when.Add(-time.Hour)},
		{ID: 33, Type: domain.ChatTypeSupergroup, Title: "Dev talk", Username: "devtalk", LastMessageDate: when.Add(-2 * time.Hour), UnreadCount: 7},
		{ID: 44, Type: domain.ChatTypeChannel, Title: "News", LastMessageDate: when.Add(-3 * time.Hour)},
	}
	for i, w := range want {
		if page.Chats[i] != w {
			t.Errorf("chat[%d]:\n got %+v\nwant %+v", i, page.Chats[i], w)
		}
	}

	// Access hashes must survive: without them history and send cannot build
	// an InputPeer, so a chat would appear in the list and fail on open.
	wantPeers := []domain.Peer{
		{ID: 11, Type: domain.ChatTypePrivate, AccessHash: 1111},
		{ID: 22, Type: domain.ChatTypeGroup},
		{ID: 33, Type: domain.ChatTypeSupergroup, AccessHash: 3333},
		{ID: 44, Type: domain.ChatTypeChannel, AccessHash: 4444},
	}
	for i, w := range wantPeers {
		if page.Peers[i] != w {
			t.Errorf("peer[%d]:\n got %+v\nwant %+v", i, page.Peers[i], w)
		}
	}
}

// A full MessagesDialogs response is the complete list by definition, so the
// walk must stop even when the page happens to be exactly `limit` long.
func TestDialogsFetcher_FullResponseReportsNoMorePages(t *testing.T) {
	t.Parallel()

	api := &stubGetDialogs{responses: []tg.MessagesDialogsClass{
		&tg.MessagesDialogs{
			Dialogs:  []tg.DialogClass{dialogAt(&tg.PeerUser{UserID: 1}, 5, 0, false)},
			Messages: []tg.MessageClass{topMessage(5, &tg.PeerUser{UserID: 1}, time.Unix(1_700_000_000, 0).UTC())},
			Users:    []tg.UserClass{&tg.User{ID: 1, AccessHash: 9, FirstName: "Solo"}},
		},
	}}

	page, err := NewDialogsFetcher(api).FetchDialogs(context.Background(), 1, coresync.DialogCursor{})
	if err != nil {
		t.Fatalf("FetchDialogs: %v", err)
	}
	if page.HasMore {
		t.Fatalf("MessagesDialogs is complete by definition, HasMore must be false")
	}
	if !page.Next.IsZero() {
		t.Fatalf("no next page means no cursor, got %+v", page.Next)
	}
}

// A slice response that filled the page yields a cursor built from the last
// dialog — Telegram pages by position, not by numeric offset.
func TestDialogsFetcher_SliceBuildsCursorFromLastDialog(t *testing.T) {
	t.Parallel()

	newest := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	oldest := newest.Add(-6 * time.Hour)
	api := &stubGetDialogs{responses: []tg.MessagesDialogsClass{
		&tg.MessagesDialogsSlice{
			Count: 500,
			Dialogs: []tg.DialogClass{
				dialogAt(&tg.PeerUser{UserID: 7}, 70, 0, false),
				dialogAt(&tg.PeerChannel{ChannelID: 8}, 80, 0, false),
			},
			Messages: []tg.MessageClass{
				topMessage(70, &tg.PeerUser{UserID: 7}, newest),
				topMessage(80, &tg.PeerChannel{ChannelID: 8}, oldest),
			},
			Users: []tg.UserClass{&tg.User{ID: 7, AccessHash: 77, FirstName: "Seven"}},
			Chats: []tg.ChatClass{&tg.Channel{ID: 8, AccessHash: 88, Title: "Eight", Megagroup: true}},
		},
	}}

	page, err := NewDialogsFetcher(api).FetchDialogs(context.Background(), 2, coresync.DialogCursor{})
	if err != nil {
		t.Fatalf("FetchDialogs: %v", err)
	}
	if !page.HasMore {
		t.Fatalf("a full slice page must report HasMore")
	}
	want := coresync.DialogCursor{
		Date:           int(oldest.Unix()),
		ID:             80,
		PeerID:         8,
		PeerAccessHash: 88,
		PeerType:       string(domain.ChatTypeSupergroup),
	}
	if page.Next != want {
		t.Fatalf("cursor:\n got %+v\nwant %+v", page.Next, want)
	}
}

// The cursor has to reach the wire, otherwise page two repeats page one and the
// walk spins on the same dialogs until it hits the page cap.
func TestDialogsFetcher_SendsCursorOnTheWire(t *testing.T) {
	t.Parallel()

	api := &stubGetDialogs{responses: []tg.MessagesDialogsClass{&tg.MessagesDialogs{}}}
	cursor := coresync.DialogCursor{
		Date:           1_700_000_000,
		ID:             42,
		PeerID:         555,
		PeerAccessHash: 666,
		PeerType:       string(domain.ChatTypePrivate),
	}

	if _, err := NewDialogsFetcher(api).FetchDialogs(context.Background(), 50, cursor); err != nil {
		t.Fatalf("FetchDialogs: %v", err)
	}
	if len(api.calls) != 1 {
		t.Fatalf("want 1 call, got %d", len(api.calls))
	}
	req := api.calls[0]
	if req.OffsetDate != cursor.Date || req.OffsetID != cursor.ID || req.Limit != 50 {
		t.Errorf("offsets: got date=%d id=%d limit=%d", req.OffsetDate, req.OffsetID, req.Limit)
	}
	peer, ok := req.OffsetPeer.(*tg.InputPeerUser)
	if !ok {
		t.Fatalf("want InputPeerUser for a private cursor, got %T", req.OffsetPeer)
	}
	if peer.UserID != 555 || peer.AccessHash != 666 {
		t.Errorf("offset peer: got id=%d hash=%d", peer.UserID, peer.AccessHash)
	}
}

func TestDialogsFetcher_FirstCallStartsAtTop(t *testing.T) {
	t.Parallel()

	api := &stubGetDialogs{responses: []tg.MessagesDialogsClass{&tg.MessagesDialogs{}}}
	if _, err := NewDialogsFetcher(api).FetchDialogs(context.Background(), 10, coresync.DialogCursor{}); err != nil {
		t.Fatalf("FetchDialogs: %v", err)
	}
	if _, ok := api.calls[0].OffsetPeer.(*tg.InputPeerEmpty); !ok {
		t.Fatalf("a zero cursor must send InputPeerEmpty, got %T", api.calls[0].OffsetPeer)
	}
}

// A dialog whose user or chat object is missing from the response cannot be
// addressed later — inventing an access hash would surface a chat that errors
// the moment it is opened.
func TestDialogsFetcher_SkipsDialogsWithoutMetadata(t *testing.T) {
	t.Parallel()

	api := &stubGetDialogs{responses: []tg.MessagesDialogsClass{
		&tg.MessagesDialogs{
			Dialogs: []tg.DialogClass{
				dialogAt(&tg.PeerUser{UserID: 1}, 10, 0, false),       // user present
				dialogAt(&tg.PeerUser{UserID: 2}, 20, 0, false),       // user absent
				dialogAt(&tg.PeerChannel{ChannelID: 3}, 30, 0, false), // channel absent
				&tg.DialogFolder{}, // archive pseudo-dialog
			},
			Users: []tg.UserClass{&tg.User{ID: 1, AccessHash: 11, FirstName: "Present"}},
		},
	}}

	page, err := NewDialogsFetcher(api).FetchDialogs(context.Background(), 100, coresync.DialogCursor{})
	if err != nil {
		t.Fatalf("FetchDialogs: %v", err)
	}
	if len(page.Chats) != 1 || page.Chats[0].ID != 1 {
		t.Fatalf("want only the resolvable dialog, got %+v", page.Chats)
	}
}

// A dialog with no cached top message still belongs in the list; it just sorts
// with a zero date rather than disappearing.
func TestDialogsFetcher_MissingTopMessageLeavesZeroDate(t *testing.T) {
	t.Parallel()

	api := &stubGetDialogs{responses: []tg.MessagesDialogsClass{
		&tg.MessagesDialogs{
			Dialogs: []tg.DialogClass{dialogAt(&tg.PeerUser{UserID: 5}, 999, 0, false)},
			Users:   []tg.UserClass{&tg.User{ID: 5, AccessHash: 55, FirstName: "Quiet"}},
		},
	}}

	page, err := NewDialogsFetcher(api).FetchDialogs(context.Background(), 100, coresync.DialogCursor{})
	if err != nil {
		t.Fatalf("FetchDialogs: %v", err)
	}
	if len(page.Chats) != 1 {
		t.Fatalf("want the chat kept, got %d", len(page.Chats))
	}
	if !page.Chats[0].LastMessageDate.IsZero() {
		t.Fatalf("want zero date, got %v", page.Chats[0].LastMessageDate)
	}
}

// Message ids are unique per chat, not globally: two dialogs sharing a top
// message id must not inherit each other's timestamp.
func TestDialogsFetcher_TopMessageDatesAreScopedToPeer(t *testing.T) {
	t.Parallel()

	first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	second := time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)
	api := &stubGetDialogs{responses: []tg.MessagesDialogsClass{
		&tg.MessagesDialogs{
			Dialogs: []tg.DialogClass{
				dialogAt(&tg.PeerUser{UserID: 1}, 7, 0, false),
				dialogAt(&tg.PeerUser{UserID: 2}, 7, 0, false),
			},
			Messages: []tg.MessageClass{
				topMessage(7, &tg.PeerUser{UserID: 1}, first),
				topMessage(7, &tg.PeerUser{UserID: 2}, second),
			},
			Users: []tg.UserClass{
				&tg.User{ID: 1, AccessHash: 11, FirstName: "One"},
				&tg.User{ID: 2, AccessHash: 22, FirstName: "Two"},
			},
		},
	}}

	page, err := NewDialogsFetcher(api).FetchDialogs(context.Background(), 100, coresync.DialogCursor{})
	if err != nil {
		t.Fatalf("FetchDialogs: %v", err)
	}
	if !page.Chats[0].LastMessageDate.Equal(first) || !page.Chats[1].LastMessageDate.Equal(second) {
		t.Fatalf("dates crossed over: %v / %v", page.Chats[0].LastMessageDate, page.Chats[1].LastMessageDate)
	}
}

// The cursor describes a position, so all of its parts must come from the same
// dialog. Building it from separate sources mismatched them when the final
// dialog was skipped — the peer came from the last decoded chat, the message id
// from the last raw entry — and Telegram answers an inconsistent position by
// skipping or repeating pages.
func TestDialogsFetcher_CursorPartsComeFromOneDialog(t *testing.T) {
	t.Parallel()

	when := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	api := &stubGetDialogs{responses: []tg.MessagesDialogsClass{
		&tg.MessagesDialogsSlice{
			Count: 500,
			Dialogs: []tg.DialogClass{
				dialogAt(&tg.PeerUser{UserID: 7}, 70, 0, false),
				dialogAt(&tg.PeerUser{UserID: 999}, 990, 0, false), // user absent from the response
			},
			Messages: []tg.MessageClass{topMessage(70, &tg.PeerUser{UserID: 7}, when)},
			Users:    []tg.UserClass{&tg.User{ID: 7, AccessHash: 77, FirstName: "Seven"}},
		},
	}}

	page, err := NewDialogsFetcher(api).FetchDialogs(context.Background(), 2, coresync.DialogCursor{})
	if err != nil {
		t.Fatalf("FetchDialogs: %v", err)
	}
	if !page.HasMore {
		t.Fatalf("a full slice page must still report HasMore")
	}
	want := coresync.DialogCursor{
		Date:           int(when.Unix()),
		ID:             70,
		PeerID:         7,
		PeerAccessHash: 77,
		PeerType:       string(domain.ChatTypePrivate),
	}
	if page.Next != want {
		t.Fatalf("cursor:\n got %+v\nwant %+v", page.Next, want)
	}
}

// int(time.Time{}.Unix()) is -62135596800, which Telegram reads as "from the
// beginning" — paging on it re-fetches page one until the page cap stops the
// walk. Ending the walk loses the tail of a very large account; continuing
// would spin on the same dialogs.
func TestDialogsFetcher_MissingDateOnLastDialogEndsPaging(t *testing.T) {
	t.Parallel()

	api := &stubGetDialogs{responses: []tg.MessagesDialogsClass{
		&tg.MessagesDialogsSlice{
			Count:   500,
			Dialogs: []tg.DialogClass{dialogAt(&tg.PeerUser{UserID: 7}, 70, 0, false)},
			Users:   []tg.UserClass{&tg.User{ID: 7, AccessHash: 77, FirstName: "Seven"}},
			// No Messages: the top message is absent, so the date is zero.
		},
	}}

	page, err := NewDialogsFetcher(api).FetchDialogs(context.Background(), 1, coresync.DialogCursor{})
	if err != nil {
		t.Fatalf("FetchDialogs: %v", err)
	}
	if page.HasMore {
		t.Fatalf("a dateless last dialog cannot produce a valid cursor, HasMore must be false")
	}
	if page.Next.Date < 0 {
		t.Fatalf("negative offset_date leaked into the cursor: %d", page.Next.Date)
	}
	if len(page.Chats) != 1 {
		t.Fatalf("the chat itself must still be returned, got %d", len(page.Chats))
	}
}

// Every decoded dialog was skipped: there is no position to page from, so the
// walk has to end rather than re-request the same page.
func TestDialogsFetcher_AllDialogsSkippedEndsPaging(t *testing.T) {
	t.Parallel()

	api := &stubGetDialogs{responses: []tg.MessagesDialogsClass{
		&tg.MessagesDialogsSlice{
			Count:   500,
			Dialogs: []tg.DialogClass{dialogAt(&tg.PeerUser{UserID: 1}, 10, 0, false)},
			// Users omitted entirely — nothing resolves.
		},
	}}

	page, err := NewDialogsFetcher(api).FetchDialogs(context.Background(), 1, coresync.DialogCursor{})
	if err != nil {
		t.Fatalf("FetchDialogs: %v", err)
	}
	if page.HasMore || !page.Next.IsZero() {
		t.Fatalf("no decoded dialog means no cursor: HasMore=%v Next=%+v", page.HasMore, page.Next)
	}
}

func TestDialogsFetcher_TranslatesFloodWait(t *testing.T) {
	t.Parallel()

	api := &stubGetDialogs{err: tgerr.New(420, "FLOOD_WAIT_11")}
	_, err := NewDialogsFetcher(api).FetchDialogs(context.Background(), 100, coresync.DialogCursor{})

	var flood *coresync.FloodWaitError
	if !errors.As(err, &flood) {
		t.Fatalf("want a FloodWaitError, got %v", err)
	}
	if flood.RetryAfter != 11*time.Second {
		t.Fatalf("want 11s retry, got %v", flood.RetryAfter)
	}
}

func TestDialogsFetcher_WrapsTransportError(t *testing.T) {
	t.Parallel()

	boom := errors.New("connection reset")
	api := &stubGetDialogs{err: boom}
	_, err := NewDialogsFetcher(api).FetchDialogs(context.Background(), 100, coresync.DialogCursor{})
	if !errors.Is(err, boom) {
		t.Fatalf("want the transport error wrapped, got %v", err)
	}
}

func TestUserTitle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		user *tg.User
		want string
	}{
		{"first and last", &tg.User{FirstName: "Ада", LastName: "Лавлейс"}, "Ада Лавлейс"},
		{"first only", &tg.User{FirstName: "Ада"}, "Ада"},
		{"last only", &tg.User{LastName: "Лавлейс"}, "Лавлейс"},
		{"username fallback", &tg.User{Username: "ada"}, "@ada"},
		{"deleted account", &tg.User{ID: 5, Deleted: true}, "Deleted account"},
		{"nameless keeps id", &tg.User{ID: 42}, "user 42"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := userTitle(tc.user); got != tc.want {
				t.Fatalf("userTitle = %q, want %q", got, tc.want)
			}
		})
	}
}

// stubDialogsWithSelf is the dialog stub plus the one users call the self
// lookup needs.
type stubDialogsWithSelf struct {
	stubGetDialogs
	self *tg.User
	err  error
}

func (s *stubDialogsWithSelf) UsersGetUsers(_ context.Context, ids []tg.InputUserClass) ([]tg.UserClass, error) {
	if s.err != nil {
		return nil, s.err
	}
	if len(ids) != 1 {
		return nil, fmt.Errorf("asked for %d users, want exactly self", len(ids))
	}
	if _, ok := ids[0].(*tg.InputUserSelf); !ok {
		return nil, fmt.Errorf("asked for %T, want InputUserSelf", ids[0])
	}
	return []tg.UserClass{s.self}, nil
}

func TestDialogsFetcher_SelfDialogIsSavedMessages(t *testing.T) {
	t.Parallel()

	stub := &stubDialogsWithSelf{self: &tg.User{ID: 8385, AccessHash: 0xABC, FirstName: "Me", Username: "me", Self: true}}
	chat, peer, err := NewDialogsFetcher(stub).SelfDialog(context.Background())
	if err != nil {
		t.Fatalf("SelfDialog: %v", err)
	}
	if chat.ID != 8385 || chat.Title != SavedMessagesTitle || chat.Type != domain.ChatTypePrivate {
		t.Fatalf("chat = %+v", chat)
	}
	if peer.ID != 8385 || peer.AccessHash != 0xABC || peer.Type != domain.ChatTypePrivate {
		t.Fatalf("peer = %+v", peer)
	}
}

func TestDialogsFetcher_SelfDialogNeedsTheUsersCall(t *testing.T) {
	t.Parallel()

	if _, _, err := NewDialogsFetcher(&stubGetDialogs{}).SelfDialog(context.Background()); err == nil {
		t.Fatal("a client without users.getUsers reported a self dialog")
	}
}

// When the server does list the dialog with yourself, it is named the way
// every official client names it rather than after the account.
func TestDialogsFetcher_ListedSelfDialogIsSavedMessages(t *testing.T) {
	t.Parallel()

	me := &tg.User{ID: 8385, AccessHash: 1, FirstName: "Pavel", Self: true, Status: &tg.UserStatusOnline{Expires: 1}}
	at := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	stub := &stubGetDialogs{responses: []tg.MessagesDialogsClass{&tg.MessagesDialogs{
		Dialogs:  []tg.DialogClass{dialogAt(&tg.PeerUser{UserID: 8385}, 1, 0, false)},
		Messages: []tg.MessageClass{topMessage(1, &tg.PeerUser{UserID: 8385}, at)},
		Users:    []tg.UserClass{me},
	}}}
	page, err := NewDialogsFetcher(stub).FetchDialogs(context.Background(), 10, coresync.DialogCursor{})
	if err != nil {
		t.Fatalf("FetchDialogs: %v", err)
	}
	if len(page.Chats) != 1 || page.Chats[0].Title != SavedMessagesTitle {
		t.Fatalf("chats = %+v, want Saved Messages", page.Chats)
	}
	if page.Chats[0].Online || !page.Chats[0].LastSeen.IsZero() {
		t.Fatalf("the chat with yourself shows a presence: %+v", page.Chats[0])
	}
}

// Muted, marked unread, online, last seen: all four arrive on the dialog
// page and are kept.
func TestDialogsFetcher_KeepsTheListFacts(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	seen := at.Add(-time.Hour)
	friend := &tg.User{ID: 42, AccessHash: 1, FirstName: "Friend", Status: &tg.UserStatusOffline{WasOnline: int(seen.Unix())}}
	other := &tg.User{ID: 43, AccessHash: 1, FirstName: "Online", Status: &tg.UserStatusOnline{Expires: int(at.Unix()) + 60}}
	d1, _ := dialogAt(&tg.PeerUser{UserID: 42}, 1, 2, false).(*tg.Dialog)
	d1.UnreadMark = true
	d1.ReadOutboxMaxID = 1
	d1.NotifySettings.SetMuteUntil(2147483647)
	d2, _ := dialogAt(&tg.PeerUser{UserID: 43}, 2, 0, false).(*tg.Dialog)
	stub := &stubGetDialogs{responses: []tg.MessagesDialogsClass{&tg.MessagesDialogs{
		Dialogs:  []tg.DialogClass{d1, d2},
		Messages: []tg.MessageClass{topMessage(1, &tg.PeerUser{UserID: 42}, at), topMessage(2, &tg.PeerUser{UserID: 43}, at)},
		Users:    []tg.UserClass{friend, other},
	}}}
	page, err := NewDialogsFetcher(stub).FetchDialogs(context.Background(), 10, coresync.DialogCursor{})
	if err != nil {
		t.Fatalf("FetchDialogs: %v", err)
	}
	if len(page.Chats) != 2 {
		t.Fatalf("chats = %+v", page.Chats)
	}
	c := page.Chats[0]
	if c.ReadOutboxMaxID != 1 {
		t.Fatalf("read pointer = %d, want 1", c.ReadOutboxMaxID)
	}
	if !c.UnreadMark || !c.Muted(at) || c.Online || !c.LastSeen.Equal(seen) {
		t.Fatalf("first chat lost a fact: %+v", c)
	}
	if o := page.Chats[1]; !o.Online || o.Muted(at) || o.UnreadMark {
		t.Fatalf("second chat: %+v", o)
	}
}
