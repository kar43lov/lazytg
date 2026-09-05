package app

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/ui/panes/chats"
	"github.com/kar43lov/lazytg/internal/ui/safetext"
)

// A link is the one thing in a text message a terminal cannot follow by
// itself, and the thing a graphical client makes clickable first. "o" on a
// message that carries one hands it to the browser; a place on a map goes
// the same way, as the map's address.

// openableLink is the first http(s) address a message carries: a link
// hidden behind words, a bare one Telegram marked, a bare one it did not,
// or the map for a location. Empty when there is nothing to follow.
func openableLink(msg domain.Message) string {
	if msg.Media != nil {
		switch msg.Media.Kind {
		case domain.MediaKindLocation:
			return mapLink(msg.Media.Filename)
		default:
			if msg.Media.FileID != 0 {
				// An attachment is what "o" opens; the caption's links
				// wait their turn behind it.
				return ""
			}
		}
	}
	runes := []rune(msg.Text)
	for _, e := range msg.Entities {
		switch e.Kind {
		case domain.EntityTextURL:
			if u := httpOnly(e.URL); u != "" {
				return u
			}
		case domain.EntityURL:
			if e.Offset >= 0 && e.End() <= len(runes) {
				if u := httpOnly(string(runes[e.Offset:e.End()])); u != "" {
					return u
				}
			}
		}
	}
	if m := bareLink.FindString(msg.Text); m != "" {
		return httpOnly(strings.TrimRight(m, ".,;:!?)»”"))
	}
	return ""
}

// bareLink is a link nobody marked — an older row, or a client that sends
// none. Generous about what ends it and strict about how it starts.
var bareLink = regexp.MustCompile(`(?i)\bhttps?://[^\s<>"'` + "`" + `]+`)

// httpOnly returns the address when it is a web one, cleaned of anything
// that could steer the terminal on its way to the status line.
func httpOnly(raw string) string {
	raw = safetext.CleanLine(strings.TrimSpace(raw))
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	if s := strings.ToLower(u.Scheme); s != "http" && s != "https" {
		return ""
	}
	return u.String()
}

// mapLink turns the "lat,long" a location carries into a map anyone can
// open. OpenStreetMap, because it needs no account and belongs to nobody
// who would want to know which places this user looks at.
func mapLink(coords string) string {
	var lat, long float64
	if _, err := fmt.Sscanf(coords, "%f,%f", &lat, &long); err != nil {
		return ""
	}
	return fmt.Sprintf("https://www.openstreetmap.org/?mlat=%.6f&mlon=%.6f#map=16/%.6f/%.6f", lat, long, lat, long)
}

// linkOpenedMsg reports the browser handoff.
type linkOpenedMsg struct {
	link string
	err  error
}

func (a App) cmdOpenLink(link string) tea.Cmd {
	opener := a.opener
	return func() tea.Msg {
		if opener == nil {
			return linkOpenedMsg{link: link, err: fmt.Errorf("no browser opener on this platform")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		return linkOpenedMsg{link: link, err: opener.OpenURL(ctx, link)}
	}
}

// noticeMsg puts a sentence in the status bar and nothing else. It is
// how a chord that ended up doing nothing says why, since a silent key is
// indistinguishable from a broken one.
type noticeMsg string

func noticeCmd(text string) tea.Cmd {
	return func() tea.Msg { return noticeMsg(text) }
}

// nothingToOpen explains an "o" that opened nothing: a link of a kind the
// browser is not given, an attachment that has no file, or no attachment
// at all.
func (a App) nothingToOpen() string {
	cur, ok := a.thread.CursorMessage()
	if !ok {
		return "nothing to open: no message under the cursor"
	}
	if cur.Media != nil && cur.Media.FileID == 0 && cur.Media.Kind != domain.MediaKindLocation {
		return "nothing to open: a " + string(cur.Media.Kind) + " has no file behind it"
	}
	if hasNonWebLink(cur) {
		return "not opened: only http and https links are handed to the browser"
	}
	return "nothing to open: no link or attachment at the cursor"
}

// nothingToDownload explains a Ctrl+D that saved nothing.
func (a App) nothingToDownload() string {
	if cur, ok := a.thread.CursorMessage(); ok && cur.Media != nil && cur.Media.FileID == 0 {
		return "nothing to download: a " + string(cur.Media.Kind) + " has no file behind it"
	}
	return "nothing to download: no attachment at the cursor"
}

// hasNonWebLink reports a link the message carries that openableLink
// passed over — the reason "o" did nothing was the scheme, not the absence.
func hasNonWebLink(msg domain.Message) bool {
	for _, e := range msg.Entities {
		if e.Kind == domain.EntityTextURL && e.URL != "" {
			return true
		}
	}
	return false
}

func (a App) applyLinkOpened(msg linkOpenedMsg) App {
	if msg.err != nil {
		a.status = a.status.SetNotice("could not open the link: " + msg.err.Error())
		return a
	}
	host := msg.link
	if u, err := url.Parse(msg.link); err == nil && u.Host != "" {
		host = u.Host
	}
	a.status = a.status.SetNotice("opened " + host + " in the browser")
	return a
}

// messageLink is the address of a message, the one "Copy link" gives in
// every official client: t.me/<username>/<id> when the chat has a public
// handle, t.me/c/<id>/<id> for a private channel or supergroup, which
// opens for members. A person or a basic group has no address at all —
// Telegram never made one — and the empty string says so.
func messageLink(chat chats.ChatItem, messageID int64) string {
	if chat.Username() != "" {
		return fmt.Sprintf("https://t.me/%s/%d", chat.Username(), messageID)
	}
	switch chat.Type() {
	case domain.ChatTypeChannel, domain.ChatTypeSupergroup:
		return fmt.Sprintf("https://t.me/c/%d/%d", chat.ID(), messageID)
	}
	return ""
}

// cmdCopyLink puts the address of the message under the cursor on the
// clipboard, or says why there is none.
func (a App) cmdCopyLink() (App, tea.Cmd, bool) {
	msg, ok := a.thread.CursorMessage()
	if !ok {
		return a, nil, false
	}
	chat, ok := a.chats.ItemByID(msg.ChatID)
	if !ok {
		return a, nil, false
	}
	link := messageLink(chat, msg.ID)
	if link == "" {
		a.status = a.status.SetNotice("no link: messages in a private chat or a basic group have no address")
		return a, nil, true
	}
	a.status = a.status.SetNotice("copied " + link)
	return a, tea.SetClipboard(link), true
}
