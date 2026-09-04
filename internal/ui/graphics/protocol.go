// Package graphics draws images in terminals that can show them.
//
// A photo in a chat client is not decoration: "look at this" is half of what
// people send each other, and a client that answers with `[🖼 photo_222.jpg,
// 2.0 MiB]` is asking the user to leave it to see the thing they were sent.
// lazytg already hands attachments to the system viewer, which is the honest
// answer for video — no terminal plays video — but a still image is something
// several terminals can draw themselves.
//
// This implements the Kitty graphics protocol, which Ghostty, kitty and
// WezTerm all speak. Not sixel: it is older, more widely supported and much
// worse — a palette of 256 colours, no transparency, and an encoding that
// makes a photo several times larger on the wire than its PNG.
//
// Nothing here decides *when* to draw. Detection reports what the terminal
// can do, the caller decides whether to spend the download, and a terminal
// that cannot draw gets the badge it got before.
package graphics

import (
	"os"
	"strings"
)

// Protocol names an image protocol a terminal understands.
type Protocol string

const (
	// ProtocolNone means the terminal cannot draw images, or said nothing
	// about it. Treated as "cannot": drawing into a terminal that does not
	// understand the escape leaves the base64 payload on screen as text,
	// which is worse than no picture.
	ProtocolNone Protocol = ""
	// ProtocolKitty is the Kitty graphics protocol.
	ProtocolKitty Protocol = "kitty"
)

// Detect reports which protocol the current terminal supports, reading the
// environment rather than querying the terminal.
//
// A query would be more accurate and is what a graphics library does: write
// a request, read the reply, time out if none comes. It is also a round trip
// on a terminal that may be attached over ssh, performed while the TUI is
// starting, and it fails in the direction that hurts — a slow answer looks
// like no answer. The environment is enough to recognise the three terminals
// that implement this, and getting it wrong costs a badge rather than a
// broken screen.
//
// The override exists because the environment lies in one direction that
// matters: multiplexers. tmux sets TERM to its own value and does not pass
// graphics through by default, so a kitty inside tmux is reported as
// incapable — correctly, but a user with passthrough configured can say so.
func Detect(env func(string) string) Protocol {
	if env == nil {
		env = os.Getenv
	}
	switch strings.ToLower(strings.TrimSpace(env(OverrideEnv))) {
	case "kitty":
		return ProtocolKitty
	case "none", "off", "0":
		return ProtocolNone
	}

	// A multiplexer's TERM hides whatever the outer terminal can do, and
	// neither tmux nor screen forwards graphics escapes unless configured
	// to. Reporting the outer terminal's capability here would paint the
	// payload as text into the user's pane.
	if env("TMUX") != "" || strings.HasPrefix(env("TERM"), "screen") {
		return ProtocolNone
	}

	term := env("TERM")
	switch {
	case strings.Contains(term, "kitty"):
		// xterm-kitty, and xterm-ghostty — Ghostty implements the same
		// protocol and identifies itself this way.
		return ProtocolKitty
	case strings.Contains(term, "ghostty"):
		return ProtocolKitty
	}
	switch env("TERM_PROGRAM") {
	case "ghostty", "Ghostty", "WezTerm":
		return ProtocolKitty
	}
	if env("KITTY_WINDOW_ID") != "" {
		return ProtocolKitty
	}
	return ProtocolNone
}

// OverrideEnv forces the answer: "kitty" to draw anyway (a multiplexer with
// passthrough configured), "none" to never draw.
const OverrideEnv = "LAZYTG_IMAGE_PROTOCOL"

// Supported reports whether images can be drawn at all.
func (p Protocol) Supported() bool { return p != ProtocolNone }
