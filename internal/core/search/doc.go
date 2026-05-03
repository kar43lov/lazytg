// Package search provides the local FTS5-backed full-text search service for
// lazytg. It indexes messages.text into the messages_fts virtual table (see
// migrations/0005_fts.sql), runs lazy/explicit reindex passes and serves
// query lookups for the TUI search overlay.
//
// MUST NOT import gotd or bubbletea — depguard enforces this. Storage access
// happens through narrow interfaces (IndexStore, MessageReader) so that unit
// tests can swap in fakes without touching the real SQLite repo.
package search
