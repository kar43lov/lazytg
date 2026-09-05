package tg

import (
	"strings"
	"time"

	"github.com/gotd/td/tg"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// nameDirectory names the users and chats a response came with, so a
// forwarded message can say who wrote it. Nil is fine: the name is then
// whatever the header carries by itself.
type nameDirectory map[int64]string

// directoryOf reads the names out of the objects a response carried.
func directoryOf(users []tg.UserClass, chats []tg.ChatClass) nameDirectory {
	if len(users) == 0 && len(chats) == 0 {
		return nil
	}
	dir := make(nameDirectory, len(users)+len(chats))
	for _, u := range users {
		if user, ok := u.(*tg.User); ok {
			dir[user.ID] = userTitle(user)
		}
	}
	for _, c := range chats {
		switch chat := c.(type) {
		case *tg.Chat:
			dir[chat.ID] = chat.Title
		case *tg.Channel:
			dir[chat.ID] = chat.Title
		}
	}
	return dir
}

// forwardOf reads where a forwarded message came from. The name is the
// one the response gave for the origin, or the one the header carries
// when the origin hid their account; a channel post's author is added
// after the channel, the way every client draws it. Nil for a message
// written where it stands.
func forwardOf(m *tg.Message, dir nameDirectory) *domain.Forward {
	fwd, ok := m.GetFwdFrom()
	if !ok {
		return nil
	}
	f := &domain.Forward{}
	if from, ok := fwd.GetFromID(); ok {
		f.FromID = peerIDToInt64(from)
		f.From = dir[f.FromID]
	}
	if name, ok := fwd.GetFromName(); ok && f.From == "" {
		f.From = name
	}
	if author, ok := fwd.GetPostAuthor(); ok && author != "" {
		if f.From == "" {
			f.From = author
		} else {
			f.From = f.From + " (" + author + ")"
		}
	}
	f.From = strings.TrimSpace(f.From)
	if fwd.Date != 0 {
		f.Date = time.Unix(int64(fwd.Date), 0).UTC()
	}
	return f
}
