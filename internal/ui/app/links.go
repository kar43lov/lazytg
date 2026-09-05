package app

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
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
