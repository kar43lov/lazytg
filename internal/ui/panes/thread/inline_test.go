package thread

import (
	"strings"
	"testing"
	"time"

	"github.com/kar43lov/lazytg/internal/core/events"
	"github.com/kar43lov/lazytg/internal/ui/graphics"
)

func fakeImage(rows int) graphics.Image {
	return graphics.Image{Escape: "\x1b_Ga=T,f=100;PAYLOAD\x1b\\", Rows: rows, Cols: 20}
}

func TestShowImage_ReservesTheRowsThePictureNeeds(t *testing.T) {
	t.Parallel()

	m := markThread(t, msgAt(1, 0, "look at this"), msgAt(2, 1, "and this"))
	// Counted on the rendered body rather than the View: the viewport is a
	// fixed-height window onto it, so a body that grew by six rows shows as
	// the same number of screen lines.
	beforeBody, _ := m.renderContent()
	before := strings.Count(beforeBody, "\n")

	m = m.ShowImage(1, fakeImage(6))
	afterBody, _ := m.renderContent()
	after := strings.Count(afterBody, "\n")

	// A picture is not part of the text grid: the terminal draws it from
	// the cursor downward and the grid has to be told to leave room, or the
	// next message is drawn on top of it.
	if after < before+6 {
		t.Fatalf("view grew by %d rows, want at least 6 for the picture", after-before)
	}
	if !strings.Contains(afterBody, "PAYLOAD") {
		t.Fatal("the escape never reached the rendered body")
	}
}

func TestShowImage_TogglesOff(t *testing.T) {
	t.Parallel()

	m := markThread(t, msgAt(1, 0, "one"))
	m = m.ShowImage(1, fakeImage(4))
	if !m.HasImage(1) {
		t.Fatal("image not installed")
	}
	m = m.HideImage(1)
	if m.HasImage(1) {
		t.Fatal("image still installed after hiding")
	}
	body, _ := m.renderContent()
	if strings.Contains(body, "PAYLOAD") {
		t.Fatal("the escape survived the hide")
	}
}

// Opening another chat has to take the pictures with it. The terminal placed
// them at a screen position and knows nothing about which conversation they
// belonged to.
func TestOpenChat_DropsDrawnImages(t *testing.T) {
	t.Parallel()

	m := markThread(t, msgAt(1, 0, "one"))
	m = m.ShowImage(1, fakeImage(4))
	if m.ImageCount() != 1 {
		t.Fatalf("setup: ImageCount = %d", m.ImageCount())
	}

	m, _ = m.OpenChat(99)
	if m.ImageCount() != 0 {
		t.Fatalf("images survived a chat switch: %d", m.ImageCount())
	}
}

// The same reset drops the marks, for a related reason: a message id is
// unique per chat, so a mark carried across can match a different message
// rather than simply missing.
func TestOpenChat_DropsMarks(t *testing.T) {
	t.Parallel()

	m := markThread(t, msgAt(1, 0, "one"), msgAt(2, 1, "two"))
	m = m.SetCursor(1).ToggleMark()
	if m.MarkCount() != 1 {
		t.Fatalf("setup: MarkCount = %d", m.MarkCount())
	}

	m, _ = m.OpenChat(99)
	if m.MarkCount() != 0 {
		t.Fatalf("marks survived a chat switch: %d", m.MarkCount())
	}
}

func TestShowImage_DoesNotMutateThePriorModel(t *testing.T) {
	t.Parallel()

	before := markThread(t, msgAt(1, 0, "one"), msgAt(2, 1, "two")).ShowImage(1, fakeImage(3))
	after := before.ShowImage(2, fakeImage(3))

	if before.ImageCount() != 1 {
		t.Fatalf("the earlier model gained an image: %d", before.ImageCount())
	}
	if after.ImageCount() != 2 {
		t.Fatalf("the later model has %d images, want 2", after.ImageCount())
	}
}

// A zero-row image would contribute an escape with no room reserved, so the
// next message would be drawn over whatever the terminal painted.
func TestShowImage_IgnoresAnImageWithNoRows(t *testing.T) {
	t.Parallel()

	m := markThread(t, msgAt(1, 0, "one"))
	m = m.ShowImage(1, graphics.Image{Escape: "\x1b_Gx\x1b\\", Rows: 0})
	body, _ := m.renderContent()
	if strings.Contains(body, "\x1b_Gx") {
		t.Fatal("an image with no reserved rows was drawn anyway")
	}
}

func TestInline_SurvivesAMessageArriving(t *testing.T) {
	t.Parallel()

	m := markThread(t, msgAt(1, 0, "one"))
	m = m.ShowImage(1, fakeImage(4))

	m, _ = m.applyIncoming(events.MessageReceived{
		ChatID: 1, MessageID: 2, Text: "later", Date: time.Now(),
	})
	if !m.HasImage(1) {
		t.Fatal("a new message dropped the picture the user was looking at")
	}
}
