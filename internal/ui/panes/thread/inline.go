package thread

import (
	"strings"

	"github.com/kar43lov/lazytg/internal/ui/graphics"
)

// Drawing a photo inside the thread.
//
// The badge answers "there is a picture here"; it does not answer "what is
// in it", and for a photo that is most of the message. Terminals that speak
// the Kitty graphics protocol can show it, and the ones that cannot keep the
// badge they had — this is an addition to the thread, never a replacement for
// the text description.
//
// Two rules keep it from being annoying. Nothing is drawn without being
// asked: the user presses a key on the message, because a thread that
// downloads every photo it scrolls past is both slow and a traffic pattern no
// ordinary client produces. And an image the user has opened stays open until
// they close it or leave the chat, so scrolling does not lose their place in
// a conversation they are looking through.

// inlineImage is a picture currently drawn in the thread.
type inlineImage struct {
	escape string
	rows   int
}

// ShowImage installs a rendered image against a message id, replacing
// whatever was there. The rows are reserved in the layout; the escape is
// written on the first of them.
func (m Model) ShowImage(messageID int64, img graphics.Image) Model {
	if m.inline == nil {
		m.inline = make(map[int64]inlineImage)
	} else {
		m.inline = cloneInline(m.inline)
	}
	m.inline[messageID] = inlineImage{escape: img.Escape, rows: img.Rows}
	m.viewport.SetContent(m.renderAll())
	return m
}

// HideImage removes the picture under a message, if there is one.
func (m Model) HideImage(messageID int64) Model {
	if _, ok := m.inline[messageID]; !ok {
		return m
	}
	m.inline = cloneInline(m.inline)
	delete(m.inline, messageID)
	m.viewport.SetContent(m.renderAll())
	return m
}

// HasImage reports whether a message is currently showing one.
func (m Model) HasImage(messageID int64) bool {
	_, ok := m.inline[messageID]
	return ok
}

// ClearImages drops every drawn image.
//
// Called when the content underneath changes wholesale — opening another
// chat, loading a jump window. An image is placed by the terminal at the
// position it was written to and is not part of the text grid, so leaving the
// escapes in a body that now describes a different conversation would draw
// last chat's photos over this one's messages.
func (m Model) ClearImages() Model {
	if len(m.inline) == 0 {
		return m
	}
	m.inline = nil
	m.viewport.SetContent(m.renderAll())
	return m
}

// ImageCount reports how many images are drawn. Test helper.
func (m Model) ImageCount() int { return len(m.inline) }

func cloneInline(in map[int64]inlineImage) map[int64]inlineImage {
	out := make(map[int64]inlineImage, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// imageBlock returns the rows an image occupies under a message, ready to be
// appended to the rendered block.
//
// The escape rides on the first row and the rest are blank. The terminal
// draws the picture from the cursor position downward without moving the
// cursor (C=1 in the escape), so the blank rows are what stop the next
// message being drawn on top of it — the image is not part of the text grid
// and the grid has to be told to leave room.
func (m Model) imageBlock(messageID int64) (string, int) {
	img, ok := m.inline[messageID]
	if !ok {
		return "", 0
	}
	if img.rows < 1 {
		return "", 0
	}
	rows := make([]string, img.rows)
	rows[0] = img.escape
	return strings.Join(rows, "\n"), img.rows
}
