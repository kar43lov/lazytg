package chats

// chatsLoadedMsg carries a freshly-sorted slice of chat items into Update.
// It is the only message that mutates the underlying list contents — the
// caller (Init or the debounced reload tick) sorts before publishing so
// Update never has to worry about ordering.
type chatsLoadedMsg struct {
	items []ChatItem
}

// chatsLoadFailedMsg is delivered when the repo read fails. We surface the
// error via slog and keep the previous list contents — a transient SQLite
// hiccup should not blank the UI.
type chatsLoadFailedMsg struct {
	err error
}

// reloadDebouncedMsg fires after a tea.Tick scheduled by a DialogUpdated
// event. The generation field is used to drop stale ticks: if more than
// one DialogUpdated arrives inside the debounce window, only the last
// scheduled tick (highest generation) actually triggers a repo read.
type reloadDebouncedMsg struct {
	generation uint64
}

// ChatSelectedMsg is published when the user activates the highlighted
// row (Enter). Exported so the app can route it into the thread pane in
// Task 8 / Task 11 wiring.
type ChatSelectedMsg struct {
	ChatID int64
}
