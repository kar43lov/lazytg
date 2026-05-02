// Package sync owns the read-side data flow that keeps lazytg's local SQLite
// state in step with Telegram: history backfill, live update ingestion,
// outgoing-message orchestration, reconnect logic and rate limiting.
//
// The package MUST stay free of gotd and bubbletea imports — depguard
// enforces this. Concrete MTProto behaviour reaches us through small
// interfaces (HistoryProvider, SenderInterface, …) that internal/tg
// satisfies.
package sync
