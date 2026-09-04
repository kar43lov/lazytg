package tg

import (
	"time"

	"github.com/gotd/td/tg"

	"github.com/kar43lov/lazytg/internal/core/events"
)

// Typing notifications, inbound only.
//
// Receiving these costs nothing: the server pushes them whether or not a
// client looks, and they are most of what makes a conversation feel live
// rather than like a mailbox. Sending this account's own is a different
// question with a different answer — it would be a request every few seconds
// for as long as somebody is writing, on an account already under observation
// for running an unofficial client — and it is deliberately not done.

// publishTyping converts a typing update into a bus event.
//
// The three variants exist because Telegram names the chat differently in
// each: a private dialog is identified by the user typing in it, a basic
// group carries its own id plus who is typing, and a channel the same again.
// They are flattened here into one event, because everything downstream cares
// about the chat and the person, not about which of the three shapes carried
// them.
func (d *UpdatesDispatcher) publishTyping(chatID, fromID int64, action tg.SendMessageActionClass, now time.Time) {
	if chatID == 0 {
		return
	}
	label := typingLabel(action)
	if label == "" {
		// sendMessageCancelAction, and anything else that does not
		// describe an activity. Nothing is published: the indicator
		// expires on its own, which is what every client relies on
		// because the cancel notification is not reliably sent.
		return
	}
	d.bus.Publish(events.PeerTyping{
		ChatID: chatID,
		FromID: fromID,
		Action: label,
		At:     now,
	})
}

// typingLabel names the activity in the words a status line wants, or ""
// when the action describes no activity.
//
// The distinctions are kept rather than flattened to "typing" because they
// change what the reader should do: "recording a voice message" means wait,
// "sending a photo" means something is on its way, and "typing" means keep
// the conversation going. Telegram's own clients show exactly these.
func typingLabel(action tg.SendMessageActionClass) string {
	switch action.(type) {
	case *tg.SendMessageTypingAction:
		return "typing"
	case *tg.SendMessageRecordAudioAction:
		return "recording a voice message"
	case *tg.SendMessageUploadAudioAction:
		return "sending a voice message"
	case *tg.SendMessageRecordVideoAction:
		return "recording a video"
	case *tg.SendMessageUploadVideoAction:
		return "sending a video"
	case *tg.SendMessageRecordRoundAction:
		return "recording a video message"
	case *tg.SendMessageUploadRoundAction:
		return "sending a video message"
	case *tg.SendMessageUploadPhotoAction:
		return "sending a photo"
	case *tg.SendMessageUploadDocumentAction:
		return "sending a file"
	case *tg.SendMessageChooseStickerAction:
		return "choosing a sticker"
	case *tg.SendMessageChooseContactAction:
		return "choosing a contact"
	case *tg.SendMessageGeoLocationAction:
		return "sharing a location"
	case *tg.SendMessageGamePlayAction:
		return "playing a game"
	default:
		// Includes sendMessageCancelAction and the draft actions, none of
		// which is an activity to announce. A kind added by Telegram
		// later lands here too, and showing nothing is the right answer
		// for a label this client does not know.
		return ""
	}
}
