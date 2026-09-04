package app

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/kar43lov/lazytg/internal/core/domain"
	"github.com/kar43lov/lazytg/internal/ui/graphics"
)

// Showing a photo inside the thread.
//
// The badge says there is a picture; this shows it, in the terminals that can
// draw one. It is a separate gesture from "open" rather than a replacement
// for it: `o` hands the file to the system viewer, which is the only honest
// answer for video and the better one for a photo you want to keep looking
// at, while `i` answers "what is this" without leaving the client.
//
// Nothing is drawn unasked. A thread that fetched every photo it scrolled
// past would be slow, would spend the user's bandwidth, and would produce a
// request pattern no ordinary client does — on an account already watched for
// running an unofficial one.

// inlineImageReadyMsg carries a rendered image back to the UI goroutine.
type inlineImageReadyMsg struct {
	messageID int64
	image     graphics.Image
	err       error
}

// cmdToggleInlineImage draws the picture on the message under the cursor, or
// removes it if it is already there.
//
// The download is the same one `o` and Ctrl-D use, so a photo already on disk
// costs nothing: the dedup cache answers without touching the network.
func (a App) cmdToggleInlineImage() (App, tea.Cmd, bool) {
	msg, ok := a.thread.CursorMessage()
	if !ok {
		return a, nil, false
	}
	if a.thread.HasImage(msg.ID) {
		a.thread = a.thread.HideImage(msg.ID)
		// Taking the escape out of the rendered body is not enough to make
		// the picture go away: the terminal holds it outside the text grid
		// and painting spaces over those cells leaves it there. It has to be
		// told, and tea.Raw is how a sequence reaches the terminal without
		// going through a frame.
		return a, tea.Raw(graphics.DeleteImage(imageID(msg.ID))), true
	}
	if msg.Media == nil || !msg.Media.Kind.IsPhoto() {
		// Deliberately photos only. A video's first frame would need a
		// decoder this client does not have, and a sticker is usually WebP
		// or animated — both would be a picture that is wrong rather than
		// a picture that is missing.
		a.status = a.status.SetNotice("nothing to show here — inline drawing is for photos")
		return a, nil, true
	}
	if !a.imageProtocol.Supported() {
		a.status = a.status.SetNotice("this terminal cannot draw images — press o to open it instead")
		return a, nil, true
	}
	if a.downloader == nil {
		a.status = a.status.SetNotice("not connected")
		return a, nil, true
	}

	cols, rows := a.inlineImageBox()
	chatID := a.thread.ChatID()
	title, _ := a.chatTitle(chatID)
	downloader := a.downloader
	media := *msg.Media
	id := msg.ID
	a.status = a.status.SetNotice("fetching the picture…")

	return a, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
		defer cancel()
		path, err := downloader.Download(ctx, chatID, title, media)
		if err != nil {
			return inlineImageReadyMsg{messageID: id, err: err}
		}
		img, err := graphics.Encode(path, imageID(id), cols, rows, cellAspect)
		return inlineImageReadyMsg{messageID: id, image: img, err: err}
	}, true
}

// clearDrawnImagesCmd takes every picture off the screen, and reports nil
// when there is nothing drawn.
//
// Switching chats has to do this even though the thread drops its own record
// of them: an image is placed by the terminal outside the text grid, so a
// pane that now shows a different conversation would still have last chat's
// photos sitting on top of it.
func (a App) clearDrawnImagesCmd() tea.Cmd {
	if a.thread.ImageCount() == 0 {
		return nil
	}
	return tea.Raw(graphics.DeleteAll)
}

// applyInlineImage installs a rendered image, or reports why there is none.
func (a App) applyInlineImage(msg inlineImageReadyMsg) App {
	if msg.err != nil {
		a.log.Warn("inline image failed", "message_id", msg.messageID, "err", msg.err)
		a.status = a.status.SetNotice(fmt.Sprintf("could not draw it: %v", msg.err))
		return a
	}
	a.thread = a.thread.ShowImage(msg.messageID, msg.image)
	a.status = a.status.SetNotice("")
	return a
}

// inlineImageBox is how much room a picture may take: the thread's width,
// and at most half its height.
//
// Half rather than all of it, because the picture is part of a conversation:
// filling the pane would push every message off screen and leave the user
// looking at one photo with no idea what was said around it.
func (a App) inlineImageBox() (cols, rows int) {
	l := a.layout()
	cols = l.threadW - paneHPadding
	rows = (l.paneH - 2) / 2
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	return cols, rows
}

// cellAspect is how much taller a terminal cell is than it is wide.
//
// Every terminal font anyone uses sits close to 2. Getting it wrong does not
// break anything — the terminal scales the image into the cell box it is
// given — it just makes pictures look squashed or stretched, which is the
// single most common bug in terminal image code.
const cellAspect = 2.0

// imageID turns a message id into the number the terminal files the picture
// under.
//
// Message ids are per-chat and 64-bit; the protocol's are global to the
// program and 32-bit, so this is a narrowing and two different chats can in
// principle collide on one. The consequence of a collision is that opening a
// picture replaces another one that is not on screen — acceptable — while the
// alternative, a counter, would leak ids for every photo ever opened in the
// session and lose the property that redrawing the same message is idempotent.
// Zero is not a valid id, so it is moved out of the way.
func imageID(messageID int64) uint32 {
	id := uint32(messageID & 0x7fffffff) //nolint:gosec // deliberately narrowed, see above
	if id == 0 {
		return 1
	}
	return id
}

// ensure domain stays imported for the media kind check above.
var _ = domain.MediaKindPhoto
