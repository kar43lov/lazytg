package tg

import (
	"fmt"
	"strings"
	"time"

	"github.com/gotd/td/tg"

	"github.com/kar43lov/lazytg/internal/core/domain"
)

// A service message is Telegram's own line in a conversation: somebody
// joined, the title changed, a message was pinned, a call happened. They
// were dropped on the way in, which left a group that had only ever been
// created and joined looking identical to an empty one — and left the
// reader with no way to tell why a conversation went quiet (they left) or
// why it changed its name. They are stored as ordinary messages whose text
// is the sentence Telegram's own clients would print, with the actor as
// the sender so the name resolves the way any other sender's does.

// convertService turns a service message into a row.
func convertService(m *tg.MessageService, chatID int64, self *Self) domain.Message {
	return domain.Message{
		ID:       int64(m.ID),
		ChatID:   chatID,
		FromID:   serviceSender(m, chatID),
		Date:     time.Unix(int64(m.Date), 0).UTC(),
		Text:     describeAction(m.Action),
		ReplyTo:  serviceReplyTo(m),
		Outgoing: m.Out || self.Owns(chatID),
	}
}

func serviceSender(m *tg.MessageService, chatID int64) int64 {
	if m.Out {
		return 0
	}
	if from, ok := m.GetFromID(); ok {
		return peerIDToInt64(from)
	}
	return chatID
}

func serviceReplyTo(m *tg.MessageService) int64 {
	header, ok := m.GetReplyTo()
	if !ok {
		return 0
	}
	if h, ok := header.(*tg.MessageReplyHeader); ok {
		if id, ok := h.GetReplyToMsgID(); ok {
			return int64(id)
		}
	}
	return 0
}

// describeAction words an action the way the official clients do. Kinds
// this build does not know are named after the constructor rather than
// dropped, so a server newer than the client still leaves a line.
func describeAction(a tg.MessageActionClass) string {
	switch v := a.(type) {
	case *tg.MessageActionChatCreate:
		return "created the group " + quote(v.Title)
	case *tg.MessageActionChatEditTitle:
		return "changed the title to " + quote(v.Title)
	case *tg.MessageActionChatEditPhoto:
		return "changed the group photo"
	case *tg.MessageActionChatDeletePhoto:
		return "removed the group photo"
	case *tg.MessageActionChatAddUser:
		if len(v.Users) == 1 {
			return "added a member"
		}
		return fmt.Sprintf("added %d members", len(v.Users))
	case *tg.MessageActionChatDeleteUser:
		return "removed a member"
	case *tg.MessageActionChatJoinedByLink:
		return "joined by invite link"
	case *tg.MessageActionChatJoinedByRequest:
		return "joined the group"
	case *tg.MessageActionChannelCreate:
		return "created the channel " + quote(v.Title)
	case *tg.MessageActionChatMigrateTo:
		return "the group was upgraded to a supergroup"
	case *tg.MessageActionChannelMigrateFrom:
		return "the group " + quote(v.Title) + " was upgraded to a supergroup"
	case *tg.MessageActionPinMessage:
		return "pinned a message"
	case *tg.MessageActionHistoryClear:
		return "cleared the history"
	case *tg.MessageActionPhoneCall:
		return describeCall(v)
	case *tg.MessageActionContactSignUp:
		return "joined Telegram"
	case *tg.MessageActionScreenshotTaken:
		return "took a screenshot"
	case *tg.MessageActionSetMessagesTTL:
		if v.Period == 0 {
			return "turned auto-delete off"
		}
		return "set messages to auto-delete after " + describePeriod(v.Period)
	case *tg.MessageActionTopicCreate:
		return "created the topic " + quote(v.Title)
	case *tg.MessageActionTopicEdit:
		if title, ok := v.GetTitle(); ok {
			return "renamed the topic to " + quote(title)
		}
		return "edited the topic"
	case *tg.MessageActionGroupCall:
		if d, ok := v.GetDuration(); ok {
			return "group call ended after " + describePeriod(d)
		}
		return "started a group call"
	case *tg.MessageActionGroupCallScheduled:
		return "scheduled a group call"
	case *tg.MessageActionInviteToGroupCall:
		return "invited to the group call"
	case *tg.MessageActionSetChatTheme:
		return "changed the chat theme"
	case *tg.MessageActionGiftPremium:
		return "sent a Telegram Premium gift"
	case *tg.MessageActionBotAllowed:
		return "allowed the bot to message"
	case *tg.MessageActionCustomAction:
		return v.Message
	case *tg.MessageActionEmpty, nil:
		return "service message"
	default:
		name := strings.TrimPrefix(a.TypeName(), "messageAction")
		return "service: " + name
	}
}

func describeCall(c *tg.MessageActionPhoneCall) string {
	kind := "call"
	if c.Video {
		kind = "video call"
	}
	if d, ok := c.GetDuration(); ok && d > 0 {
		return kind + ", " + describePeriod(d)
	}
	switch c.Reason.(type) {
	case *tg.PhoneCallDiscardReasonMissed:
		return "missed " + kind
	case *tg.PhoneCallDiscardReasonBusy:
		return kind + " declined"
	default:
		return kind + " cancelled"
	}
}

func describePeriod(seconds int) string {
	switch {
	case seconds >= 86400 && seconds%86400 == 0:
		return plural(seconds/86400, "day")
	case seconds >= 3600 && seconds%3600 == 0:
		return plural(seconds/3600, "hour")
	case seconds >= 60:
		return plural(seconds/60, "minute")
	default:
		return plural(seconds, "second")
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

func quote(s string) string {
	return "“" + s + "”"
}
