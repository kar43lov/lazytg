//go:build sqlcipher

package sqlite

// SQLCipher driver placeholder. The CGo-backed implementation is planned for
// Stage 3 of the lazytg roadmap. Until then, building with -tags sqlcipher
// must NOT silently produce a binary that uses the unencrypted modernc driver
// — that would be a security misrepresentation. Instead this init panics so
// the failure is visible at first run.
//
// driverName is declared here (matching the planned Stage 3 driver registration
// name) so the rest of the package compiles cleanly under -tags sqlcipher.
// Nothing actually opens a database/sql handle before init runs, so the panic
// fires before driverName is consulted.
//
// When the real driver lands, replace this file with:
//
//	import _ "github.com/mutecomm/go-sqlcipher/v4"
//	(keep const driverName = "sqlite3", drop the panic).
const driverName = "sqlite3"

func init() {
	panic("sqlcipher build tag is reserved for lazytg Stage 3 (CGo SQLCipher); rebuild without -tags sqlcipher for now")
}
