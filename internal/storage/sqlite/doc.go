// Package sqlite is the SQLite-backed implementation of the storage repository.
//
// The pure-Go driver (modernc.org/sqlite) is used by default. An opt-in CGo
// SQLCipher build is selectable via the "sqlcipher" build tag (see
// driver_sqlcipher.go).
//
// This package MUST NOT import internal/ui or internal/tg — depguard enforces
// this rule in CI.
package sqlite
