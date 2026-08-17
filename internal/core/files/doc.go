// Package files contains the gotd-free download / upload pipeline used by
// Stage 3 to back the Ctrl-D / Ctrl-U user-facing actions.
//
// MUST NOT import gotd or charmbracelet/bubbletea (enforced via depguard
// in the project's golangci-lint config). Telegram-specific work is
// delegated through the Downloader / Uploader interfaces declared here;
// the concrete implementations live in internal/tg/files.go and translate
// the gotd-typed APIs into our domain-typed ones.
//
// The package owns three pieces:
//
//   - FileStore (store.go) — on-disk path resolution. Sanitises chat
//     titles for use as directory names and creates the destination
//     tree with 0700 permissions.
//   - DedupCache (dedup.go) — wraps the SQLite downloaded_files table so
//     a second Ctrl-D on the same media short-circuits to the cached
//     path instead of re-streaming bytes.
//   - DownloadService (download.go) — orchestrates the full flow:
//     dedup probe → mkdir → tmp file → progress events → rename →
//     dedup record → completed event.
package files
