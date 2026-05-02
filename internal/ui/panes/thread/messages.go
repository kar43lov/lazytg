package thread

import "github.com/pgmac/lazytg/internal/core/domain"

// messagesLoadedMsg is the result of an OpenChat fetch. messages are in
// ascending date order (oldest first) ready to be appended to the
// viewport content. hasMore reports whether the repo had more rows
// beyond the requested page — populated via the +1 probe trick in
// loadCmd.
type messagesLoadedMsg struct {
	chatID   int64
	messages []domain.Message
	hasMore  bool
}

// messagesPaginationLoadedMsg carries the "scroll-up loaded another
// page" payload. messages are also ascending and are *prepended* to the
// existing slice; the thread model bumps the viewport offset by the
// freshly-rendered line count so the user's reading position stays
// stable as new content slides in above.
type messagesPaginationLoadedMsg struct {
	chatID   int64
	messages []domain.Message
	hasMore  bool
}

// messagesLoadFailedMsg surfaces a repo failure to Update. We log and
// keep the previous content rather than blanking the pane — a transient
// SQLite hiccup should not destroy whatever the user was reading.
type messagesLoadFailedMsg struct {
	chatID int64
	err    error
}
