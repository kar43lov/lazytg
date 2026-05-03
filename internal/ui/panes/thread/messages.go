package thread

import "github.com/pgmac/lazytg/internal/core/domain"

// messagesLoadedMsg is the result of an OpenChat fetch. messages are in
// ascending date order (oldest first) ready to be appended to the
// viewport content. hasMore reports whether the repo had more rows
// beyond the requested page — populated via the +1 probe trick in
// loadCmd.
//
// gen is the loadGen value captured when loadCmd was scheduled.
// applyLoaded compares it against the model's current loadGen and
// drops the message on mismatch — the model has moved on (chat switch,
// search jump) and the in-flight result must not clobber the new state.
type messagesLoadedMsg struct {
	chatID   int64
	gen      uint64
	messages []domain.Message
	hasMore  bool
}

// messagesPaginationLoadedMsg carries the "scroll-up loaded another
// page" payload. messages are also ascending and are *prepended* to the
// existing slice; the thread model bumps the viewport offset by the
// freshly-rendered line count so the user's reading position stays
// stable as new content slides in above.
//
// gen mirrors messagesLoadedMsg.gen — applyPagination drops the message
// on mismatch so a stale older-page load arriving after a search jump
// does not yank a chunk of unrelated history into the jump window.
type messagesPaginationLoadedMsg struct {
	chatID   int64
	gen      uint64
	messages []domain.Message
	hasMore  bool
}

// messagesPaginationNewerLoadedMsg carries the symmetric "scroll-down
// loaded another forward page" payload. messages are appended to the
// existing slice (already-rendered window plus any live tail rows),
// dedup-merged against entries the slice already holds. Used by the
// forward-pagination path that completes the search-jump UX —
// scrolling down at the bottom of the loaded ±N window walks the
// model toward the present so the user is no longer stranded in an
// isolated slice.
type messagesPaginationNewerLoadedMsg struct {
	chatID   int64
	gen      uint64
	messages []domain.Message
	hasMore  bool
}

// messagesLoadFailedMsg surfaces a repo failure to Update. We log and
// keep the previous content rather than blanking the pane — a transient
// SQLite hiccup should not destroy whatever the user was reading.
//
// gen mirrors the success-path messages so applyLoadFailed can drop a
// stale failure (chat switched / search-jump bumped loadGen). Without
// the guard a slow loadCmd that fails after the model has moved on
// would still flip m.loading=false; with a deferred applyPendingScroll
// in flight (the search-jump fallback path), that early idle signal can
// land BEFORE the real load completes — applyPendingScroll then runs,
// fails to find the target id in the empty slice, and clears
// pendingScroll, so the eventual real load lands at the chat tail
// instead of the jump target.
type messagesLoadFailedMsg struct {
	chatID int64
	gen    uint64
	err    error
}

// DownloadRequestedMsg is emitted by the thread pane when the user
// presses the Download chord (Ctrl-D by default) and the most recent
// message in the thread carries a downloadable Media. The app layer
// picks it up and routes it into core/files.DownloadService.
//
// v0.1 scope: the chord operates on the latest media-bearing message in
// the visible thread. v0.2 will add per-message cursor navigation so
// the user can pick an older attachment without scrolling-then-pressing.
// Documented as a non-goal in CLAUDE.md so this is not a regression but
// a pinned interim UX.
type DownloadRequestedMsg struct {
	ChatID    int64
	MessageID int64
	ChatTitle string
	Media     domain.MediaInfo
}
