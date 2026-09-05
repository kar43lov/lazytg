package tg

import "sync/atomic"

// Self is the account's own user id, learned once the session is
// authorized and read by every converter that has to decide whose message
// something is.
//
// It exists for one chat: Saved Messages. There the peer is the account
// itself, and Telegram marks nothing in it as outgoing — the flag says
// "sent to somebody else", and there is nobody else — so every message the
// account wrote to itself came through as somebody else's: labelled with
// the chat's name, refused by the editor, counted by the unread rule. The
// converters treat a message in the self chat as the account's own; the id
// is what tells them which chat that is. Zero until it is known, which
// changes nothing about any other chat.
type Self struct {
	id atomic.Int64
}

// Set records the id.
func (s *Self) Set(id int64) {
	if s == nil {
		return
	}
	s.id.Store(id)
}

// ID is the recorded id, or 0 while unknown.
func (s *Self) ID() int64 {
	if s == nil {
		return 0
	}
	return s.id.Load()
}

// Owns reports whether chatID is the account's own chat.
func (s *Self) Owns(chatID int64) bool {
	id := s.ID()
	return id != 0 && chatID == id
}
